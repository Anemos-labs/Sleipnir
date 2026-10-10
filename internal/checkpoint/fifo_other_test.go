//go:build !unix

package checkpoint

import "errors"

// mkfifo is not available on this platform.
func mkfifo(string, uint32) error { return errors.New("no named pipes here") }
