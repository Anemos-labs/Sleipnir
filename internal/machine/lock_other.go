//go:build !unix && !windows

package machine

import "os"

// tryLock always succeeds where file locks are not used: the slots are then shared within one process only.
func tryLock(*os.File) (bool, error) { return true, nil }

// unlockFile does nothing where file locks are not used.
func unlockFile(*os.File) {}
