package workspace

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/reee344/sleipnir/internal/gitx"
)

// PruneOptions tunes Prune.
type PruneOptions struct {
	// Force also removes stale trees that have uncommitted changes and deletes
	// stale branches that hold commits nowhere else. It destroys work; the default
	// never does.
	Force bool
	// Salvage commits a stale tree's uncommitted changes onto its branch before
	// removing the directory, and keeps the branch: nothing is lost, the clutter
	// is gone.
	Salvage bool
	// DryRun reports what would happen without changing anything.
	DryRun bool
	// MinAge skips trees created more recently than this.
	MinAge time.Duration
}

// PruneAction is one thing Prune did (or, in a dry run, would do), or left alone.
type PruneAction struct {
	Agent  string
	Path   string
	Branch string
	Reason string
}

// PruneReport lists what Prune did.
type PruneReport struct {
	// Removed trees (directory and registration gone).
	Removed []PruneAction
	// Kept are stale trees left in place, with the reason (uncommitted changes, an
	// owner that may still be running, ...).
	Kept []PruneAction
	// BranchesRemoved and BranchesKept are leftover branches under the prefix.
	BranchesRemoved []PruneAction
	BranchesKept    []PruneAction
	// Live counts trees skipped because their owner process is running.
	Live int
}

// Prune cleans up after crashed sessions: worktrees whose owner process is gone,
// registrations whose directory vanished, and the branches they leave behind.
//
// It refuses to touch anything it did not create: a worktree needs our marker
// (in its private git directory), a path under Dir and a branch under Prefix, and
// a directory is verified (real directory, no symlink, .git file into this
// repository) before it is deleted. Worktrees of running sessions are skipped.
// Unless Force is set nothing with unique work is destroyed: a dirty tree is kept
// (or salvaged into a commit), a branch with commits that exist nowhere else is
// kept.
//
// Unlike `git worktree prune`, which drops every stale entry in the repository,
// this only ever drops our own.
func (m *Manager) Prune(ctx context.Context, opts PruneOptions) (*PruneReport, error) {
	if err := m.ensure(ctx); err != nil {
		return nil, err
	}
	rep, err := m.prune(ctx, opts)
	if err == nil {
		m.emit(EventPrune, "", "", map[string]any{
			"removed": len(rep.Removed), "kept": len(rep.Kept), "branches_removed": len(rep.BranchesRemoved),
			"branches_kept": len(rep.BranchesKept), "live": rep.Live, "dry_run": opts.DryRun,
		})
	}
	return rep, err
}

func (m *Manager) prune(ctx context.Context, opts PruneOptions) (*PruneReport, error) {
	m.st.lock.Lock()
	defer m.st.lock.Unlock()

	rep := &PruneReport{}
	entries, err := m.ownedEntries(ctx)
	if err != nil {
		return nil, err
	}
	all, err := m.st.base.Worktrees(ctx)
	if err != nil {
		return nil, err
	}
	attached := map[string]bool{}
	for _, w := range all {
		if w.Branch != "" {
			attached[w.Branch] = true
		}
	}
	protected := map[string]bool{}
	sort.Slice(entries, func(i, j int) bool { return entries[i].wt.Path < entries[j].wt.Path })

	for _, e := range entries {
		mk := e.mk
		act := PruneAction{Agent: mk.Agent, Path: e.wt.Path, Branch: mk.Branch}
		if st := mk.owner(); st == ownerAlive || st == ownerUnknown {
			rep.Live++
			protected[mk.Prefix+"/"+integrationName] = true
			protected[mk.Prefix+"/"+baseBranchName] = true
			if mk.Branch != "" {
				protected[mk.Branch] = true
			}
			continue
		}
		if opts.MinAge > 0 && m.now().Sub(mk.Created) < opts.MinAge {
			act.Reason = "younger than MinAge"
			rep.Kept = append(rep.Kept, act)
			protected[mk.Branch] = true
			continue
		}
		m.pruneOne(ctx, opts, e, act, rep)
	}

	m.pruneHalfCreated(ctx, opts, rep)

	// Branches nobody has checked out and no live tree needs.
	refs, err := m.st.base.Branches(ctx, m.st.prefix+"/")
	if err != nil {
		return rep, err
	}
	// What is checked out now, not before this call removed trees.
	attached = map[string]bool{}
	after, _ := m.st.base.Worktrees(ctx)
	for _, w := range after {
		if w.Branch != "" {
			attached[w.Branch] = true
		}
	}
	for _, r := range refs {
		act := PruneAction{Branch: r.Name}
		if attached[r.Name] || protected[r.Name] {
			continue
		}
		others := m.sessionRefsExcluding(ctx, r.Name)
		n, err := m.st.base.CommitsOnlyOn(ctx, r.SHA, m.st.prefix+"/*", others...)
		if err != nil {
			act.Reason = "cannot check: " + err.Error()
			rep.BranchesKept = append(rep.BranchesKept, act)
			continue
		}
		if n > 0 && !opts.Force {
			act.Reason = fmt.Sprintf("holds %d commit(s) that exist nowhere else", n)
			rep.BranchesKept = append(rep.BranchesKept, act)
			continue
		}
		if opts.DryRun {
			act.Reason = "dry run"
			rep.BranchesRemoved = append(rep.BranchesRemoved, act)
			continue
		}
		if err := m.st.base.DeleteBranch(ctx, r.Name); err != nil {
			act.Reason = "delete failed: " + err.Error()
			rep.BranchesKept = append(rep.BranchesKept, act)
			continue
		}
		rep.BranchesRemoved = append(rep.BranchesRemoved, act)
	}
	return rep, nil
}

// pruneHalfCreated removes the registrations that died before they got a marker
// (see orphan.go). The repository lock is held by the caller.
func (m *Manager) pruneHalfCreated(ctx context.Context, opts PruneOptions, rep *PruneReport) {
	ents, err := os.ReadDir(m.st.dirReal)
	if err != nil {
		return
	}
	minAge := max(orphanAge, opts.MinAge)
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		dest := filepath.Join(m.st.dirReal, e.Name())
		if _, ok := m.halfCreated(dest, minAge); !ok {
			continue
		}
		act := PruneAction{Agent: e.Name(), Path: dest, Reason: "half-created tree: registered, but its creation never finished"}
		if opts.DryRun {
			act.Reason += " (dry run)"
		} else if !m.clearHalfCreated(ctx, dest, minAge) {
			act.Reason = "half-created tree that could not be removed"
			rep.Kept = append(rep.Kept, act)
			continue
		}
		rep.Removed = append(rep.Removed, act)
	}
}

// sessionRefsExcluding is sessionRefs without one branch (a branch is not
// "elsewhere" for itself).
func (m *Manager) sessionRefsExcluding(ctx context.Context, name string) []string {
	var out []string
	for _, r := range m.sessionRefs(ctx) {
		if r != "refs/heads/"+name {
			out = append(out, r)
		}
	}
	return out
}

// pruneOne handles one stale tree. The repository lock is held by the caller.
func (m *Manager) pruneOne(ctx context.Context, opts PruneOptions, e ownedEntry, act PruneAction, rep *PruneReport) {
	keep := func(reason string) {
		act.Reason = reason
		rep.Kept = append(rep.Kept, act)
	}
	mk := e.mk
	missing := e.wt.Prunable
	admin := e.admin
	if !missing {
		var err error
		if admin, _, err = m.verifyTreeDir(ctx, e.wt.Path); err != nil {
			keep("not safe to delete: " + err.Error())
			return
		}
		repo, err := m.st.base.Reopen(ctx, e.wt.Path)
		if err != nil {
			keep("cannot open: " + err.Error())
			return
		}
		st, err := repo.Status(ctx)
		if err != nil {
			keep("cannot read its state: " + err.Error())
			return
		}
		if !st.Clean() && !opts.Force {
			if !opts.Salvage {
				keep("uncommitted changes (Salvage commits them, Force discards them)")
				return
			}
			if opts.DryRun {
				act.Reason = "would salvage uncommitted changes into " + mk.Branch
				rep.Removed = append(rep.Removed, act)
				return
			}
			if mk.Branch == "" {
				keep("uncommitted changes in a detached tree cannot be salvaged onto a branch")
				return
			}
			if err := m.salvage(ctx, repo, mk); err != nil {
				keep("salvage failed: " + err.Error())
				return
			}
			act.Reason = "uncommitted changes salvaged into " + mk.Branch
		}
	}
	unique := 0
	branchExists := false
	if mk.Branch != "" {
		if tip, err := m.st.base.BranchSHA(ctx, mk.Branch); err == nil {
			branchExists = true
			n, err := m.st.base.CommitsOnlyOn(ctx, tip, m.st.prefix+"/*", m.sessionRefs(ctx)...)
			if err != nil {
				keep("cannot check its commits: " + err.Error())
				return
			}
			unique = n
		}
	}
	if opts.DryRun {
		if act.Reason == "" {
			act.Reason = "dry run"
		}
		rep.Removed = append(rep.Removed, act)
		return
	}
	if err := m.deleteTree(ctx, e.wt.Path, admin, true); err != nil {
		keep("remove failed: " + err.Error())
		return
	}
	m.mu.Lock()
	delete(m.trees, mk.Agent)
	m.mu.Unlock()
	rep.Removed = append(rep.Removed, act)
	if branchExists {
		branchAct := PruneAction{Agent: mk.Agent, Branch: mk.Branch}
		if unique > 0 && !opts.Force {
			branchAct.Reason = fmt.Sprintf("holds %d commit(s) that exist nowhere else", unique)
			rep.BranchesKept = append(rep.BranchesKept, branchAct)
		} else if err := m.st.base.DeleteBranch(ctx, mk.Branch); err != nil {
			branchAct.Reason = "delete failed: " + err.Error()
			rep.BranchesKept = append(rep.BranchesKept, branchAct)
		} else {
			rep.BranchesRemoved = append(rep.BranchesRemoved, branchAct)
		}
	}
}

// salvage commits a dead session's uncommitted work onto its branch.
func (m *Manager) salvage(ctx context.Context, repo *gitx.Repo, mk *marker) error {
	if err := m.checkCommittable(ctx, repo, repo.Root()); err != nil {
		return err
	}
	_, err := repo.CommitAll(ctx, "sleipnir: salvage of "+mk.Agent+" (its session ended before the work was committed)",
		gitx.Author{Name: mk.Agent, Email: mk.Agent + "@sleipnir.invalid", When: m.now()})
	return err
}

// NestedRepoError lists the directories of a tree that are git repositories of
// their own (an agent cloned something into its tree). Recording one would store a
// bare pointer to a commit that no clone of the project can fetch, so it is refused
// like an oversized file is; the remedy is the same: delete it, or list it in
// .gitignore.
type NestedRepoError struct {
	Paths []string
}

func (e *NestedRepoError) Error() string {
	list, more := e.Paths, ""
	if len(list) > 5 {
		list, more = list[:5], fmt.Sprintf(" and %d more", len(e.Paths)-5)
	}
	return fmt.Sprintf("workspace: %d director(ies) are git repositories of their own (%s%s): remove them or add them to .gitignore",
		len(e.Paths), strings.Join(list, ", "), more)
}

func (e *NestedRepoError) Is(target error) bool { return target == ErrNestedRepo }

// checkCommittable is the guard every commit of an agent's work passes: nothing in
// what would be recorded may be a nested repository or exceed the size limit (once
// in history a file stays in the repository for good). It works on any repository
// handle.
func (m *Manager) checkCommittable(ctx context.Context, repo *gitx.Repo, root string) error {
	limit := m.maxFileBytes()
	st, err := repo.StatusWith(ctx, gitx.StatusOptions{Untracked: "all", MaxBytes: 32 << 20})
	if err != nil {
		return err
	}
	// With every untracked file listed individually, an untracked entry that still
	// ends in a slash is a directory git will not look into: a repository.
	var nested []string
	for _, p := range st.Untracked {
		if strings.HasSuffix(p, "/") {
			nested = append(nested, path.Clean(p))
		}
	}
	if len(nested) > 0 {
		sort.Strings(nested)
		return &NestedRepoError{Paths: nested}
	}
	if limit < 0 {
		return nil
	}
	seen := map[string]bool{}
	var big []string
	visit := func(p string) {
		if seen[p] {
			return
		}
		seen[p] = true
		if fi, err := os.Lstat(root + string(os.PathSeparator) + p); err == nil && fi.Mode().IsRegular() && fi.Size() > limit {
			big = append(big, path.Clean(p))
		}
	}
	for _, c := range st.Staged {
		visit(c.Path)
	}
	for _, c := range st.Unstaged {
		visit(c.Path)
	}
	for _, p := range st.Untracked {
		visit(p)
	}
	if len(big) > 0 {
		sort.Strings(big)
		return &TooLargeError{Files: big, Limit: limit}
	}
	return nil
}
