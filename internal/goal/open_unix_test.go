//go:build unix

package goal

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A pipe put in the spec's place after its path was checked is refused at once: the open does not wait for a writer.
func TestSpecSwappedForAPipeIsRefusedWithoutWaiting(t *testing.T) {
	root := t.TempDir()
	spec := filepath.Join(root, "SPEC.md")
	if err := os.WriteFile(spec, []byte("the spec"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { specChecked = func() {} })
	var swapErr error
	specChecked = func() {
		if swapErr = os.Remove(spec); swapErr == nil {
			swapErr = syscall.Mkfifo(spec, 0o644)
		}
	}
	done := make(chan error, 1)
	go func() {
		_, err := readSpec("SPEC.md", root, root)
		done <- err
	}()
	select {
	case err := <-done:
		if swapErr != nil {
			t.Skipf("cannot make a pipe here: %v", swapErr)
		}
		if err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Errorf("readSpec of a pipe: %v; want a refusal that it is not a regular file", err)
		}
	case <-time.After(10 * time.Second):
		f, _ := os.OpenFile(spec, os.O_WRONLY, 0) // release the blocked open
		if f != nil {
			f.Close()
		}
		t.Fatal("readSpec waited on a pipe put in the spec's place")
	}
}
