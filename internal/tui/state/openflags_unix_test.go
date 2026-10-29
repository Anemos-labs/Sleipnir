//go:build unix

package state

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A FIFO put where a regular file was checked does not hold the open: it returns at once, and the check after it refuses the FIFO.
func TestReadFlagsDoNotWaitForAFIFOWriter(t *testing.T) {
	p := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		t.Skip(err)
	}
	done := make(chan *os.File, 1)
	go func() {
		f, err := os.OpenFile(p, readFlags, 0)
		if err != nil {
			t.Error(err)
		}
		done <- f
	}()
	select {
	case f := <-done:
		if f == nil {
			return
		}
		defer f.Close()
		if fi, err := f.Stat(); err != nil || fi.Mode().IsRegular() {
			t.Errorf("the FIFO reads as a regular file: %v %v", fi, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("opening a FIFO waited for a writer")
	}
}
