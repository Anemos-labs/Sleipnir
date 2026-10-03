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

// configureProc leaves process attributes unchanged on this platform.
func configureProc(cmd *exec.Cmd) {}

// termGroup requests non-forced process-tree termination through taskkill.
func termGroup(pid int) { taskkill(pid, false) }

// killGroup requests forced process-tree termination through taskkill.
func killGroup(pid int) { taskkill(pid, true) }

// groupAlive cannot be answered cheaply here; reporting false makes shutdown
// rely on the leader alone.
func groupAlive(pid int) bool { return false }

// taskkill terminates a Windows process tree with optional force and falls back to killing one
// process elsewhere, ignoring errors.
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
