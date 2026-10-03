//go:build !unix

package workspace

import "os"

// openNoFollow opens a file using os.Open on this platform, without a no-follow guarantee.
func openNoFollow(path string) (*os.File, error) { return os.Open(path) }
