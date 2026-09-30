//go:build unix

package session

import (
	"os/exec"
	"syscall"
)

// isolate puts the command in its own process group so a timeout can kill the
// whole tree (test runners spawn children).
func isolate(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error {
		if c.Process == nil {
			return nil
		}
		return syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
	}
}
