//go:build !unix

package sandbox

// processAlive reports whether a process with this id exists; without a way to ask, every owner is taken to be alive, so
// nothing is pruned.
func processAlive(int) bool { return true }
