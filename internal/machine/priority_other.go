//go:build !unix && !windows

package machine

import "os/exec"

// startLow starts cmd as it is: this platform has no priority to lower.
func startLow(cmd *exec.Cmd) error { return cmd.Start() }
