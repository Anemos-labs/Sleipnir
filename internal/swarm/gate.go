package swarm

import (
	"context"
	"strings"
	"sync"
	"time"
)

// WarmGate stops a fan-out from paying for the same prefix many times.
//
// A provider only serves a cached prefix once some request has begun
// responding, so N agents launched together over a cold shared prefix would all
// prefill it at full price. The gate lets one request through as the primer,
// holds the rest until the primer's first byte, then releases them to read what
// it wrote. When the prefix is already warm nobody waits. No extra warm-up
// request is needed: the first real request is the primer.
//
// Keys and levels. The physical prefix a request needs is nested: the shared
// prefix (tools, constitution, shared pin) on the engine its routing key pins
// it to, then the role prefix (plus the role pin) inside it. A key is therefore
// a list of levels joined by "|", outermost first, and the gate keeps one state
// per level path ("a", "a|b"): a request must pass every level, being the primer
// of each level that is cold. Agents of a second role are not released onto a
// role prefix nobody has written yet, and agents on a second shard are not
// released onto an engine that has never seen the shared one. A request that is
// the primer at an outer level never waits at an inner one behind a request that
// itself passed the outer level, so levels cannot deadlock.
//
// Timing. The provider measures an entry's lifetime from the start of the
// request that wrote or last read it, so a level is presumed warm from that
// request's start (not its first byte) for the lifetime less a safety margin.
//
// A stuck primer (a slow cold prefill, a hung connection) must neither hold the
// swarm forever nor release it all at once onto a cold prefix. After maxWait
// one waiter is released as a co-primer, after another maxWait two more, then
// four, and so on: the first of them to see a first byte warms the level for
// everyone still waiting, and at most a bounded number of duplicate cold
// prefills is ever in flight.
type WarmGate struct {
	ttl     time.Duration
	margin  time.Duration
	maxWait time.Duration
	now     func() time.Time

	mu   sync.Mutex
	keys map[string]*gateKey
}

type gateKey struct {
	warmUntil time.Time
	priming   bool
	done      chan struct{} // closed when this priming generation ends or the level turns warm
	closed    bool
	primerAt  time.Time // when the current primer was elected
	credited  int       // escalation intervals already credited
	tokens    int       // co-primer releases available
}

// gateHeld is what one request holds at one level.
type gateHeld struct {
	ks     *gateKey
	primer bool
}

// NewWarmGate builds a gate. ttl is the provider's entry lifetime; maxWait
// bounds how long a follower waits for a primer before a co-primer is released.
func NewWarmGate(ttl, maxWait time.Duration) *WarmGate {
	if ttl == 0 {
		ttl = 5 * time.Minute
	}
	if maxWait == 0 {
		maxWait = 45 * time.Second
	}
	margin := ttl / 5
	if margin > 20*time.Second {
		margin = 20 * time.Second
	}
	return &WarmGate{ttl: ttl, margin: margin, maxWait: maxWait, now: time.Now, keys: map[string]*gateKey{}}
}

// window is how long after a request starts its prefix is presumed warm.
func (g *WarmGate) window() time.Duration { return g.ttl - g.margin }

// levels expands "a|b|c" into the level paths "a", "a|b", "a|b|c".
func levels(key string) []string {
	parts := strings.Split(key, "|")
	out := make([]string, len(parts))
	for i := range parts {
		out[i] = strings.Join(parts[:i+1], "|")
	}
	return out
}

// Enter is called before a request over prefix key. It returns a function to call
// when the response starts (started(true)) or the request fails first
// (started(false)); callers must always call it exactly once.
func (g *WarmGate) Enter(ctx context.Context, key string) (func(ok bool), error) {
	var held []gateHeld
	for _, lv := range levels(key) {
		h, err := g.enter(ctx, lv)
		if err != nil {
			g.release(held, false, time.Time{})
			return nil, err
		}
		held = append(held, h)
	}
	start := g.now()
	var once sync.Once
	return func(ok bool) { once.Do(func() { g.release(held, ok, start) }) }, nil
}

// enter passes one level: straight through when warm, as its primer when cold and
// nobody is priming, otherwise waiting for the primer (and escalating past it).
func (g *WarmGate) enter(ctx context.Context, path string) (gateHeld, error) {
	for {
		g.mu.Lock()
		ks := g.keys[path]
		if ks == nil {
			ks = &gateKey{}
			g.keys[path] = ks
		}
		now := g.now()
		switch {
		case now.Before(ks.warmUntil):
			g.mu.Unlock()
			return gateHeld{ks: ks}, nil
		case !ks.priming:
			ks.priming, ks.closed = true, false
			ks.done = make(chan struct{})
			ks.primerAt, ks.credited, ks.tokens = now, 0, 0
			g.mu.Unlock()
			return gateHeld{ks: ks, primer: true}, nil
		}
		// Someone else is priming. Has it taken so long that a co-primer is due?
		for ks.credited < int(now.Sub(ks.primerAt)/g.maxWait) {
			ks.credited++
			ks.tokens += 1 << (ks.credited - 1)
		}
		if ks.tokens > 0 {
			ks.tokens--
			g.mu.Unlock()
			return gateHeld{ks: ks}, nil
		}
		done := ks.done
		wait := ks.primerAt.Add(time.Duration(ks.credited+1) * g.maxWait).Sub(now)
		g.mu.Unlock()
		if wait < time.Millisecond {
			wait = time.Millisecond
		}
		t := time.NewTimer(wait)
		select {
		case <-done:
			// Re-check: the primer warmed the level, or failed and a waiter may now
			// become the primer.
			t.Stop()
		case <-ctx.Done():
			t.Stop()
			return gateHeld{}, ctx.Err()
		case <-t.C:
		}
	}
}

// release settles a request's levels. On success every level it passed is warm
// for a window measured from the request's start (a read refreshes the entry,
// and a co-primer's first byte proves the prefix readable, waking the waiters);
// a primer that ends, successfully or not, hands its level over.
func (g *WarmGate) release(held []gateHeld, ok bool, start time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, h := range held {
		if ok {
			if until := start.Add(g.window()); until.After(h.ks.warmUntil) {
				h.ks.warmUntil = until
			}
		}
		if h.primer {
			h.ks.priming = false
		}
		if (h.primer || ok) && h.ks.done != nil && !h.ks.closed {
			h.ks.closed = true
			close(h.ks.done)
		}
	}
}

// Warm reports whether a prefix is presumed warm: every level of the key.
func (g *WarmGate) Warm(key string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	for _, lv := range levels(key) {
		ks := g.keys[lv]
		if ks == nil || !now.Before(ks.warmUntil) {
			return false
		}
	}
	return true
}
