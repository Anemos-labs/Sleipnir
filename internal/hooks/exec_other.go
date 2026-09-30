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

func shellCommand(command string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.Command("cmd", "/C", command)
	}
	return exec.Command("sh", "-c", command)
}

func configureCmd(cmd *exec.Cmd) {}

func termGroup(pid int) { taskkill(pid, false) }

func killGroup(pid int) { taskkill(pid, true) }

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

func exitStatus(ps *os.ProcessState) int {
	if ps == nil {
		return -1
	}
	return ps.ExitCode()
}
