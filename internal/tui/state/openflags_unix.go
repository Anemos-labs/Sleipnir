//go:build unix

package state

import (
	"os"
	"syscall"
)

// readFlags opens read-only and without blocking: a FIFO put where a file was checked would otherwise hold the open until a writer
// comes. What was opened is checked to be a regular file after the open.
const readFlags = os.O_RDONLY | syscall.O_NONBLOCK
