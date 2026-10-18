//go:build !unix

package workspace

import (
	"errors"
	"os"
)

// errNoLocks is the error of tryLock where owner locks are not used.
var errNoLocks = errors.New("workspace: owner locks are not used on this platform")

// tryLock fails where owner locks are not used, so an unknown owner stays unknown.
func tryLock(*os.File) error { return errNoLocks }

// unlock does nothing where owner locks are not used.
func unlock(*os.File) {}
