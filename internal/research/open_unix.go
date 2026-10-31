//go:build unix

package research

import "syscall"

// openNonblock makes the open of a ledger return at once on a FIFO, which would otherwise wait for a writer.
const openNonblock = syscall.O_NONBLOCK
