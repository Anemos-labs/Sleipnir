//go:build linux

package shell

import "golang.org/x/sys/unix"

func hardenProcess() error { return unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0) }

// dumpable reports the process's PR_GET_DUMPABLE value (tests).
func dumpable() (int, error) { return unix.PrctlRetInt(unix.PR_GET_DUMPABLE, 0, 0, 0, 0) }
