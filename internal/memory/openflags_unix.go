//go:build unix

package memory

import (
	"os"
	"syscall"
)

// openFlags opens read-only and without blocking: opening a FIFO for reading
// would otherwise wait for a writer forever, and an instruction file's name is
// something a repository chooses. The file's type is checked after the open.
const openFlags = os.O_RDONLY | syscall.O_NONBLOCK
