package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"

	"github.com/reee344/sleipnir/internal/gitx"
)

// errMissingDir is returned by verifyTreeDir when the directory is gone; callers
// then look the registration up by path instead.
var errMissingDir = errors.New("workspace: tree directory is missing")

// verifyTreeDir checks that path is a directory this manager created, and
// therefore may delete. Every check is about not deleting something else:
//
//   - the path is strictly inside Dir (lexically);
//   - it is a real directory, not a symlink, and resolving symlinks does not change
//     it (an agent that replaced its own tree with a symlink to another directory
//     must not turn Remove into a recursive delete of that directory);
//   - the repository's own record of worktrees (.git/worktrees/<id>/gitdir, which
//     the agent's tree does not contain) lists exactly this path, and that
//     administrative directory carries our marker for this path, under our prefix.
//
// The tree's own .git file is deliberately not consulted: the agent owns it and
// may have rewritten it, and nothing about deleting the directory depends on it.
func (m *Manager) verifyTreeDir(ctx context.Context, p string) (admin string, mk *marker, err error) {
	p = filepath.Clean(p)
	if !filepath.IsAbs(p) || !strictlyUnder(m.st.dirReal, p) {
		return "", nil, fmt.Errorf("%w: %s", ErrOutsideDir, p)
	}
	fi, err := os.Lstat(p)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil, errMissingDir
		}
		return "", nil, err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return "", nil, fmt.Errorf("%w: %s is a symbolic link", ErrOutsideDir, p)
	}
	if !fi.IsDir() {
		return "", nil, fmt.Errorf("%w: %s is not a directory", ErrForeign, p)
	}
	if real, err := filepath.EvalSymlinks(p); err != nil || real != p {
		return "", nil, fmt.Errorf("%w: %s reaches its location through a symbolic link", ErrOutsideDir, p)
	}
	return m.lookupMarker(p)
}

// lookupMarker finds the registration and marker of the worktree at p.
func (m *Manager) lookupMarker(p string) (string, *marker, error) {
	a := scanAdminDirs(m.st.base.CommonDir()).find(p)
	if a == nil {
		return "", nil, fmt.Errorf("%w: %s is not a worktree of this repository", ErrForeign, p)
	}
	if a.mk == nil {
		return "", nil, fmt.Errorf("%w: %s has no marker", ErrForeign, p)
	}
	if !m.ownsMarker(a.mk) || !samePath(a.mk.Path, p) {
		return "", nil, fmt.Errorf("%w: the marker of %s names another manager or path", ErrForeign, p)
	}
	return a.dir, a.mk, nil
}

// verifyMissing is verifyTreeDir for a registration whose directory is gone.
func (m *Manager) verifyMissing(p string) (string, *marker, error) {
	p = filepath.Clean(p)
	if !filepath.IsAbs(p) || !strictlyUnder(m.st.dirReal, resolveLoose(p)) {
		return "", nil, fmt.Errorf("%w: %s", ErrOutsideDir, p)
	}
	return m.lookupMarker(p)
}

// deleteTree removes a verified tree's directory and registration. git's own
// `worktree remove` is preferred; it insists that the tree's .git file points back
// at the registration, which an agent may have broken, so that file is put right
// first (it is our tree, being deleted). If git still refuses, the directory and
// the registration are deleted directly - both were verified by the caller.
func (m *Manager) deleteTree(ctx context.Context, p, admin string, force bool) error {
	if fi, err := os.Lstat(p); err == nil && fi.IsDir() && fi.Mode()&os.ModeSymlink == 0 {
		gitFile := filepath.Join(p, ".git")
		if gfi, err := os.Lstat(gitFile); err == nil && gfi.IsDir() {
			_ = os.RemoveAll(gitFile) // a replacement repository planted by the agent
		}
		_ = os.WriteFile(gitFile, []byte("gitdir: "+admin+"\n"), 0o644)
	}
	err := m.st.base.WorktreeRemove(ctx, p, force)
	if err == nil || !force {
		return err
	}
	wantParent := resolveLoose(filepath.Join(m.st.base.CommonDir(), "worktrees"))
	if filepath.Dir(resolveLoose(admin)) != wantParent {
		return err
	}
	if rerr := os.RemoveAll(p); rerr != nil {
		return errors.Join(err, rerr)
	}
	if rerr := os.RemoveAll(admin); rerr != nil {
		return errors.Join(err, rerr)
	}
	return nil
}

// sessionRefs are the refs whose contents count as "elsewhere" when deciding
// whether a branch's commits are safe: every integration branch under our prefix
// (merged work lives there) and the base snapshot branches (they are not agent
// work).
func (m *Manager) sessionRefs(ctx context.Context) []string {
	refs, err := m.st.base.Branches(ctx, m.st.prefix+"/")
	if err != nil {
		return nil
	}
	var out []string
	for _, r := range refs {
		switch path.Base(r.Name) {
		case integrationName, baseBranchName:
			out = append(out, "refs/heads/"+r.Name)
		}
	}
	return out
}

// uniqueCommits counts commits on tip that exist nowhere else (see CommitsOnlyOn).
func (m *Manager) uniqueCommits(ctx context.Context, tip string) (int, error) {
	return m.st.base.CommitsOnlyOn(ctx, tip, m.st.prefix+"/*", m.sessionRefs(ctx)...)
}

// Remove deletes the tree's directory and its registration, and its branch when
// that loses nothing. Without force it refuses (ErrDirty) a tree with uncommitted
// changes and (ErrUnmerged) a branch whose commits exist nowhere else, so a
// finished-and-merged tree goes away quietly and an abandoned one with work in it
// does not. With force everything is discarded.
//
// It refuses (ErrOutsideDir, ErrForeign) anything that is not verifiably a
// directory this manager created under its Dir, whatever the Tree says about its
// own path.
func (t *Tree) Remove(ctx context.Context, force bool) error {
	return t.m.removeTree(ctx, t, force)
}

func (m *Manager) removeTree(ctx context.Context, t *Tree, force bool) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.isRemoved() {
		return nil
	}
	if err := m.ensure(ctx); err != nil {
		return err
	}
	return m.removeLocked(ctx, t, force)
}

// discard removes a tree that failed part-way through creation.
func (m *Manager) discard(ctx context.Context, t *Tree, force bool) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return m.removeLocked(ctx, t, force)
}

func (m *Manager) removeLocked(ctx context.Context, t *Tree, force bool) error {
	admin, mk, err := m.verifyTreeDir(ctx, t.Path)
	missing := errors.Is(err, errMissingDir)
	if missing {
		admin, mk, err = m.verifyMissing(t.Path)
	}
	if err != nil {
		return err
	}
	if mk.Agent != t.Agent {
		return fmt.Errorf("%w: %s belongs to agent %s", ErrForeign, t.Path, mk.Agent)
	}
	if !force && !missing {
		if st, err := t.repo.Status(ctx); err != nil {
			return err
		} else if !st.Clean() {
			return fmt.Errorf("%w: %s (%d change(s))", ErrDirty, t.Path, len(st.Staged)+len(st.Unstaged)+len(st.Untracked)+len(st.Conflicted))
		}
	}
	if !force && t.Branch != "" {
		tip, err := m.st.base.BranchSHA(ctx, t.Branch)
		if err == nil {
			n, err := m.uniqueCommits(ctx, tip)
			if err != nil {
				return err
			}
			if n > 0 {
				return fmt.Errorf("%w: %s has %d commit(s) that exist nowhere else (merge it, or Remove with force)", ErrUnmerged, t.Branch, n)
			}
		} else if gitx.KindOf(err) != gitx.KindNotFound {
			return err
		}
	}

	m.st.lock.Lock()
	defer m.st.lock.Unlock()
	if err := m.deleteTree(ctx, t.Path, admin, force || missing); err != nil {
		return err
	}
	var errs []error
	if t.Branch != "" {
		if err := m.st.base.DeleteBranch(ctx, t.Branch); err != nil && gitx.KindOf(err) != gitx.KindNotFound {
			// "not found" would mean it is already gone; anything else is worth reporting
			if _, berr := m.st.base.BranchSHA(ctx, t.Branch); !errors.Is(berr, gitx.ErrNotFound) {
				errs = append(errs, err)
			}
		}
	}
	t.removed.Store(true)
	if cur, ok := livePaths.Load(t.Path); ok && cur.(*Manager) == m {
		livePaths.Delete(t.Path)
	}
	m.mu.Lock()
	if cur := m.trees[t.Agent]; cur == t {
		delete(m.trees, t.Agent)
	}
	m.mu.Unlock()
	m.emit(EventRemove, t.Agent, "", map[string]any{"path": t.Path, "branch": t.Branch, "force": force})
	return errors.Join(errs...)
}
