//go:build !unix

package workspace

import "os"

func openNoFollow(path string) (*os.File, error) { return os.Open(path) }
