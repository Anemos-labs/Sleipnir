//go:build !(linux || darwin || freebsd || netbsd || openbsd || dragonfly)

package term

import "os"

func newReader(f *os.File) Reader { return plainReader{f} }
