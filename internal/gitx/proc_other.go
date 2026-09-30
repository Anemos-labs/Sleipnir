//go:build !unix

package gitx

import "os/exec"

// Non-Unix platforms have no process groups to signal; the leader is killed and
// WaitDelay bounds the wait for pipes held by strays.
func configureProc(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return cmd.Process.Kill()
	}
}

func killGroup(pid int) {}
