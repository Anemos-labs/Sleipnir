//go:build !windows

package term

import "os"

// enableVT is for Windows consoles: elsewhere a terminal says what it is in TERM, and there is nothing to switch on.
func enableVT(*os.File) bool { return false }
