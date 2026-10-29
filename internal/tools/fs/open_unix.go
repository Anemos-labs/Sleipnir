//go:build unix

package fs

import "syscall"

// noWait makes open(2) fail instead of waiting: a FIFO with no writer (or, for
// writing, no reader) blocks the caller until one appears, and nothing can cancel
// a blocking open.
const noWait = syscall.O_NONBLOCK

// noFollow makes open(2) refuse a symlink in the last component: a path the tools resolved and authorized names a file,
// and a link there now was put there since.
const noFollow = syscall.O_NOFOLLOW
