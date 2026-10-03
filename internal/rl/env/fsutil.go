package env

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// This file holds the filesystem primitives every other part of the package
// relies on to stay inside the directories it was given. The recurring
// hazard is a symbolic link planted by the agent: a workspace is attacker
// controlled data, so any path taken from it, and any directory walked in it,
// is treated as untrusted.

// gitLike reports whether a path component could be interpreted as git's
// control directory by git itself or by the filesystem it lands on: ".git" in
// any case, the NTFS short name "git~1", names with trailing dots or spaces
// (NTFS ignores them), NTFS alternate data streams, and names that only differ
// by code points HFS+ ignores. A diff or hidden file naming such a component
// could overwrite hooks or configuration, so all of them are rejected.
func gitLike(name string) bool {
	n := strings.ToLower(name)
	n = strings.Map(func(r rune) rune {
		switch {
		case r >= 0x200c && r <= 0x200f, r >= 0x202a && r <= 0x202e, r >= 0x206a && r <= 0x206f, r == 0xfeff:
			return -1 // ignorable on HFS+
		}
		return r
	}, n)
	n = strings.TrimRight(n, ". ")
	return n == ".git" || n == "git~1" || strings.HasPrefix(n, ".git:")
}

// validRelPath rejects anything that is not a plain, canonical, relative,
// slash-separated path that stays inside its base directory and avoids git's
// control names.
func validRelPath(p string) error {
	switch {
	case p == "":
		return errors.New("empty path")
	case !utf8.ValidString(p):
		return errors.New("path is not valid UTF-8")
	case strings.ContainsRune(p, 0):
		return errors.New("path contains a NUL byte")
	case len(p) > 4096:
		return errors.New("path is too long")
	case strings.HasPrefix(p, "/") || filepath.IsAbs(p) || filepath.VolumeName(p) != "":
		return errors.New("path is absolute")
	}
	if strings.Contains(p, "\\") {
		return errors.New("path contains a backslash")
	}
	if path.Clean(p) != p {
		return errors.New("path is not canonical (dot segments, empty segments or a trailing slash)")
	}
	for _, c := range strings.Split(p, "/") {
		switch {
		case c == "..":
			return errors.New("path has a '..' segment")
		case len(c) > 255:
			return errors.New("path component is too long")
		case gitLike(c):
			return errors.New("path has a .git component")
		}
	}
	return nil
}

// atomicWriteFile writes data to path through a temporary file in the same
// directory, so a reader (or a crash) never sees a half-written file.
func atomicWriteFile(p string, data []byte, perm fs.FileMode) error {
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(p)+"-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(name)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, p); err != nil {
		return err
	}
	ok = true
	return nil
}

// ---- writing inside a root without ever following a symlink out of it ----

// ensureDirIn creates rel (and its parents) below root, replacing anything that
// is not a real directory. Replacing rather than following is deliberate:
// when the agent's diff planted a symlink where a hidden file's directory has
// to be, following it would write outside the checkout, and refusing would let
// the agent block the verifier by planting such a link.
func ensureDirIn(root *os.Root, rel string) error {
	if rel == "" || rel == "." {
		return nil
	}
	cur := ""
	for _, part := range strings.Split(rel, "/") {
		cur = path.Join(cur, part)
		fi, err := root.Lstat(cur)
		switch {
		case err == nil && fi.IsDir():
			continue
		case err == nil:
			if err := removeIn(root, cur); err != nil {
				return err
			}
		case errors.Is(err, fs.ErrNotExist):
		default:
			return err
		}
		if err := root.Mkdir(cur, 0o755); err != nil {
			return err
		}
	}
	return nil
}

// removeIn removes rel below root: a file or symlink is unlinked, a directory
// is emptied recursively. Symlinks are never followed.
func removeIn(root *os.Root, rel string) error {
	fi, err := root.Lstat(rel)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return root.Remove(rel)
	}
	d, err := root.Open(rel)
	if err != nil {
		return err
	}
	ents, err := d.ReadDir(-1)
	_ = d.Close()
	if err != nil {
		return err
	}
	for _, e := range ents {
		if err := removeIn(root, path.Join(rel, e.Name())); err != nil {
			return err
		}
	}
	return root.Remove(rel)
}

// createFileIn creates rel below root for writing, replacing any existing
// file, symlink or directory at that path, and returns the open file. The final
// open is O_EXCL, so even a racing symlink cannot redirect the write. mode is
// applied with fchmod on the descriptor rather than chmod on the path: the
// umask may have removed bits the caller asked for (the exec bit of a hidden
// script), and a path-based chmod would be a second chance for a swapped-in
// symlink.
func createFileIn(root *os.Root, rel string, mode fs.FileMode) (*os.File, error) {
	if err := validRelPath(rel); err != nil {
		return nil, fmt.Errorf("%s: %w", rel, err)
	}
	if dir := path.Dir(rel); dir != "." {
		if err := ensureDirIn(root, dir); err != nil {
			return nil, err
		}
	}
	if err := removeIn(root, rel); err != nil {
		return nil, err
	}
	f, err := root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode.Perm())
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(mode.Perm()); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// writeFileIn creates rel below root with the given content (see createFileIn).
func writeFileIn(root *os.Root, rel string, data []byte, mode fs.FileMode) error {
	f, err := createFileIn(root, rel, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// ---- removal ----

// removeAllNoFollow deletes path and everything below it. Unlike os.RemoveAll it
// first makes directories writable, because tool caches such as the Go module
// cache are created read-only on purpose and would otherwise survive cleanup.
// Symbolic links are removed, never followed, so a link to /home cannot make a
// workspace cleanup delete the target's contents.
func removeAllNoFollow(p string) error {
	fi, err := os.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return os.Remove(p)
	}
	var last error
	for attempt := 0; attempt < 4; attempt++ {
		makeWritable(p)
		if last = os.RemoveAll(p); last == nil {
			return nil
		}
		// A straggler process (or a slow filesystem) may still be creating files;
		// give it a moment and try again.
		time.Sleep(time.Duration(attempt+1) * 25 * time.Millisecond)
	}
	return last
}

// makeWritable adds owner rwx to every real directory below p. Entries are
// changed through a descriptor opened with O_NOFOLLOW where the platform has
// one, so a symlink swapped in during the walk cannot redirect the chmod.
func makeWritable(p string) {
	_ = filepath.WalkDir(p, func(cur string, d fs.DirEntry, err error) error {
		if d != nil && d.IsDir() {
			if fi, e := d.Info(); e == nil && fi.Mode().Perm()&0o700 != 0o700 {
				_ = chmodDirNoFollow(cur, fi.Mode().Perm()|0o700)
			}
		}
		return nil // keep going whatever happens
	})
}

// ---- copy-on-write tree cloning ----

// cloner copies directory trees, preferring the cheapest strategy the host
// supports: reflink clones (btrfs, xfs, APFS) make a per-rollout copy of a
// multi-gigabyte dependency tree nearly free, plain "cp -a" is the universal
// fallback, and a pure Go copy covers hosts without a usable cp. The winning
// strategy is remembered so a missing flag is only discovered once.
type cloner struct {
	strategy atomic.Int32 // index of the strategy that last worked
}

// cloneCommands returns platform-specific directory-copy commands in preference order, trying
// filesystem cloning before ordinary copying where supported.
func cloneCommands(src, dst string) [][]string {
	s, d := src+string(filepath.Separator)+".", dst+string(filepath.Separator)
	switch runtime.GOOS {
	case "linux":
		return [][]string{
			{"cp", "-a", "--reflink=auto", "--", s, d},
			{"cp", "-a", "--", s, d},
		}
	case "darwin":
		return [][]string{
			{"cp", "-a", "-c", s, d}, // clonefile on APFS
			{"cp", "-a", s, d},
		}
	default:
		return [][]string{{"cp", "-a", s, d}}
	}
}

// clone makes dst an independent copy of src. dst must not exist.
func (c *cloner) clone(ctx context.Context, src, dst string) error {
	fi, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("clone: %s is not a directory", src)
	}
	if _, err := os.Lstat(dst); err == nil {
		return fmt.Errorf("clone: destination %s already exists", dst)
	}
	cmds := cloneCommands(src, dst)
	start := int(c.strategy.Load())
	if start >= len(cmds) {
		start = 0
	}
	if bin, err := exec.LookPath("cp"); err == nil {
		for i := start; i < len(cmds); i++ {
			if err := os.MkdirAll(dst, 0o700); err != nil {
				return err
			}
			cmd := exec.CommandContext(ctx, bin, cmds[i][1:]...)
			cmd.Env = []string{"LC_ALL=C", "PATH=/usr/bin:/bin"}
			out, err := cmd.CombinedOutput()
			if err == nil {
				// cp -a copies the permissions of "src/." onto dst.
				c.strategy.Store(int32(i))
				return nil
			}
			if ctx.Err() != nil {
				_ = removeAllNoFollow(dst)
				return ctx.Err()
			}
			_ = removeAllNoFollow(dst)
			_ = out // the next strategy (or the Go fallback) tries again
		}
	}
	return copyTreeGo(ctx, src, dst)
}

// copyTreeGo is the portable fallback: it copies regular files, directories and
// symlinks (as symlinks) and skips devices, sockets and FIFOs, which never
// belong in a task workspace.
func copyTreeGo(ctx context.Context, src, dst string) error {
	type dirMode struct {
		path string
		mode fs.FileMode
		mod  time.Time
	}
	var dirs []dirMode
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			dirs = append(dirs, dirMode{target, info.Mode().Perm(), info.ModTime()})
		case d.Type()&fs.ModeSymlink != 0:
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case d.Type().IsRegular():
			return copyFile(p, target, info)
		default:
			// device, socket, FIFO: not copied
		}
		return nil
	})
	if err != nil {
		return err
	}
	// Directory modes and times are applied last, deepest first, so read-only
	// directories can still be filled and their mtimes survive.
	for i := len(dirs) - 1; i >= 0; i-- {
		_ = os.Chmod(dirs[i].path, dirs[i].mode)
		_ = os.Chtimes(dirs[i].path, dirs[i].mod, dirs[i].mod)
	}
	return nil
}

func copyFile(src, dst string, info fs.FileInfo) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := os.Chmod(dst, info.Mode().Perm()); err != nil {
		return err
	}
	return os.Chtimes(dst, info.ModTime(), info.ModTime())
}
