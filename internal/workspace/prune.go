package workspace

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/gitx"
)

// PruneOptions tunes Prune.
type PruneOptions struct {
	// RequireInactive leaves the entire namespace untouched if any matching
	// worktree has a live or uncertain owner. The check holds the repository lock.
	RequireInactive bool
	// KeepPrefixes preserves every tree and ref in these exact session namespaces,
	// including stopped owners. Callers use it for sessions that can be resumed.
	KeepPrefixes []string
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
	if opts.RequireInactive {
		for _, e := range entries {
			if !keptPrefix(e.mk.Prefix, opts.KeepPrefixes) && e.mk.ownerIn(e.admin) != ownerDead {
				rep.Live++
			}
		}
		if rep.Live > 0 {
			return rep, nil
		}
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
		if keptPrefix(mk.Prefix, opts.KeepPrefixes) {
			continue
		}
		if st := mk.ownerIn(e.admin); st == ownerAlive || st == ownerUnknown {
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
		if keptPrefix(r.Name, opts.KeepPrefixes) {
			continue
		}
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

// keptPrefix matches a namespace or a child ref without matching a similar name.
func keptPrefix(name string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if name == prefix || strings.HasPrefix(name, prefix+"/") {
			return true
		}
	}
	return false
}

// pruneHalfCreated removes the registrations that died before they got a marker
// (see orphan.go). The repository lock is held by the caller.
func (m *Manager) pruneHalfCreated(ctx context.Context, opts PruneOptions, rep *PruneReport) {
	if keptPrefix(m.st.prefix, opts.KeepPrefixes) {
		return
	}
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
		// The tree's .git file is the agent's to rewrite: a handle that it points at
		// another repository's git directory would read, and salvage would commit
		// into, that repository.
		if !samePath(repo.GitDir(), admin) {
			keep("its .git does not point at its registration")
			return
		}
		// The private git directory's commondir is as writable as the tree: one that names another repository's objects
		// and refs would have salvage commit onto that repository's branch.
		if !samePath(repo.CommonDir(), m.st.base.CommonDir()) {
			keep("its git directory does not share this repository's objects and refs")
			return
		}

		st, err := repo.Status(ctx)
		if err != nil {
			keep("cannot read its state: " + err.Error())
			return
		}
		// Commits made on a detached HEAD (or on HEAD moved off the tree's branch)
		// are on no branch: removing the tree removes the HEAD and reflog that are
		// their only reference.
		if !opts.Force {
			if n, err := m.headOnlyCommits(ctx, repo, mk.Branch); err != nil {
				keep("cannot check its HEAD: " + err.Error())
				return
			} else if n > 0 {
				keep(fmt.Sprintf("its HEAD holds %d commit(s) that are on no branch (Force discards them)", n))
				return
			}
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

// headOnlyCommits counts the commits reachable from the tree's HEAD that no
// branch, tag or session ref holds, when HEAD is not the tip of the tree's
// branch (when it is, the branch check of pruneOne covers them).
func (m *Manager) headOnlyCommits(ctx context.Context, repo *gitx.Repo, branch string) (int, error) {
	head, err := repo.Head(ctx)
	if err != nil && gitx.KindOf(err) != gitx.KindNotFound {
		return 0, err // HEAD could not be read: the tree is kept
	}
	if err != nil || head == "" {
		return 0, nil // no commit yet: nothing to lose
	}
	if branch != "" {
		if tip, err := m.st.base.BranchSHA(ctx, branch); err == nil && tip == head {
			return 0, nil
		}
	}
	return m.uniqueCommits(ctx, head)
}

// salvage commits a dead session's uncommitted work onto its branch. It commits
// only when the tree has that branch checked out, and it fails (so the tree is
// kept) when what it recorded cannot stand for the work: a gitlink to a
// repository .gitmodules does not declare records a commit id, not the nested
// repository's content, which deleting the tree would destroy.
func (m *Manager) salvage(ctx context.Context, repo *gitx.Repo, mk *marker) error {
	if cur, err := repo.Branch(ctx); err != nil {
		return err
	} else if cur != mk.Branch {
		return fmt.Errorf("workspace: the tree has %q checked out, not its branch %q", cur, mk.Branch)
	}
	if err := m.checkCommittable(ctx, repo, repo.Root()); err != nil {
		return err
	}
	before, err := repo.Head(ctx)
	if err != nil {
		return err
	}
	msg := m.withAgentTrailer("Save unfinished work\n\nThe session that made these changes ended before they were finished.", mk.Agent)
	after, err := repo.CommitAllWith(ctx, msg, m.agentAuthor(mk.Agent), workCommit)
	if err != nil || after == "" || after == before {
		return err
	}
	if before == "" {
		return nil
	}
	return m.checkCommitted(ctx, repo, before, after)
}

// NestedRepoError lists the directories of a tree that are git repositories of
// their own (an agent cloned something into its tree). Recording one would store a
// bare pointer to a commit that no clone of the project can fetch, so it is refused
// like an oversized file is; the remedy is the same: delete it, or list it in
// .gitignore.
type NestedRepoError struct {
	Paths []string
	// Committed says the pointers are already in commits of the tree.
	Committed bool
}

func (e *NestedRepoError) Error() string {
	list, more := e.Paths, ""
	if len(list) > 5 {
		list, more = list[:5], fmt.Sprintf(" and %d more", len(e.Paths)-5)
	}
	if e.Committed {
		return fmt.Sprintf("workspace: %d path(s) were committed as pointers to git repositories of their own, not declared as submodules (%s%s): take them out of the commits (git rm --cached, then commit again) or declare them in .gitmodules",
			len(e.Paths), strings.Join(list, ", "), more)
	}
	return fmt.Sprintf("workspace: %d director(ies) are git repositories of their own (%s%s): remove them or add them to .gitignore",
		len(e.Paths), strings.Join(list, ", "), more)
}

// Is makes nested-repository errors match the ErrNestedRepo sentinel.
func (e *NestedRepoError) Is(target error) bool { return target == ErrNestedRepo }

// maxCommittedEntries bounds how many changed files checkCommitted examines; a
// change that touches more is refused unexamined (a variable so that a test can
// lower it).
var maxCommittedEntries = 100000

// checkCommitted applies the same guards to what an agent committed itself (a
// coding agent's habit: it runs git commit in its tree) as checkCommittable
// applies to what the harness commits for it. It looks at the entries the range
// base..to added or modified: blobs above the size limit, and gitlinks that
// .gitmodules does not declare. Once such a commit is on the integration branch it
// is in the project's history for good.
func (m *Manager) checkCommitted(ctx context.Context, repo *gitx.Repo, base, to string) error {
	return m.checkEntries(ctx, repo, base, to, true)
}

// checkEntries is checkCommitted, with the gitlink check only when gitlinks is
// set: an intermediate commit's gitlink is judged by the .gitmodules of the end
// of the submission, which checkCommitted reads.
func (m *Manager) checkEntries(ctx context.Context, repo *gitx.Repo, base, to string, gitlinks bool) error {
	entries, truncated, err := repo.ChangedEntries(ctx, base, to, maxCommittedEntries)
	if err != nil {
		return err
	}
	limit := m.maxFileBytes()
	var big, pointers []string
	for _, e := range entries {
		switch {
		case e.Mode == "160000":
			if gitlinks && e.OldMode != "160000" {
				pointers = append(pointers, e.Path)
			}
		case limit >= 0 && e.Size > limit:
			big = append(big, e.Path)
		}
	}
	if len(pointers) > 0 {
		declared, err := repo.SubmodulePaths(ctx, to)
		if err != nil {
			return err
		}
		isDeclared := make(map[string]bool, len(declared))
		for _, d := range declared {
			isDeclared[path.Clean(d)] = true
		}
		var undeclared []string
		for _, p := range pointers {
			if !isDeclared[p] {
				undeclared = append(undeclared, p)
			}
		}
		if len(undeclared) > 0 {
			sort.Strings(undeclared)
			return &NestedRepoError{Paths: undeclared, Committed: true}
		}
	}
	if len(big) > 0 {
		sort.Strings(big)
		return &TooLargeError{Files: big, Limit: limit, Committed: true}
	}
	// An incomplete listing proves nothing about the entries it left out, whether
	// or not a size limit applies: the gitlink check needs every entry too.
	if truncated && (gitlinks || limit >= 0) {
		return &TooLargeError{Limit: limit, TooMany: maxCommittedEntries}
	}
	return nil
}

// maxCheckedCommits bounds how many commits of one submission checkHistory
// examines one by one; a submission with more is refused unexamined (a variable so
// that a test can lower it).
var maxCheckedCommits = 1000

// emptyTree is git's id of the tree with no entries, the parent of a root commit.
const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// checkHistory is checkCommitted for every commit of base..to on its own, not only
// for the change from base to to: a merge keeps the intermediate commits, so a
// blob added by one and deleted by the next is in the project's history although
// the final tree does not show it. Each commit is judged against its first parent
// (the content a merge's other parents bring is judged with their own commits),
// for the size limit; whether a gitlink is declared is a question about the end
// of the submission, which checkCommitted asks.
func (m *Manager) checkHistory(ctx context.Context, repo *gitx.Repo, base, to string) error {
	if err := m.checkCommitted(ctx, repo, base, to); err != nil {
		return err
	}
	if m.maxFileBytes() < 0 {
		return nil // no size limit: there is nothing to check commit by commit
	}
	res, err := repo.Git(ctx, "rev-list", "--parents", "--max-count="+strconv.Itoa(maxCheckedCommits+1), to, "--not", base)
	if err != nil {
		return err
	}
	var commits [][]string
	for _, line := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
		if f := strings.Fields(line); len(f) > 0 {
			commits = append(commits, f)
		}
	}
	if len(commits) > maxCheckedCommits {
		return &TooLargeError{Limit: m.maxFileBytes(), TooManyCommits: maxCheckedCommits}
	}
	if len(commits) == 1 && len(commits[0]) == 2 && commits[0][1] == base {
		return nil // the one commit is the change already checked
	}
	for _, c := range commits {
		parent := emptyTree
		if len(c) > 1 {
			parent = c[1]
		}
		if err := m.checkEntries(ctx, repo, parent, c[0], false); err != nil {
			return err
		}
	}
	return nil
}

// workCommit is how the harness stages an agent's work when it commits it
// (Tree.Commit, salvage): every change except untracked files inside dependency
// stores (gitx.DependencyStores), which an install in a repository without
// ignore rules for them would otherwise turn into history.
var workCommit = gitx.CommitOptions{LeaveUntrackedStores: true}

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
	// A listing cut short says nothing about the files it did not list.
	if st.Truncated {
		return &TooLargeError{Limit: limit, TooMany: max(1, len(st.Staged)+len(st.Unstaged)+len(st.Untracked))}
	}
	// Untracked files in a dependency store are not recorded (workCommit), so
	// they are not examined either.
	kept := st.Untracked[:0:0]
	for _, p := range st.Untracked {
		if !gitx.InDependencyStore(p) {
			kept = append(kept, p)
		}
	}
	st.Untracked = kept
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
