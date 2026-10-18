//go:build !unix && !windows

package session

// dirLocks says whether lockDir excludes other processes.
const dirLocks = false

// lockDir is a no-op where advisory file locks are not used: on those systems
// two processes can still open one session directory, and nothing stops them.
func lockDir(string) (func(), error) { return func() {}, nil }
