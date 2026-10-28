//go:build !unix

package fs

// There are no FIFOs to wait on here.
const noWait = 0

// noFollow is zero here: the resolved path is opened as it is.
const noFollow = 0
