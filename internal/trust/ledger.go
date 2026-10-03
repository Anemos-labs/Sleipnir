package trust

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Entry is what a person said yes to, for one directory.
type Entry struct {
	// Digest is the digest of the footprint that was seen.
	Digest string `json:"digest"`
	// Files are the files of it and the start of the hash of each, so that a change can be named, which the digest alone cannot.
	Files map[string]string `json:"files"`
	// Saved is the day it was said, for whoever lists the entries.
	Saved string `json:"saved"`
}

// State is what the ledger says about a directory whose footprint has just been read.
type State int

const (
	// Unknown: nothing was said about this directory.
	Unknown State = iota
	// Trusted: the person said yes to exactly these files.
	Trusted
	// Changed: the person said yes to files, and these are not those.
	Changed
)

// Ledger is the user's own record of the projects they trusted, in their state directory (never in a repository: a repository must not be
// able to trust itself). The file is read whole for every question and written whole for every change; it is small, and nothing holds
// it open, so a program that is killed leaves it as it was. Two programs that change it at once can lose the one that wrote first (the
// answer is asked again), never produce a file that is not whole.
type Ledger struct {
	path string
	mu   sync.Mutex
}

const ledgerVersion = 1

// OpenLedger returns the ledger kept at path. Nothing is read until it is asked.
func OpenLedger(path string) *Ledger { return &Ledger{path: path} }

type ledgerFile struct {
	Version  int              `json:"version"`
	Projects map[string]Entry `json:"projects"`
}

// load reads the file. A file that is not there is an empty ledger; one that cannot be understood is too, and is told apart so that a
// write puts it aside instead of over it.
func (l *Ledger) load() (m map[string]Entry, damaged bool) {
	b, err := os.ReadFile(l.path)
	if err != nil {
		return map[string]Entry{}, false
	}
	var doc ledgerFile
	if json.Unmarshal(b, &doc) != nil || doc.Version != ledgerVersion || doc.Projects == nil {
		return map[string]Entry{}, true
	}
	return doc.Projects, false
}

// Check says what the ledger has about the directory the footprint was read for, and what it has, if it has anything.
func (l *Ledger) Check(cwd string, fp *Footprint) (State, Entry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	m, _ := l.load()
	e, ok := m[cwd]
	switch {
	case !ok:
		return Unknown, Entry{}
	case !fp.Partial && e.Digest == fp.Digest:
		return Trusted, e
	}
	return Changed, e
}

// ErrNotRememberable is what Remember says of a footprint that has nothing in it, or that has more than a scan can read: the digest
// of the second does not cover all of it, and a yes that covered less than what was shown would be a lie.
var ErrNotRememberable = errors.New("this project cannot be remembered: ")

// Remember records the footprint as what the person trusts for cwd.
func (l *Ledger) Remember(cwd string, fp *Footprint, now time.Time) error {
	switch {
	case fp.Empty():
		return fmt.Errorf("%wit has no files that trust would unlock", ErrNotRememberable)
	case fp.Partial:
		return fmt.Errorf("%wit has more files than can be read here (%d files and %d MB at the most)", ErrNotRememberable, maxFiles, maxBytes>>20)
	}
	files := make(map[string]string, len(fp.Files))
	for _, f := range fp.Files {
		files[f.Path] = f.Sum[:16]
	}
	return l.update(func(m map[string]Entry) {
		m[cwd] = Entry{Digest: fp.Digest, Files: files, Saved: now.UTC().Format("2006-01-02")}
	})
}

// Forget removes what was said about cwd and reports whether there was anything.
func (l *Ledger) Forget(cwd string) (bool, error) {
	var had bool
	err := l.update(func(m map[string]Entry) {
		_, had = m[cwd]
		delete(m, cwd)
	})
	return had, err
}

// ForgetAll removes everything and reports how many projects it had.
func (l *Ledger) ForgetAll() (int, error) {
	var n int
	err := l.update(func(m map[string]Entry) {
		n = len(m)
		clear(m)
	})
	return n, err
}

// All returns every entry, by directory.
func (l *Ledger) All() map[string]Entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	m, _ := l.load()
	return m
}

// update reads the file again, changes what it holds and writes it back whole. A file that was not understood is put aside (as
// trust.json.damaged) and not written over.
func (l *Ledger) update(change func(map[string]Entry)) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	m, damaged := l.load()
	if damaged {
		_ = os.Rename(l.path, l.path+".damaged")
	}
	change(m)
	b, err := json.MarshalIndent(ledgerFile{Version: ledgerVersion, Projects: m}, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(l.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".trust-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), l.path)
}

// Change is one way the files of a project are not the ones that were trusted.
type Change struct {
	Path string
	// What is "added", "changed" or "removed".
	What string
}

// Changes says how the footprint differs from what the entry trusted, by path.
func Changes(prev Entry, fp *Footprint) []Change {
	var out []Change
	now := map[string]string{}
	for _, f := range fp.Files {
		now[f.Path] = f.Sum[:16]
		switch old, ok := prev.Files[f.Path]; {
		case !ok:
			out = append(out, Change{f.Path, "added"})
		case old != f.Sum[:16]:
			out = append(out, Change{f.Path, "changed"})
		}
	}
	for p := range prev.Files {
		if _, ok := now[p]; !ok {
			out = append(out, Change{p, "removed"})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// DescribeChanges is Changes as a sentence for a person: the first few files, and how many there were. Paths are cleaned for display.
func DescribeChanges(cs []Change) string {
	if len(cs) == 0 {
		return ""
	}
	var parts []string
	for i, c := range cs {
		if i == 3 {
			parts = append(parts, fmt.Sprintf("and %d more", len(cs)-i))
			break
		}
		parts = append(parts, Show(c.Path)+" "+c.What)
	}
	return joinWords(parts)
}

// joinWords joins display fragments with comma-space separators.
func joinWords(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}
