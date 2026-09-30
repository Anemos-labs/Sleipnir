//go:build !unix

package events

import "os"

const (
	openReadFlags = os.O_RDONLY
	openLogFlags  = os.O_CREATE | os.O_RDWR
)

func isSymlinkRefusal(error) bool { return false }
