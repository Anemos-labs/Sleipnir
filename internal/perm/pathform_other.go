//go:build !windows

package perm

import "os"

// pathLink identifies entries whose target must be followed before permission checks.
func pathLink(fi os.FileInfo) bool { return fi.Mode()&os.ModeSymlink != 0 }

// canonicalPath preserves case-sensitive native path spelling on POSIX filesystems.
func canonicalPath(p string) string { return p }

// pathProblem imposes no additional namespace restrictions on POSIX paths.
func pathProblem(p string) string { return "" }
