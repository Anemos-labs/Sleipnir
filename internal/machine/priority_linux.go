package machine

import (
	"os/exec"
	"runtime"

	"golang.org/x/sys/unix"
)

// ioprioLow is the I/O priority of a low-priority command: the lowest level (7) of the best-effort class (2), which still gets
// the disk when nothing else wants it, unlike the idle class.
const ioprioLow = 2<<13 | 7

// startLow starts cmd from a thread of its own whose nice value and I/O priority are lowered first. On Linux both belong to
// the thread, and a process inherits them from the thread that forks it, so the command has them from its first instruction:
// nothing it starts can be quicker than the change. The thread is never unlocked and so ends with the goroutine, taking the
// lowered priority with it.
func startLow(cmd *exec.Cmd) error {
	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread() // no UnlockOSThread: the thread is discarded when this goroutine returns
		if raw, err := unix.Getpriority(unix.PRIO_PROCESS, 0); err == nil && 20-raw < lowNice {
			_ = unix.Setpriority(unix.PRIO_PROCESS, 0, lowNice)
		}
		_, _, _ = unix.Syscall(unix.SYS_IOPRIO_SET, 1 /* IOPRIO_WHO_PROCESS */, 0, ioprioLow)
		done <- cmd.Start()
	}()
	return <-done
}
