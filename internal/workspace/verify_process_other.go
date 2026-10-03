//go:build !windows

package workspace

import "os/exec"

// runVerifyCommand executes the verifier with the configured platform process group.
func runVerifyCommand(cmd *exec.Cmd) error { return cmd.Run() }
