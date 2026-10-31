//go:build unix

package checkpoint

import (
	"os"

	"golang.org/x/sys/unix"
)

// rmdirIn removes the empty directory name in parent and nothing else: unlinkat with AT_REMOVEDIR, relative to the opened
// parent, fails on a file or a link that replaced the directory instead of deleting it.
func rmdirIn(parent *os.Root, name string) error {
	d, err := parent.Open(".")
	if err != nil {
		return err
	}
	defer d.Close()
	if err := unix.Unlinkat(int(d.Fd()), name, unix.AT_REMOVEDIR); err != nil {
		return &os.PathError{Op: "rmdir", Path: name, Err: err}
	}
	return nil
}
