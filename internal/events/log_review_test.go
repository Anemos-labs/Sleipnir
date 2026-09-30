package events

// Adversarial review tests for the event log (docs/reviews/swarm-concurrency.md):
// durability of the group commit, subscriber lifecycle, Close racing
// Emit/Subscribe, and the blob store's trust in files it finds on disk.
// TestConc_* are gated repros (SLEIPNIR_REVIEW=1, fail while the finding is
// open); TestConcSound_* is an ungated stress check.
//
//	SLEIPNIR_REVIEW=1 go test -race -count=1 -run 'TestConc_' ./internal/events

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/core"
)

// concGate skips a defect repro unless SLEIPNIR_REVIEW is set (same switch the
// security review uses). Repros assert the CORRECT behaviour and therefore fail
// while the finding is open; TestConcSound_* tests are ungated regression checks
// for behaviour the review found sound.
func concGate(t *testing.T) {
	t.Helper()
	if os.Getenv("SLEIPNIR_REVIEW") == "" {
		t.Skip("concurrency-review repro: set SLEIPNIR_REVIEW=1 (asserts the correct behaviour, fails while the finding is open)")
	}
}

func rvCountLines(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.Count(b, []byte("\n"))
}

// Group commit flushes only when the NEXT event arrives more than 5ms after the
// previous flush; there is no timer. A burst followed by silence (every agent
// waiting on a long model call, or the manager parked in `wait`) leaves the tail of
// the burst in the bufio buffer indefinitely: a crash or SIGKILL then loses far
// more than "a handful of trailing events", and anything tailing the file sees
// nothing.
func TestConc_GroupCommitLeavesTheBurstTailUnflushedInQuietPeriods(t *testing.T) {
	concGate(t)
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

// Subscribing after Close hands back a channel nobody will ever close.
func TestConc_SubscribeAfterCloseNeverDelivers(t *testing.T) {
	concGate(t)
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

// A blob whose file was torn by a crash (rename can persist before the data;
// Put never fsyncs) poisons the store: Put trusts any existing file by name, Get
// never re-hashes.
func TestConc_BlobPutTrustsATornFileForever(t *testing.T) {
	concGate(t)
	d, err := NewDirBlobs(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("layer text "), 500)
	h, _ := d.Put(payload)
	if err := os.WriteFile(d.path(h), payload[:17], 0o644); err != nil { // torn write
		t.Fatal(err)
	}
	h2, _ := d.Put(payload) // the same content again: should repair
	got, err := d.Get(h2)
	if err != nil {
		t.Fatal(err)
	}
	if core.HashBytes(got) != h {
		t.Fatalf("blob %s now holds %d corrupt bytes (want %d): Put skipped the write because the file exists and Get returns it without verifying the hash", h.Short(), len(got), len(payload))
	}
}
