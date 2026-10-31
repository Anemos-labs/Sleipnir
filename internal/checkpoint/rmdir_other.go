//go:build !unix

package checkpoint

import (
	"errors"
	"os"
)

// rmdirIn removes the directory name in parent after checking it is one; without a directory-only removal relative to a
// descriptor, a replacement between the check and the removal is not caught here.
func rmdirIn(parent *os.Root, name string) error {
	if fi, err := parent.Lstat(name); err != nil {
		return err
	} else if !fi.IsDir() {
		return errors.New("it is no longer a directory")
	}
	return parent.Remove(name)
}
