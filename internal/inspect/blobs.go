package inspect

import (
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// blobStore reads a session's content-addressed blobs (events.DirBlobs layout:
// blobs/ab/cd/<sha256>) without ever writing. Hashes reach it from two
// untrusted places, the event log and URL parameters, so every hash is checked
// to be 64 lowercase hex characters before it is turned into a path: nothing
// else can name a file, which rules out traversal by construction. Files must be
// regular (a symlink planted in a rollout directory is not followed).
type blobStore struct {
	root string

	mu      sync.Mutex
	checked time.Time
	present bool
}

// openBlobs constructs a blob reader rooted at the supplied directory without opening files yet.
func openBlobs(root string) *blobStore { return &blobStore{root: root} }

// available reports whether the blobs directory exists. The answer is cached
// briefly so a session that creates blobs/ after we started is noticed.
func (b *blobStore) available() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.present || time.Since(b.checked) < 2*time.Second {
		return b.present
	}
	b.checked = time.Now()
	if fi, err := os.Stat(b.root); err == nil && fi.IsDir() {
		b.present = true
	}
	return b.present
}

// validHash reports whether h is a full SHA-256 in lowercase hex.
func validHash(h string) bool {
	if len(h) != 64 {
		return false
	}
	for i := 0; i < len(h); i++ {
		c := h[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// path is the DirBlobs location of a validated hash.
func (b *blobStore) path(h string) (string, bool) {
	if b == nil || !validHash(h) {
		return "", false
	}
	return filepath.Join(b.root, h[:2], h[2:4], h), true
}

// size returns a blob's length.
func (b *blobStore) size(h string) (int64, bool) {
	if !b.available() {
		return 0, false
	}
	p, ok := b.path(h)
	if !ok {
		return 0, false
	}
	fi, err := os.Lstat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return 0, false
	}
	return fi.Size(), true
}

// read returns up to max bytes of a blob and the blob's full size.
func (b *blobStore) read(h string, max int) ([]byte, int64, bool) {
	if !b.available() {
		return nil, 0, false
	}
	p, ok := b.path(h)
	if !ok {
		return nil, 0, false
	}
	fi, err := os.Lstat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return nil, 0, false
	}
	f, err := openRegular(p)
	if err != nil {
		return nil, 0, false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, int64(max)))
	if err != nil {
		return nil, 0, false
	}
	return data, fi.Size(), true
}

// sizeCached memoises size lookups for hashes that recur on every request
// (tool lists, the shared pin). Callers hold s.mu.
func (s *Session) blobSizeLocked(h string) (int64, bool) {
	if h == "" || s.blobs == nil {
		return 0, false
	}
	if n, ok := s.blobSize[h]; ok {
		return n, n >= 0
	}
	n, ok := s.blobs.size(h)
	if !ok {
		// Not cached negatively: the blob may be written a moment after the event.
		return 0, false
	}
	if len(s.blobSize) > 50000 {
		s.blobSize = map[string]int64{}
	}
	s.blobSize[h] = n
	return n, true
}
