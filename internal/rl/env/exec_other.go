//go:build !unix

package env

import (
	"os"
	"os/exec"
)

const (
	sigTerm = 15
	sigKill = 9
)

// Without process groups the best available behaviour is to kill the leader.
// Descendants may survive; the package documentation says this platform is
// unsuitable for untrusted agents.
func configureProc(c *exec.Cmd) {}

func signalGroup(pid, sig int) {
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Kill()
	}
}

func killGroup(pid int) { signalGroup(pid, sigKill) }

func exitInfo(ps *os.ProcessState) (code, sig int) {
	if ps == nil {
		return -1, 0
	}
	return ps.ExitCode(), 0
}
