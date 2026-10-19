//go:build !unix && !windows

package lessons

import "os"

// tryLock always succeeds where file locks are not used: the in-process mutex of a path still orders one process's writers,
// but two processes are not excluded.
func tryLock(*os.File) (bool, error) { return true, nil }

// unlockFile does nothing where file locks are not used.
func unlockFile(*os.File) {}
