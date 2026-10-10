//go:build !windows

package main

import (
	"os"
	"syscall"
)

// holdLock takes f's exclusive advisory lock, as a process that uses a recorded session does; the lock lasts until f closes.
func holdLock(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_EX) }
