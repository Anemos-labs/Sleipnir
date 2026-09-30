package checkpoint

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/events"
)

// testEnv is a store plus a project root inside a private temp dir. The root is
// a subdirectory so tests can also reach a sibling directory outside it.
type testEnv struct {
	t     *testing.T
	s     *Store
	base  string // temp dir holding root and dir
	root  string
	dir   string
	blobs *events.MemBlobs
}

func newEnv(t *testing.T) *testEnv {
	t.Helper()
	base := t.TempDir()
	e := &testEnv{t: t, base: base, root: filepath.Join(base, "proj"), dir: filepath.Join(base, "cp"), blobs: events.NewMemBlobs()}
	if err := os.MkdirAll(e.root, 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := New(e.dir, e.blobs, e.root)
	if err != nil {
		t.Fatal(err)
	}
	e.s = s
	return e
}

func (e *testEnv) abs(rel string) string {
	if filepath.IsAbs(rel) {
		return rel
	}
	return filepath.Join(e.root, rel)
}

func (e *testEnv) write(rel, content string) {
	e.t.Helper()
	e.writeMode(rel, content, 0o644)
}

func (e *testEnv) writeMode(rel, content string, mode os.FileMode) {
	e.t.Helper()
	p := e.abs(rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		e.t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil { // WriteFile is subject to the umask
		e.t.Fatal(err)
	}
}

func (e *testEnv) read(rel string) string {
	e.t.Helper()
	b, err := os.ReadFile(e.abs(rel))
	if err != nil {
		e.t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

func (e *testEnv) exists(rel string) bool {
	_, err := os.Lstat(e.abs(rel))
	return err == nil
}

func (e *testEnv) mode(rel string) os.FileMode {
	e.t.Helper()
	fi, err := os.Lstat(e.abs(rel))
	if err != nil {
		e.t.Fatal(err)
	}
	return fi.Mode()
}

// edit does what a write tool does: announce the write, create parents, write.
func (e *testEnv) edit(agent, rel, content string) {
	e.t.Helper()
	if err := e.s.Before(agent, e.abs(rel)); err != nil {
		e.t.Fatalf("Before(%s, %s): %v", agent, rel, err)
	}
	if err := os.MkdirAll(filepath.Dir(e.abs(rel)), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(e.abs(rel), []byte(content), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

// editAfter is edit plus the optional post-write notification.
func (e *testEnv) editAfter(agent, rel, content string) {
	e.t.Helper()
	e.edit(agent, rel, content)
	e.s.After(agent, e.abs(rel))
}

func (e *testEnv) remove(agent, rel string) {
	e.t.Helper()
	if err := e.s.Before(agent, e.abs(rel)); err != nil {
		e.t.Fatalf("Before(%s, %s): %v", agent, rel, err)
	}
	if err := os.Remove(e.abs(rel)); err != nil {
		e.t.Fatal(err)
	}
}

// reopen builds a second Store over the same directory, as a restarted process would.
func (e *testEnv) reopen() *Store {
	e.t.Helper()
	s, err := New(e.dir, e.blobs, e.root)
	if err != nil {
		e.t.Fatal(err)
	}
	return s
}

func result(t *testing.T, rep RestoreReport, path string) FileResult {
	t.Helper()
	for _, f := range rep.Files {
		if f.Path == path {
			return f
		}
	}
	t.Fatalf("no result for %q in %+v", path, rep.Files)
	return FileResult{}
}

func mustRestore(t *testing.T, s *Store, id string, opts RestoreOpts) RestoreReport {
	t.Helper()
	rep, err := s.Restore(id, opts)
	if err != nil {
		t.Fatalf("Restore(%s): %v", id, err)
	}
	return rep
}

// flakyBlobs wraps a Blobs with switchable failures and an optional delay.
type flakyBlobs struct {
	events.Blobs
	mu      sync.Mutex
	failPut bool
	failGet map[core.Hash]bool
	delay   time.Duration
}

func (b *flakyBlobs) setFailPut(v bool) {
	b.mu.Lock()
	b.failPut = v
	b.mu.Unlock()
}

func (b *flakyBlobs) failGetFor(h core.Hash) {
	b.mu.Lock()
	if b.failGet == nil {
		b.failGet = map[core.Hash]bool{}
	}
	b.failGet[h] = true
	b.mu.Unlock()
}

func (b *flakyBlobs) Put(data []byte) (core.Hash, error) {
	b.mu.Lock()
	fail, delay := b.failPut, b.delay
	b.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}
	if fail {
		return "", errors.New("disk full (injected)")
	}
	return b.Blobs.Put(data)
}

func (b *flakyBlobs) Get(h core.Hash) ([]byte, error) {
	b.mu.Lock()
	fail := b.failGet[h]
	b.mu.Unlock()
	if fail {
		return nil, errors.New("blob unreadable (injected)")
	}
	return b.Blobs.Get(h)
}
