//go:build unix

package events

import (
	"errors"
	"os"
	"syscall"
)

const (
	// openReadFlags opens a file for reading without following a symlink at the
	// end of the path and without waiting for a writer on a FIFO: the state
	// directory is somewhere an agent's tools may be able to write.
	openReadFlags = os.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
	// openLogFlags opens (creating) the event log for reading and writing under the
	// same rules.
	openLogFlags = os.O_CREATE | os.O_RDWR | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
)

// isSymlinkRefusal reports the error an O_NOFOLLOW open of a symlink returns.
func isSymlinkRefusal(err error) bool {
	return errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.EMLINK)
}
