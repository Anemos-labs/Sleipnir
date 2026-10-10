//go:build unix && !linux

package runner

import "syscall"

// setPdeathsig does nothing where the kernel has no parent-death signal.
func setPdeathsig(*syscall.SysProcAttr) {}
