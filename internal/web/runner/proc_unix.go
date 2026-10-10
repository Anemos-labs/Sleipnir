//go:build unix

package runner

import (
	"errors"
	"os/exec"
	"syscall"
)

// setGroup starts the command in a process group of its own, so that stopping it stops what it started (setPdeathsig adds, on
// Linux, that it dies with the server).
func setGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	setPdeathsig(cmd.SysProcAttr)
}

// terminate asks the command's process group to end (SIGTERM).
func terminate(cmd *exec.Cmd) { signalGroup(cmd, syscall.SIGTERM) }

// kill ends the command's process group (SIGKILL).
func kill(cmd *exec.Cmd) { signalGroup(cmd, syscall.SIGKILL) }

// signalGroup sends sig to the process group the command leads.
func signalGroup(cmd *exec.Cmd, sig syscall.Signal) {
	if cmd.Process == nil || cmd.Process.Pid <= 0 {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, sig)
}

// exitStatus is the exit status of a finished command: its code, 128 plus the signal that ended it, or -1.
func exitStatus(cmd *exec.Cmd, err error) int {
	st := cmd.ProcessState
	if st == nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			st = ee.ProcessState
		}
	}
	if st == nil {
		return -1
	}
	if ws, ok := st.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return st.ExitCode()
}
