//go:build unix

package gitx

import (
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// configureProc puts the command in a session of its own: pid == process-group
// id, so signalling -pid reaches every descendant that did not deliberately leave
// (hooks or filters we failed to disarm, a credential helper), and the command
// has no controlling terminal, so anything that tries to open /dev/tty for a
// prompt fails at once instead of stopping the harness's terminal.
//
// Stopping is done in two steps because of what git does on each signal. On
// SIGTERM git runs its signal handler, which deletes the lock files it holds
// (index.lock, refs/.../x.lock, worktree admin directories it was creating) and
// then dies; on SIGKILL it cannot, and the lock stays behind and makes every later
// write to that repository fail until someone deletes it by hand. A cancelled or
// timed-out call must not poison the repository for the next one, so the group is
// asked to stop first and only killed when it does not (a hook that traps TERM, a
// helper that ignores it) after termGrace.
//
// The returned function must be called once the command has been waited for: it
// cancels the pending escalation and, if a stop was requested, makes sure nothing
// of the group is left behind.
func configureProc(cmd *exec.Cmd) (finish func()) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	var (
		mu    sync.Mutex
		timer *time.Timer
		done  bool
		pid   int
	)
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		mu.Lock()
		defer mu.Unlock()
		if pid == 0 {
			pid = cmd.Process.Pid
		}
		if !done && timer == nil {
			p := pid
			timer = time.AfterFunc(termGrace, func() { killGroup(p) })
		}
		return syscall.Kill(-pid, syscall.SIGTERM)
	}
	return func() {
		mu.Lock()
		defer mu.Unlock()
		done = true
		if timer != nil {
			timer.Stop()
			// A stop was requested: whatever of the group survived it (a stray that
			// ignores SIGTERM but no longer holds our pipes) does not outlive the call.
			killGroup(pid)
		}
	}
}

// killGroup kills the process group led by pid.
func killGroup(pid int) {
	if pid > 0 {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}
}
