//go:build unix

package env

import (
	"os"
	"os/exec"
	"syscall"
)

const (
	sigTerm = int(syscall.SIGTERM)
	sigKill = int(syscall.SIGKILL)
)

// configureProc puts the command in a session of its own: its pid is then also
// its process-group id, so kill(-pid) reaches every descendant that did not
// deliberately leave the group, and the command loses the harness's
// controlling terminal, so anything that tries to open /dev/tty (ssh, sudo, a
// credential prompt) fails at once instead of stopping on SIGTTIN.
func configureProc(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// signalGroup sends sig to the process group led by pid. A pgid cannot be
// reused while any member is alive, so the signal can only reach the command's
// own descendants (or nobody).
func signalGroup(pid, sig int) { _ = syscall.Kill(-pid, syscall.Signal(sig)) }

// killGroup sends SIGKILL to the process group identified by pid.
func killGroup(pid int) { signalGroup(pid, sigKill) }

// exitInfo decodes a wait status: the exit code, or 128+N and N for a process
// killed by signal N.
func exitInfo(ps *os.ProcessState) (code, sig int) {
	if ps == nil {
		return -1, 0
	}
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal()), int(ws.Signal())
	}
	return ps.ExitCode(), 0
}
