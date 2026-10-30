//go:build !unix

package sched

import (
	"context"
	"os/exec"
	"runtime"

	"github.com/anemos-labs/sleipnir/internal/executil"
)

// checkCommand is how a change check's command line is run: cmd /C on Windows, sh -c elsewhere.
func checkCommand(ctx context.Context, command string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		cmd := exec.CommandContext(ctx, "cmd", "/C", command)
		executil.ConfigureShell(cmd)
		return cmd
	}
	return exec.CommandContext(ctx, "sh", "-c", command)
}
