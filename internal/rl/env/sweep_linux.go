//go:build linux

package env

import (
	"bytes"
	"io"
	"os"
	"strconv"
	"syscall"
)

// sweepMarker kills every process whose initial environment carries the run
// marker. A daemon that double-forked out of the command's process group keeps
// the marker (environments are inherited) unless it scrubs its own, so this
// finds most stragglers that killGroup cannot. It is a best-effort net: it
// needs /proc, only sees processes the harness may read (same user), and a
// determined process can clear its environment. Real containment needs a
// container or a cgroup.
func sweepMarker(marker string) int {
	needle := []byte(MarkerEnv + "=" + marker + "\x00")
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return 0
	}
	self := os.Getpid()
	killed := 0
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 1 || pid == self {
			continue
		}
		f, err := os.Open("/proc/" + e.Name() + "/environ")
		if err != nil {
			continue // exited, or not ours
		}
		b, _ := io.ReadAll(io.LimitReader(f, 1<<20))
		f.Close()
		if bytes.Contains(append(b, 0), needle) {
			if syscall.Kill(pid, syscall.SIGKILL) == nil {
				killed++
			}
		}
	}
	return killed
}
