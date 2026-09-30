//go:build unix

package mdfile

import (
	"os"
	"syscall"
)

// openRead opens for reading without blocking. Opening a FIFO read-only
// normally waits for a writer; a repository that plants one where a definition
// file is expected would hang the loader forever. With O_NONBLOCK the open
// returns at once and the regular-file check rejects it.
func openRead(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}
