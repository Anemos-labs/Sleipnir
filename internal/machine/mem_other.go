//go:build !linux

package machine

// totalMemory is not read on this platform: memory is not watched.
func totalMemory() (int64, bool) { return 0, false }

// availableMemory is not read on this platform.
func availableMemory() (int64, bool) { return 0, false }

// GroupRSS is not read on this platform: every group is absent.
func GroupRSS([]int) map[int]int64 { return map[int]int64{} }
