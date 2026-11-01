//go:build unix && !linux

package machine

import (
	"os/exec"

	"golang.org/x/sys/unix"
)

// startLow starts cmd and lowers its nice value. Here the nice value belongs to the whole process, so it is changed once the
// command runs: a process it starts in its first instants keeps the default priority.
func startLow(cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	if cur, err := unix.Getpriority(unix.PRIO_PROCESS, cmd.Process.Pid); err != nil || cur < lowNice {
		_ = unix.Setpriority(unix.PRIO_PROCESS, cmd.Process.Pid, lowNice)
	}
	return nil
}
