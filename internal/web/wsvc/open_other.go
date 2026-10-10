//go:build !unix

package wsvc

import (
	"errors"
	"os"
)

// openNoFollow opens a file for reading when it is a regular file (the check and the
// open are two steps here: platforms without O_NOFOLLOW).
func openNoFollow(path string) (*os.File, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	return os.Open(path)
}
