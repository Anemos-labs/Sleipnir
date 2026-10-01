//go:build windows

package main

import "syscall"

// sendSignalToSelf reports that it cannot: Windows has no kill(2), a process cannot be sent SIGTERM, and the tests that need
// one skip. (syscall.Kill does not exist there, which is why this is a function of its own: CI vets the test files of every
// platform that is released.)
func sendSignalToSelf(sig syscall.Signal) (ok bool, err error) {
	return false, nil
}
