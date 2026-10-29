//go:build windows

package perm

import "path/filepath"

// rootedPath returns the absolute path p cleaned.
func rootedPath(p string) string { return filepath.Clean(p) }
