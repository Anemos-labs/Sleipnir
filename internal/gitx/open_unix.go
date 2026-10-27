//go:build unix

package gitx

import "syscall"

// noWaitFlag keeps an open from waiting for a writer when the path names a FIFO.
const noWaitFlag = syscall.O_NONBLOCK

// noFollowFlag refuses a symlink at the end of the path.
const noFollowFlag = syscall.O_NOFOLLOW
