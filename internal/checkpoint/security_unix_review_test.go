//go:build unix

package checkpoint

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A FIFO planted where a manifest belongs must not hang New (the harness would never start), and it
// keeps its number like any other file that cannot be read.
func TestSec_S37_FIFOManifestDoesNotHang(t *testing.T) {
	f := newForge(t)
	if err := syscall.Mkfifo(filepath.Join(f.state, "cp_0001.json"), 0o600); err != nil {
		t.Skipf("cannot create a fifo: %v", err)
	}
	if err := syscall.Mkfifo(filepath.Join(f.state, "meta.json"), 0o600); err != nil {
		t.Skipf("cannot create a fifo: %v", err)
	}
	done := make(chan *Store, 1)
	go func() {
		s, err := New(f.state, f.blobs, f.root)
		if err != nil {
			t.Error(err)
		}
		done <- s
	}()
	select {
	case s := <-done:
		if s == nil {
			return
		}
		if len(s.Warnings()) != 1 {
			t.Fatalf("warnings = %v", s.Warnings())
		}
		if id := s.Begin("x"); id != "cp_0002" {
			t.Fatalf("id = %s", id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("New blocked on a FIFO")
	}
	if _, err := os.Stat(filepath.Join(f.state, "cp_0001.json")); err != nil {
		t.Fatal(err)
	}
}
