package harness

import (
	"context"
	"sync"
	"time"

	"github.com/anemos-labs/sleipnir/internal/provider"
)

// RateLimit paces the requests of every rollout that shares it. A swarm's governor limits one swarm; with many
// rollouts in flight each has its own, so the endpoint saw their sum, and a benchmark run at --concurrency 8 against a
// limit of 600 a minute was a rate-limit test of the endpoint. One RateLimit given to the Harness is the whole run's.
type RateLimit struct {
	mu       sync.Mutex
	interval time.Duration
	next     time.Time
	now      func() time.Time
}

// NewRateLimit allows rpm requests a minute, spread evenly. rpm <= 0 returns nil: no pacing.
func NewRateLimit(rpm int) *RateLimit {
	if rpm <= 0 {
		return nil
	}
	return &RateLimit{interval: time.Minute / time.Duration(rpm), now: time.Now}
}

// Wait blocks until the caller's slot comes up, or the context ends. Slots are handed out in order of asking.
func (r *RateLimit) Wait(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	now := r.now()
	slot := r.next
	if slot.Before(now) {
		slot = now
	}
	r.next = slot.Add(r.interval)
	r.mu.Unlock()
	wait := slot.Sub(now)
	if wait <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// paced waits for the run's shared slot before every request, retries included: a repeated request is one the
// endpoint sees again.
type paced struct {
	provider.Provider
	limit *RateLimit
}

// Do implements provider.Provider.
func (p *paced) Do(ctx context.Context, req *provider.Request, on func(provider.Event)) (*provider.Response, error) {
	if err := p.limit.Wait(ctx); err != nil {
		return nil, err
	}
	return p.Provider.Do(ctx, req, on)
}
