//go:build !windows

package main

import (
	"os"
	"syscall"
)

// sendSignalToSelf delivers sig to this process, as a terminal or a supervisor would. It reports whether the platform can.
func sendSignalToSelf(sig syscall.Signal) (ok bool, err error) {
	return true, syscall.Kill(os.Getpid(), sig)
}
