//go:build !unix

package workspace

import (
	"os"
	"os/exec"
)

// Non-Unix platforms: no /proc, no process groups. Liveness falls back to "assume
// alive" (never prune a tree we cannot prove is abandoned). Windows verification
// uses a job object to contain and terminate descendants.

func pidExists(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	_ = p
	return true
}

// procStart returns zero when process start-time inspection is unavailable on this platform.
func procStart(pid int) int64 { return 0 }

// configureCmd makes context cancellation kill the started process on this platform; no process
// group is created.
func configureCmd(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return cmd.Process.Kill()
	}
}

// killGroup has no effect on platforms without workspace process-group termination support.
func killGroup(pid int) {}

// chownLike leaves destination ownership unchanged on platforms without ownership copying.
func chownLike(dst string, fi os.FileInfo) {}
