//go:build unix

package config

import "syscall"

// noWaitFlag keeps an open from waiting for a writer when the path names a FIFO.
const noWaitFlag = syscall.O_NONBLOCK
