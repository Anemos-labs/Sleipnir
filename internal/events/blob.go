package events

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/reee344/sleipnir/internal/core"
)

// Blobs is a content-addressed store for payloads too large or too repetitive
// to inline in the event log: full tool outputs, rendered layers, request
// bodies, images. Identical content is stored once, which is what makes
// logging 50 agents that share a pinned prefix affordable.
type Blobs interface {
	Put(data []byte) (core.Hash, error)
	Get(h core.Hash) ([]byte, error)
	Has(h core.Hash) bool
}

// ErrBlobNotFound is returned by Get for unknown hashes.
var ErrBlobNotFound = errors.New("blob not found")

// DirBlobs stores blobs on disk under dir/ab/cd/<hash>.
type DirBlobs struct{ dir string }

// NewDirBlobs opens (creating if needed) a blob directory.
func NewDirBlobs(dir string) (*DirBlobs, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &DirBlobs{dir: dir}, nil
}

func (d *DirBlobs) path(h core.Hash) string {
	s := string(h)
	if len(s) < 5 {
		return filepath.Join(d.dir, "_", s)
	}
	return filepath.Join(d.dir, s[:2], s[2:4], s)
}

// Put stores data and returns its hash. Writes are atomic (temp file +
// rename), so concurrent Puts of the same content are safe.
func (d *DirBlobs) Put(data []byte) (core.Hash, error) {
	h := core.HashBytes(data)
	p := d.path(h)
	if _, err := os.Stat(p); err == nil {
		return h, nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".put-*")
	if err != nil {
		return "", err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	return h, nil
}

// Get reads a blob.
func (d *DirBlobs) Get(h core.Hash) ([]byte, error) {
	b, err := os.ReadFile(d.path(h))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrBlobNotFound, h.Short())
	}
	return b, err
}

// Has reports whether a blob exists.
func (d *DirBlobs) Has(h core.Hash) bool {
	_, err := os.Stat(d.path(h))
	return err == nil
}

// MemBlobs is an in-memory Blobs for tests and ephemeral sessions.
type MemBlobs struct {
	mu sync.RWMutex
	m  map[core.Hash][]byte
}

// NewMemBlobs returns an empty in-memory store.
func NewMemBlobs() *MemBlobs { return &MemBlobs{m: map[core.Hash][]byte{}} }

// Put implements Blobs.
func (m *MemBlobs) Put(data []byte) (core.Hash, error) {
	h := core.HashBytes(data)
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.m[h]; !ok {
		m.m[h] = append([]byte(nil), data...)
	}
	return h, nil
}

// Get implements Blobs.
func (m *MemBlobs) Get(h core.Hash) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	b, ok := m.m[h]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrBlobNotFound, h.Short())
	}
	return append([]byte(nil), b...), nil
}

// Has implements Blobs.
func (m *MemBlobs) Has(h core.Hash) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.m[h]
	return ok
}

// Len reports how many distinct blobs are stored.
func (m *MemBlobs) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.m)
}
