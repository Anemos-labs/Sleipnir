//go:build !unix

package fs

// There are no FIFOs to wait on here.
const noWait = 0
