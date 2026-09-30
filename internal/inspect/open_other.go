//go:build !unix

package inspect

import (
	"errors"
	"os"
)

var errNotRegular = errors.New("inspect: not a regular file")

// openRegular opens p for reading only if it is a regular file (see open_unix.go;
// there is no portable O_NOFOLLOW, so a symlink is refused by Lstat first).
func openRegular(p string) (*os.File, error) {
	if fi, err := os.Lstat(p); err != nil {
		return nil, err
	} else if !fi.Mode().IsRegular() {
		return nil, errNotRegular
	}
	f, err := os.Open(p)
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
