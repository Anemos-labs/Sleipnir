//go:build !unix

package shell

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
)

// Non-Unix platforms (Windows in practice) have no process groups to signal.
// This is deliberately minimal: taskkill /T walks the process tree, which is
// the closest equivalent, and everything else degrades to killing the leader.

// configureCmd leaves process attributes unchanged on non-Unix platforms.
func configureCmd(cmd *exec.Cmd) {}

// termGroup requests non-forced termination through the platform taskkill helper.
func termGroup(pid int) { taskkill(pid, false) }

// killGroup requests forced termination through the platform taskkill helper.
func killGroup(pid int) { taskkill(pid, true) }

// groupAlive cannot be answered cheaply here; reporting false makes killTree
// rely on the leader alone.
func groupAlive(pid int) bool { return false }

func taskkill(pid int, force bool) {
	if runtime.GOOS != "windows" {
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Kill()
		}
		return
	}
	args := []string{"/T", "/PID", strconv.Itoa(pid)}
	if force {
		args = append(args, "/F")
	}
	_ = exec.Command("taskkill", args...).Run()
}

// exitFrom uses process exit state when available and otherwise preserves the wait error with code
// -1.
func exitFrom(ps *os.ProcessState, err error) exitStatus {
	if ps == nil {
		return exitStatus{code: -1, err: err}
	}
	return exitStatus{code: ps.ExitCode()}
}
