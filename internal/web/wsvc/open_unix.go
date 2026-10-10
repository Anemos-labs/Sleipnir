//go:build unix

package wsvc

import (
	"os"
	"syscall"
)

// openNoFollow opens a file for reading without following a symlink at the end of the
// path and without waiting for a writer on a FIFO.
func openNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}
