//go:build unix

package checkpoint

import (
	"errors"
	"os"
	"syscall"
)

// openReadFlags opens a manifest without following a symlink at the end of the
// path and without waiting for a writer on a FIFO: the state directory is
// somewhere an agent's tools may be able to write.
const openReadFlags = os.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK

// isSymlinkRefusal reports the error an O_NOFOLLOW open of a symlink returns.
func isSymlinkRefusal(err error) bool {
	return errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.EMLINK)
}
