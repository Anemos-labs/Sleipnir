//go:build unix

package workspace

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// tryLock takes an exclusive advisory lock on f without waiting; it conflicts
// with every other open of the file, in this process too.
func tryLock(f *os.File) error {
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) {
			return errLockHeld
		}
		return err
	}
	return nil
}

// unlock gives back the lock of tryLock.
func unlock(f *os.File) { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN) }
