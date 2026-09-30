package events

// Adversarial review tests for the event log (docs/reviews/swarm-concurrency.md):
// durability of the group commit, subscriber lifecycle, Close racing
// Emit/Subscribe, and the blob store's trust in files it finds on disk. The two
// defect repros (a burst's tail left in the buffer, Subscribe after Close) were
// gated behind SLEIPNIR_REVIEW=1 while they failed; both are fixed and are
// ordinary regression tests now, together with the edge cases the fix creates.

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/core"
)

func rvCountLines(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.Count(b, []byte("\n"))
}

// waitLines polls until path holds want lines and returns how long that took.
func waitLines(t *testing.T, path string, want int, within time.Duration) time.Duration {
	t.Helper()
	start := time.Now()
	for {
		n := rvCountLines(t, path)
		if n >= want {
			return time.Since(start)
		}
		if time.Since(start) > within {
			t.Fatalf("after %v only %d of %d events are on disk", within, n, want)
		}
		time.Sleep(500 * time.Microsecond)
	}
}

// countingWriter counts the write syscalls the log makes; fail makes them error.
type countingWriter struct {
	f    *os.File
	n    atomic.Int64
	fail atomic.Bool
}

func (c *countingWriter) Write(p []byte) (int, error) {
	c.n.Add(1)
	if c.fail.Load() {
		return 0, errors.New("disk full (test)")
	}
	return c.f.Write(p)
}

// instrument swaps the log's buffered writer for one over a countingWriter. Same
// package: a test may reach the log's internals, production code has no hook.
func instrument(t *testing.T, l *Log) *countingWriter {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.w.Flush(); err != nil {
		t.Fatal(err)
	}
	cw := &countingWriter{f: l.f}
	l.w = bufio.NewWriterSize(cw, 64<<10)
	return cw
}

// Group commit used to flush only when the NEXT event arrived more than 5ms after
// the previous flush; there was no timer. A burst followed by silence (every agent
// waiting on a long model call, or the manager parked in `wait`) left the tail of
// the burst in the bufio buffer indefinitely: a crash or SIGKILL then lost far
// more than "a handful of trailing events", and anything tailing the file saw
// nothing. A timer now flushes the tail when the window ends.
func TestConc_GroupCommitFlushesTheBurstTailInQuietPeriods(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir, "s")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	const n = 50
	for i := 0; i < n; i++ {
		if _, err := l.Emit("a", "x", i); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(500 * time.Millisecond) // quiet: no further events
	on := rvCountLines(t, filepath.Join(dir, "events.jsonl"))
	if want := n + 1; on != want {
		t.Fatalf("%d events emitted (plus log.open) and half a second later only %d are on disk; the rest sit in the 64KB buffer until the next Emit/Flush/Close", n, on)
	}
}

// The tail is out within a few milliseconds, not merely "eventually": the bound is the 5ms
// window plus scheduling, checked here with a generous margin for a loaded machine.
func TestGroupCommitTailIsFlushedWithinAFewMilliseconds(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir, "s")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	path := filepath.Join(dir, "events.jsonl")
	total := 1
	var worst time.Duration
	for round := 0; round < 5; round++ {
		for i := 0; i < 20; i++ {
			if _, err := l.Emit("a", "x", i); err != nil {
				t.Fatal(err)
			}
			total++
		}
		d := waitLines(t, path, total, 2*time.Second)
		worst = max(worst, d)
		time.Sleep(20 * time.Millisecond) // quiet
	}
	t.Logf("slowest tail flush: %v (window %v)", worst, groupCommitWindow)
	if worst > 250*time.Millisecond {
		t.Fatalf("the tail of a burst took %v to reach the disk; want a few milliseconds", worst)
	}
}

// An isolated event is still written before Emit returns: the timer must not add latency to the
// common case.
func TestGroupCommitWritesAnIsolatedEventAtOnce(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir, "s")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	path := filepath.Join(dir, "events.jsonl")
	for i := 0; i < 3; i++ {
		time.Sleep(3 * groupCommitWindow) // quiet: the window has passed
		if _, err := l.Emit("a", "x", i); err != nil {
			t.Fatal(err)
		}
		if got, want := rvCountLines(t, path), i+2; got != want {
			t.Fatalf("event %d: %d lines on disk right after Emit returned, want %d", i, got, want)
		}
	}
}

// Bursts keep sharing writes: thousands of events cost a handful of write syscalls, not one each.
func TestGroupCommitStillGroupsABurst(t *testing.T) {
	l, err := Open(t.TempDir(), "s")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	cw := instrument(t, l)
	const n = 20_000
	start := time.Now()
	for i := 0; i < n; i++ {
		if _, err := l.Emit("a", "x", i); err != nil {
			t.Fatal(err)
		}
	}
	elapsed := time.Since(start)
	time.Sleep(5 * groupCommitWindow) // let the tail go out
	writes := cw.n.Load()
	t.Logf("%d events in %v took %d write calls", n, elapsed, writes)
	if writes > n/20 {
		t.Fatalf("%d write calls for %d events: the group commit is not grouping", writes, n)
	}
	if got, want := rvCountLines(t, filepath.Join(l.Dir(), "events.jsonl")), n+1; got != want {
		t.Fatalf("%d events on disk, want %d", got, want)
	}
}

// Flush and Close write out everything, whatever the timer has or has not done yet.
func TestFlushAndCloseWriteEverythingEmittedBefore(t *testing.T) {
	for _, tc := range []struct {
		name string
		end  func(*Log) error
	}{
		{"Flush", func(l *Log) error { return l.Flush() }},
		{"Close", func(l *Log) error { return l.Close() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			l, err := Open(dir, "s")
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			const n = 200
			for i := 0; i < n; i++ {
				if _, err := l.Emit("a", "x", i); err != nil {
					t.Fatal(err)
				}
			}
			if err := tc.end(l); err != nil { // straight after the burst: the timer has not fired
				t.Fatal(err)
			}
			if got, want := rvCountLines(t, filepath.Join(dir, "events.jsonl")), n+1; got != want {
				t.Fatalf("%d events on disk right after %s, want %d", got, tc.name, want)
			}
		})
	}
}

// After Close the timer is gone: nothing writes to the closed file, nothing panics, and a second
// Close is harmless. Run with -race.
func TestCloseStopsTheBackgroundFlush(t *testing.T) {
	for i := 0; i < 20; i++ {
		l, err := Open(t.TempDir(), "s")
		if err != nil {
			t.Fatal(err)
		}
		for j := 0; j < 5; j++ {
			l.Emit("a", "x", j) // arms the timer
		}
		if err := l.Close(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * groupCommitWindow)
		l.mu.Lock()
		armed := l.timerArmed
		l.mu.Unlock()
		if armed {
			// The callback ran after Close and must have cleared the flag.
			t.Fatal("the flush timer is still armed after Close")
		}
		if err := l.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := l.Emit("a", "x", 0); err == nil {
			t.Fatal("Emit on a closed log succeeded")
		}
	}
}

// A failed flush by the timer is not lost: the next Emit, Flush and Close report it (bufio's error
// is sticky), so a caller learns that the log stopped being durable.
func TestATimerFlushFailureIsReportedByTheNextCall(t *testing.T) {
	l, err := Open(t.TempDir(), "s")
	if err != nil {
		t.Fatal(err)
	}
	cw := instrument(t, l)
	time.Sleep(3 * groupCommitWindow)
	if _, err := l.Emit("a", "x", 1); err != nil { // flushed at once: window passed
		t.Fatal(err)
	}
	cw.fail.Store(true)
	if _, err := l.Emit("a", "x", 2); err != nil { // buffered, the timer will try to write it
		t.Fatalf("a buffered event must not fail: %v", err)
	}
	time.Sleep(6 * groupCommitWindow) // the timer fires and fails
	if _, err := l.Emit("a", "x", 3); err == nil {
		t.Fatal("Emit after a failed timer flush reported no error")
	}
	if err := l.Flush(); err == nil {
		t.Fatal("Flush after a failed timer flush reported no error")
	}
	if err := l.Close(); err == nil {
		t.Fatal("Close after a failed timer flush reported no error")
	}
}

// Subscribing after Close hands back a channel that is already closed (it used to be one nobody
// would ever close).
func TestConc_SubscribeAfterCloseNeverDelivers(t *testing.T) {
	l, err := Open(t.TempDir(), "s")
	if err != nil {
		t.Fatal(err)
	}
	l.Close()
	ch, cancel := l.Subscribe(4)
	defer cancel()
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("event from a closed log")
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("Subscribe on a closed log returned an open channel that will never receive or close: a consumer ranging over it leaks forever")
	}
}

func TestSubscribeAfterCloseEdgeCases(t *testing.T) {
	l, err := Open(t.TempDir(), "s")
	if err != nil {
		t.Fatal(err)
	}
	// A subscriber that was open when the log closed sees its channel closed, and its cancel is harmless.
	live, cancelLive := l.Subscribe(4)
	l.Emit("a", "x", 1)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	var got int
	for range live {
		got++
	}
	if got != 1 {
		t.Fatalf("live subscriber received %d events before Close, want 1", got)
	}
	cancelLive()
	cancelLive()

	// Late subscribers, with any buffer size, get closed channels; cancelling them (twice) is a no-op.
	for _, buf := range []int{-1, 0, 1, 1000} {
		ch, cancel := l.Subscribe(buf)
		select {
		case _, ok := <-ch:
			if ok {
				t.Fatalf("buffer %d: event from a closed log", buf)
			}
		default:
			t.Fatalf("buffer %d: the channel is not closed", buf)
		}
		cancel()
		cancel()
	}
	l.mu.Lock()
	n := len(l.subs)
	l.mu.Unlock()
	if n != 0 {
		t.Fatalf("a closed log registered %d late subscribers", n)
	}
	// A consumer ranging over a late channel ends.
	ch, cancel := l.Subscribe(8)
	defer cancel()
	done := make(chan struct{})
	go func() {
		for range ch {
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("a consumer ranging over a late subscription never ended")
	}
}

// Subscribing concurrently with Close never hangs and never delivers after the close: whichever
// wins, the channel ends.
func TestSubscribeRacingCloseAlwaysEnds(t *testing.T) {
	for round := 0; round < 30; round++ {
		l, err := Open(t.TempDir(), "s")
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		for g := 0; g < 8; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ch, cancel := l.Subscribe(4)
				defer cancel()
				timeout := time.After(5 * time.Second)
				for {
					select {
					case _, ok := <-ch:
						if !ok {
							return
						}
					case <-timeout:
						t.Error("subscriber never saw its channel end")
						return
					}
				}
			}()
		}
		go l.Emit("a", "x", 1)
		l.Close()
		wg.Wait()
	}
}

// Emit, Subscribe, cancel, Flush and Close from many goroutines at once must never
// panic (send on closed channel, double close), deadlock or reorder.
func TestConcSound_LogCloseRacesEmitAndSubscribeStress(t *testing.T) {
	for round := 0; round < 8; round++ {
		l, err := Open(t.TempDir(), "s")
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		var afterClose atomic.Int32
		stop := make(chan struct{})
		for g := 0; g < 8; g++ {
			wg.Add(1)
			go func(g int) {
				defer wg.Done()
				for i := 0; ; i++ {
					select {
					case <-stop:
						return
					default:
					}
					if _, err := l.Emit(fmt.Sprintf("a%d", g), "e", i); err != nil {
						afterClose.Add(1)
					}
				}
			}(g)
		}
		for g := 0; g < 4; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-stop:
						return
					default:
					}
					ch, cancel := l.Subscribe(2)
					done := make(chan struct{})
					go func() {
						var last uint64
						for e := range ch {
							if e.Seq <= last {
								panic("subscriber saw events out of order")
							}
							last = e.Seq
						}
						close(done)
					}()
					time.Sleep(time.Duration(round%3) * 100 * time.Microsecond)
					cancel()
					cancel() // idempotent
					<-done
				}
			}()
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = l.Flush()
				}
			}
		}()
		time.Sleep(15 * time.Millisecond)
		if err := l.Close(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
		close(stop)
		wg.Wait()
		if afterClose.Load() == 0 {
			t.Fatalf("round %d: Emit never reported the closed log", round)
		}
	}
}

// Regression check. This was a finding (C-19): a blob whose file was torn by a
// crash poisoned the store, because Put trusted any existing file by name and Get
// never re-hashed. DirBlobs was hardened while this review was under way (Put
// verifies size and content and replaces a torn file; Get re-hashes), so this now
// passes.
func TestConcSound_BlobPutRepairsATornFile(t *testing.T) {
	d, err := NewDirBlobs(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("layer text "), 500)
	h, _ := d.Put(payload)
	if err := os.WriteFile(d.path(h), payload[:17], 0o644); err != nil { // torn write
		t.Fatal(err)
	}
	h2, _ := d.Put(payload) // the same content again: must repair
	got, err := d.Get(h2)
	if err != nil {
		t.Fatal(err)
	}
	if core.HashBytes(got) != h {
		t.Fatalf("blob %s now holds %d corrupt bytes (want %d)", h.Short(), len(got), len(payload))
	}
}
