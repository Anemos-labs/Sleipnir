package events

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
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

var (
	// ErrBlobNotFound is returned by Get for unknown hashes.
	ErrBlobNotFound = errors.New("blob not found")
	// ErrBlobCorrupt is returned by Get when what is stored under a hash does not
	// hash to it (a torn write, bit rot, or someone rewriting the file), or is not
	// a regular file. The bytes are never returned.
	ErrBlobCorrupt = errors.New("blob is corrupt")
	// ErrInvalidHash is returned for a hash that is not 64 lowercase hex digits:
	// such a string is never turned into a path.
	ErrInvalidHash = errors.New("invalid blob hash")
	// ErrBlobTooLarge is returned by GetMax for a blob over the caller's limit; the
	// content was not read into memory.
	ErrBlobTooLarge = errors.New("blob exceeds the size limit")
)

// ValidHash reports whether h has the form core.HashBytes produces: 64 lowercase
// hex digits. DirBlobs builds file paths from hashes, so anything else (a "..",
// a slash, a NUL) must never get that far; code that reads hashes from files it
// does not control (a checkpoint manifest) can use it to vet them.
func ValidHash(h core.Hash) bool {
	if len(h) != 64 {
		return false
	}
	for i := 0; i < len(h); i++ {
		if c := h[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// DirBlobs stores blobs on disk under dir/ab/cd/<hash>. The directory is private
// to the user (0700, files 0600), a blob is only ever handed out after its content
// has been re-hashed, and only well-formed hashes are accepted, so neither a
// corrupted store nor a hostile hash string can serve the wrong bytes or reach a
// file outside it.
type DirBlobs struct {
	dir string

	mu       sync.Mutex
	verified map[core.Hash]struct{} // blobs Put has already checked on disk, so repeated Puts stay cheap
}

// maxVerified bounds the verified set; it is simply cleared when full.
const maxVerified = 4096

// NewDirBlobs opens (creating if needed) a blob directory.
func NewDirBlobs(dir string) (*DirBlobs, error) {
	if err := makePrivateDir(dir); err != nil {
		return nil, err
	}
	return &DirBlobs{dir: dir, verified: map[core.Hash]struct{}{}}, nil
}

// makePrivateDir creates dir (and any missing parents) readable by the owner only
// and, when it already exists with wider permissions (state written by an older
// version), takes the group and other bits away. Only dir itself is adjusted.
func makePrivateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if fi, err := os.Stat(dir); err == nil && fi.IsDir() && fi.Mode().Perm()&0o077 != 0 {
		_ = os.Chmod(dir, fi.Mode().Perm()&^0o077) // best effort: it may not be ours
	}
	return nil
}

// path returns where blob h lives. h must be valid (see ValidHash).
func (d *DirBlobs) path(h core.Hash) string {
	s := string(h)
	if len(s) < 5 {
		return filepath.Join(d.dir, "_", s)
	}
	return filepath.Join(d.dir, s[:2], s[2:4], s)
}

// Put stores data and returns its hash. Writes are atomic (temp file +
// rename), so concurrent Puts of the same content are safe. A file that is
// already there under the hash is trusted only if it really holds data: a
// short, torn or rewritten file is replaced.
func (d *DirBlobs) Put(data []byte) (core.Hash, error) {
	h := core.HashBytes(data)
	p := d.path(h)
	if d.intact(p, h, len(data)) {
		return h, nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".put-*") // 0600
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
	d.markVerified(h)
	return h, nil
}

// intact reports whether p is a regular file holding exactly the size bytes that
// hash to h. The (cheap) size check runs every time; the content is hashed once
// per process and hash.
func (d *DirBlobs) intact(p string, h core.Hash, size int) bool {
	fi, err := os.Lstat(p) // a symlink is not a blob
	if err != nil || !fi.Mode().IsRegular() || fi.Size() != int64(size) {
		return false
	}
	d.mu.Lock()
	_, ok := d.verified[h]
	d.mu.Unlock()
	if ok {
		return true
	}
	b, err := readBlobFile(p, int64(size))
	if err != nil || core.HashBytes(b) != h {
		return false
	}
	d.markVerified(h)
	return true
}

func (d *DirBlobs) forget(h core.Hash) {
	d.mu.Lock()
	delete(d.verified, h)
	d.mu.Unlock()
}

func (d *DirBlobs) markVerified(h core.Hash) {
	d.mu.Lock()
	if len(d.verified) >= maxVerified {
		clear(d.verified)
	}
	d.verified[h] = struct{}{}
	d.mu.Unlock()
}

// Get reads a blob. The content is hashed before it is returned: if it does not
// hash to h the error wraps ErrBlobCorrupt and no bytes are returned.
func (d *DirBlobs) Get(h core.Hash) ([]byte, error) { return d.GetMax(h, math.MaxInt64) }

// GetMax is Get for callers that must bound their memory: a blob larger than max
// bytes is refused (ErrBlobTooLarge) before it is read.
func (d *DirBlobs) GetMax(h core.Hash, max int64) ([]byte, error) {
	if !ValidHash(h) {
		return nil, fmt.Errorf("%w: %.64q", ErrInvalidHash, string(h))
	}
	b, err := readBlobFile(d.path(h), max)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrBlobNotFound, h.Short())
	}
	if err != nil {
		d.forget(h)
		return nil, fmt.Errorf("blob %s: %w", h.Short(), err)
	}
	if got := core.HashBytes(b); got != h {
		d.forget(h) // Put must look at the file again rather than trust an earlier check
		return nil, fmt.Errorf("%w: %s holds %d bytes that hash to %s", ErrBlobCorrupt, h.Short(), len(b), got.Short())
	}
	return b, nil
}

// readBlobFile reads a regular file of at most max bytes without following a
// symlink at the end of the path and without blocking on a FIFO someone planted
// in its place.
func readBlobFile(p string, max int64) ([]byte, error) {
	f, err := os.OpenFile(p, openReadFlags, 0)
	if err != nil {
		if isSymlinkRefusal(err) {
			return nil, fmt.Errorf("%w: not a regular file", ErrBlobCorrupt)
		}
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: not a regular file", ErrBlobCorrupt)
	}
	if fi.Size() > max {
		return nil, fmt.Errorf("%w: %d bytes, limit %d", ErrBlobTooLarge, fi.Size(), max)
	}
	lim := max // read one byte more than allowed: the file may have grown since the stat
	if lim < math.MaxInt64 {
		lim++
	}
	b, err := io.ReadAll(io.LimitReader(f, lim))
	if err == nil && int64(len(b)) > max {
		return nil, fmt.Errorf("%w: over %d bytes", ErrBlobTooLarge, max)
	}
	return b, err
}

// Has reports whether a blob exists (as a regular file; its content is not
// checked, Get does that).
func (d *DirBlobs) Has(h core.Hash) bool {
	if !ValidHash(h) {
		return false
	}
	fi, err := os.Lstat(d.path(h))
	return err == nil && fi.Mode().IsRegular()
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

// GetMax is Get with a size limit, like DirBlobs.GetMax.
func (m *MemBlobs) GetMax(h core.Hash, max int64) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	b, ok := m.m[h]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrBlobNotFound, h.Short())
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("%w: %d bytes, limit %d", ErrBlobTooLarge, len(b), max)
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

// All returns a copy of every stored blob, for tests and for dumping an
// in-memory session to disk.
func (m *MemBlobs) All() map[core.Hash][]byte {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[core.Hash][]byte, len(m.m))
	for h, b := range m.m {
		out[h] = append([]byte(nil), b...)
	}
	return out
}

// Len reports how many distinct blobs are stored.
func (m *MemBlobs) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.m)
}
