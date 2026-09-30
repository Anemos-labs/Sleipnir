//go:build unix

package term

import (
	"os"
	"syscall"
	"testing"
)

// sigwinch delivers SIGWINCH to this process, as a terminal emulator does when its window changes size.
func sigwinch(t *testing.T) {
	t.Helper()
	if err := syscall.Kill(os.Getpid(), syscall.SIGWINCH); err != nil {
		t.Fatal(err)
	}
}
