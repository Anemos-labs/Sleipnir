//go:build unix

package gitx

import (
	"os/exec"
	"syscall"
)

// configureProc puts the command in a session of its own: pid == process-group
// id, so killing -pid reaches every descendant that did not deliberately leave
// (hooks or filters we failed to disarm, a credential helper), and the command
// has no controlling terminal, so anything that tries to open /dev/tty for a
// prompt fails at once instead of stopping the harness's terminal.
func configureProc(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// killGroup kills the process group led by pid.
func killGroup(pid int) { _ = syscall.Kill(-pid, syscall.SIGKILL) }
