package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/windows"
)

// lockDir excludes other writers for the lifetime of the open lock handle.
// Acquisition never waits, and the operating system releases the lock if the
// process dies. The returned release function is safe to call more than once.
func lockDir(dir string) (unlock func(), err error) {
	f, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	var offset windows.Overlapped
	handle := windows.Handle(f.Fd())
	if err := windows.LockFileEx(handle, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &offset); err != nil {
		_ = f.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, fmt.Errorf("the session in %s is in use by another sleipnir process", dir)
		}
		return nil, fmt.Errorf("lock session directory %s: %w", dir, err)
	}
	return sync.OnceFunc(func() {
		_ = windows.UnlockFileEx(handle, 0, 1, 0, &offset)
		_ = f.Close()
	}), nil
}
