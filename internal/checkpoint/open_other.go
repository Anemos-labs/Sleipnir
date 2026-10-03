//go:build !unix

package checkpoint

import "os"

const openReadFlags = os.O_RDONLY

// isSymlinkRefusal returns false on platforms without a recognized symlink-refusal error.
func isSymlinkRefusal(error) bool { return false }
