package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// ownerLockFile sits next to markerFile. The process that owns a tree holds an
// advisory lock on it for as long as it owns the tree. The kernel drops the lock
// when that process exits and a reboot drops every lock, so an unlocked file
// proves the owner is gone where a pid from another boot or namespace cannot.
// The name avoids git's ".lock" suffix: stale git locks in an administrative
// directory are deleted on sight.
const ownerLockFile = "sleipnir-workspace.owner"

// errLockHeld is the error of tryLock when another open file holds the lock.
var errLockHeld = errors.New("workspace: owner lock held")

// ownerLocks maps an administrative directory to the open lock file this process
// holds for it.
var ownerLocks sync.Map

// holdOwnerLock takes the owner lock of the tree whose administrative directory
// is admin and keeps it until dropOwnerLock or exit. A lock that cannot be taken
// leaves the tree owned through its marker alone, as before the lock existed.
func holdOwnerLock(admin string) {
	f, err := os.OpenFile(filepath.Join(admin, ownerLockFile), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return
	}
	if err := tryLock(f); err != nil {
		f.Close()
		return
	}
	if old, loaded := ownerLocks.Swap(admin, f); loaded {
		old.(*os.File).Close()
	}
}

// dropOwnerLock gives back the owner lock this process holds for admin, if any.
func dropOwnerLock(admin string) {
	if f, ok := ownerLocks.LoadAndDelete(admin); ok {
		f.(*os.File).Close()
	}
}

// ownerIn is mk.owner, settled through the owner lock in admin when the marker
// alone cannot tell: a marker from another boot or pid namespace whose lock
// nobody holds belongs to a stopped process, and one whose lock is held belongs
// to a running one. Without a lock file (a tree from an older version, or a
// platform without advisory locks) the answer stays ownerUnknown.
func (mk *marker) ownerIn(admin string) ownerState {
	st := mk.owner()
	if st != ownerUnknown || admin == "" {
		return st
	}
	if _, ok := ownerLocks.Load(admin); ok {
		return ownerAlive
	}
	f, err := os.OpenFile(filepath.Join(admin, ownerLockFile), os.O_RDWR, 0)
	if err != nil {
		return ownerUnknown
	}
	defer f.Close()
	switch err := tryLock(f); {
	case err == nil:
		unlock(f)
		return ownerDead
	case errors.Is(err, errLockHeld):
		return ownerAlive
	default:
		return ownerUnknown
	}
}
