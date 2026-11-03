//go:build unix

package settings

import "syscall"

// noWait keeps an open from waiting for the other end when the path names a FIFO: the check that it is a regular file follows.
const noWait = syscall.O_NONBLOCK
