package swarm

import (
	"container/heap"
	"context"
	"sync"
	"time"

	"github.com/reee344/sleipnir/internal/agent"
	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/provider"
)

// GovernorConfig bounds outbound model traffic.
type GovernorConfig struct {
	// RPM is the request budget per minute (marketplaces limit requests, not
	// tokens: 600/min per key on Heimdall). 0 disables rate pacing.
	RPM int
	// Burst is the bucket size; defaults to RPM/6 (ten seconds of budget).
	Burst int
	// MaxConcurrent caps in-flight requests. 0 means unlimited.
	MaxConcurrent int
	Now           func() time.Time
}

// Governor paces requests for a whole swarm. It implements agent.Limiter.
//
// Requests are admitted in priority order (interactive/manager before workers
// before background compaction) as concurrency slots and rate-limit tokens
// become available. A 429 pauses admission for the server's Retry-After and
// tightens the effective rate; sustained success loosens it again, so the swarm
// converges on what the endpoint will actually take.
type Governor struct {
	cfg GovernorConfig

	mu       sync.Mutex
	tokens   float64
	last     time.Time
	rate     float64 // effective requests per second
	max      float64
	inflight int
	pause    time.Time
	q        waitQueue
	seq      uint64
	timer    *time.Timer
	okRun    int
}

// NewGovernor builds a governor.
func NewGovernor(cfg GovernorConfig) *Governor {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	g := &Governor{cfg: cfg, last: cfg.Now()}
	if cfg.RPM > 0 {
		if cfg.Burst == 0 {
			cfg.Burst = max(cfg.RPM/6, 1)
			g.cfg.Burst = cfg.Burst
		}
		g.rate = float64(cfg.RPM) / 60
		g.max = g.rate
		g.tokens = float64(cfg.Burst)
	}
	return g
}

type waiter struct {
	prio  int
	seq   uint64
	ready chan struct{}
	idx   int
}

type waitQueue []*waiter

func (q waitQueue) Len() int { return len(q) }
func (q waitQueue) Less(i, j int) bool {
	if q[i].prio != q[j].prio {
		return q[i].prio < q[j].prio
	}
	return q[i].seq < q[j].seq
}
func (q waitQueue) Swap(i, j int) { q[i], q[j] = q[j], q[i]; q[i].idx, q[j].idx = i, j }
func (q *waitQueue) Push(x any)   { w := x.(*waiter); w.idx = len(*q); *q = append(*q, w) }
func (q *waitQueue) Pop() any {
	old := *q
	n := len(old)
	w := old[n-1]
	*q = old[:n-1]
	return w
}

// Acquire implements agent.Limiter.
func (g *Governor) Acquire(ctx context.Context, prio int) (agent.Release, error) {
	w := &waiter{prio: prio, ready: make(chan struct{}, 1)}
	g.mu.Lock()
	g.seq++
	w.seq = g.seq
	heap.Push(&g.q, w)
	g.dispatchLocked()
	g.mu.Unlock()

	select {
	case <-w.ready:
		return g.release, nil
	case <-ctx.Done():
		g.mu.Lock()
		select {
		case <-w.ready:
			// Admitted concurrently with cancellation: give the slot back.
			g.inflight--
			g.dispatchLocked()
		default:
			if w.idx >= 0 && w.idx < len(g.q) && g.q[w.idx] == w {
				heap.Remove(&g.q, w.idx)
			}
		}
		g.mu.Unlock()
		return nil, ctx.Err()
	}
}

func (g *Governor) release(_ *core.Usage, err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.inflight--
	if err != nil {
		if pe, ok := provider.AsError(err); ok && pe.Kind == provider.ErrRateLimit {
			g.okRun = 0
			wait := pe.RetryAfter
			if wait <= 0 {
				wait = time.Second
			}
			if until := g.cfg.Now().Add(wait); until.After(g.pause) {
				g.pause = until
			}
			if g.rate > 0 {
				g.rate = max(g.rate*0.8, g.max*0.1) // multiplicative decrease
				g.tokens = 0
			}
		}
	} else if g.rate > 0 && g.rate < g.max {
		g.okRun++
		if g.okRun >= 20 { // additive increase after sustained success
			g.okRun = 0
			g.rate = min(g.rate+g.max*0.05, g.max)
		}
	}
	g.dispatchLocked()
}

// refillLocked adds tokens for elapsed time.
func (g *Governor) refillLocked(now time.Time) {
	if g.rate <= 0 {
		return
	}
	dt := now.Sub(g.last).Seconds()
	if dt > 0 {
		g.tokens = min(g.tokens+dt*g.rate, float64(g.cfg.Burst))
		g.last = now
	}
}

// dispatchLocked admits as many waiters as concurrency, tokens and any pause
// allow, then arms a timer for the next opportunity.
func (g *Governor) dispatchLocked() {
	now := g.cfg.Now()
	g.refillLocked(now)
	for g.q.Len() > 0 {
		if g.cfg.MaxConcurrent > 0 && g.inflight >= g.cfg.MaxConcurrent {
			return // a release will re-dispatch
		}
		if now.Before(g.pause) {
			g.armLocked(g.pause.Sub(now))
			return
		}
		if g.rate > 0 && g.tokens < 1 {
			need := (1 - g.tokens) / g.rate
			g.armLocked(time.Duration(need * float64(time.Second)))
			return
		}
		w := heap.Pop(&g.q).(*waiter)
		w.idx = -1
		if g.rate > 0 {
			g.tokens--
		}
		g.inflight++
		w.ready <- struct{}{}
	}
}

func (g *Governor) armLocked(d time.Duration) {
	if d < time.Millisecond {
		d = time.Millisecond
	}
	if g.timer != nil {
		g.timer.Stop()
	}
	g.timer = time.AfterFunc(d, func() {
		g.mu.Lock()
		g.dispatchLocked()
		g.mu.Unlock()
	})
}

// Stats is a snapshot for dashboards.
type Stats struct {
	InFlight int
	Queued   int
	Rate     float64 // effective requests per minute
	Paused   bool
}

// Stats reports current state.
func (g *Governor) Stats() Stats {
	g.mu.Lock()
	defer g.mu.Unlock()
	return Stats{InFlight: g.inflight, Queued: g.q.Len(), Rate: g.rate * 60, Paused: g.cfg.Now().Before(g.pause)}
}
