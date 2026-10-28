//go:build !unix

package session

import "os"

// readFlags opens read-only.
const readFlags = os.O_RDONLY
