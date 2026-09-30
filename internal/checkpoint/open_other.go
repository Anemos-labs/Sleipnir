//go:build !unix

package checkpoint

import "os"

const openReadFlags = os.O_RDONLY

func isSymlinkRefusal(error) bool { return false }
