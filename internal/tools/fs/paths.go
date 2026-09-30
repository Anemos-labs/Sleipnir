package fs

import (
	"errors"
	"fmt"
	iofs "io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf8"
)

const maxPathBytes = 32 << 10

var errSymlinkLoop = errors.New("too many levels of symbolic links")

// absClean makes p absolute against cwd and cleans it. A leading "~" is expanded
// because models routinely write ~/x, and silently creating a directory literally
// named "~" under the project is never what anyone wants.
func absClean(cwd, p string) (string, error) {
	if p == "" {
		return "", errors.New("path is empty")
	}
	if len(p) > maxPathBytes {
		return "", errors.New("path is too long")
	}
	if strings.IndexByte(p, 0) >= 0 {
		return "", errors.New("path contains a NUL byte")
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			p = filepath.Join(home, p[1:])
		}
	}
	if !filepath.IsAbs(p) {
		if cwd == "" {
			wd, err := os.Getwd()
			if err != nil {
				return "", err
			}
			cwd = wd
		}
		if !filepath.IsAbs(cwd) {
			abs, err := filepath.Abs(cwd)
			if err != nil {
				return "", err
			}
			cwd = abs
		}
		p = filepath.Join(cwd, p)
	}
	return filepath.Clean(p), nil
}

func splitPath(p string) []string {
	parts := strings.Split(filepath.ToSlash(p), "/")
	out := parts[:0]
	for _, s := range parts {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// realPath resolves every symlink in the absolute, clean path p, like
// realpath(3), except that a tail that does not exist yet is allowed and is
// appended lexically. That is what a write to a new file needs: the deepest
// existing ancestor decides where the file really lands, and a dangling symlink
// at the end (write-through-symlink) still resolves to its target.
//
// filepath.EvalSymlinks cannot be used because it fails on the first missing
// component.
func realPath(p string) (string, error) {
	vol := filepath.VolumeName(p)
	sep := string(filepath.Separator)
	resolved := vol + sep
	pending := splitPath(p[len(vol):])
	links := 0
	missing := false
	for len(pending) > 0 {
		c := pending[0]
		pending = pending[1:]
		switch c {
		case ".", "":
			continue
		case "..":
			resolved = filepath.Dir(resolved)
			continue
		}
		next := filepath.Join(resolved, c)
		if missing {
			resolved = next
			continue
		}
		fi, err := os.Lstat(next)
		if err != nil {
			if errors.Is(err, iofs.ErrNotExist) {
				missing = true
				resolved = next
				continue
			}
			return "", err
		}
		if fi.Mode()&iofs.ModeSymlink != 0 {
			links++
			if links > 255 {
				return "", errSymlinkLoop
			}
			target, err := os.Readlink(next)
			if err != nil {
				return "", err
			}
			if filepath.IsAbs(target) {
				resolved = filepath.VolumeName(target) + sep
			}
			pending = append(splitPath(target[len(filepath.VolumeName(target)):]), pending...)
			continue
		}
		resolved = next
	}
	return resolved, nil
}

// relWithin returns p relative to base when p is base or lies under it.
func relWithin(base, p string) (string, bool) {
	rel, err := filepath.Rel(base, p)
	if err != nil {
		return "", false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}

// resolve canonicalises a model-supplied path.
//
// The result is the symlink-resolved location, expressed under Env.Root's own
// spelling when it lies inside the project. Keeping Root's spelling matters when
// the project directory is itself reached through a symlink (macOS /tmp, bind
// mounts): permission rules, Guard leases and snapshots are all written against
// Root, and a raw realpath would make every in-project file look "outside". A
// symlink inside the project that leads out of it is reported by its real,
// outside location, so the permission engine sees the escape.
func (k *call) resolve(raw string) (string, error) {
	lex, err := absClean(k.env.Cwd, raw)
	if err != nil {
		return "", err
	}
	real, err := realPath(lex)
	if err != nil {
		return "", err
	}
	if k.env.Root == "" {
		return real, nil
	}
	root, err := absClean(k.env.Cwd, k.env.Root)
	if err != nil {
		return real, nil
	}
	realRoot, err := realPath(root)
	if err != nil {
		return real, nil
	}
	if rel, ok := relWithin(realRoot, real); ok {
		return filepath.Join(root, rel), nil
	}
	return real, nil
}

// resolveEntry is resolve for operations on a directory entry itself (deleting a
// file): if the final component is a symlink it stays the link, so removing
// "vendor/lib.go" removes the link and never the file it points to. The
// directory part is canonicalised as usual.
func (k *call) resolveEntry(raw string) (string, error) {
	lex, err := absClean(k.env.Cwd, raw)
	if err != nil {
		return "", err
	}
	if fi, err := os.Lstat(lex); err == nil && fi.Mode()&iofs.ModeSymlink != 0 {
		dir, err := k.resolve(filepath.Dir(lex))
		if err != nil {
			return "", err
		}
		return filepath.Join(dir, filepath.Base(lex)), nil
	}
	return k.resolve(raw)
}

// resolveArg is resolve with a model-facing error message.
func (k *call) resolveArg(raw string) (string, string, string) {
	return k.resolveArgWith(raw, k.resolve)
}

func (k *call) resolveArgWith(raw string, resolve func(string) (string, error)) (string, string, string) {
	if strings.TrimSpace(raw) == "" {
		return "", "", "path is required"
	}
	canon, err := resolve(raw)
	if err != nil {
		if errors.Is(err, errSymlinkLoop) {
			return "", "", fmt.Sprintf("cannot resolve %s: symlink loop", clip(raw, 200))
		}
		return "", "", fmt.Sprintf("cannot resolve %s: %s", clip(raw, 200), osReason(err))
	}
	return canon, k.display(canon), ""
}

// display renders a canonical path the way tools print it: relative to the
// working directory when inside it (so it can be pasted straight into the next
// call), absolute otherwise.
func (k *call) display(canon string) string {
	if !k.cwdInit {
		k.cwdInit = true
		if c, err := absClean(k.env.Cwd, "."); err == nil {
			k.cwdAbs = c
			if r, err := realPath(c); err == nil && r != c {
				k.cwdReal = r
			}
		}
	}
	if k.cwdAbs == "" {
		return safeName(filepath.ToSlash(canon))
	}
	if rel, ok := relWithin(k.cwdAbs, canon); ok {
		return safeName(filepath.ToSlash(rel))
	}
	if k.cwdReal != "" {
		if rel, ok := relWithin(k.cwdReal, canon); ok {
			return safeName(filepath.ToSlash(rel))
		}
	}
	return safeName(filepath.ToSlash(canon))
}

// safeName makes a path or file name safe to embed in line-oriented output.
// File names are data controlled by whoever wrote the repository: a name with a
// newline could fake extra lines in a listing (for instance one that looks like
// an instruction), so names containing control characters or invalid UTF-8 are
// shown quoted, with the offending bytes escaped.
func safeName(s string) string {
	if !needsQuoting(s) {
		return s
	}
	return strconv.Quote(s)
}

func needsQuoting(s string) bool {
	if !utf8.ValidString(s) {
		return true
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || r == '\u2028' || r == '\u2029' {
			return true
		}
	}
	return false
}

// osReason turns an OS error into the short lowercase phrase models can act on
// ("no such file or directory"), without the duplicated path of *PathError.
func osReason(err error) string {
	var pe *iofs.PathError
	if errors.As(err, &pe) {
		err = pe.Err
	}
	var le *os.LinkError
	if errors.As(err, &le) {
		err = le.Err
	}
	if errors.Is(err, errSymlinkLoop) || errors.Is(err, syscall.ELOOP) {
		return "too many levels of symbolic links"
	}
	return err.Error()
}

// clip shortens s to at most n runes for messages.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := cutRunes(s, n)
	return cut + "…"
}

// cutRunes returns the longest prefix of s holding at most n runes. Invalid
// UTF-8 bytes count as one rune each, so it never splits a valid character.
func cutRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	i, count := 0, 0
	for i < len(s) && count < n {
		_, w := utf8.DecodeRuneInString(s[i:])
		i += w
		count++
	}
	return s[:i]
}
