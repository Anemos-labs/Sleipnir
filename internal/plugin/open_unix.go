//go:build unix

package plugin

import "syscall"

// openNonblock makes the open of a file being installed return at once on a FIFO, which would otherwise wait for a writer.
const openNonblock = syscall.O_NONBLOCK
