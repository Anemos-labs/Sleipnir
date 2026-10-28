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
// AutoRun and wraps the raw script for cmd's /S quote handling. The invocation
// may end a wrapper's argv (wrapper args... cmd /C script): the wrapper's own
// arguments are escaped as usual and cmd's part is spelled the same way. Other
// commands are unchanged. An explicit command line takes precedence, and other
// process attributes are preserved. Call before starting the command.
func ConfigureShell(cmd *exec.Cmd) {
	n := len(cmd.Args)
	if n < 3 || !strings.EqualFold(cmd.Args[n-2], "/c") {
		return
	}
	shell := cmd.Path
	if n > 3 {
		shell = cmd.Args[n-3]
	}
	if strings.TrimSuffix(strings.ToLower(filepath.Base(shell)), ".exe") != "cmd" {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	if cmd.SysProcAttr.CmdLine == "" {
		line := syscall.EscapeArg(cmd.Path)
		for _, a := range cmd.Args[1 : n-2] { // the wrapper's arguments and cmd itself
			line += " " + syscall.EscapeArg(a)
		}
		cmd.SysProcAttr.CmdLine = line + ` /D /S /C "` + cmd.Args[n-1] + `"`
	}
}
