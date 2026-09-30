//go:build unix

package env

import (
	"errors"
	"io/fs"
	"os"
	"syscall"
)

// chmodDirNoFollow changes the mode of a directory through a descriptor opened
// with O_NOFOLLOW|O_DIRECTORY: if the path was swapped for a symlink since it
// was inspected, the open fails instead of chmod-ing the link's target.
//
// A directory the caller cannot read (mode 000, which is what this exists to
// repair) cannot be opened at all. For that case only, the type is checked again
// and the mode is changed by path: the remaining window is a swap between the
// check and the chmod, and it needs a live process racing the cleanup that has
// just killed everything the workspace started.
func chmodDirNoFollow(path string, mode fs.FileMode) error {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err == nil {
		defer syscall.Close(fd)
		return syscall.Fchmod(fd, uint32(mode.Perm()))
	}
	if !errors.Is(err, syscall.EACCES) {
		return err
	}
	fi, lerr := os.Lstat(path)
	if lerr != nil || !fi.IsDir() {
		return err
	}
	return os.Chmod(path, mode.Perm())
}
