//go:build unix

package workspace

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// pidExists reports whether a live (non-zombie) process has this pid.
func pidExists(pid int) bool {
	if pid <= 0 {
		return false // kill(0, ...) and kill(-1, ...) address whole groups, not a process
	}
	if err := syscall.Kill(pid, 0); err != nil && !errors.Is(err, syscall.EPERM) {
		return false
	}
	if st, ok := procStat(pid); ok && st.state == 'Z' {
		return false // dead, waiting to be reaped
	}
	return true
}

// procInfo is what procStat can tell about a process: its state letter ('Z' for a
// zombie, the /proc letters otherwise) and a value that identifies when it started.
type procInfo struct {
	state byte
	start int64
}

// procStat is implemented per platform (proc_linux.go, proc_darwin.go,
// proc_stat_other.go); it reports false where the platform cannot say.

// procStart returns a value that identifies when the process started (0 when
// unknown), to tell a live owner from an unrelated process that reused its pid.
func procStart(pid int) int64 {
	if st, ok := procStat(pid); ok {
		return st.start
	}
	return 0
}

// configureCmd puts a verifier command in a session of its own: pid == process
// group id, so killing -pid reaches everything it started, and it has no
// controlling terminal.
func configureCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// killGroup kills the process group led by pid (stragglers a finished verifier
// left behind).
func killGroup(pid int) { _ = syscall.Kill(-pid, syscall.SIGKILL) }

// chownLike gives dst the owner of src when we are privileged enough to (a
// harness running as root over a user's project must not turn the user's files
// into root's).
func chownLike(dst string, fi os.FileInfo) {
	if os.Geteuid() != 0 {
		return
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		_ = os.Lchown(dst, int(st.Uid), int(st.Gid))
	}
}
