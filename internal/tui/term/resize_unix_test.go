//go:build unix

package term

import (
	"os"
	"runtime"
	"testing"
	"time"
)

// waitForGoroutines waits until the process has no more goroutines than base. A goroutine that has returned may still be
// counted for a moment, so this yields until the count settles; the deadline is a hang guard, not a timing.
func waitForGoroutines(t *testing.T, base int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for runtime.NumGoroutine() > base {
		if time.Now().After(deadline) {
			t.Fatalf("goroutines outlived stop: %d now, %d before", runtime.NumGoroutine(), base)
		}
		runtime.Gosched()
	}
}

func TestWatchResizeStopEndsTheGoroutineAndClosesTheChannel(t *testing.T) {
	r, w, err := os.Pipe() // not a terminal: the watcher never has a size to send, which is fine
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()

	// The first signal.Notify starts a goroutine of the signal package that lives for the rest of the process; start it
	// before measuring.
	_, stop := WatchResize(w)
	stop()
	base := runtime.NumGoroutine()

	for i := 0; i < 50; i++ {
		sizes, stop := WatchResize(w)
		stop()
		stop() // idempotent
		if _, ok := <-sizes; ok {
			t.Fatal("the channel must be closed by stop")
		}
	}
	waitForGoroutines(t, base)
}

func TestWatchResizeOnANonTerminalSendsNothing(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	sizes, stop := WatchResize(w)
	sigwinch(t)
	stop() // whether the watcher saw the signal or the stop first, a pipe has no size to send
	if s, ok := <-sizes; ok {
		t.Errorf("a size from a pipe: %+v", s)
	}
}
