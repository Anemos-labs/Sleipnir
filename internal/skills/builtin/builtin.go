// Package builtin holds the skills that ship inside the Sleipnir binary and
// puts them on disk, where the skills loader reads them like any other skill
// directory.
//
// Built-in skills follow the same progressive disclosure as user skills: only
// their one-line descriptions join the listing in the shared prompt layer, and a
// body is read when the model loads it with the skill tool. They have the lowest
// precedence, so a project or user skill of the same name replaces one.
//
// The files are written once per distinct content under
// <stateDir>/cache/skills/<digest>/builtin, so every skill's directory is a
// stable absolute path for the lifetime of that content, and two binaries with
// different skills never share a directory.
package builtin

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"

	"github.com/anemos-labs/sleipnir/internal/skills"
)

//go:embed all:data
var files embed.FS

// root is the directory of files that holds one directory per skill.
const root = "data"

// Label is the name the materialized directory carries, which the loader shows
// as the source of each built-in skill ("builtin/blender/SKILL.md").
const Label = "builtin"

// FS returns the built-in skills: one directory per skill, each with a
// SKILL.md.
func FS() fs.FS {
	sub, err := fs.Sub(files, root)
	if err != nil {
		panic(err) // the embedded tree always has its root
	}
	return sub
}

// Digest identifies the embedded content: the hex SHA-256 of every file's path
// and bytes, in path order.
func Digest() string {
	h := sha256.New()
	for _, p := range paths() {
		b, _ := fs.ReadFile(FS(), p)
		sum := sha256.Sum256(b)
		h.Write([]byte(p))
		h.Write([]byte{0})
		h.Write(sum[:])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// paths lists the embedded regular files in path order.
func paths() []string {
	var out []string
	_ = fs.WalkDir(FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			out = append(out, p)
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// Materialize writes the built-in skills under stateDir (normally ~/.sleipnir)
// and returns the directory that holds them. Content that is already there is
// reused; a directory is published by an atomic rename, so a concurrent session
// sees either nothing or the complete tree.
func Materialize(stateDir string) (string, error) {
	if stateDir == "" {
		return "", errors.New("builtin skills: no state directory")
	}
	base := filepath.Join(stateDir, "cache", "skills", Digest()[:16])
	dir := filepath.Join(base, Label)
	if complete(dir) {
		return dir, nil
	}
	if err := os.MkdirAll(filepath.Dir(base), 0o700); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(base), ".tmp-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	for _, p := range paths() {
		b, err := fs.ReadFile(FS(), p)
		if err != nil {
			return "", err
		}
		dst := filepath.Join(tmp, Label, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(dst, b, 0o644); err != nil {
			return "", err
		}
	}
	if err := os.Rename(tmp, base); err != nil {
		if complete(dir) { // another session published the same content first
			return dir, nil
		}
		// A partial tree from an interrupted older layout: replace it.
		if rmErr := os.RemoveAll(base); rmErr != nil {
			return "", err
		}
		if err := os.Rename(tmp, base); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// complete reports whether dir holds exactly the embedded files with their
// embedded bytes: a file added to the directory (a skill planted in a trusted
// location) makes it incomplete as surely as one changed or removed.
func complete(dir string) bool {
	want := paths()
	n := 0
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir():
			return nil
		case !d.Type().IsRegular():
			return errors.New("not a regular file")
		}
		n++
		return nil
	})
	if err != nil || n != len(want) {
		return false
	}
	for _, p := range want {
		want, _ := fs.ReadFile(FS(), p)
		got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(p)))
		if err != nil || string(got) != string(want) {
			return false
		}
	}
	return true
}

// Extra materializes the built-in skills and returns them as a trusted skills
// directory for skills.Opts.Extra. The error says why they are unavailable;
// callers report it and carry on without them.
func Extra(stateDir string) (skills.Extra, error) {
	dir, err := Materialize(stateDir)
	if err != nil {
		return skills.Extra{}, err
	}
	return skills.Extra{Dir: dir, Trusted: true}, nil
}

// Names lists the built-in skills by directory name, in order.
func Names() []string {
	entries, _ := fs.ReadDir(FS(), ".")
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			if _, err := fs.Stat(FS(), path.Join(e.Name(), skills.FileName)); err == nil {
				out = append(out, e.Name())
			}
		}
	}
	return out
}
