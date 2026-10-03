//go:build !(linux || darwin || freebsd || netbsd || openbsd || dragonfly)

package term

import "os"

// newReader wraps the file in a reader without cancellation support on this platform.
func newReader(f *os.File) Reader { return plainReader{f} }
