package workspace

import (
	"fmt"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// runVerifyCommand contains the verifier and its descendants in a Windows job.
// Go's process attributes cannot assign a job at creation. A suspended shell
// provides a parent already in the job, so the real command inherits membership
// atomically before any repository code runs. The parent never executes code.
// Cancellation and normal completion both terminate every remaining job member.
func runVerifyCommand(cmd *exec.Cmd) error {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return fmt.Errorf("create verification job: %w", err)
	}
	defer windows.CloseHandle(job)
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		return fmt.Errorf("configure verification job: %w", err)
	}

	parent := exec.Command(cmd.Path, "/D", "/C", "exit")
	parent.Dir, parent.Env = cmd.Dir, cmd.Env
	parent.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED | windows.CREATE_NO_WINDOW}
	if err := parent.Start(); err != nil {
		return fmt.Errorf("create verification job parent: %w", err)
	}
	defer func() {
		_ = parent.Process.Kill()
		_ = parent.Wait()
	}()
	handle, err := windows.OpenProcess(windows.PROCESS_CREATE_PROCESS|windows.PROCESS_DUP_HANDLE|
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(parent.Process.Pid))
	if err != nil {
		return fmt.Errorf("open verification job parent: %w", err)
	}
	defer windows.CloseHandle(handle)
	if err := windows.AssignProcessToJobObject(job, handle); err != nil {
		return fmt.Errorf("assign verification job parent: %w", err)
	}
	defer windows.TerminateJobObject(job, 1)

	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.ParentProcess = syscall.Handle(handle)
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NO_WINDOW
	cmd.Cancel = func() error { return windows.TerminateJobObject(job, 1) }
	return cmd.Run()
}
