package swarm

import (
	"context"
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
type WarmGate struct {
	ttl     time.Duration
	maxWait time.Duration
	now     func() time.Time

	mu   sync.Mutex
	keys map[string]*gateKey
}

type gateKey struct {
	warmUntil time.Time
	priming   bool
	done      chan struct{}
}

// NewWarmGate builds a gate. ttl is how long a prefix is presumed warm after a
// request begins; maxWait bounds how long a follower waits for a primer.
func NewWarmGate(ttl, maxWait time.Duration) *WarmGate {
	if ttl == 0 {
		ttl = 4 * time.Minute
	}
	if maxWait == 0 {
		maxWait = 45 * time.Second
	}
	return &WarmGate{ttl: ttl, maxWait: maxWait, now: time.Now, keys: map[string]*gateKey{}}
}

// Enter is called before a request over prefix key. It returns a function to call
// when the response starts (started(true)) or the request fails first
// (started(false)); callers must always call it exactly once.
func (g *WarmGate) Enter(ctx context.Context, key string) (func(ok bool), error) {
	for {
		g.mu.Lock()
		ks := g.keys[key]
		if ks == nil {
			ks = &gateKey{}
			g.keys[key] = ks
		}
		now := g.now()
		switch {
		case now.Before(ks.warmUntil):
			g.mu.Unlock()
			return g.finisher(ks, false), nil
		case !ks.priming:
			ks.priming = true
			ks.done = make(chan struct{})
			g.mu.Unlock()
			return g.finisher(ks, true), nil
		}
		done := ks.done
		g.mu.Unlock()
		select {
		case <-done:
			// Re-check: either the primer warmed the prefix or it failed and a
			// waiter may now become the primer.
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(g.maxWait):
			return func(bool) {}, nil // never deadlock a swarm on a stuck primer
		}
	}
}

func (g *WarmGate) finisher(ks *gateKey, primer bool) func(ok bool) {
	var once sync.Once
	return func(ok bool) {
		once.Do(func() {
			g.mu.Lock()
			defer g.mu.Unlock()
			if ok {
				ks.warmUntil = g.now().Add(g.ttl)
			}
			if primer {
				ks.priming = false
				close(ks.done)
			}
		})
	}
}

// Warm reports whether a prefix is presumed warm.
func (g *WarmGate) Warm(key string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	ks := g.keys[key]
	return ks != nil && g.now().Before(ks.warmUntil)
}
