//go:build unix

package trust

import (
	"os"
	"syscall"
)

// openFlags opens read-only and without blocking: opening a FIFO for reading would otherwise wait for a writer for ever, and the names
// of the files under a repository's .claude are its own. The type of what was opened is checked after the open.
const openFlags = os.O_RDONLY | syscall.O_NONBLOCK
