//go:build unix

package shell

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// configureCmd puts the command in a session of its own. That isolates the
// process tree (pid == process-group id, so kill(-pid) reaches every
// descendant that did not deliberately leave) and detaches it from the
// harness's controlling terminal: a command that opens /dev/tty (ssh, sudo, git
// credential prompts) fails at once instead of stopping on SIGTTIN and
// corrupting the user's terminal UI.
func configureCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

func termGroup(pid int) { _ = syscall.Kill(-pid, syscall.SIGTERM) }

func killGroup(pid int) { _ = syscall.Kill(-pid, syscall.SIGKILL) }

// groupAlive reports whether any process (zombies included, until they are
// reaped) remains in the group.
func groupAlive(pid int) bool {
	err := syscall.Kill(-pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func exitFrom(ps *os.ProcessState, err error) exitStatus {
	if ps == nil {
		return exitStatus{code: -1, err: err}
	}
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return exitStatus{code: 128 + int(ws.Signal()), signal: int(ws.Signal())}
	}
	return exitStatus{code: ps.ExitCode()}
}
