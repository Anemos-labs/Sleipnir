//go:build unix

package hooks

import (
	"os"
	"os/exec"
	"syscall"
)

// shellCommand is how a hook's command line is run: by the POSIX shell, as
// Claude Code does, so that existing hooks (pipes, &&, $VARS) work unchanged.
func shellCommand(command string) *exec.Cmd {
	return exec.Command("/bin/sh", "-c", command)
}

// configureCmd puts the hook in a session of its own. That makes the process id
// the process-group id, so kill(-pid) reaches every descendant that did not
// deliberately leave, and detaches the hook from the harness's controlling
// terminal: a hook that opens /dev/tty (ssh, sudo, a credential prompt) fails
// at once instead of stopping on SIGTTIN and corrupting the user's terminal.
func configureCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

func termGroup(pid int) { _ = syscall.Kill(-pid, syscall.SIGTERM) }

func killGroup(pid int) { _ = syscall.Kill(-pid, syscall.SIGKILL) }

// exitStatus follows the shell convention: the exit code, or 128+N when signal N
// ended the process.
func exitStatus(ps *os.ProcessState) int {
	if ps == nil {
		return -1
	}
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return ps.ExitCode()
}
