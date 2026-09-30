package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

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
//   - its .git is a plain file pointing into <common dir>/worktrees/ (git's own
//     layout for linked worktrees);
//   - that administrative directory carries our marker, for this path, under our
//     prefix.
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
	gitFile := filepath.Join(p, ".git")
	gfi, err := os.Lstat(gitFile)
	if err != nil || !gfi.Mode().IsRegular() {
		return "", nil, fmt.Errorf("%w: %s has no .git file", ErrForeign, p)
	}
	data, err := os.ReadFile(gitFile)
	if err != nil || len(data) > 4096 {
		return "", nil, fmt.Errorf("%w: unreadable .git file in %s", ErrForeign, p)
	}
	line := strings.TrimSpace(string(data))
	gd, ok := strings.CutPrefix(line, "gitdir:")
	if !ok {
		return "", nil, fmt.Errorf("%w: %s/.git is not a gitdir file", ErrForeign, p)
	}
	gd = strings.TrimSpace(gd)
	if !filepath.IsAbs(gd) {
		gd = filepath.Join(p, gd)
	}
	gd = resolveLoose(gd)
	wantParent := resolveLoose(filepath.Join(m.st.base.CommonDir(), "worktrees"))
	if filepath.Dir(gd) != wantParent {
		return "", nil, fmt.Errorf("%w: %s belongs to a different repository", ErrForeign, p)
	}
	mk, err = readMarker(gd)
	if err != nil {
		return "", nil, fmt.Errorf("%w: %s has no valid marker (%v)", ErrForeign, p, err)
	}
	if !m.ownsMarker(mk) || !samePath(mk.Path, p) {
		return "", nil, fmt.Errorf("%w: marker of %s names another manager or path", ErrForeign, p)
	}
	return gd, mk, nil
}

// verifyMissing is verifyTreeDir for a registration whose directory is gone: the
// admin directory is found by the path it recorded.
func (m *Manager) verifyMissing(p string) (string, *marker, error) {
	p = filepath.Clean(p)
	if !filepath.IsAbs(p) || !strictlyUnder(m.st.dirReal, resolveLoose(p)) {
		return "", nil, fmt.Errorf("%w: %s", ErrOutsideDir, p)
	}
	a := scanAdminDirs(m.st.base.CommonDir()).find(p)
	if a == nil || a.mk == nil || !m.ownsMarker(a.mk) || !samePath(a.mk.Path, p) {
		return "", nil, fmt.Errorf("%w: no registration of ours for %s", ErrForeign, p)
	}
	return a.dir, a.mk, nil
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
	_, mk, err := m.verifyTreeDir(ctx, t.Path)
	missing := errors.Is(err, errMissingDir)
	if missing {
		_, mk, err = m.verifyMissing(t.Path)
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
	if err := m.st.base.WorktreeRemove(ctx, t.Path, force || missing); err != nil {
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
	m.mu.Lock()
	if cur := m.trees[t.Agent]; cur == t {
		delete(m.trees, t.Agent)
	}
	m.mu.Unlock()
	m.emit(EventRemove, t.Agent, "", map[string]any{"path": t.Path, "branch": t.Branch, "force": force})
	return errors.Join(errs...)
}
