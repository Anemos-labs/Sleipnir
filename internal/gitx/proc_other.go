//go:build !unix

package gitx

import "os/exec"

// Non-Unix platforms have no process groups to signal; the leader is killed and
// WaitDelay bounds the wait for pipes held by strays.
func configureProc(cmd *exec.Cmd) (finish func()) {
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return cmd.Process.Kill()
	}
	return func() {}
}

// killGroup performs no process-group signaling on this platform.
func killGroup(pid int) {}
