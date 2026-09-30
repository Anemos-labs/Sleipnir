//go:build !unix

package env

import (
	"io/fs"
	"os"
)

// chmodDirNoFollow is the portable fallback. Without O_NOFOLLOW descriptors the
// best available protection is an lstat immediately before the chmod.
func chmodDirNoFollow(path string, mode fs.FileMode) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return nil
	}
	return os.Chmod(path, mode.Perm())
}
