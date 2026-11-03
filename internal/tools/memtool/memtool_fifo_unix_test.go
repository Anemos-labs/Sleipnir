//go:build unix

package memtool

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A FIFO where the notes or the .gitignore beside them should be is refused at once: nothing waits for a writer that never comes.
func TestNotesRefuseAFIFOAtOnce(t *testing.T) {
	dir := t.TempDir()
	notes := filepath.Join(dir, "notes.md")
	if err := syscall.Mkfifo(filepath.Join(dir, ".gitignore"), 0o644); err != nil {
		t.Skip(err)
	}
	if err := syscall.Mkfifo(notes, 0o644); err != nil {
		t.Skip(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := WriteNotes(notes, []string{"x"}); err == nil {
			t.Error("notes were written over a FIFO")
		}
		if err := ignoreBeside(notes); err == nil {
			t.Error("a FIFO .gitignore was taken")
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a FIFO held the notes")
	}
	_ = os.Remove(notes)
}
