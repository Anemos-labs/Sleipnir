package checkpoint

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// confined performs a rewind's changes to one path inside the project through an
// os.Root opened on the project root: every directory on the way is resolved
// relative to the root at the moment of the operation, so a directory replaced
// (by a symlink, say) after the path was checked cannot lead a write, a rename
// or a removal outside the project. Recorded absolute paths, which lie outside
// any root, are not confined and use the plain pathname functions.
type confined struct {
	root *os.Root // nil for an absolute key
	rel  string   // the key, relative to root
	abs  string   // the resolved pathname (resolveKey)
}

// confine returns the confined view of key, whose resolved pathname is abs. The
// caller closes it.
func (s *Store) confine(key, abs string) (*confined, error) {
	p := filepath.FromSlash(key)
	if filepath.IsAbs(p) {
		return &confined{abs: abs}, nil
	}
	root, err := os.OpenRoot(s.realRoot)
	if err != nil {
		return nil, err
	}
	return &confined{root: root, rel: p, abs: abs}, nil
}

// close releases the root.
func (c *confined) close() {
	if c.root != nil {
		c.root.Close()
	}
}

// remove deletes the path's entry.
func (c *confined) remove() error {
	if c.root == nil {
		return os.Remove(c.abs)
	}
	return c.root.Remove(c.rel)
}

// mkdirParents creates the path's missing parent directories.
func (c *confined) mkdirParents() error {
	if c.root == nil {
		return os.MkdirAll(filepath.Dir(c.abs), 0o755)
	}
	if d := filepath.Dir(c.rel); d != "." {
		return c.root.MkdirAll(d, 0o755)
	}
	return nil
}

// chmodLooked runs between chmod's look at the path and its change of the mode: a test swaps the path there.
var chmodLooked = func(rel string) {}

// chmod changes the path's mode, refusing a symlink (as chmodNoFollow). The set-ID bits are kept only on what is a directory
// now: what the path was when the rewind was planned may have changed since.
func (c *confined) chmod(mode fs.FileMode) error {
	if c.root == nil {
		if fi, err := os.Lstat(c.abs); err != nil || !fi.IsDir() {
			mode = restorableMode(mode)
		}
		return chmodNoFollow(c.abs, mode)
	}
	fi, err := c.root.Lstat(c.rel)
	if err != nil {
		return err
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return errors.New("the path is a symlink")
	}
	if !fi.IsDir() {
		mode = restorableMode(mode)
	}
	chmodLooked(c.rel)
	if mode == restorableMode(mode) {
		return c.root.Chmod(c.rel, mode)
	}
	// Set-ID bits go only on the directory that was looked at: change the mode through a descriptor whose own type is checked, so
	// a file swapped in for the directory after the Lstat does not get them.
	f, err := c.root.OpenFile(c.rel, openReadFlags, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	if dfi, err := f.Stat(); err != nil || !dfi.IsDir() || !os.SameFile(fi, dfi) {
		return errors.New("the path changed while it was restored")
	}
	return f.Chmod(mode)
}

// mkdirAll creates the path as a directory, with any missing parents.
func (c *confined) mkdirAll() error {
	if c.root == nil {
		return os.MkdirAll(c.abs, 0o755)
	}
	return c.root.MkdirAll(c.rel, 0o755)
}

// chown gives the path the recorded owner, best effort (as chownPath).
func (c *confined) chown(own *owner) {
	if c.root == nil {
		chownPath(c.abs, own)
		return
	}
	if own != nil {
		_ = c.root.Lchown(c.rel, own.UID, own.GID)
	}
}

// tmpName is a fresh name beside the path for a file that is renamed over it.
func (c *confined) tmpName() (string, error) {
	var rnd [6]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return "", err
	}
	name := ".sleipnir-tmp-" + hex.EncodeToString(rnd[:])
	if c.root == nil {
		return filepath.Join(filepath.Dir(c.abs), name), nil
	}
	return filepath.Join(filepath.Dir(c.rel), name), nil
}

// writeFile replaces the path with data, atomically, like writeFileAtomic.
func (c *confined) writeFile(data []byte, mode fs.FileMode, own *owner) error {
	if c.root == nil {
		return writeFileAtomic(c.abs, data, mode, own)
	}
	tmp, err := c.tmpName()
	if err != nil {
		return err
	}
	f, err := c.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil && own != nil {
		_ = c.root.Lchown(tmp, own.UID, own.GID) // best effort, as chownPath
	}
	if werr == nil {
		werr = c.root.Chmod(tmp, restorableMode(mode))
	}
	if werr == nil {
		werr = c.root.Rename(tmp, c.rel)
	}
	if werr != nil {
		_ = c.root.Remove(tmp)
	}
	return werr
}

// symlink replaces the path with a symlink to target, atomically.
func (c *confined) symlink(target string, own *owner) error {
	if c.root == nil {
		return symlinkAtomic(target, c.abs, own)
	}
	tmp, err := c.tmpName()
	if err != nil {
		return err
	}
	if err := c.root.Symlink(target, tmp); err != nil {
		return err
	}
	if own != nil {
		_ = c.root.Lchown(tmp, own.UID, own.GID)
	}
	if err := c.root.Rename(tmp, c.rel); err != nil {
		_ = c.root.Remove(tmp)
		return err
	}
	return nil
}

// restorableMode is a recorded file mode without the set-user-ID and
// set-group-ID bits. The manifest is not authenticated: with them, recorded
// content restored by a privileged process (and given the recorded owner) would
// become a program that runs with that owner's rights.
func restorableMode(m fs.FileMode) fs.FileMode {
	return m &^ (fs.ModeSetuid | fs.ModeSetgid)
}
