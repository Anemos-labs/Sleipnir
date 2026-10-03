//go:build !linux && !darwin

package ptytest

import (
	"fmt"
	"os"
	"runtime"
	"syscall"
)

// On a system this package does not open terminals on, Start skips the test with
// ErrUnsupported. The rest of the API still compiles, so that a test that uses it builds
// everywhere.

// openPty returns ErrUnsupported with the current OS and architecture on platforms without PTY
// support.
func openPty() (master, slave *os.File, err error) {
	return nil, nil, fmt.Errorf("%w: %s/%s", ErrUnsupported, runtime.GOOS, runtime.GOARCH)
}

// ctty returns no controlling-terminal attributes on unsupported platforms.
func ctty() *syscall.SysProcAttr { return nil }

// killGroup does nothing on platforms where this PTY implementation cannot start a process group.
func killGroup(int) {}

// setWinsize reports ErrUnsupported without changing the file on this platform.
func setWinsize(*os.File, uint16, uint16) error { return ErrUnsupported }

// inputPending reports that terminal input queue inspection is unsupported on this platform.
func inputPending(*os.File) (int, error) { return 0, ErrUnsupported }

// outputPending reports that terminal output queue inspection is unsupported on this platform.
func outputPending(*os.File) (int, error) { return 0, ErrUnsupported }
