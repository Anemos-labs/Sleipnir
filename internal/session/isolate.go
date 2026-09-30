package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/reee344/sleipnir/internal/checkpoint"
	"github.com/reee344/sleipnir/internal/config"
	"github.com/reee344/sleipnir/internal/gitx"
	"github.com/reee344/sleipnir/internal/swarm"
	"github.com/reee344/sleipnir/internal/tools"
	"github.com/reee344/sleipnir/internal/workspace"
)

// Worktree isolation (swarm.isolation = "worktree", or Options.Isolation).
//
// The session decides early (planIsolation: before the provider is built, so a
// project that cannot be isolated fails at once) and builds late (buildIsolation: the
// workspace manager and merge queue that the swarm's Deps carry). The trees live in the
// user's cache directory, never inside the repository and never under the state
// directory:
//
//	<cache>/sleipnir/worktrees/<session id>/<agent>       the trees; branch sleipnir/<session id>/<agent>
//	<cache>/sleipnir/worktrees/<session id>/_integration  the merge queue's tree
//
// where <cache> is the platform's per-user cache directory ($XDG_CACHE_HOME or
// ~/.cache on Linux): large, disposable, re-creatable data belongs there. The state
// directory (~/.sleipnir) is out: it is a protected configuration directory, where
// every write asks for approval in every mode. Nothing in the configuration chooses
// the location either: a project's config file must not be able to point where a
// harness creates and deletes directories.
//
// The permission engine treats the session's directory as part of the workspace and
// applies every rule that is relative to the workspace inside each tree as it does in
// the checkout (perm.Config.TreeParents): the project's own protections (a denied
// directory, the ask rule on .sleipnir/) hold for a worker in its copy, because its
// work reaches the checkout by a merge. Every writer is also confined to its own tree
// (perm.Engine.Confine, done by the swarm).

// isoPlan is the session's decision about isolation.
type isoPlan struct {
	commit  bool
	repo    *gitx.Repo
	subdir  string // the session's directory relative to the repository root ("" for the root)
	top     string // <cache>/sleipnir/worktrees: the directory every session's trees are under
	dir     string // <cache>/sleipnir/worktrees/<session id>
	prefix  string // sleipnir/<session id>
	mgr     *workspace.Manager
	queue   *workspace.Queue
	ownsDir bool // the session created dir
}

// isolationMode is the effective mode ("none" or "worktree") and whether the person
// asked for it explicitly (an option or flag rather than the configuration).
func (s *Session) isolationMode() (mode string, explicit bool, err error) {
	o := s.opts
	switch v := strings.ToLower(strings.TrimSpace(o.Isolation)); v {
	case "":
		return s.cfg.Swarm.IsolationMode(), false, nil
	case config.IsolationNone, "shared":
		return config.IsolationNone, true, nil
	case config.IsolationWorktree:
		return config.IsolationWorktree, true, nil
	default:
		return "", true, fmt.Errorf("isolation %q: want %q or %q", o.Isolation, config.IsolationNone, config.IsolationWorktree)
	}
}

// planIsolation decides whether this session's swarm isolates its writers and checks
// that it can: a git repository whose root is the project root, with a commit to
// start from and, for Options.Commit, a clean checkout on a branch. It touches
// nothing in the repository.
func (s *Session) planIsolation(ctx context.Context) error {
	o := s.opts
	mode, explicit, err := s.isolationMode()
	if err != nil {
		return err
	}
	if o.Commit && mode != config.IsolationWorktree {
		return errors.New("commit needs worktree isolation (--isolation worktree, or swarm.isolation in the configuration)")
	}
	if !o.Swarm {
		// The configuration applies to swarms; a single agent has nobody to be isolated
		// from. Asking for it in so many words, though, is a mistake worth saying so.
		if o.Commit || (explicit && mode == config.IsolationWorktree) {
			return errors.New("isolation applies to swarm sessions (--swarm N): a single agent edits your checkout directly")
		}
		return nil
	}
	if mode != config.IsolationWorktree {
		return nil
	}

	repo, err := gitx.Open(o.Root)
	switch {
	case err == nil:
	case gitx.KindOf(err) == gitx.KindNotARepo:
		return fmt.Errorf("worktree isolation needs a git repository, and %s is not inside one (git init and commit, or use --isolation none)", o.Root)
	case gitx.KindOf(err) == gitx.KindNoGit:
		return fmt.Errorf("worktree isolation needs git: %w", err)
	default:
		return fmt.Errorf("worktree isolation: cannot open the repository at %s: %w", o.Root, err)
	}
	if repo.IsBare() {
		return errors.New("worktree isolation needs a repository with a work tree, and this one is bare")
	}
	realRoot := evalLoose(o.Root)
	if !samePathLoose(repo.Root(), realRoot) {
		return fmt.Errorf("worktree isolation needs the project root to be the repository's root: the project root is %s and the repository's is %s (run from the repository's root, or use --isolation none)", o.Root, repo.Root())
	}
	if _, err := repo.Head(ctx); err != nil {
		if gitx.KindOf(err) == gitx.KindNotFound {
			return errors.New("worktree isolation starts every tree from a commit, and the repository has none yet: commit the project first")
		}
		return fmt.Errorf("worktree isolation: %w", err)
	}
	if o.Commit {
		if br, err := repo.Branch(ctx); err != nil {
			return fmt.Errorf("commit: %w", err)
		} else if br == "" {
			return errors.New("commit needs a checked-out branch to move, and HEAD is detached")
		}
		dirty, err := sourceDirty(ctx, repo)
		if err != nil {
			return fmt.Errorf("commit: %w", err)
		}
		if dirty {
			return errors.New("commit needs a clean checkout: your working tree has uncommitted changes (commit or stash them, or leave --commit out: the result is then applied as uncommitted edits)")
		}
	}

	top := worktreesRoot(o.Home)
	dir := filepath.Join(top, s.ID)
	switch {
	case inside(repo.Root(), evalLoose(dir)):
		return fmt.Errorf("worktree isolation keeps its trees under %s, which is inside the repository %s: set XDG_CACHE_HOME to a directory outside it", top, repo.Root())
	case inside(evalLoose(filepath.Join(o.Home, ".sleipnir")), evalLoose(dir)), inside(evalLoose(filepath.Join(o.Home, ".claude")), evalLoose(dir)):
		return fmt.Errorf("worktree isolation keeps its trees under %s, which is inside a protected configuration directory (every write there asks for approval): set XDG_CACHE_HOME to a directory outside it", top)
	}
	plan := &isoPlan{commit: o.Commit, repo: repo, top: top, dir: dir, prefix: "sleipnir/" + s.ID}
	if rel, err := filepath.Rel(realRoot, evalLoose(o.Cwd)); err == nil && rel != "." && filepath.IsLocal(rel) {
		plan.subdir = filepath.ToSlash(rel)
	}
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		plan.ownsDir = true
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("worktree isolation: %w", err)
	}
	s.iso = plan
	return nil
}

// worktreesRoot is the directory the trees of every session of this user live under.
func worktreesRoot(home string) string {
	return filepath.Join(cacheRoot(home), "sleipnir", "worktrees")
}

// cacheRoot is the per-user cache directory: the platform's own for the real home
// directory. A session run for another home (tests, the RL harness) keeps everything
// under that home instead.
func cacheRoot(home string) string {
	if h, err := os.UserHomeDir(); err == nil && home != "" && samePathLoose(h, home) {
		if c, err := os.UserCacheDir(); err == nil && c != "" {
			return c
		}
	}
	return filepath.Join(home, ".cache")
}

// sourceDirty reports whether the working tree differs from HEAD (tracked edits and
// untracked, non-ignored files): the state the workspace manager calls "dirty".
func sourceDirty(ctx context.Context, repo *gitx.Repo) (bool, error) {
	tree, err := repo.SnapshotTree(ctx)
	if err != nil {
		return false, err
	}
	head, err := repo.ResolveTree(ctx, "HEAD")
	if err != nil {
		return false, err
	}
	return tree != head, nil
}

// buildIsolation makes the workspace manager and the merge queue and returns what the
// swarm needs. It first sweeps up after sessions that died without cleaning (their
// trees, once their process is gone, are committed onto their own branches and
// removed; branches that hold work that exists nowhere else are kept and reported).
func (s *Session) buildIsolation(ctx context.Context) (*swarm.Isolation, error) {
	p := s.iso
	if p == nil {
		return nil, nil
	}
	s.pruneStale(ctx, p)

	mgr := &workspace.Manager{
		Repo: p.repo, Dir: p.dir, Prefix: p.prefix,
		// The trees start from what the person sees now, uncommitted edits included, and
		// the result is applied on top of exactly that. With Commit the checkout is clean
		// (planIsolation checked) and the base is the branch's commit.
		Snapshot: !p.commit,
		OnEvent:  workspace.EmitTo(s.Log),
	}
	q, err := workspace.NewQueue(ctx, mgr, workspace.QueueOptions{VerifyCmd: s.opts.Verify})
	if err != nil {
		if errors.Is(err, workspace.ErrNoCommits) {
			return nil, errors.New("worktree isolation starts every tree from a commit, and the repository has none yet: commit the project first")
		}
		return nil, fmt.Errorf("worktree isolation: %w", err)
	}
	p.mgr, p.queue = mgr, q
	return &swarm.Isolation{Manager: mgr, Queue: q, Commit: p.commit, Subdir: p.subdir, Checkpoints: s.treeCheckpoints}, nil
}

// pruneStale cleans up after crashed sessions of this repository. It never touches a
// tree whose owner is running, and never destroys work: uncommitted changes are
// committed onto the tree's branch first, and a branch with commits that exist nowhere
// else is kept. What it kept is worth a line to the person.
func (s *Session) pruneStale(ctx context.Context, p *isoPlan) {
	sweeper := &workspace.Manager{Repo: p.repo, Dir: p.top, Prefix: "sleipnir", OnEvent: workspace.EmitTo(s.Log)}
	pctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	rep, err := sweeper.Prune(pctx, workspace.PruneOptions{Salvage: true})
	if err != nil {
		s.tell("warn", "could not clean up worktrees left by earlier sessions: "+firstLine(err.Error(), 200))
		return
	}
	var kept []string
	for _, b := range rep.BranchesKept {
		kept = append(kept, b.Branch)
	}
	for _, a := range rep.Kept {
		if a.Path != "" {
			kept = append(kept, a.Path)
		}
	}
	if len(rep.Removed) == 0 && len(kept) == 0 {
		return
	}
	msg := fmt.Sprintf("cleaned up %d worktree(s) that earlier sessions of this repository left behind", len(rep.Removed))
	if len(kept) > 0 {
		shown := kept
		if len(shown) > 3 {
			shown = shown[:3]
		}
		msg += fmt.Sprintf("; %d branch(es) or tree(s) hold work that was never merged and were kept: %s", len(kept), strings.Join(shown, ", "))
		if len(kept) > len(shown) {
			msg += fmt.Sprintf(" and %d more", len(kept)-len(shown))
		}
		msg += " (git log <branch> shows it; sleipnir never deletes it)"
	}
	s.tell("info", msg)
}

// treeCheckpoints builds the checkpoint hooks of one worker tree: a store of its own,
// rooted at the tree, so the paths it records are the project's paths. The person's
// /rewind works on the checkout's store: what the merge queue's result changes there
// is recorded in it when it is applied (swarm.Integrate).
func (s *Session) treeCheckpoints(agent, dir string) (tools.Snapshotter, func(agent, path string)) {
	st, err := checkpoint.New(filepath.Join(s.Dir, "checkpoints-trees", agent), s.Blobs, dir)
	if err != nil {
		s.tell("warn", "no separate checkpoints for "+agent+"'s worktree: "+firstLine(err.Error(), 160))
		return s.Ckpt, s.Ckpt.After // outside its root the store records absolute paths: the edit is still recorded
	}
	st.Begin(agent + " worktree")
	return st, st.After
}

// Finish ends an isolated run: the swarm stops, the verified result reaches the
// person's checkout (as uncommitted edits, or as commits with Options.Commit), the
// trees that hold nothing unmerged are removed and the merge queue closes. The report
// says what happened; when the result could not be applied it names the branch that
// holds it and the one command that gets it. It is safe to call more than once (the
// report of the first call is returned again) and returns nil for a session that is
// not an isolated swarm, which it leaves running.
func (s *Session) Finish(ctx context.Context) *swarm.IntegrationReport {
	if s.Swarm == nil || s.iso == nil {
		return nil
	}
	s.mu.Lock()
	if s.finish != nil {
		rep := s.finish
		s.mu.Unlock()
		return rep
	}
	s.mu.Unlock()
	rep := s.Swarm.Finish(ctx)
	s.mu.Lock()
	first := s.finish == nil
	if first {
		s.finish = rep
	}
	s.mu.Unlock()
	if first && rep != nil && !rep.Applied {
		s.tell("warn", rep.Message)
	}
	return rep
}

// releaseIsolation undoes buildIsolation when the session could not be built.
func (s *Session) releaseIsolation() {
	p := s.iso
	if p == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if p.queue != nil {
		_ = p.queue.Close(ctx)
		repo := p.repo
		_ = repo.DeleteBranch(ctx, p.queue.Branch())
		_ = repo.DeleteBranch(ctx, p.prefix+"/_base")
	}
	if p.ownsDir {
		_ = os.Remove(p.dir) // only if it is empty
	}
}

// tell logs a notice and shows it through the sink.
func (s *Session) tell(level, msg string) {
	s.Log.Emit("", "notice", map[string]any{"level": level, "msg": msg})
	if s.opts.Sink != nil {
		s.opts.Sink.Notice("", level, msg)
	}
}

// evalLoose resolves symlinks in the longest existing prefix of p and appends the
// rest, so it works for paths that do not exist yet.
func evalLoose(p string) string {
	p = filepath.Clean(p)
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	parent := filepath.Dir(p)
	if parent == p {
		return p
	}
	return filepath.Join(evalLoose(parent), filepath.Base(p))
}

func samePathLoose(a, b string) bool { return evalLoose(a) == evalLoose(b) }

// inside reports whether p is dir or lies under it.
func inside(dir, p string) bool {
	rel, err := filepath.Rel(dir, p)
	if err != nil {
		return false
	}
	return rel == "." || filepath.IsLocal(rel)
}
