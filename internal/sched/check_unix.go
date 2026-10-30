//go:build unix

package sched

import (
	"context"
	"os/exec"
	"syscall"
)

// checkCommand is how a change check's command line is run: by the POSIX shell, in a process group of its own, so that a timeout
// ends every process the check started and not only the shell.
func checkCommand(ctx context.Context, command string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	return cmd
}

// endGroup kills what is left of the check's process group after the shell has exited: a process it started in the background. A
// group with no members left is no error (the id cannot have been reused while the group had members).
func endGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
