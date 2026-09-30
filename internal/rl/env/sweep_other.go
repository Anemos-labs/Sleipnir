//go:build !linux

package env

// sweepMarker is a no-op where there is no /proc to search.
func sweepMarker(marker string) int { return 0 }
