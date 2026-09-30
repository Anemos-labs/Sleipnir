//go:build unix

package inspect

import (
	"errors"
	"os"
	"syscall"
)

var errNotRegular = errors.New("inspect: not a regular file")

// openRegular opens p for reading only if it is a regular file: a symlink is not
// followed and a FIFO does not block the open. Session directories are places
// an agent's tools may be able to write, so the inspector never trusts what a
// path turns out to be.
func openRegular(p string) (*os.File, error) {
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		f.Close()
		return nil, errNotRegular
	}
	return f, nil
}
