//go:build unix

package gitx

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

// processRunning reports whether pid is a live process (zombies count as dead).
func processRunning(pid int) bool {
	if err := syscall.Kill(pid, 0); err != nil {
		return false
	}
	if b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat"); err == nil && strings.Contains(string(b), ") Z ") {
		return false
	}
	return true
}
