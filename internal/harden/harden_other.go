//go:build !linux && !darwin

package harden

import "runtime"

// No mechanism is used on other platforms: Windows lets a same-user process read
// another's memory and environment with OpenProcess, and the BSDs have their own
// (procctl) that is not wired here. SECURITY.md says what that means.
func platformHarden(optOut bool) Status {
	return Status{
		OptedOut: optOut,
		Notes:    []string{"no process hardening on " + runtime.GOOS + ": same-user processes can read this process's memory and environment; keep credentials out of the environment and sandbox the shell"},
	}
}
