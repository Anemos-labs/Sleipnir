//go:build !unix && !windows

package sched

import (
	"errors"
	"os"
)

// errHeld is the error of tryLockFile when another open file holds the lock; it is never returned here.
var errHeld = errors.New("held")

// tryLockFile always succeeds where advisory file locks are not used.
func tryLockFile(*os.File) error { return nil }

// unlockFile does nothing where advisory file locks are not used.
func unlockFile(*os.File) error { return nil }
