//go:build unix

package mdfile

import (
	"errors"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A FIFO where a definition file should be must be rejected at once: opening it
// for reading normally blocks until someone writes.
func TestReadFileDoesNotBlockOnFIFO(t *testing.T) {
	root := realTemp(t)
	fifo := filepath.Join(root, "SKILL.md")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := ReadFile(fifo, ReadOpts{Contain: root})
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrNotRegular) {
			t.Fatalf("err = %v, want ErrNotRegular", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ReadFile blocked on a FIFO")
	}
}

// A symlink to a FIFO is the same trap with one more hop.
func TestReadFileDoesNotBlockOnSymlinkedFIFO(t *testing.T) {
	root := realTemp(t)
	fifo := filepath.Join(root, "pipe")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	link := filepath.Join(root, "SKILL.md")
	symlink(t, fifo, link)
	done := make(chan error, 1)
	go func() {
		_, err := ReadFile(link, ReadOpts{})
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrNotRegular) {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ReadFile blocked on a symlinked FIFO")
	}
}
