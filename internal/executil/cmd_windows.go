// Package executil provides platform-specific process setup shared by command runners.
package executil

import (
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// ConfigureShell preserves the script in a cmd /C invocation without applying
// CommandLineToArgvW escaping, which cmd.exe does not understand. It disables
// AutoRun and wraps the raw script for cmd's /S quote handling. Other commands
// are unchanged. An explicit command line takes precedence, and other process
// attributes are preserved. Call before starting the command.
func ConfigureShell(cmd *exec.Cmd) {
	name := strings.TrimSuffix(strings.ToLower(filepath.Base(cmd.Path)), ".exe")
	if name != "cmd" || len(cmd.Args) != 3 || !strings.EqualFold(cmd.Args[1], "/c") {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	if cmd.SysProcAttr.CmdLine == "" {
		cmd.SysProcAttr.CmdLine = syscall.EscapeArg(cmd.Path) + ` /D /S /C "` + cmd.Args[2] + `"`
	}
}
