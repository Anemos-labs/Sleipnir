//go:build !unix

package trust

import "os"

// openFlags opens read-only. There are no FIFOs to worry about here.
const openFlags = os.O_RDONLY
