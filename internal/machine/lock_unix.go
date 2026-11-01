//go:build unix

package machine

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// tryLock takes an exclusive advisory lock on f without waiting. It reports false, nil when another open of the file holds
// it, in this process or another; the operating system drops the lock when the holder dies.
func tryLock(f *os.File) (bool, error) {
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// unlockFile gives back the lock of tryLock.
func unlockFile(f *os.File) { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN) }
