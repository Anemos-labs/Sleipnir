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
	dst := filepath.Join(base, m.ID)
	if _, err := os.Stat(dst); err == nil {
		return Manifest{}, fmt.Errorf("a plugin %q is already installed in %s; remove it first", m.ID, dst)
	}
	if err := os.Rename(stage, dst); err != nil {
		return Manifest{}, err
	}
	return LoadDir(dst)
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
