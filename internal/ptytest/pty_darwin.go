//go:build darwin

package ptytest

import (
	"bytes"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// fionread is the ioctl that says how many bytes can be read without waiting: _IOR('f', 127,
// int), the same on every macOS architecture (x/sys/unix does not define it).
const fionread = 0x4004667f

// outputQueueRequest, asked of the master, is the bytes the program wrote that the master has not
// read: on macOS the terminal's output queue, TIOCOUTQ (FIONREAD on the master is what the slave
// could read, which is the other direction).
const outputQueueRequest = unix.TIOCOUTQ

// openPty opens a pseudo-terminal: posix_openpt, grantpt, unlockpt and ptsname, which on
// macOS are an open of /dev/ptmx and three ioctls on it (TIOCPTYGRANT, TIOCPTYUNLK and
// TIOCPTYGNAME, which fills a 128-byte buffer with the slave's name).
//
// The master stays a blocking descriptor: a pseudo-terminal master does not reliably
// work with kqueue, and Close ends the reading goroutine by killing the command, which
// closes the other end.
func openPty() (master, slave *os.File, err error) {
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: opening /dev/ptmx: %v", ErrUnsupported, err)
	}
	if err := unix.IoctlSetInt(fd, unix.TIOCPTYGRANT, 0); err != nil { // grantpt
		unix.Close(fd)
		return nil, nil, fmt.Errorf("grantpt: %w", err)
	}
	if err := unix.IoctlSetInt(fd, unix.TIOCPTYUNLK, 0); err != nil { // unlockpt
		unix.Close(fd)
		return nil, nil, fmt.Errorf("unlockpt: %w", err)
	}
	var buf [128]byte // ptsname
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), uintptr(unix.TIOCPTYGNAME), uintptr(unsafe.Pointer(&buf[0]))); e != 0 {
		unix.Close(fd)
		return nil, nil, fmt.Errorf("ptsname: %w", e)
	}
	end := bytes.IndexByte(buf[:], 0)
	if end <= 0 {
		unix.Close(fd)
		return nil, nil, fmt.Errorf("ptsname: no name returned")
	}
	name := string(buf[:end])
	sfd, err := unix.Open(name, unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		unix.Close(fd)
		return nil, nil, fmt.Errorf("%w: opening %s: %v", ErrUnsupported, name, err)
	}
	return os.NewFile(uintptr(fd), "/dev/ptmx"), os.NewFile(uintptr(sfd), name), nil
}
