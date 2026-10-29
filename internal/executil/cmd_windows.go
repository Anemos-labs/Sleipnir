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
// may end a wrapper's argv (wrapper args... cmd /C script): the wrapper parses
// its own command line and builds cmd's, so every argument is escaped for it as
// usual, the script included, and only AutoRun is turned off (cmd /D /C). Other
// commands are unchanged. An explicit command line takes precedence, and other
// process attributes are preserved. Call before starting the command.
func ConfigureShell(cmd *exec.Cmd) {
	n := len(cmd.Args)
	if n < 3 || !strings.EqualFold(cmd.Args[n-2], "/c") {
		return
	}
	if n > 3 {
		if isCmd(cmd.Args[n-3]) && (cmd.SysProcAttr == nil || cmd.SysProcAttr.CmdLine == "") {
			cmd.Args = append(append(cmd.Args[:n-2:n-2], "/D"), cmd.Args[n-2:]...)
		}
		return
	}
	if !isCmd(cmd.Path) {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	if cmd.SysProcAttr.CmdLine == "" {
		cmd.SysProcAttr.CmdLine = syscall.EscapeArg(cmd.Path) + ` /D /S /C "` + cmd.Args[2] + `"`
	}
}

// isCmd reports whether path names cmd.exe.
func isCmd(path string) bool {
	return strings.TrimSuffix(strings.ToLower(filepath.Base(path)), ".exe") == "cmd"
}
