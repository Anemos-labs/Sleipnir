package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Half-created trees.
//
// A tree comes into being in two steps: git registers the worktree (its private
// administrative directory, the directory with a .git file in it, the branch), and
// then we write our ownership marker into the administrative directory. Everything
// that decides whether a tree is ours - Remove, Prune, adoption - needs that
// marker, so a process that dies between the two steps (or a git command that had
// to be killed while it was registering) leaves a registration nobody may touch.
// The name would then be taken for good: Create would answer ErrExists forever.
//
// These trees are recognizable without any marker precisely because the second
// step never happened: nothing has been checked out into them (registration uses
// --no-checkout and population comes after the marker), so the directory holds
// nothing but the .git file that points at the registration, and no agent has had
// a chance to put work in it. That is the only thing removed, and only when every
// part of the picture fits.

// orphanAge is how long a half-created tree must have existed before another
// process may take it for abandoned. Inside one process the repository lock
// already keeps a creation in progress out of anyone's way; across processes a
// creation takes milliseconds, so minutes is a wide margin.
const orphanAge = 2 * time.Minute

// halfCreated reports whether dest is a registered worktree that never got its
// marker and holds nothing but its .git file, and returns its administrative
// directory. minAge is how old the .git file must be (0: any age, for a caller
// that knows the registration is its own doing).
func (m *Manager) halfCreated(dest string, minAge time.Duration) (admin string, ok bool) {
	dest = filepath.Clean(dest)
	// only a direct child of Dir has the shape of one of our trees
	if filepath.Dir(dest) != m.st.dirReal {
		return "", false
	}
	fi, err := os.Lstat(dest)
	if err != nil || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		return "", false
	}
	if real, err := filepath.EvalSymlinks(dest); err != nil || real != dest {
		return "", false
	}
	ents, err := os.ReadDir(dest)
	if err != nil || len(ents) != 1 || ents[0].Name() != ".git" || !ents[0].Type().IsRegular() {
		return "", false
	}
	gitFile := filepath.Join(dest, ".git")
	gfi, err := os.Lstat(gitFile)
	if err != nil || !gfi.Mode().IsRegular() || gfi.Size() > 4096 {
		return "", false
	}
	if minAge > 0 && time.Since(gfi.ModTime()) < minAge {
		return "", false
	}
	// The registration is the repository's own record: it must list exactly this
	// directory, sit directly under <common dir>/worktrees, carry no marker (a marked
	// tree is somebody's, handled by the ordinary paths), and be the one the .git
	// file points at.
	a := scanAdminDirs(m.st.base.CommonDir()).find(dest)
	if a == nil || a.mk != nil {
		return "", false
	}
	if filepath.Dir(a.dir) != filepath.Join(m.st.base.CommonDir(), "worktrees") {
		return "", false
	}
	data, err := os.ReadFile(gitFile)
	if err != nil {
		return "", false
	}
	target, found := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
	if !found || !samePath(strings.TrimSpace(target), a.dir) {
		return "", false
	}
	return a.dir, true
}

// clearHalfCreated removes a tree that halfCreated recognizes, registration and
// directory. The repository lock must be held. It reports whether the name is
// free afterwards.
func (m *Manager) clearHalfCreated(ctx context.Context, dest string, minAge time.Duration) bool {
	admin, ok := m.halfCreated(dest, minAge)
	if !ok {
		return false
	}
	if err := m.st.base.WorktreeRemove(ctx, dest, true); err == nil {
		return true
	}
	// git wants a healthy registration (and refuses locked ones); what is here was
	// verified above, so it is deleted directly.
	if filepath.Dir(admin) != filepath.Join(m.st.base.CommonDir(), "worktrees") {
		return false
	}
	return os.RemoveAll(dest) == nil && os.RemoveAll(admin) == nil
}

// abandonAdd cleans up after a `git worktree add` that was interrupted (cancelled,
// timed out): git removes what it made when it is asked to stop, but a command that
// had to be killed, or that finished a moment before the signal arrived, may have
// left a registration, an empty directory or the new branch. The caller holds the
// repository lock and had checked that dest and branch did not exist, so whatever is
// there now is that command's doing. The caller's context is gone by definition, so
// this runs on its own.
func (m *Manager) abandonAdd(ctx context.Context, dest, branch, base string) {
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	if !m.clearHalfCreated(cctx, dest, 0) {
		// git makes the directory before it writes anything into it; an empty one is
		// certainly ours to remove (os.Remove refuses a directory with anything in it).
		_ = os.Remove(dest)
	}
	if branch != "" {
		// The command created it, at the base, so it holds no commit of its own; one
		// that points anywhere else is not the command's.
		if sha, err := m.st.base.BranchSHA(cctx, branch); err == nil && sha == base {
			_ = m.st.base.DeleteBranch(cctx, branch)
		}
	}
}
