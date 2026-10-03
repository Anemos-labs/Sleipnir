//go:build !unix

package events

import "os"

const (
	openReadFlags = os.O_RDONLY
	openLogFlags  = os.O_CREATE | os.O_RDWR
)

// isSymlinkRefusal returns false where the platform has no recognized no-follow error.
func isSymlinkRefusal(error) bool { return false }
