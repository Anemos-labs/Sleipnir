//go:build unix

package memory

import (
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
	"time"
)

func TestFIFOInstructionFileDoesNotBlock(t *testing.T) {
	w := newWorld(t)
	if err := syscall.Mkfifo(filepath.Join(w.root, "AGENTS.md"), 0o644); err != nil {
		t.Skipf("cannot create a fifo: %v", err)
	}
	tree(t, w.root, map[string]string{"CLAUDE.md": "ok"})
	type res struct {
		srcs []Source
		err  error
	}
	done := make(chan res, 1)
	go func() {
		s, err := w.load("")
		done <- res{s, err}
	}()
	select {
	case r := <-done:
		if r.err != nil || !reflect.DeepEqual(paths(r.srcs), []string{"CLAUDE.md|project"}) {
			t.Fatalf("got %v, %v", paths(r.srcs), r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Load blocked on a FIFO")
	}
}

// A FIFO named like an import (or an instruction file in a nested directory) is
// skipped without waiting for a writer, wherever it turns up.
func TestFIFOImportDoesNotBlock(t *testing.T) {
	w := newWorld(t)
	if err := syscall.Mkfifo(filepath.Join(w.root, "pipe.md"), 0o644); err != nil {
		t.Skipf("cannot create a fifo: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(w.root, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(w.root, "pkg", "CLAUDE.md"), 0o644); err != nil {
		t.Skipf("cannot create a fifo: %v", err)
	}
	if err := os.Symlink("pipe.md", filepath.Join(w.root, "SLEIPNIR.md")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	tree(t, w.root, map[string]string{"AGENTS.md": "rules\n@pipe.md\n@after.md", "after.md": "after the pipe"})
	done := make(chan []Source, 1)
	go func() {
		s, _ := w.load("pkg")
		done <- s
	}()
	select {
	case srcs := <-done:
		if got := paths(srcs); !reflect.DeepEqual(got, []string{"AGENTS.md|project", "after.md|import"}) {
			t.Fatalf("got %v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Load blocked on a FIFO")
	}
}
