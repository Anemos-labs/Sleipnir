//go:build linux

package ptytest

import (
	"fmt"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

// fionread is the ioctl that says how many bytes can be read without waiting (TIOCINQ,
// also known as FIONREAD).
const fionread = unix.TIOCINQ

// outputQueueRequest, asked of the master, is the bytes the program wrote that the master has not
// read: on Linux the master's input queue, FIONREAD again.
const outputQueueRequest = unix.TIOCINQ

// openPty opens a pseudo-terminal: posix_openpt, grantpt, unlockpt and ptsname, which
// on Linux are an open of /dev/ptmx and two ioctls (devpts needs no grant).
func openPty() (master, slave *os.File, err error) {
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: opening /dev/ptmx: %v", ErrUnsupported, err)
	}
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil { // unlockpt
		unix.Close(fd)
		return nil, nil, fmt.Errorf("unlockpt: %w", err)
	}
	n, err := unix.IoctlGetInt(fd, unix.TIOCGPTN) // ptsname: /dev/pts/N
	if err != nil {
		unix.Close(fd)
		return nil, nil, fmt.Errorf("ptsname: %w", err)
	}
	name := "/dev/pts/" + strconv.Itoa(n)
	sfd, err := unix.Open(name, unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		unix.Close(fd)
		return nil, nil, fmt.Errorf("%w: opening %s: %v", ErrUnsupported, name, err)
	}
	// The master is read by a goroutine that Close has to be able to stop. A non-blocking
	// descriptor is handed to the runtime's poller, where a blocked read ends when the file
	// is closed; a blocking one would end only when the other end does.
	if err := unix.SetNonblock(fd, true); err != nil {
		unix.Close(fd)
		unix.Close(sfd)
		return nil, nil, err
	}
	return os.NewFile(uintptr(fd), "/dev/ptmx"), os.NewFile(uintptr(sfd), name), nil
}
