//go:build !unix

package state

import "os"

// readFlags opens read-only.
const readFlags = os.O_RDONLY
