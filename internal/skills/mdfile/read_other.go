//go:build !unix

package mdfile

import "os"

// openRead opens for reading. Platforms without FIFOs in the file system need
// no special handling.
func openRead(path string) (*os.File, error) { return os.Open(path) }
