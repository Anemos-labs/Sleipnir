//go:build unix

package workspace

import (
	"os"
	"syscall"
)

// openNoFollow opens a file for reading and refuses to follow a symbolic link at
// the last component (a file replaced by a link while we copy must not make us
// read some other file).
func openNoFollow(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(fd), path), nil
}
