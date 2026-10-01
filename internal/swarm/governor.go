package swarm

import (
	"context"
	"sync"
	"time"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/provider"
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
	// MaxPause caps how long one Retry-After can stop admission (default 60s): a
	// hostile or broken endpoint must not be able to freeze the whole swarm.
	MaxPause time.Duration
	// Admit, when set, is asked before every request (with no governor lock held)
	// and may refuse it: the swarm's budget check.
	Admit func() error
	// OnEvent, when set, is told about rate-limit episodes (with no lock held).
	OnEvent func(action string, data map[string]any)
	Now     func() time.Time
}

// numPrio is the number of priority classes (agent.PrioInteractive, PrioWorker,
// PrioBackground and one spare); larger numbers are clamped to the last.
const numPrio = 4

// agingEvery is how many higher-priority admissions a waiting request tolerates
// before it is admitted ahead of them: strict priority would let a steady stream
// of worker requests starve background compaction forever.
const agingEvery = 8

// Governor paces requests for a whole swarm. It implements agent.Limiter.
//
// Requests are admitted in priority order (interactive/manager before workers
// before background compaction), first come first served within a priority, as
// concurrency slots and rate-limit tokens become available; a lower priority is
// never starved for more than a few admissions. A 429 pauses admission for the
// server's Retry-After (capped) and tightens the effective rate once per episode;
// sustained success loosens it again, so the swarm converges on what the endpoint
// will actually take.
type Governor struct {
	cfg GovernorConfig

	mu       sync.Mutex
	tokens   float64
	last     time.Time
	rate     float64 // effective requests per second
	max      float64
	inflight int
	pause    time.Time
	episode  time.Time // 429s before this instant belong to the current episode
	qs       [numPrio][]*waiter
	skipped  [numPrio]int
	queued   int
	timer    *time.Timer
	okRun    int
}

// NewGovernor builds a governor.
func NewGovernor(cfg GovernorConfig) *Governor {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.MaxPause <= 0 {
		cfg.MaxPause = 60 * time.Second
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
	ready chan error // nil: admitted; non-nil: refused
	dead  bool       // cancelled while queued
}

func clampPrio(p int) int {
	switch {
	case p < 0:
		return 0
	case p >= numPrio:
		return numPrio - 1
	}
	return p
}

// Acquire implements agent.Limiter.
func (g *Governor) Acquire(ctx context.Context, prio int) (agent.Release, error) {
	if g.cfg.Admit != nil {
		if err := g.cfg.Admit(); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	w := &waiter{prio: clampPrio(prio), ready: make(chan error, 1)}
	g.mu.Lock()
	g.qs[w.prio] = append(g.qs[w.prio], w)
	g.queued++
	g.dispatchLocked()
	g.mu.Unlock()

	select {
	case err := <-w.ready:
		if err != nil {
			return nil, err
		}
		return g.release, nil
	case <-ctx.Done():
		g.mu.Lock()
		select {
		case err := <-w.ready:
			if err == nil {
				// Admitted concurrently with cancellation: give the slot back.
				g.inflight--
				g.dispatchLocked()
			}
		default:
			w.dead = true
			g.queued--
		}
		g.mu.Unlock()
		return nil, ctx.Err()
	}
}

func (g *Governor) release(_ *core.Usage, err error) {
	var ev map[string]any
	g.mu.Lock()
	g.inflight--
	now := g.cfg.Now()
	if err != nil {
		if pe, ok := provider.AsError(err); ok && pe.Kind == provider.ErrRateLimit {
			ev = g.rateLimitedLocked(now, pe.RetryAfter)
		}
	} else if g.rate > 0 && g.rate < g.max {
		g.okRun++
		if g.okRun >= 20 { // additive increase after sustained success
			g.okRun = 0
			g.rate = min(g.rate+g.max*0.05, g.max)
		}
	}
	g.dispatchLocked()
	g.mu.Unlock()
	if ev != nil && g.cfg.OnEvent != nil {
		g.cfg.OnEvent("rate-limited", ev)
	}
}

// rateLimitedLocked handles one 429. Admission pauses for the Retry-After (capped
// at MaxPause); the rate is cut once per episode: the requests that were in flight
// when the endpoint started refusing all come back 429 within moments of each
// other, and treating each as a fresh signal would collapse the rate to its floor
// after a single burst.
func (g *Governor) rateLimitedLocked(now time.Time, retryAfter time.Duration) map[string]any {
	wait := retryAfter
	if wait <= 0 {
		wait = time.Second
	}
	if wait > g.cfg.MaxPause {
		wait = g.cfg.MaxPause
	}
	if until := now.Add(wait); until.After(g.pause) {
		g.pause = until
	}
	if now.Before(g.episode) {
		return nil
	}
	g.episode = now.Add(max(wait, time.Second))
	g.okRun = 0
	if g.rate > 0 {
		g.rate = max(g.rate*0.8, g.max*0.1) // multiplicative decrease
		g.tokens = 0
	}
	return map[string]any{"rate_per_min": g.rate * 60, "pause_ms": wait.Milliseconds(), "retry_after_ms": retryAfter.Milliseconds(), "inflight": g.inflight, "queued": g.queued}
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

// nextLocked removes and returns the waiter to admit next: the best priority
// class, unless a lower class has been passed over agingEvery times.
func (g *Governor) nextLocked() *waiter {
	best := -1
	for p := 0; p < numPrio; p++ {
		g.trimLocked(p)
		if len(g.qs[p]) > 0 {
			best = p
			break
		}
	}
	if best < 0 {
		return nil
	}
	pick := best
	for p := best + 1; p < numPrio; p++ {
		if len(g.qs[p]) > 0 && g.skipped[p] >= agingEvery {
			pick = p
			break
		}
	}
	for p := pick + 1; p < numPrio; p++ {
		if len(g.qs[p]) > 0 {
			g.skipped[p]++
		}
	}
	g.skipped[pick] = 0
	w := g.qs[pick][0]
	g.qs[pick][0] = nil
	g.qs[pick] = g.qs[pick][1:]
	return w
}

// trimLocked drops cancelled waiters from the front of a queue.
func (g *Governor) trimLocked(p int) {
	q := g.qs[p]
	for len(q) > 0 && q[0].dead {
		q[0] = nil
		q = q[1:]
	}
	g.qs[p] = q
}

// dispatchLocked admits as many waiters as concurrency, tokens and any pause
// allow, then arms a timer for the next opportunity.
func (g *Governor) dispatchLocked() {
	now := g.cfg.Now()
	g.refillLocked(now)
	for g.queued > 0 {
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
		w := g.nextLocked()
		if w == nil {
			g.queued = 0
			return
		}
		g.queued--
		if g.rate > 0 {
			g.tokens--
		}
		g.inflight++
		w.ready <- nil
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
	return Stats{InFlight: g.inflight, Queued: g.queued, Rate: g.rate * 60, Paused: g.cfg.Now().Before(g.pause)}
}
