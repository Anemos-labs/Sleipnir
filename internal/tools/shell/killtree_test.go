//go:build unix

package shell

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

type nopSink struct{}

func (nopSink) write(string, []byte) {}
func (nopSink) detach()              {}

// SIGKILL cannot be caught or ignored, and it is not instant: the kernel finishes a process a moment after the signal is sent, a
// moment that a loaded machine makes longer. A member of the group that is still dying when killTree returns is running after the
// Shutdown, the timeout or the kill that was to end it (the nightly run, which runs the suite three times under the race detector,
// found a process that "survived Shutdown" this way once). The leader being reaped says nothing of the others, which are not its
// children, so killTree asks about the group until it is empty. Here the group answers that it is alive for the first few asks after
// the kill, as one that is slow to die does.
func TestKillTreeWaitsForTheGroupToBeGoneAfterSIGKILL(t *testing.T) {
	m := NewManager(Options{KillGrace: 50 * time.Millisecond})
	t.Cleanup(m.Shutdown)
	sh, err := m.shell()
	if err != nil {
		t.Skip(err)
	}
	if err := m.begin(); err != nil {
		t.Fatal(err)
	}
	defer m.end()
	// a process that ignores SIGTERM, so that the grace runs out and the kill is a SIGKILL (once it says that it does: a signal that
	// comes before the trap is set ends it)
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	p, err := m.startProc(procSpec{path: sh.path, args: append(append([]string(nil), sh.flags...), `trap '' TERM; : > ready; while :; do sleep 0.1; done`),
		dir: dir, env: os.Environ(), sink: nopSink{}})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the process to ignore SIGTERM", time.Minute, func() bool { _, err := os.Stat(ready); return err == nil })
	var asks atomic.Int32
	const slow = 3 // asks after the kill that still find a live member
	p.alive = func(int) bool { return asks.Add(1) <= slow }

	p.killTree()

	if got := asks.Load(); got <= slow {
		t.Fatalf("killTree returned after asking %d times whether the group was gone, and was told it was alive for the first %d", got, slow)
	}
	if !p.leaderDone() {
		t.Error("killTree returned before the leader was reaped")
	}
}
