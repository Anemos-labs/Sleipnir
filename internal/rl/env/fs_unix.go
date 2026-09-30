//go:build unix

package env

import (
	"io/fs"
	"syscall"
)

// chmodDirNoFollow changes the mode of a directory through a descriptor opened
// with O_NOFOLLOW|O_DIRECTORY: if the path was swapped for a symlink since it
// was inspected, the open fails instead of chmod-ing the link's target.
func chmodDirNoFollow(path string, mode fs.FileMode) error {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	return syscall.Fchmod(fd, uint32(mode.Perm()))
}
