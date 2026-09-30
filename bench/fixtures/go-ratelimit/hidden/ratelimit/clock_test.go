package ratelimit_test

import (
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"example.com/svc/ratelimit"
)

// fakeClock only moves when the test advances it. It is set far away from the
// real time, so that a stray call of time.Now or time.Since shows up.
type fakeClock struct{ now time.Time }

func newClock() *fakeClock {
	return &fakeClock{now: time.Date(2001, 9, 9, 1, 46, 40, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time          { return c.now }
func (c *fakeClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

// What existing callers use must keep its shape, and the new functions must have
// exactly the documented one: these declarations do not compile otherwise.
var (
	_ func(float64, int) *ratelimit.Limiter                  = ratelimit.New
	_ func(float64, int) *ratelimit.Keyed                    = ratelimit.NewKeyed
	_ func(*ratelimit.Limiter) bool                          = (*ratelimit.Limiter).Allow
	_ func(*ratelimit.Limiter, int) bool                     = (*ratelimit.Limiter).AllowN
	_ func(*ratelimit.Limiter) float64                       = (*ratelimit.Limiter).Tokens
	_ func(*ratelimit.Keyed, string) bool                    = (*ratelimit.Keyed).Allow
	_ func(*ratelimit.Keyed) int                             = (*ratelimit.Keyed).Len
	_ func(*ratelimit.Keyed, time.Duration) int              = (*ratelimit.Keyed).Sweep
	_ func(float64, int, ratelimit.Clock) *ratelimit.Limiter = ratelimit.NewWithClock
	_ func(float64, int, ratelimit.Clock) *ratelimit.Keyed   = ratelimit.NewKeyedWithClock
	_ ratelimit.Clock                                        = (*fakeClock)(nil)
	_ interface{ Now() time.Time }                           = ratelimit.Clock(nil)
)

func approx(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("Tokens() = %v, want %v", got, want)
	}
}

func TestClockStartsFull(t *testing.T) {
	l := ratelimit.NewWithClock(2, 4, newClock())
	approx(t, l.Tokens(), 4)
	for i := 1; i <= 4; i++ {
		if !l.Allow() {
			t.Fatalf("call %d of the burst was denied", i)
		}
	}
	if l.Allow() {
		t.Fatal("call 5 was allowed although the bucket should be empty")
	}
	approx(t, l.Tokens(), 0)
}

func TestClockRefill(t *testing.T) {
	clk := newClock()
	l := ratelimit.NewWithClock(2, 4, clk) // 2 tokens per second, holds at most 4
	if !l.AllowN(3) {
		t.Fatal("AllowN(3) was denied")
	}
	approx(t, l.Tokens(), 1)
	if l.AllowN(2) {
		t.Fatal("AllowN(2) with 1 token was allowed")
	}
	approx(t, l.Tokens(), 1) // a denied call takes nothing
	clk.Advance(250 * time.Millisecond)
	approx(t, l.Tokens(), 1.5)
	if l.AllowN(2) {
		t.Fatal("AllowN(2) with 1.5 tokens was allowed")
	}
	if !l.Allow() {
		t.Fatal("Allow with 1.5 tokens was denied")
	}
	approx(t, l.Tokens(), 0.5)
	clk.Advance(250 * time.Millisecond) // exactly one token again
	if !l.Allow() {
		t.Fatal("Allow with exactly 1 token was denied")
	}
	approx(t, l.Tokens(), 0)
	if l.Allow() {
		t.Fatal("Allow on an empty bucket was allowed")
	}
}

func TestClockRefillStopsAtBurst(t *testing.T) {
	clk := newClock()
	l := ratelimit.NewWithClock(2, 4, clk)
	l.AllowN(4)
	clk.Advance(time.Hour)
	approx(t, l.Tokens(), 4)
	if l.AllowN(5) {
		t.Fatal("AllowN(5) was allowed with a capacity of 4")
	}
	if !l.AllowN(4) {
		t.Fatal("AllowN(4) was denied on a full bucket")
	}
	if l.Allow() {
		t.Fatal("the bucket should be empty again")
	}
}

func TestClockFrozenTimeAddsNothing(t *testing.T) {
	l := ratelimit.NewWithClock(1000, 2, newClock())
	l.AllowN(2)
	for i := 0; i < 100; i++ {
		if l.Allow() {
			t.Fatalf("call %d was allowed although no time has passed", i+1)
		}
	}
	approx(t, l.Tokens(), 0)
}

func TestClockAllowNBiggerThanBurst(t *testing.T) {
	clk := newClock()
	l := ratelimit.NewWithClock(10, 3, clk)
	if l.AllowN(4) {
		t.Fatal("AllowN(4) was allowed with a capacity of 3")
	}
	approx(t, l.Tokens(), 3) // and nothing was taken
	clk.Advance(time.Hour)
	if l.AllowN(4) {
		t.Fatal("AllowN(4) was allowed with a capacity of 3, after an hour")
	}
}

func TestClockAllowNNonPositive(t *testing.T) {
	l := ratelimit.NewWithClock(1, 3, newClock())
	for _, n := range []int{0, -1, -100} {
		if !l.AllowN(n) {
			t.Fatalf("AllowN(%d) was denied", n)
		}
	}
	approx(t, l.Tokens(), 3)
}

func TestClockFractionalRate(t *testing.T) {
	clk := newClock()
	l := ratelimit.NewWithClock(0.5, 1, clk) // one token every two seconds
	if !l.Allow() || l.Allow() {
		t.Fatal("want the first call allowed and the second denied")
	}
	clk.Advance(time.Second)
	if l.Allow() {
		t.Fatal("allowed after 1s at a rate of 0.5 per second")
	}
	clk.Advance(time.Second)
	if !l.Allow() {
		t.Fatal("denied after 2s at a rate of 0.5 per second")
	}
}

func TestClockLimitersDoNotShareTime(t *testing.T) {
	a, b := newClock(), newClock()
	la := ratelimit.NewWithClock(1, 1, a)
	lb := ratelimit.NewWithClock(1, 1, b)
	la.Allow()
	lb.Allow()
	a.Advance(time.Second)
	if !la.Allow() {
		t.Fatal("la was not refilled by its own clock")
	}
	if lb.Allow() {
		t.Fatal("lb was refilled by the other clock")
	}
}

func TestClockConcurrentCallsGrantExactlyTheBurst(t *testing.T) {
	l := ratelimit.NewWithClock(1, 100, newClock())
	var granted atomic.Int64
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				if l.Allow() {
					granted.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	if n := granted.Load(); n != 100 {
		t.Fatalf("%d calls were allowed, want exactly 100", n)
	}
}

func TestNilClockMeansRealTime(t *testing.T) {
	l := ratelimit.NewWithClock(0.001, 2, nil)
	if !l.Allow() || !l.Allow() || l.Allow() {
		t.Fatal("NewWithClock(_, _, nil) should allow exactly the burst of 2 at once")
	}
	k := ratelimit.NewKeyedWithClock(0.001, 1, nil)
	if !k.Allow("a") || k.Allow("a") || !k.Allow("b") {
		t.Fatal("NewKeyedWithClock(_, _, nil) should behave like NewKeyed")
	}
}

func TestKeyedRefillUsesTheClock(t *testing.T) {
	clk := newClock()
	k := ratelimit.NewKeyedWithClock(1, 2, clk) // 1 token per second, burst 2
	if !k.Allow("a") || !k.Allow("a") || k.Allow("a") {
		t.Fatal(`want "a": allowed, allowed, denied`)
	}
	if !k.Allow("b") {
		t.Fatal(`"b" has its own bucket`)
	}
	clk.Advance(time.Second)
	if !k.Allow("a") {
		t.Fatal(`"a" was not refilled after 1s: the limiters of a Keyed must use its clock`)
	}
	if k.Allow("a") {
		t.Fatal(`"a" got more than the one token a second allows`)
	}
	clk.Advance(2 * time.Second)
	if !k.Allow("a") || !k.Allow("a") || k.Allow("a") {
		t.Fatal(`after 2s "a" should have 2 tokens (the capacity): allowed, allowed, denied`)
	}
}

func TestKeyedSweep(t *testing.T) {
	clk := newClock()
	k := ratelimit.NewKeyedWithClock(1, 1, clk)
	k.Allow("a") // used at 0s
	clk.Advance(30 * time.Second)
	k.Allow("b") // used at 30s
	clk.Advance(30 * time.Second)
	k.Allow("c") // used at 60s
	if n := k.Len(); n != 3 {
		t.Fatalf("Len() = %d, want 3", n)
	}
	// now: "a" has been idle for 60s, "b" for 30s, "c" for 0s
	if n := k.Sweep(45 * time.Second); n != 1 {
		t.Fatalf("Sweep(45s) = %d, want 1", n)
	}
	if n := k.Len(); n != 2 {
		t.Fatalf("Len() = %d after the first sweep, want 2", n)
	}
	if n := k.Sweep(45 * time.Second); n != 0 {
		t.Fatalf("second Sweep(45s) = %d, want 0", n)
	}
	clk.Advance(20 * time.Second) // "b" has been idle for 50s, "c" for 20s
	if n := k.Sweep(45 * time.Second); n != 1 {
		t.Fatalf("Sweep(45s) = %d, want 1", n)
	}
	clk.Advance(time.Hour)
	if n := k.Sweep(time.Minute); n != 1 {
		t.Fatalf("Sweep(1m) = %d, want 1", n)
	}
	if n := k.Len(); n != 0 {
		t.Fatalf("Len() = %d, want 0", n)
	}
}

func TestKeyedSweepKeepsKeysIdleExactlyMaxIdle(t *testing.T) {
	clk := newClock()
	k := ratelimit.NewKeyedWithClock(1, 1, clk)
	k.Allow("a")
	clk.Advance(10 * time.Second)
	if n := k.Sweep(10 * time.Second); n != 0 {
		t.Fatalf("Sweep forgot a key that was idle for exactly maxIdle (%d keys)", n)
	}
	clk.Advance(time.Nanosecond)
	if n := k.Sweep(10 * time.Second); n != 1 {
		t.Fatalf("Sweep = %d for a key idle for more than maxIdle, want 1", n)
	}
}

func TestKeyedDeniedCallsCountAsUse(t *testing.T) {
	clk := newClock()
	k := ratelimit.NewKeyedWithClock(0.001, 1, clk)
	if !k.Allow("a") { // used at 0s
		t.Fatal(`first Allow("a") was denied`)
	}
	clk.Advance(40 * time.Second)
	if k.Allow("a") { // denied, but a use at 40s
		t.Fatal(`second Allow("a") was allowed`)
	}
	clk.Advance(40 * time.Second) // 80s: idle for 40s since the denied call
	if n := k.Sweep(time.Minute); n != 0 {
		t.Fatalf("Sweep(1m) forgot a key whose last call, a denied one, was 40s ago (%d keys)", n)
	}
	clk.Advance(30 * time.Second) // 110s: idle for 70s
	if n := k.Sweep(time.Minute); n != 1 {
		t.Fatalf("Sweep(1m) = %d, want 1", n)
	}
}

func TestKeyedSweptKeyStartsFullAgain(t *testing.T) {
	clk := newClock()
	k := ratelimit.NewKeyedWithClock(0.001, 2, clk) // a token takes 1000s
	if !k.Allow("a") || !k.Allow("a") || k.Allow("a") {
		t.Fatal(`want "a": allowed, allowed, denied`)
	}
	clk.Advance(time.Minute)
	if n := k.Sweep(30 * time.Second); n != 1 {
		t.Fatalf("Sweep(30s) = %d, want 1", n)
	}
	if !k.Allow("a") || !k.Allow("a") || k.Allow("a") {
		t.Fatal(`after being forgotten "a" should get a full bucket of 2 again`)
	}
}

func TestKeyedsDoNotShareTime(t *testing.T) {
	a, b := newClock(), newClock()
	ka := ratelimit.NewKeyedWithClock(1, 1, a)
	kb := ratelimit.NewKeyedWithClock(1, 1, b)
	ka.Allow("x")
	kb.Allow("x")
	a.Advance(time.Hour)
	if n := ka.Sweep(time.Minute); n != 1 {
		t.Fatalf("ka.Sweep = %d, want 1", n)
	}
	if n := kb.Sweep(time.Minute); n != 0 {
		t.Fatalf("kb.Sweep = %d, want 0: its clock has not moved", n)
	}
}
