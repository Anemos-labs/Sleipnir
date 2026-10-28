//go:build !unix

package app

import "os"

// readFlags opens read-only.
const readFlags = os.O_RDONLY
