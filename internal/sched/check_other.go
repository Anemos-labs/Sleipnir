//go:build !unix

package sched

import (
	"context"
	"os/exec"
	"runtime"
	"strconv"

	"github.com/anemos-labs/sleipnir/internal/executil"
)

// checkCommand is how a change check's command line is run: cmd /C on Windows, whose timeout ends the whole process tree
// (taskkill /T), and sh -c elsewhere.
func checkCommand(ctx context.Context, command string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		cmd := exec.CommandContext(ctx, "cmd", "/C", command)
		executil.ConfigureShell(cmd)
		cmd.Cancel = func() error {
			_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
			return cmd.Process.Kill()
		}
		return cmd
	}
	return exec.CommandContext(ctx, "sh", "-c", command)
}

// endGroup leaves processes alone here: without process groups, a tree that outlived its shell cannot be found once the shell is
// gone, and only the timeout ends a running one.
func endGroup(*exec.Cmd) {}
