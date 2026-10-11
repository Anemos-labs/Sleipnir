// Package dirs completes the folder a person types for a new session, the way a shell completes a path, and says what the folder is:
// whether it exists, whether it is a git repository, whether it is empty, and what its files suggest to check a change with.
//
// Only folder names are read, never file names or contents, and every read is bounded: a folder with more entries than maxScan is
// listed from its first maxScan, and a listing offers at most MaxEntries. A name that holds a control character, a character that
// reorders text, or bytes that are not UTF-8 is never offered (it can still be typed), so that what the page draws is what the name is.
package dirs

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

const (
	// MaxEntries is the most subfolders one listing offers.
	MaxEntries = 60
	// maxScan is the most entries of one folder that are read to find them.
	maxScan = 5000
	// maxPath is the longest path taken, in bytes.
	maxPath = 4096
	// maxUp is how many levels above a folder are looked at for a git repository.
	maxUp = 64
)

// ErrEmpty is a path with nothing in it.
var ErrEmpty = errors.New("the path is empty")

// ErrBad is a path that cannot be read as one (it is too long, holds a NUL byte or bytes that are not UTF-8, or names another
// person's home folder, which is not supported).
var ErrBad = errors.New("that is not a path")

// Options say what a leading ~ stands for and what a relative path is relative to.
type Options struct {
	// Home is the home folder; without it a path that starts with ~ is refused.
	Home string
	// Base is the folder a relative path is relative to (the folder the server was started in, as --cwd is to a process).
	Base string
}

// markers are the files that say what a project is built and tested with, in the order they are suggested.
var markers = []struct{ file, cmd string }{
	{"go.mod", "go test {dirs}"},
	{"Cargo.toml", "cargo test"},
	{"package.json", "npm test"},
	{"pyproject.toml", "pytest"},
	{"pytest.ini", "pytest"},
	{"setup.py", "pytest"},
	{"pom.xml", "mvn test"},
	{"build.gradle", "gradle test"},
	{"build.gradle.kts", "gradle test"},
	{"Makefile", "make test"},
}

// endsWithSep reports whether s ends in a path separator of either kind (a Windows path is typed with either).
func endsWithSep(s string) bool {
	return s != "" && (s[len(s)-1] == '/' || s[len(s)-1] == filepath.Separator)
}

// Expand turns what was typed into an absolute, clean path: a leading ~ is the home folder, a relative path is relative to o.Base.
func Expand(text string, o Options) (string, error) {
	s := strings.TrimSpace(text)
	switch {
	case s == "":
		return "", ErrEmpty
	case len(s) > maxPath || strings.ContainsRune(s, 0) || !utf8.ValidString(s):
		return "", ErrBad
	}
	if s[0] == '~' {
		rest := s[1:]
		if rest != "" && rest[0] != '/' && rest[0] != filepath.Separator {
			return "", errors.New("~name is not supported: write the path out")
		}
		if o.Home == "" {
			return "", errors.New("the home folder is not known")
		}
		s = filepath.Join(o.Home, rest)
	}
	if !filepath.IsAbs(s) {
		if o.Base == "" {
			return "", errors.New("a relative path needs a folder to start from: write it in full")
		}
		s = filepath.Join(o.Base, s)
	}
	return filepath.Clean(s), nil
}

// Complete lists the subfolders that complete text and says what text names. A path that ends in a separator (or is ~, or ends in
// ..) is a folder whose subfolders are all listed; with any other last part, the folders of its parent that start with that part
// are listed (a lone . starts the names of the hidden folders).
func Complete(text string, o Options) (wire.DirListing, error) {
	abs, err := Expand(text, o)
	if err != nil {
		return wire.DirListing{}, err
	}
	typed := strings.TrimSpace(text)
	i := strings.LastIndexAny(typed, "/"+string(filepath.Separator))
	prefix, head := typed[i+1:], typed[:i+1]
	parent := abs
	if typed != "~" && prefix != "" && prefix != ".." {
		parent = o.Base
		if head != "" {
			if parent, err = Expand(head, o); err != nil {
				return wire.DirListing{}, err
			}
		}
	} else {
		prefix = ""
	}
	out := wire.DirListing{Dir: parent, Prefix: prefix, Home: o.Home, Sep: string(filepath.Separator), State: Inspect(abs)}
	out.Entries, out.More = list(parent, prefix)
	return out, nil
}

// Inspect says what the absolute path abs names, without listing anything but whether a folder is empty.
func Inspect(abs string) wire.DirState {
	st := wire.DirState{Path: abs, Git: "none", Verify: []wire.VerifyHint{}}
	if fi, err := os.Stat(filepath.Dir(abs)); err == nil && fi.IsDir() {
		st.ParentOK = true
	}
	fi, err := os.Stat(abs)
	if err != nil || !fi.IsDir() {
		st.Exists = err == nil
		st.Git, st.GitRoot = gitAround(filepath.Dir(abs))
		if st.Git == "repo" {
			st.Git = "inside" // the folder to make would be in that repository, it would not be one
		}
		return st
	}
	st.Exists, st.Dir = true, true
	if f, err := os.Open(abs); err == nil {
		_, err := f.Readdirnames(1)
		st.Readable, st.Empty = err == nil || err == io.EOF, err == io.EOF
		f.Close()
	}
	st.Git, st.GitRoot = gitAround(abs)
	if st.Git == "inside" && st.GitRoot == abs {
		st.Git, st.GitRoot = "repo", ""
	}
	seen := map[string]bool{}
	for _, m := range markers {
		if seen[m.cmd] {
			continue
		}
		if mi, err := os.Stat(filepath.Join(abs, m.file)); err == nil && !mi.IsDir() {
			seen[m.cmd] = true
			st.Verify = append(st.Verify, wire.VerifyHint{Cmd: m.cmd, Why: m.file})
		}
	}
	return st
}

// gitAround looks from dir upwards for the folder that holds a .git (a folder, or a file as a linked work tree has): "repo" with no
// root when dir is it, "inside" with the root when a folder above is, else "none".
func gitAround(dir string) (kind, root string) {
	for i := 0; i < maxUp; i++ {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			if i == 0 {
				return "repo", ""
			}
			return "inside", dir
		}
		up := filepath.Dir(dir)
		if up == dir {
			break
		}
		dir = up
	}
	return "none", ""
}

// shown reports whether a name can be offered: valid UTF-8 with no control or format character (the ones that reorder or hide text).
func shown(name string) bool {
	if !utf8.ValidString(name) {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == ' ' || r == ' ' {
			return false
		}
	}
	return true
}

// list reads the subfolders of parent whose names start with prefix, ignoring case; names that start with a dot only when prefix
// does. The best come first: those that match the case typed, then by name.
func list(parent, prefix string) ([]wire.DirEntry, bool) {
	out := []wire.DirEntry{}
	f, err := os.Open(parent)
	if err != nil {
		return out, false
	}
	defer f.Close()
	low := strings.ToLower(prefix)
	dots := strings.HasPrefix(prefix, ".")
	var found []wire.DirEntry
	for scanned := 0; scanned < maxScan; {
		batch, err := f.ReadDir(256)
		for _, e := range batch {
			scanned++
			name := e.Name()
			if !shown(name) || (!dots && strings.HasPrefix(name, ".")) || !strings.HasPrefix(strings.ToLower(name), low) {
				continue
			}
			path := filepath.Join(parent, name)
			link := e.Type()&fs.ModeSymlink != 0
			if link {
				if fi, err := os.Stat(path); err != nil || !fi.IsDir() {
					continue
				}
			} else if !e.IsDir() {
				continue
			}
			found = append(found, wire.DirEntry{Name: name, Path: path, Link: link})
		}
		if err != nil {
			break
		}
	}
	sort.SliceStable(found, func(i, j int) bool {
		a, b := found[i].Name, found[j].Name
		if ca, cb := strings.HasPrefix(a, prefix), strings.HasPrefix(b, prefix); ca != cb {
			return ca
		}
		if la, lb := strings.ToLower(a), strings.ToLower(b); la != lb {
			return la < lb
		}
		return a < b
	})
	more := len(found) > MaxEntries
	if more {
		found = found[:MaxEntries]
	}
	for i := range found {
		if _, err := os.Lstat(filepath.Join(found[i].Path, ".git")); err == nil {
			found[i].Repo = true
		}
	}
	return append(out, found...), more
}
