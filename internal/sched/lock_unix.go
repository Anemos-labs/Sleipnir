//go:build unix

package sched

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// errHeld is the error of tryLockFile when another open file holds the lock.
var errHeld = errors.New("held")

// tryLockFile takes an exclusive advisory lock on f without waiting (flock: it conflicts with every other open of the file, in this
// process too).
func tryLockFile(f *os.File) error {
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) {
			return errHeld
		}
		return err
	}
	return nil
}

// unlockFile gives back the lock of tryLockFile.
func unlockFile(f *os.File) error { return unix.Flock(int(f.Fd()), unix.LOCK_UN) }
