//go:build unix

package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// lockDir takes an exclusive advisory lock on a session directory for the life of
// the process, so that two processes cannot append to one event log (resuming a
// session that is still running elsewhere would interleave two histories under
// one sequence). The operating system drops the lock when the process dies, so a
// crash never leaves a stale lock behind.
func lockDir(dir string) (unlock func(), err error) {
	f, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("the session in %s is in use by another sleipnir process", dir)
		}
		return nil, err
	}
	return func() { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN); _ = f.Close() }, nil
}
