//go:build !unix

package memtool

// noWait is zero where opens have no non-blocking mode.
const noWait = 0
