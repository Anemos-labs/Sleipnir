//go:build !windows

// Package executil provides platform-specific process setup shared by command runners.
package executil

import "os/exec"

// ConfigureShell leaves non-Windows command lines unchanged.
func ConfigureShell(cmd *exec.Cmd) {}
