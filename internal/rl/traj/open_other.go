//go:build !unix

package traj

// openNonblock is zero where the file system has no FIFOs to wait on.
const openNonblock = 0
