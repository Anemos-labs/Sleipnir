//go:build unix

package traj

import "syscall"

// openNonblock makes the open of an event log return at once on a FIFO, which would otherwise wait for a writer.
const openNonblock = syscall.O_NONBLOCK
