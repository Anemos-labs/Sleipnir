//go:build !unix

package workspace

import (
	"os"
	"os/exec"
)

// Non-Unix platforms: no /proc, no process groups. Liveness falls back to "assume
// alive" (never prune a tree we cannot prove is abandoned) and a verifier is
// killed by killing its leader.

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

func procStart(pid int) int64 { return 0 }

func configureCmd(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return cmd.Process.Kill()
	}
}

func killGroup(pid int) {}

func chownLike(dst string, fi os.FileInfo) {}
