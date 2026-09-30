package workspace

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// DefaultCopyExcludes are left out of copy-mode snapshots unless they are the
// project itself. Names without a slash match at any depth.
var DefaultCopyExcludes = []string{
	".git", ".hg", ".svn", ".sleipnir",
	"node_modules", "__pycache__", ".pytest_cache", ".mypy_cache", ".DS_Store",
}

// cloneOpts configures cloneTree.
type cloneOpts struct {
	// skipTop names entries skipped at the top level only (the ".git" file of a
	// worktree that must not be overwritten).
	skipTop map[string]bool
	// excludes are names (any depth) or slash paths (from the root) to leave out.
	excludes []string
	// skipAbs are absolute directories never entered: the workspace directory
	// itself when it lives inside the source, which would otherwise be copied into
	// itself without end.
	skipAbs []string
}

func (o cloneOpts) excluded(rel, name string) bool {
	for _, e := range o.excludes {
		if strings.Contains(e, "/") {
			if rel == strings.Trim(e, "/") {
				return true
			}
		} else if name == e {
			return true
		}
	}
	return false
}

// cloneTree copies the directory tree src into the existing directory dst as
// `cp -a --reflink=auto` would, and more carefully:
//
//   - file contents are cloned copy-on-write where the filesystem can (FICLONE),
//     falling back to an in-kernel copy, so creating a tree for a large project
//     costs metadata, not bytes;
//   - modes and modification times are preserved (git's stat cache and the
//     "unchanged" verdict depend on them), and ownership when we may (root);
//   - symlinks are never followed, and are rewritten when they would point out of
//     the copy or back into the source (see rewriteLink), because a link that
//     resolved inside the source but resolves elsewhere in the copy is a way out
//     of the tree;
//   - sockets, devices and pipes are skipped;
//   - entries are visited in name order, so the result is deterministic.
//
// It fails on anything it cannot read: a snapshot that silently lacks files is
// worse than none.
func cloneTree(ctx context.Context, src, dst string, o cloneOpts) error {
	src, dst = filepath.Clean(src), filepath.Clean(dst)
	var n int
	var walk func(rel string) error
	walk = func(rel string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		dir := filepath.Join(src, filepath.FromSlash(rel))
		ents, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, e := range ents {
			name := e.Name()
			childRel := path.Join(rel, name)
			if rel == "" && o.skipTop[name] {
				continue
			}
			if o.excluded(childRel, name) {
				continue
			}
			from := filepath.Join(src, filepath.FromSlash(childRel))
			skip := false
			for _, a := range o.skipAbs {
				if from == a {
					skip = true
				}
			}
			if skip {
				continue
			}
			to := filepath.Join(dst, filepath.FromSlash(childRel))
			fi, err := os.Lstat(from)
			if err != nil {
				return err
			}
			n++
			if n%256 == 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			switch mode := fi.Mode(); {
			case mode&os.ModeSymlink != 0:
				target, err := os.Readlink(from)
				if err != nil {
					return err
				}
				if err := os.Symlink(rewriteLink(src, childRel, target), to); err != nil {
					return err
				}
				chownLike(to, fi)
			case mode.IsDir():
				// Create private, fill, then apply the real mode: a source directory
				// without owner write permission must still be populated.
				if err := os.Mkdir(to, 0o700); err != nil && !os.IsExist(err) {
					return err
				}
				if err := walk(childRel); err != nil {
					return err
				}
				if err := os.Chmod(to, mode.Perm()|(mode&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky))); err != nil {
					return err
				}
				chownLike(to, fi)
			case mode.IsRegular():
				if err := copyFile(from, to, fi); err != nil {
					return fmt.Errorf("copying %s: %w", childRel, err)
				}
			default:
				// socket, device, named pipe: not project content
			}
		}
		return nil
	}
	return walk("")
}

// rewriteLink returns the target a copied symlink should have. relLink is the
// link's path relative to the source root, target its original text.
//
//   - A relative target that stays inside the tree is kept as is: it means the same
//     thing in the copy.
//   - A relative target that climbs out of the source is replaced by the absolute
//     path it named originally. Kept relative, it would resolve against the copy's
//     location instead, and could land in a sibling agent's tree.
//   - An absolute target inside the source is made relative, so the copy does not
//     write through to the original.
//   - Any other absolute target is kept.
func rewriteLink(srcRoot, relLink, target string) string {
	linkDir := path.Dir(relLink)
	if filepath.IsAbs(target) {
		rel, err := filepath.Rel(srcRoot, target)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return target
		}
		back, err := filepath.Rel(filepath.FromSlash(linkDir), rel)
		if err != nil {
			return target
		}
		return back
	}
	joined := path.Clean(path.Join(linkDir, filepath.ToSlash(target)))
	if joined == ".." || strings.HasPrefix(joined, "../") {
		return filepath.Join(srcRoot, filepath.FromSlash(linkDir), target)
	}
	return target
}

// copyFile copies one regular file, cloning where possible.
func copyFile(from, to string, fi os.FileInfo) error {
	in, err := openNoFollow(from)
	if err != nil {
		return err
	}
	defer in.Close()
	// The Lstat that classified this entry and this open are two moments: refuse
	// if the file has been swapped for something else in between.
	if ofi, err := in.Stat(); err != nil || !os.SameFile(ofi, fi) {
		return fmt.Errorf("%s changed while being copied", from)
	}
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if err := cloneContents(out, in); err != nil {
		out.Close()
		os.Remove(to)
		return err
	}
	if err := out.Chmod(fi.Mode().Perm() | (fi.Mode() & (os.ModeSetuid | os.ModeSetgid | os.ModeSticky))); err != nil {
		out.Close()
		os.Remove(to)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(to)
		return err
	}
	if err := os.Chtimes(to, fi.ModTime(), fi.ModTime()); err != nil {
		return err
	}
	chownLike(to, fi)
	return nil
}

// cloneContents copies in to out: a reflink when the platform and filesystem
// allow, otherwise io.Copy (which uses copy_file_range where it exists).
func cloneContents(out, in *os.File) error {
	if tryReflink(out, in) {
		return nil
	}
	if _, err := out.Seek(0, io.SeekStart); err != nil {
		return err
	}
	_, err := io.Copy(out, in)
	return err
}

// copyIndexFile copies a git index file byte for byte (atomically).
func copyIndexFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".index-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, dst); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// copyExtras copies the CreateOptions.Copy paths from the source work tree into t.
// A path that does not exist in the source is skipped (an optional .env). The
// destination is never reached through a symbolic link: a tracked symlink in the
// tree that points outside must not let a copy write there.
func (m *Manager) copyExtras(ctx context.Context, t *Tree, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	srcRoot := m.st.source
	if m.st.mode == ModeWorktree {
		if m.Repo.IsBare() {
			return fmt.Errorf("%w: a bare repository has no work tree to copy from", ErrUnsupported)
		}
		srcRoot = m.Repo.Root()
	}
	for _, p := range paths {
		rel := path.Clean(filepath.ToSlash(p))
		if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") || path.IsAbs(rel) || strings.ContainsRune(rel, 0) {
			return fmt.Errorf("%w: copy path %q must be relative and inside the project", ErrBadName, printable(p))
		}
		if rel == ".git" || strings.HasPrefix(rel, ".git/") {
			return fmt.Errorf("%w: copy path %q is inside .git", ErrBadName, printable(p))
		}
		from := filepath.Join(srcRoot, filepath.FromSlash(rel))
		fi, err := os.Lstat(from)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if err := ensurePlainParents(t.Path, rel); err != nil {
			return err
		}
		to := filepath.Join(t.Path, filepath.FromSlash(rel))
		if _, err := os.Lstat(to); err == nil {
			continue // already present in the base: the tree's own version wins
		}
		switch {
		case fi.IsDir():
			if err := os.Mkdir(to, 0o700); err != nil {
				return err
			}
			if err := cloneTree(ctx, from, to, cloneOpts{excludes: []string{".git"}}); err != nil {
				return err
			}
			_ = os.Chmod(to, fi.Mode().Perm())
		case fi.Mode().IsRegular():
			if err := copyFile(from, to, fi); err != nil {
				return err
			}
		case fi.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(from)
			if err != nil {
				return err
			}
			if err := os.Symlink(rewriteLink(srcRoot, rel, target), to); err != nil {
				return err
			}
		}
	}
	return nil
}

// ensurePlainParents creates the parent directories of rel under root, refusing to
// pass through a symbolic link.
func ensurePlainParents(root, rel string) error {
	dir := root
	parts := strings.Split(path.Dir(rel), "/")
	if len(parts) == 1 && parts[0] == "." {
		return nil
	}
	for _, part := range parts {
		dir = filepath.Join(dir, part)
		fi, err := os.Lstat(dir)
		switch {
		case err == nil && fi.Mode()&os.ModeSymlink != 0:
			return fmt.Errorf("%w: %s is a symbolic link", ErrOutsideDir, dir)
		case err == nil && !fi.IsDir():
			return fmt.Errorf("%w: %s is not a directory", ErrExists, dir)
		case err == nil:
		case os.IsNotExist(err):
			if err := os.Mkdir(dir, 0o755); err != nil {
				return err
			}
		default:
			return err
		}
	}
	return nil
}
