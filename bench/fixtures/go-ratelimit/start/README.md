# ratelimit

Token-bucket rate limiters (package `ratelimit`) and the HTTP middleware that uses
them (package `middleware`).

## The task: make the time injectable

The limiters call `time.Now()` and `time.Since()` directly, so a test of anything
that depends on elapsed time has to sleep. Refactor package `ratelimit` so that
the limiters read the time from a `Clock` that can be replaced, **without changing
anything that existing callers use**.

Add exactly this to package `ratelimit`:

```go
// Clock is where a limiter gets the time from.
type Clock interface {
	Now() time.Time
}

// NewWithClock is like New, but reads the time from clock.
// A nil clock means the real time, time.Now.
func NewWithClock(rate float64, burst int, clock Clock) *Limiter

// NewKeyedWithClock is like NewKeyed, but every reading of the time, including
// those of the limiters the Keyed creates for its keys, comes from clock.
// A nil clock means the real time, time.Now.
func NewKeyedWithClock(rate float64, burst int, clock Clock) *Keyed
```

- `Clock` has exactly one method, `Now() time.Time`. A test type that has just that
  method (a fake clock whose time only moves when the test says so) must satisfy it.
- A limiter or keyed limiter built with a clock takes **every** reading of the time
  from that clock: never from `time.Now`, `time.Since` or `time.Until`.
- Everything that exists today keeps its name, signature and behaviour:
  `New`, `NewKeyed`, and all methods of `Limiter` and `Keyed`. `New(rate, burst)` and
  `NewKeyed(rate, burst)` still use the real time.
- Package `middleware` stands for the existing callers. Do not change it; it must
  keep compiling and working as it is.

## Behaviour

This is how the limiters behave today. It must stay exactly this way; it is what
the tests check, with a fake clock.

### Limiter

A bucket holds at most `burst` tokens and gains `rate` tokens per second, continuously.

- `New(rate, burst)`: `rate` (tokens per second) is positive, `burst` (the capacity)
  is at least 1. The bucket starts full.
- Whenever a limiter is used, it first adds `rate` times the seconds that have passed
  since it last did so, up to the capacity `burst`.
- `AllowN(n)`: if the bucket holds at least `n` tokens, takes them and returns
  true. Otherwise returns false and takes nothing (a denied call never takes part of
  the tokens). An `n` larger than `burst` can therefore never be allowed. For `n <= 0`
  it returns true and takes nothing.
- `Allow()` is `AllowN(1)`.
- `Tokens()` returns the number of tokens in the bucket (after the refill) as a
  `float64`; it may be fractional.

### Keyed

One independent limiter per string key.

- `Allow(key)` is `Allow` on the limiter of `key`. A key that has not been seen before
  gets a new limiter with a full bucket. Every call counts as a use of the key at the
  time of the call, whether it is allowed or denied.
- `Len()` is the number of keys currently tracked.
- `Sweep(maxIdle)` forgets every key whose last use is **more than** `maxIdle` ago
  (a key that was last used exactly `maxIdle` ago stays) and returns how many keys it
  forgot. When a forgotten key comes back, it gets a new limiter with a full bucket.

## Example

```go
type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time          { return c.now }
func (c *fakeClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

clk := &fakeClock{now: time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)}
l := ratelimit.NewWithClock(2, 4, clk) // 2 tokens per second, holds at most 4
l.AllowN(3)                            // true: 1 token left
l.AllowN(2)                            // false: nothing is taken
clk.Advance(250 * time.Millisecond)    // gains 0.5 token
l.Tokens()                             // 1.5
l.Allow()                              // true: 0.5 left
clk.Advance(time.Hour)                 // refills, but never beyond the capacity
l.Tokens()                             // 4

k := ratelimit.NewKeyedWithClock(1, 1, clk)
k.Allow("a")                           // true
k.Allow("a")                           // false
clk.Advance(time.Second)
k.Allow("a")                           // true again: one token per second
```

## Checking your work

`go test ./...` runs the tests in the repository: they use the real clock, with
generous margins, and pass today. More tests, which use a fake clock and the new
functions, are run when your solution is verified.
