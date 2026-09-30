//go:build unix

package input

import (
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
)

func TestPathsSkipFIFOs(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(root, "pipe"), 0o600); err != nil {
		t.Skip("cannot make a FIFO here:", err)
	}
	if _, got := complete(Paths(root), "@"); !reflect.DeepEqual(got, []string{"@file.txt "}) {
		t.Errorf("a FIFO is not something to mention in a prompt: %q", got)
	}
}

// A history path that is a FIFO would block an open for ever: it is refused before it is opened.
func TestHistoryRefusesAFIFOWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skip("cannot make a FIFO here:", err)
	}
	h, err := OpenHistory(path)
	if err == nil || h == nil {
		t.Fatalf("a FIFO is refused: %v", err)
	}
	if err := h.Add("x"); err == nil {
		t.Error("and so is writing to it")
	}
}
