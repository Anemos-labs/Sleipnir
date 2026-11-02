//go:build unix

package workspace

import (
	"context"
	"os/exec"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// A verifier cancelled that stops on SIGTERM inside the grace period, its group then cleaned up, is not sent the SIGKILL the
// cancellation scheduled: by then its group id could be another program's.
func TestACancelledVerifierThatStoppedIsNotKilledLater(t *testing.T) {
	var kills atomic.Int32
	old := lateKill
	lateKill = func(pid int, sig syscall.Signal) error { kills.Add(1); return nil }
	t.Cleanup(func() { lateKill = old })

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "sh", "-c", "trap 'exit 0' TERM; while :; do sleep 0.05; done")
	stop := configureCmd(cmd)
	if err := cmd.Start(); err != nil {
		t.Skip(err)
	}
	time.Sleep(200 * time.Millisecond) // the trap is set
	cancel()
	_ = cmd.Wait()
	killGroup(cmd.Process.Pid)
	stop()
	time.Sleep(termGrace + 300*time.Millisecond)
	if n := kills.Load(); n != 0 {
		t.Errorf("a SIGKILL was sent %d times after the group was gone", n)
	}
}
