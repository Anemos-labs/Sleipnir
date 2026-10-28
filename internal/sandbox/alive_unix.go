//go:build unix

package sandbox

import (
	"errors"
	"syscall"
)

// processAlive reports whether a process with this id exists on this host.
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
