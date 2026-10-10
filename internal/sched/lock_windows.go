package sched

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// errHeld is the error of tryLockFile when another open file holds the lock.
var errHeld = errors.New("held")

// lockOffset is where the locked byte lies: far past the content, so that the process id written at the start stays readable.
const lockOffset = 1 << 30

// tryLockFile takes an exclusive lock on one byte of f without waiting.
func tryLockFile(f *os.File) error {
	ol := windows.Overlapped{Offset: lockOffset}
	err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &ol)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return errHeld
	}
	return err
}

// unlockFile gives back the lock of tryLockFile.
func unlockFile(f *os.File) error {
	ol := windows.Overlapped{Offset: lockOffset}
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &ol)
}
