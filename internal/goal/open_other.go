//go:build !unix

package goal

// openNonblock is zero where the file system has no FIFOs to wait on.
const openNonblock = 0
