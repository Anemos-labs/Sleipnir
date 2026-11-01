package machine

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// startLow starts cmd in the below-normal priority class, which the processes it starts inherit.
func startLow(cmd *exec.Cmd) error {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.BELOW_NORMAL_PRIORITY_CLASS
	return cmd.Start()
}
