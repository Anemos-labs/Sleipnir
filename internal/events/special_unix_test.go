//go:build unix

package events

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// within fails the test if f does not return in time: a FIFO in the state directory
// must never hang the harness.
func within(t *testing.T, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s blocked", what)
	}
}

// The state directory is somewhere an agent's tools may reach: a FIFO planted where a blob
// belongs is refused without waiting for a writer, and the next Put replaces it.
func TestSecReview_S34_FIFOInPlaceOfABlobDoesNotHang(t *testing.T) {
	d, err := NewDirBlobs(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h, err := d.Put([]byte("payload"))
	if err != nil {
		t.Fatal(err)
	}
	p := d.path(h)
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		t.Skipf("cannot create a fifo: %v", err)
	}
	within(t, "Get", func() {
		if _, err := d.Get(h); !errors.Is(err, ErrBlobCorrupt) {
			t.Errorf("Get err = %v, want ErrBlobCorrupt", err)
		}
	})
	if d.Has(h) {
		t.Error("a FIFO is not a blob")
	}
	within(t, "Put", func() {
		if _, err := d.Put([]byte("payload")); err != nil {
			t.Errorf("Put: %v", err)
		}
	})
	if got, err := d.Get(h); err != nil || string(got) != "payload" {
		t.Fatalf("after Put: %q %v", got, err)
	}
}

// A symlink where the log belongs (state written to by another user before the
// directory was made private, or by a tool) is never written through, and a FIFO is
// not waited on.
func TestSecReview_S35_OpenRefusesASymlinkedOrSpecialLog(t *testing.T) {
	base := t.TempDir()
	victim := filepath.Join(base, "victim.txt")
	if err := os.WriteFile(victim, []byte("do not touch\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "session")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(dir, "events.jsonl")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if l, err := Open(dir, "s"); err == nil {
		l.Close()
		t.Fatal("Open wrote through a symlink")
	}
	if b, _ := os.ReadFile(victim); string(b) != "do not touch\n" {
		t.Fatalf("the victim file was modified: %q", b)
	}

	dir2 := filepath.Join(base, "session2")
	if err := os.MkdirAll(dir2, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir2, "events.jsonl"), 0o600); err != nil {
		t.Skipf("cannot create a fifo: %v", err)
	}
	within(t, "Open", func() {
		if l, err := Open(dir2, "s"); err == nil {
			l.Close()
			t.Error("Open accepted a FIFO as the log")
		}
	})
}
