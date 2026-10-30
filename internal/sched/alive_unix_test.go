//go:build unix

package sched

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

// alive reports whether process pid still runs (a zombie waiting for its parent counts as gone).
func alive(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	return err != nil || !strings.Contains(string(b), ") Z ")
}
