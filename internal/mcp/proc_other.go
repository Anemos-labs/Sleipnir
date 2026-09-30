//go:build !unix

package mcp

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
)

// Non-Unix platforms (Windows in practice) have no process groups to signal.
// This is deliberately minimal: taskkill /T walks the process tree, which is
// the closest equivalent, and anything else degrades to killing the leader.

func configureProc(cmd *exec.Cmd) {}

func termGroup(pid int) { taskkill(pid, false) }

func killGroup(pid int) { taskkill(pid, true) }

// groupAlive cannot be answered cheaply here; reporting false makes shutdown
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

// resolveCommand defers to exec.LookPath, which already refuses to resolve
// names to the current directory and knows PATHEXT.
func resolveCommand(command string, env []string, dir string) (string, error) {
	return exec.LookPath(command)
}
