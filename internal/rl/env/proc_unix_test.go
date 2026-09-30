//go:build unix

package env

import (
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// alive reports whether a process exists (signal 0).
func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err != nil {
		return err == syscall.EPERM
	}
	// A zombie still answers signal 0; check /proc where available.
	if b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat"); err == nil {
		s := string(b)
		if i := strings.LastIndexByte(s, ')'); i > 0 && i+2 < len(s) {
			return s[i+2] != 'Z'
		}
	}
	return true
}

func waitDead(t testing.TB, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !alive(pid) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Fatalf("process %d is still alive", pid)
}

func mkfifo(path string) error { return syscall.Mkfifo(path, 0o644) }
