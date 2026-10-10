package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// The web sidecar of a session directory: web.json, beside events.jsonl. It holds what the web interface keeps about a session that
// is not part of its history: the name of its tab and the files the person marked reviewed. The event log has one writer (the process
// that holds the session's lock) and nothing else appends to it; the sidecar is written with an atomic rename, so a reader sees the
// old file or the new one, never a part of either.

// MetaFile is the name of the sidecar in a session directory.
const MetaFile = "web.json"

// Bounds of the sidecar: what a name, a reviewed list and the file may hold.
const (
	maxMetaBytes    = 1 << 20
	maxMetaName     = 240
	maxMetaReviewed = 20000
	maxMetaKey      = 4096
)

// Meta is the web sidecar of a session directory (web.json): its name and the person's reviewed marks (a project-relative path to
// the version it was reviewed at).
type Meta struct {
	Name     string            `json:"name,omitempty"`
	Reviewed map[string]string `json:"reviewed,omitempty"`
}

// metaLocks serialises the writers of one directory's sidecar in this process.
var metaLocks sync.Map // cleaned dir -> *sync.Mutex

// metaLock returns the mutex of a directory's sidecar.
func metaLock(dir string) *sync.Mutex {
	m, _ := metaLocks.LoadOrStore(filepath.Clean(dir), &sync.Mutex{})
	return m.(*sync.Mutex)
}

// LoadMeta reads a session's sidecar. A missing sidecar is the zero Meta; one that is larger than 1 MiB, not regular, or not JSON is
// an error.
func LoadMeta(dir string) (Meta, error) {
	var m Meta
	f, err := openRegularFile(filepath.Join(dir, MetaFile))
	if errors.Is(err, fs.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return m, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxMetaBytes+1))
	if err != nil {
		return m, err
	}
	if len(b) > maxMetaBytes {
		return m, fmt.Errorf("%s is larger than %d bytes", MetaFile, maxMetaBytes)
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return Meta{}, fmt.Errorf("%s: %w", MetaFile, err)
	}
	return m, nil
}

// openRegularFile opens path for reading, refusing a symbolic link and anything that is not a regular file.
func openRegularFile(path string) (*os.File, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", filepath.Base(path))
	}
	return os.Open(path)
}

// UpdateMeta changes a session's sidecar atomically: it reads the sidecar as it is now, lets fn change it, checks the bounds (a name
// of at most 240 bytes, at most 20,000 reviewed marks, 1 MiB in all) and writes it with a rename, readable by the user only. Writers
// of one directory in this process run one at a time; two processes writing one sidecar keep the last write whole. dir must be a
// session directory (it holds an events.jsonl). The directory's modification time is kept as it was, so that renaming a session
// does not make it look newly written to a listing or to prune.
func UpdateMeta(dir string, fn func(*Meta)) error {
	if !hasLog(dir) {
		return fmt.Errorf("%s is not a session directory", dir)
	}
	mu := metaLock(dir)
	mu.Lock()
	defer mu.Unlock()
	m, err := LoadMeta(dir)
	if err != nil {
		m = Meta{} // a damaged sidecar is replaced, not a reason to refuse a rename
	}
	fn(&m)
	if len(m.Name) > maxMetaName {
		return fmt.Errorf("the name is longer than %d bytes", maxMetaName)
	}
	if len(m.Reviewed) > maxMetaReviewed {
		return fmt.Errorf("more than %d reviewed marks", maxMetaReviewed)
	}
	for k, v := range m.Reviewed {
		if len(k) > maxMetaKey || len(v) > maxMetaKey {
			return errors.New("a reviewed mark is too long")
		}
	}
	if len(m.Reviewed) == 0 {
		m.Reviewed = nil
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if len(b) > maxMetaBytes {
		return fmt.Errorf("%s would be larger than %d bytes", MetaFile, maxMetaBytes)
	}
	before, statErr := os.Stat(dir)
	tmp, err := os.CreateTemp(dir, ".web-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, MetaFile)); err != nil {
		return err
	}
	if statErr == nil {
		_ = os.Chtimes(dir, time.Time{}, before.ModTime()) // the zero time leaves the access time alone
	}
	return nil
}
