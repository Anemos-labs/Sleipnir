//go:build !unix && !windows

package widget

import "time"

const workUnit = "wall ns"
const workResolution = uint64(500 * time.Microsecond)

var workEpoch = time.Now()

// processWork uses monotonic elapsed time on systems without a process counter.
func processWork() (uint64, error) { return uint64(time.Since(workEpoch)), nil }
