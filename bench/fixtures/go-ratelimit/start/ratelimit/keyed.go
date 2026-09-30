package ratelimit

import (
	"sync"
	"time"
)

// Keyed hands out one Limiter per key, for example one per client address. It
// is safe for concurrent use.
type Keyed struct {
	mu      sync.Mutex
	rate    float64
	burst   int
	clients map[string]*client
}

type client struct {
	lim  *Limiter
	seen time.Time // when the key was last used
}

// NewKeyed returns a Keyed whose limiters are created with New(rate, burst).
func NewKeyed(rate float64, burst int) *Keyed {
	return &Keyed{rate: rate, burst: burst, clients: make(map[string]*client)}
}

// Allow is Allow on the limiter of key. A key that has not been seen before (or
// that Sweep has forgotten) gets a new limiter with a full bucket. Every call
// counts as a use of the key, whether it is allowed or not.
func (k *Keyed) Allow(key string) bool {
	k.mu.Lock()
	c, ok := k.clients[key]
	if !ok {
		c = &client{lim: New(k.rate, k.burst)}
		k.clients[key] = c
	}
	c.seen = time.Now()
	k.mu.Unlock()
	return c.lim.Allow()
}

// Len returns the number of keys currently tracked.
func (k *Keyed) Len() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.clients)
}

// Sweep forgets the keys that were last used more than maxIdle ago and returns
// how many it forgot.
func (k *Keyed) Sweep(maxIdle time.Duration) int {
	k.mu.Lock()
	defer k.mu.Unlock()
	removed := 0
	for key, c := range k.clients {
		if time.Since(c.seen) > maxIdle {
			delete(k.clients, key)
			removed++
		}
	}
	return removed
}
