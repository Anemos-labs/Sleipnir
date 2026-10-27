//go:build !unix

package inspect

import (
	"errors"
	"os"
)

var errNotRegular = errors.New("inspect: not a regular file")

// openRegularIn opens name below r for reading only if it is a regular file (see
// open_unix.go; there is no portable O_NOFOLLOW, so a symlink is refused by Lstat
// first).
func openRegularIn(r *os.Root, name string) (*os.File, error) {
	if fi, err := r.Lstat(name); err != nil {
		return nil, err
	} else if !fi.Mode().IsRegular() {
		return nil, errNotRegular
	}
	f, err := r.Open(name)
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
