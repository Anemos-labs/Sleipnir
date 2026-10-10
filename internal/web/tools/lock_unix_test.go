//go:build unix

package tools

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// holdSession holds a session directory's lock as a running session does (another process, as far as the probe can tell).
func holdSession(t *testing.T, dir string) func() {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	return func() { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN); _ = f.Close() }
}
