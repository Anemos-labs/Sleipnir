//go:build !windows

package perm

import "path/filepath"

// rootedPath returns the absolute path p cleaned from the root, the same path written in the form code scanners know to have no
// part above the root.
func rootedPath(p string) string { return filepath.Clean("/" + p) }
