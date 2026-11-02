//go:build !windows

package workspace

import "os/exec"

// runVerifyCommand executes the verifier with the configured platform process group, started by start (the machine budget's
// Start, which lowers its priority).
func runVerifyCommand(cmd *exec.Cmd, start func(*exec.Cmd) error) error {
	if err := start(cmd); err != nil {
		return err
	}
	return cmd.Wait()
}
