package workspace

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/reee344/sleipnir/internal/gitx"
)

// Tree is one agent's isolated working directory. Methods are safe for concurrent
// use (they serialize per tree); the agent itself edits the files directly under
// Path with the ordinary tools.
type Tree struct {
	// Path is the absolute, symlink-resolved directory: what tools.Env.Cwd and Root
	// should be for the agent.
	Path string
	// Branch is the tree's branch ("" for a detached tree).
	Branch string
	// Base is the commit the tree started from. Everything Diff, Changed and Reset
	// say is relative to it.
	Base string
	// Agent is the id the tree was created for.
	Agent string
	// Mode says how the tree was made.
	Mode Mode

	m           *Manager
	repo        *gitx.Repo
	integration bool
	ref         string

	mu      sync.Mutex // serializes operations on this tree
	removed atomic.Bool
}

func (t *Tree) isRemoved() bool { return t.removed.Load() }

// Repo returns the git handle for the tree (for callers that need Status, Log ...).
func (t *Tree) Repo() *gitx.Repo { return t.repo }

func (t *Tree) check() error {
	if t.isRemoved() {
		return ErrRemoved
	}
	return nil
}

// author is the identity of commits made on behalf of this tree's agent.
func (t *Tree) author() gitx.Author {
	return gitx.Author{Name: t.Agent, Email: t.Agent + "@sleipnir.invalid", When: t.m.now()}
}

// Head returns the commit the tree is on.
func (t *Tree) Head(ctx context.Context) (string, error) {
	if err := t.check(); err != nil {
		return "", err
	}
	return t.repo.Head(ctx)
}

// Changed lists the files that differ from Base, committed or not, including new
// untracked (but not ignored) files. A rename is reported as its two ends, the
// deleted old path and the added new path, because a scope check must see both.
// Paths are relative to the tree, slash-separated, sorted.
func (t *Tree) Changed(ctx context.Context) ([]string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.check(); err != nil {
		return nil, err
	}
	return t.repo.ChangedPaths(ctx, t.Base, "")
}

// Diff returns everything the agent changed relative to Base as one patch:
// committed and uncommitted changes and new files, binary-safe (git apply takes
// it), with renames detected. It fails with ErrTooLarge rather than return a cut
// patch; DiffWith gives control over caps.
func (t *Tree) Diff(ctx context.Context) (string, error) {
	d, err := t.DiffWith(ctx, gitx.DiffOptions{Renames: true, MaxPatchBytes: t.m.maxDiffBytes()})
	if err != nil {
		return "", err
	}
	if d.Truncated {
		return "", fmt.Errorf("%w: the patch exceeds %d bytes; use DiffWith or merge the branch", ErrTooLarge, t.m.maxDiffBytes())
	}
	return d.Patch, nil
}

// DiffWith is Diff with options and the structured result (file list, stat).
func (t *Tree) DiffWith(ctx context.Context, opts gitx.DiffOptions) (*gitx.Diff, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.check(); err != nil {
		return nil, err
	}
	return t.repo.Diff(ctx, t.Base, opts)
}

// TooLargeError lists files refused by Commit for exceeding Manager.MaxFileBytes.
type TooLargeError struct {
	Files []string
	Limit int64
}

func (e *TooLargeError) Error() string {
	list := e.Files
	more := ""
	if len(list) > 5 {
		list, more = list[:5], fmt.Sprintf(" and %d more", len(e.Files)-5)
	}
	return fmt.Sprintf("workspace: %d file(s) exceed the %d byte limit (%s%s): remove them, add them to .gitignore, or raise MaxFileBytes",
		len(e.Files), e.Limit, strings.Join(list, ", "), more)
}

func (e *TooLargeError) Is(target error) bool { return target == ErrTooLarge }

// Commit records everything the agent changed on the tree's branch and returns
// the new commit, or "" with a nil error when there was nothing to commit. The
// commit is attributed to the agent, is unsigned, and runs no hooks. Files larger
// than Manager.MaxFileBytes are refused (TooLargeError), and so are directories
// that are git repositories of their own (NestedRepoError) and unresolved conflict
// markers left by Update.
func (t *Tree) Commit(ctx context.Context, msg string) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.check(); err != nil {
		return "", err
	}
	return t.commitLocked(ctx, msg)
}

func (t *Tree) commitLocked(ctx context.Context, msg string) (string, error) {
	if strings.TrimSpace(msg) == "" {
		msg = "sleipnir: work by " + t.Agent
	}
	if err := t.checkFileSizes(ctx); err != nil {
		return "", err
	}
	sha, err := t.repo.CommitAll(ctx, msg, t.author())
	if err != nil {
		return "", err
	}
	if sha != "" {
		t.m.emit(EventCommit, t.Agent, "", map[string]any{"commit": sha})
	}
	return sha, nil
}

// checkFileSizes refuses to record what must not become history: files above the
// size limit and nested repositories.
func (t *Tree) checkFileSizes(ctx context.Context) error {
	return t.m.checkCommittable(ctx, t.repo, t.Path)
}

// Reset returns the tree to Base: tracked files are restored, commits made since
// are dropped from the branch (they stay in the reflog), untracked files are
// deleted, and a merge in progress is abandoned. Ignored files (build caches) are
// kept. The tree's checkpoint history no longer describes it afterwards: begin a
// new checkpoint.
func (t *Tree) Reset(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.check(); err != nil {
		return err
	}
	if err := t.repo.ResetHard(ctx, t.Base); err != nil {
		return err
	}
	if err := t.repo.CleanUntracked(ctx); err != nil {
		return err
	}
	t.m.emit(EventReset, t.Agent, "", map[string]any{"base": t.Base})
	return nil
}

// Dirty reports whether the tree has uncommitted changes.
func (t *Tree) Dirty(ctx context.Context) (bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.check(); err != nil {
		return false, err
	}
	ok, err := t.repo.IsClean(ctx)
	return !ok, err
}

// Update brings newer work into the tree by merging ref (typically the merge
// queue's tip) into the tree's branch. On a clean merge it returns (nil, nil).
// When the two sides conflict the tree is left in the merged, conflicted state:
// the markers are in the files for the agent to resolve with its ordinary tools,
// and the returned Conflict says which files and hunks. Finish with Commit, or
// abandon with AbortUpdate. This is the "rebase task" of the queue's conflict
// path: the agent that finished later resolves against what already landed.
func (t *Tree) Update(ctx context.Context, ref string) (*Conflict, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.check(); err != nil {
		return nil, err
	}
	// git refuses to merge over uncommitted changes, and the agent's work must not
	// be lost to a conflict: record it first.
	clean, err := t.repo.IsClean(ctx)
	if err != nil {
		return nil, err
	}
	if !clean {
		if _, err := t.commitLocked(ctx, "sleipnir: work by "+t.Agent); err != nil {
			return nil, err
		}
	}
	res, err := t.repo.Merge(ctx, gitx.MergeOptions{Ref: ref, Message: "Merge " + shortRef(ref) + " into " + t.Agent, Author: t.author()})
	if err != nil {
		return nil, err
	}
	if !res.Conflicted {
		return nil, nil
	}
	return buildConflict(ctx, t.repo, conflictOpts{agent: t.Agent, theirs: ref})
}

// AbortUpdate abandons an Update that ended in conflicts.
func (t *Tree) AbortUpdate(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.check(); err != nil {
		return err
	}
	if t.repo.InProgress() == "" {
		return nil
	}
	return t.repo.Abort(ctx)
}

func shortRef(ref string) string {
	if len(ref) >= 40 && !strings.ContainsAny(ref, "/") {
		return ref[:8]
	}
	return ref
}
