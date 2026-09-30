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

func openPty() (master, slave *os.File, err error) {
	return nil, nil, fmt.Errorf("%w: %s/%s", ErrUnsupported, runtime.GOOS, runtime.GOARCH)
}

func ctty() *syscall.SysProcAttr { return nil }

func killGroup(int) {}

func setWinsize(*os.File, uint16, uint16) error { return ErrUnsupported }

func inputPending(*os.File) (int, error) { return 0, ErrUnsupported }

func outputPending(*os.File) (int, error) { return 0, ErrUnsupported }
