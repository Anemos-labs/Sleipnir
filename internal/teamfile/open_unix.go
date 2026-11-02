//go:build unix

package teamfile

import "syscall"

// noWaitFlag keeps an open from waiting for a writer when the path names a FIFO.
const noWaitFlag = syscall.O_NONBLOCK
