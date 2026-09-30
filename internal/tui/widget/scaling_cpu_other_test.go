//go:build !unix

package widget

import "time"

// processCPUTime: this system has no cheap way to ask, and costStart measures the wall clock instead.
func processCPUTime() (d time.Duration, ok bool) { return 0, false }
