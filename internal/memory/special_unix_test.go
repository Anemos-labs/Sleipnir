//go:build unix

package memory

import (
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
