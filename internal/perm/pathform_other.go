//go:build !windows

package perm

import (
	"errors"
	"os"
	"path/filepath"
)

// pathLink identifies entries whose target must be followed before permission checks.
func pathLink(fi os.FileInfo) bool { return fi.Mode()&os.ModeSymlink != 0 }

// canonicalPath preserves case-sensitive native path spelling on POSIX filesystems.
func canonicalPath(p string) string { return p }

// pathProblem imposes no additional namespace restrictions on POSIX paths.
func pathProblem(p string) string { return "" }

var errNotAbs = errors.New("not an absolute path")

// lstatAbs describes the entry an absolute path names, without following a final
// link. A relative path is refused, so a caller cannot look outside the tree it
// meant to search.
func lstatAbs(p string) (os.FileInfo, error) {
	if len(p) == 0 || p[0] != '/' {
		return nil, errNotAbs
	}
	return os.Lstat(filepath.Clean("/" + p[1:]))
}
