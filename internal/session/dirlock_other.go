//go:build !unix && !windows

package session

// lockDir is a no-op where advisory file locks are not used: on those systems
// two processes can still open one session directory, and nothing stops them.
func lockDir(string) (func(), error) { return func() {}, nil }
