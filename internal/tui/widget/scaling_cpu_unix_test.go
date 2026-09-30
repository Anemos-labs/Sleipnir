//go:build unix

package widget

import (
	"syscall"
	"time"
)

// processCPUTime is the CPU time (user and system, all threads) the process has used so far. ok is false when the system
// does not say.
func processCPUTime() (d time.Duration, ok bool) {
	var ru syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &ru) != nil {
		return 0, false
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano()), true
}
