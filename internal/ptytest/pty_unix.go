//go:build linux || darwin

package ptytest

import (
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// ctty makes the terminal the command's controlling terminal: a session of its own
// (so that Ctrl-C reaches the command and what it started, and nothing of the test
// process), with the descriptor that is its standard input, 0 in the child, as the
// terminal.
func ctty() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
}

// killGroup kills every process of the command's process group. The command leads its
// own session, so its group id is its pid; the group may already be gone, and that is
// fine.
func killGroup(pid int) { _ = syscall.Kill(-pid, syscall.SIGKILL) }

func setWinsize(f *os.File, rows, cols uint16) error {
	return control(f, func(fd int) error {
		return unix.IoctlSetWinsize(fd, unix.TIOCSWINSZ, &unix.Winsize{Row: rows, Col: cols})
	})
}

// inputPending is how many bytes of typed input the terminal holds that nothing has
// read yet (FIONREAD on the terminal's slave end; in canonical mode, the bytes of the
// lines that are complete).
func inputPending(f *os.File) (int, error) {
	var n int
	err := control(f, func(fd int) (err error) {
		n, err = unix.IoctlGetInt(fd, fionread)
		return err
	})
	return n, err
}

// outputPending is how many bytes the program has written that nobody has read from the master
// yet. It matters at the end: closing the last descriptor of the slave end discards what is
// still queued on some systems (macOS), so the test's copy is closed only when this is (nearly)
// zero. The ioctl that says so is not the same everywhere: outputQueueRequest (pty_linux.go,
// pty_darwin.go).
func outputPending(master *os.File) (int, error) {
	var n int
	err := control(master, func(fd int) (err error) {
		n, err = unix.IoctlGetInt(fd, outputQueueRequest)
		return err
	})
	return n, err
}

// control runs fn with f's descriptor. File.Fd would do as well, but it turns a file the
// runtime polls into a blocking one; this does not touch the mode.
func control(f *os.File, fn func(fd int) error) error {
	rc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var ierr error
	if err := rc.Control(func(fd uintptr) { ierr = fn(int(fd)) }); err != nil {
		return err
	}
	return ierr
}
