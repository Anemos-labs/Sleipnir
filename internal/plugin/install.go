package plugin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// IsGitSource reports whether src names a git repository rather than a directory.
func IsGitSource(src string) bool {
	return strings.HasPrefix(src, "https://") || strings.HasPrefix(src, "http://") ||
		strings.HasPrefix(src, "git@") || strings.HasPrefix(src, "ssh://") || strings.HasSuffix(src, ".git")
}

// Install copies a plugin directory, or clones a git repository, into the
// user's plugin directory under the plugin's id, and returns its manifest.
// The plugin stays off until it is turned on. An id already installed is
// refused (Remove it first). Symbolic links in a copied directory are skipped.
func Install(ctx context.Context, home, src string) (Manifest, error) {
	return InstallWith(ctx, home, src, InstallOptions{})
}

// InstallOptions are what InstallWith adds to Install.
type InstallOptions struct {
	// Replace installs over a plugin of the same id. The installed copy is
	// kept until the new one is fetched, read and checked, and is put back
	// when the new one cannot take its place.
	Replace bool
	// Check, when set, reads the fetched copy (a directory) before it is
	// installed; an error refuses it and leaves what is installed as it was.
	Check func(dir string) error
}

// InstallWith is Install with options.
func InstallWith(ctx context.Context, home, src string, o InstallOptions) (Manifest, error) {
	base := UserDir(home)
	if err := os.MkdirAll(base, 0o700); err != nil {
		return Manifest{}, err
	}
	tmp, err := os.MkdirTemp(base, ".install-")
	if err != nil {
		return Manifest{}, err
	}
	defer os.RemoveAll(tmp)
	stage := filepath.Join(tmp, "p")
	if IsGitSource(src) {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(cctx, "git", "clone", "--depth", "1", "--", src, stage)
		if out, err := cmd.CombinedOutput(); err != nil {
			return Manifest{}, fmt.Errorf("git clone %s: %v: %s", src, err, strings.TrimSpace(string(out)))
		}
		_ = os.RemoveAll(filepath.Join(stage, ".git"))
	} else {
		abs, err := filepath.Abs(src)
		if err != nil {
			return Manifest{}, err
		}
		if err := copyTree(abs, stage); err != nil {
			return Manifest{}, err
		}
	}
	m, err := LoadDir(stage)
	if err != nil {
		return Manifest{}, err
	}
	for _, b := range Builtin() {
		if b.ID == m.ID {
			return Manifest{}, fmt.Errorf("id %q is a built-in plugin's", m.ID)
		}
	}
	if o.Check != nil {
		if err := o.Check(stage); err != nil {
			return Manifest{}, err
		}
	}
	dst := filepath.Join(base, m.ID)
	if _, err := os.Stat(dst); err == nil {
		if !o.Replace {
			return Manifest{}, &InstalledError{ID: m.ID, Dir: dst}
		}
		old := filepath.Join(tmp, "old") // removed with tmp once the new copy is in place
		if err := os.Rename(dst, old); err != nil {
			return Manifest{}, err
		}
		if err := os.Rename(stage, dst); err != nil {
			_ = os.Rename(old, dst)
			return Manifest{}, err
		}
		m, err := LoadDir(dst)
		if err != nil {
			_ = os.RemoveAll(dst)
			_ = os.Rename(old, dst)
		}
		return m, err
	}
	if err := os.Rename(stage, dst); err != nil {
		return Manifest{}, err
	}
	return LoadDir(dst)
}

// InstalledError is Install's refusal of a plugin whose id is installed already.
type InstalledError struct{ ID, Dir string }

// Error says which plugin is there and what to do.
func (e *InstalledError) Error() string {
	return fmt.Sprintf("a plugin %q is already installed in %s; remove it first", e.ID, e.Dir)
}

// Remove deletes an installed plugin from the user's plugin directory.
func Remove(home, id string) (string, error) {
	if !ValidID(id) {
		return "", fmt.Errorf("%q is not a plugin id", id)
	}
	base := UserDir(home)
	ents, err := os.ReadDir(base)
	if err != nil {
		return "", fmt.Errorf("no plugin %q is installed in %s", id, base)
	}
	for _, e := range ents {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		dir := filepath.Join(base, e.Name())
		if m, err := LoadDir(dir); err == nil && m.ID == id {
			return dir, os.RemoveAll(dir)
		}
	}
	return "", fmt.Errorf("no plugin %q is installed in %s", id, base)
}

// copyTree copies the regular files and directories of src into dst.
func copyTree(src, dst string) error {
	fi, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return errors.New(src + " is not a directory")
	}
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.Name() == ".git" && d.IsDir() {
			return filepath.SkipDir
		}
		to := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(to, 0o700)
		case d.Type().IsRegular():
			return copyFile(p, to)
		}
		return nil // links and devices are not copied
	})
}

// copyFile copies one regular file, keeping its executable bit.
func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fi.Mode().Perm()&0o700|0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
