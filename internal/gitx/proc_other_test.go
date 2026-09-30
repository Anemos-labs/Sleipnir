//go:build !unix

package gitx

func processRunning(pid int) bool { return false }
