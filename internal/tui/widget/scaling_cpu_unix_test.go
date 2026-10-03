//go:build unix

package widget

import (
	"syscall"
	"time"
)

const workUnit = "CPU ns"
const workResolution = uint64(500 * time.Microsecond)

// processWork reads cumulative user and kernel CPU nanoseconds for all threads.
func processWork() (uint64, error) {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0, err
	}
	return uint64(ru.Utime.Nano() + ru.Stime.Nano()), nil
}
