//go:build unix

package checkpoint

import "syscall"

// mkfifo makes a named pipe.
func mkfifo(path string, mode uint32) error { return syscall.Mkfifo(path, mode) }
