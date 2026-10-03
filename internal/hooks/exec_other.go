//go:build !unix

package hooks

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
)

// Non-Unix platforms (Windows in practice) have no process groups to signal.
// This is deliberately minimal: taskkill /T walks the process tree, which is the
// closest equivalent, and everything else degrades to ending the leader.

// shellCommand prepares cmd /C on Windows and sh -c on other non-Unix platforms without starting
// the process.
func shellCommand(command string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.Command("cmd", "/C", command)
	}
	return exec.Command("sh", "-c", command)
}

// configureCmd leaves process attributes unchanged on this platform.
func configureCmd(cmd *exec.Cmd) {}

// termGroup requests non-forced process-tree termination through taskkill.
func termGroup(pid int) { taskkill(pid, false) }

// killGroup requests forced process-tree termination through taskkill.
func killGroup(pid int) { taskkill(pid, true) }

// taskkill terminates the Windows process tree with optional force, or kills the single process
// elsewhere, ignoring termination errors.
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

// exitStatus returns the process exit code or -1 when no process state is available.
func exitStatus(ps *os.ProcessState) int {
	if ps == nil {
		return -1
	}
	return ps.ExitCode()
}
