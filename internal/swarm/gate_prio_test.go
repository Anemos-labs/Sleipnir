package swarm

// Tests for the priority-aware warm gate (agent.PriorityGate). The gate sits in front
// of the governor, which admits requests by priority; a request that outranks the one
// priming a cold level must not wait for that primer's first byte, because the primer
// may itself still be queued behind other work.
//
// The gates here have a maxWait of an hour, so the escalation of a stuck primer (which
// releases co-primers by elapsed time) can never explain a pass: whatever gets through
// does so by priority, by warmth or by the primer's own report.

import (
	"context"
	"errors"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/agent"
)

func prioGate() *WarmGate { return NewWarmGate(time.Minute, time.Hour) }

// gateReq is one request at the gate, entered from its own goroutine.
type gateReq struct {
	passed  chan struct{}
	started func(bool)
	err     error
}

func enterAsync(g *WarmGate, ctx context.Context, key string, prio int) *gateReq {
	r := &gateReq{passed: make(chan struct{})}
	go func() {
		defer close(r.passed)
		r.started, r.err = g.EnterPrio(ctx, key, prio)
	}()
	return r
}

// passes reports whether the request got through the gate within d.
func (r *gateReq) passes(d time.Duration) bool {
	select {
	case <-r.passed:
		return true
	case <-time.After(d):
		return false
	}
}

const (
	gateHang  = 10 * time.Second      // a pass that must happen: only a bug makes it take this long
	gateStill = 60 * time.Millisecond // a wait that must persist
)

func mustPass(t *testing.T, r *gateReq, what string) {
	t.Helper()
	if !r.passes(gateHang) {
		t.Fatalf("%s did not get through the gate", what)
	}
	if r.err != nil {
		t.Fatalf("%s: %v", what, r.err)
	}
}

func mustWait(t *testing.T, r *gateReq, what string) {
	t.Helper()
	if r.passes(gateStill) {
		t.Fatalf("%s got through the gate while the level is cold and being primed", what)
	}
}

func TestWarmGatePrio_ARequestThatOutranksThePrimerDoesNotWaitForIt(t *testing.T) {
	g := prioGate()
	ctx := context.Background()
	primer := enterAsync(g, ctx, "k", agent.PrioWorker)
	mustPass(t, primer, "the primer")
	peer := enterAsync(g, ctx, "k", agent.PrioWorker)
	mustWait(t, peer, "a second worker")

	mgr := enterAsync(g, ctx, "k", agent.PrioInteractive)
	mustPass(t, mgr, "the manager, who outranks the worker priming the level")

	// The bypasser is a co-primer, not a shortcut: nothing is warm until somebody's
	// first byte, and the worker still waits.
	mustWait(t, peer, "the worker before any first byte")
	if g.Warm("k") {
		t.Fatal("the level is warm before any request has started")
	}
	// Its first byte warms the level for everyone still waiting, though the primer has
	// not reported.
	mgr.started(true)
	mustPass(t, peer, "the worker after the manager's first byte")
	if !g.Warm("k") {
		t.Fatal("the co-primer's first byte must warm the level")
	}
	peer.started(true)
	primer.started(true)
}

func TestWarmGatePrio_EqualAndLowerPrioritiesStillWaitForThePrimer(t *testing.T) {
	g := prioGate()
	ctx := context.Background()
	primer := enterAsync(g, ctx, "k", agent.PrioWorker)
	mustPass(t, primer, "the primer")
	peer := enterAsync(g, ctx, "k", agent.PrioWorker)
	low := enterAsync(g, ctx, "k", agent.PrioBackground)
	mustWait(t, peer, "a worker behind a worker (equal priority)")
	mustWait(t, low, "a background request behind a worker (lower priority)")
	primer.started(true)
	mustPass(t, peer, "the worker after the primer's first byte")
	mustPass(t, low, "the background request after the primer's first byte")
	peer.started(true)
	low.started(true)
}

func TestWarmGatePrio_NobodyOutranksAManagerPrimer(t *testing.T) {
	g := prioGate()
	ctx := context.Background()
	primer := enterAsync(g, ctx, "k", agent.PrioInteractive)
	mustPass(t, primer, "the manager as primer")
	mgr2 := enterAsync(g, ctx, "k", agent.PrioInteractive)
	w := enterAsync(g, ctx, "k", agent.PrioWorker)
	mustWait(t, mgr2, "a second request of the top priority")
	mustWait(t, w, "a worker")
	primer.started(true)
	mustPass(t, mgr2, "the second manager request")
	mustPass(t, w, "the worker")
	mgr2.started(true)
	w.started(true)
}

// A background request primes; a worker outranks it and goes on; a manager outranks
// the worker and goes on; a second of either class waits, because each class is let
// past once.
func TestWarmGatePrio_OneBypassPerPriorityClass(t *testing.T) {
	g := prioGate()
	ctx := context.Background()
	primer := enterAsync(g, ctx, "k", agent.PrioBackground)
	mustPass(t, primer, "the background primer")

	w1 := enterAsync(g, ctx, "k", agent.PrioWorker)
	mustPass(t, w1, "the first worker (outranks the primer)")
	w2 := enterAsync(g, ctx, "k", agent.PrioWorker)
	mustWait(t, w2, "a second worker (its class has already gone past the primer)")

	m1 := enterAsync(g, ctx, "k", agent.PrioInteractive)
	mustPass(t, m1, "the first manager request (outranks the worker that went past)")
	m2 := enterAsync(g, ctx, "k", agent.PrioInteractive)
	mustWait(t, m2, "a second manager request")
	bg := enterAsync(g, ctx, "k", agent.PrioBackground)
	mustWait(t, bg, "another background request")

	// One first byte, from any co-primer, releases everyone still waiting.
	w1.started(true)
	for name, r := range map[string]*gateReq{"second worker": w2, "second manager request": m2, "background request": bg} {
		mustPass(t, r, name)
	}
	for _, r := range []*gateReq{primer, m1, m2, w2, bg} {
		r.started(true)
	}
}

// A bypasser that dies before its first byte has warmed nothing and is not priming
// any more: the level is still being primed by the request that was there first.
func TestWarmGatePrio_ABypasserThatFailsLeavesThePrimerPriming(t *testing.T) {
	g := prioGate()
	ctx := context.Background()
	primer := enterAsync(g, ctx, "k", agent.PrioWorker)
	mustPass(t, primer, "the primer")
	mgr := enterAsync(g, ctx, "k", agent.PrioInteractive)
	mustPass(t, mgr, "the manager")
	w := enterAsync(g, ctx, "k", agent.PrioWorker)
	mustWait(t, w, "a worker")

	mgr.started(false) // died before its first byte
	mustWait(t, w, "a worker after the manager's request failed")
	if g.Warm("k") {
		t.Fatal("a failed request must not warm the level")
	}
	primer.started(true)
	mustPass(t, w, "the worker after the primer's first byte")
	w.started(true)
}

// When the primer ends without a first byte a waiter is elected in its place, and the
// bar starts again from that new primer: the manager may go past a worker primer again.
func TestWarmGatePrio_ANewPrimerResetsTheBar(t *testing.T) {
	g := prioGate()
	ctx := context.Background()
	p1 := enterAsync(g, ctx, "k", agent.PrioWorker)
	mustPass(t, p1, "the first primer")
	m1 := enterAsync(g, ctx, "k", agent.PrioInteractive)
	mustPass(t, m1, "the manager")
	m1.started(false)
	p1.started(false) // the primer died as well: nobody is priming

	p2 := enterAsync(g, ctx, "k", agent.PrioWorker)
	mustPass(t, p2, "a worker, elected primer of the cold level")
	m2 := enterAsync(g, ctx, "k", agent.PrioInteractive)
	mustPass(t, m2, "a manager request behind the new worker primer (the bar was reset by the election)")
	w := enterAsync(g, ctx, "k", agent.PrioWorker)
	mustWait(t, w, "a worker behind the new primer")
	p2.started(true)
	mustPass(t, w, "the worker")
	m2.started(true)
	w.started(true)
}

// The levels of a key ("shared|role") are each passed by priority.
func TestWarmGatePrio_EveryLevelOfAKeyIsPassedByPriority(t *testing.T) {
	g := prioGate()
	ctx := context.Background()
	primer := enterAsync(g, ctx, "s|r", agent.PrioWorker)
	mustPass(t, primer, "the primer of both levels")
	mgr := enterAsync(g, ctx, "s|r", agent.PrioInteractive)
	mustPass(t, mgr, "the manager, past the primer at the shared level and at the role level")

	// A worker of another role waits for the shared level, which is being primed by a
	// worker; the manager's first byte warms it.
	sib := enterAsync(g, ctx, "s|q", agent.PrioWorker)
	mustWait(t, sib, "a worker of a sibling role")
	mgr.started(true)
	mustPass(t, sib, "the sibling worker once the shared level is warm (it primes its own role level)")
	if !g.Warm("s|r") || g.Warm("s|q") {
		t.Fatalf("the manager's first byte warms its own levels only: s|r=%v s|q=%v", g.Warm("s|r"), g.Warm("s|q"))
	}
	sib.started(true)
	primer.started(true)
	if !g.Warm("s|q") {
		t.Fatal("the sibling's first byte warms its role level")
	}
}

// A request that gives up while waiting leaves the gate as it was: the primer is
// unaffected and a request that outranks it still goes past.
func TestWarmGatePrio_ACancelledWaiterLeavesTheGateUsable(t *testing.T) {
	g := prioGate()
	primer := enterAsync(g, context.Background(), "k", agent.PrioWorker)
	mustPass(t, primer, "the primer")
	ctx, cancel := context.WithCancel(context.Background())
	w := enterAsync(g, ctx, "k", agent.PrioWorker)
	mustWait(t, w, "a worker")
	cancel()
	if !w.passes(gateHang) {
		t.Fatal("a cancelled waiter must return")
	}
	if !errors.Is(w.err, context.Canceled) {
		t.Fatalf("a cancelled waiter must report its context's error, got %v", w.err)
	}
	mgr := enterAsync(g, context.Background(), "k", agent.PrioInteractive)
	mustPass(t, mgr, "the manager after a waiter gave up")
	mgr.started(true)
	primer.started(true)
}

// A plain Enter is a worker: it never outranks another worker, so a gate used through
// the old interface behaves exactly as it did.
func TestWarmGatePrio_PlainEnterCountsAsAWorker(t *testing.T) {
	g := prioGate()
	first, err := g.Enter(context.Background(), "k")
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan struct{})
	go func() {
		s, err := g.Enter(context.Background(), "k")
		if err == nil {
			s(true)
		}
		close(got)
	}()
	select {
	case <-got:
		t.Fatal("a plain Enter went past a plain Enter that is priming")
	case <-time.After(gateStill):
	}
	first(true)
	select {
	case <-got:
	case <-time.After(gateHang):
		t.Fatal("the follower did not get through after the primer's first byte")
	}
	// ...and a worker-priority EnterPrio waits behind a plain Enter just the same.
	g2 := prioGate()
	first2, _ := g2.Enter(context.Background(), "k")
	w := enterAsync(g2, context.Background(), "k", agent.PrioWorker)
	mustWait(t, w, "a worker behind a plain Enter")
	first2(true)
	mustPass(t, w, "the worker")
	w.started(true)
}

// A primer that never reports for very many escalation intervals must not wedge the
// level. The co-primer releases double every interval; uncapped, that count wrapped
// negative after 63 intervals and nobody was ever released again.
func TestWarmGate_ASilentPrimerNeverWedgesTheLevel(t *testing.T) {
	g := NewWarmGate(5*time.Minute, time.Second)
	clock := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	g.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
	if _, err := g.Enter(context.Background(), "k"); err != nil { // the primer; it never reports
		t.Fatal(err)
	}
	for _, intervals := range []int{5, 62, 63, 64, 65, 200, 100000} {
		mu.Lock()
		clock = clock.Add(time.Duration(intervals) * time.Second)
		mu.Unlock()
		r := enterAsync(g, context.Background(), "k", agent.PrioWorker)
		if !r.passes(gateHang) {
			t.Fatalf("%d intervals after a primer that never reported, a request is still held at the gate", intervals)
		}
		r.started(false) // the co-primer fails too, so the level stays cold for the next round
	}
}

// Random priorities, arrival times and hold times over one cold key: nobody is
// stranded, and the requests that get through while the level is still cold are the
// primer plus at most one per class that outranks it (three classes: at most three).
func TestWarmGatePrio_StressNeverStrandsAndBoundsTheColdPassers(t *testing.T) {
	rounds := 30
	if testing.Short() {
		rounds = 5
	}
	for round := 0; round < rounds; round++ {
		g := prioGate()
		rng := rand.New(rand.NewSource(int64(round)))
		const n = 24
		var warm atomic.Bool // set just before the first started(true)
		var cold atomic.Int32
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			prio := rng.Intn(3)
			arrive := time.Duration(rng.Intn(3000)) * time.Microsecond
			hold := time.Duration(rng.Intn(3000)) * time.Microsecond
			wg.Add(1)
			go func() {
				defer wg.Done()
				time.Sleep(arrive)
				started, err := g.EnterPrio(context.Background(), "k", prio)
				if err != nil {
					t.Error(err)
					return
				}
				// warm only ever turns true, and the level is only warm after it did: a
				// request that still reads false here passed a cold level.
				if !warm.Load() {
					cold.Add(1)
				}
				time.Sleep(hold)
				warm.Store(true)
				started(true)
			}()
		}
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(60 * time.Second):
			t.Fatalf("round %d: requests are stranded at the gate", round)
		}
		if c := cold.Load(); c > 3 {
			t.Fatalf("round %d: %d requests went past a cold level, at most 3 (the primer and one per outranking class)", round, c)
		}
		if !g.Warm("k") {
			t.Fatalf("round %d: the level is cold after every request succeeded", round)
		}
	}
}

// The same with failures, cancellations and nested keys: nothing deadlocks and the gate
// is left usable.
func TestWarmGatePrio_StressWithFailuresAndNestedKeysNeverDeadlocks(t *testing.T) {
	rounds := 30
	if testing.Short() {
		rounds = 5
	}
	keys := []string{"s", "s|a", "s|b", "t|a"}
	for round := 0; round < rounds; round++ {
		g := NewWarmGate(time.Minute, 20*time.Millisecond) // short escalation: a failed primer's waiters make progress either way
		rng := rand.New(rand.NewSource(int64(1000 + round)))
		var wg sync.WaitGroup
		for i := 0; i < 32; i++ {
			key := keys[rng.Intn(len(keys))]
			prio := rng.Intn(3)
			arrive := time.Duration(rng.Intn(4000)) * time.Microsecond
			hold := time.Duration(rng.Intn(4000)) * time.Microsecond
			fail := rng.Intn(4) == 0
			cancelAfter := time.Duration(0)
			if rng.Intn(5) == 0 {
				cancelAfter = time.Duration(1+rng.Intn(6)) * time.Millisecond
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if cancelAfter > 0 {
					time.AfterFunc(cancelAfter, cancel)
				}
				time.Sleep(arrive)
				started, err := g.EnterPrio(ctx, key, prio)
				if err != nil {
					return
				}
				time.Sleep(hold)
				started(!fail)
			}()
		}
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(60 * time.Second):
			t.Fatalf("round %d: the gate deadlocked", round)
		}
		// Whatever happened, a fresh request gets through in bounded time.
		fin, err := func() (func(bool), error) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return g.EnterPrio(ctx, "s|a", agent.PrioWorker)
		}()
		if err != nil {
			t.Fatalf("round %d: the gate is wedged after the storm: %v", round, err)
		}
		fin(true)
	}
}
