//go:build !unix

package research

// openNonblock is zero where the file system has no FIFOs to wait on.
const openNonblock = 0
