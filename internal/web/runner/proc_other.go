//go:build !unix

package runner

import "os/exec"

// setGroup does nothing where process groups are not used: a stop ends the command itself.
func setGroup(*exec.Cmd) {}

// terminate ends the command (there is no gentler signal here).
func terminate(cmd *exec.Cmd) { kill(cmd) }

// kill ends the command.
func kill(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

// exitStatus is the exit status of a finished command, or -1.
func exitStatus(cmd *exec.Cmd, _ error) int {
	if cmd.ProcessState == nil {
		return -1
	}
	return cmd.ProcessState.ExitCode()
}
