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
	// tokens: 600/min per key on Heimdallm). 0 disables rate pacing.
	RPM int
	// Burst is the bucket size; defaults to RPM/6 (ten seconds of budget).
	Burst int
	// MaxConcurrent caps in-flight requests. 0 means unlimited.
	MaxConcurrent int
	// LaneConcurrent, when set, caps the in-flight requests of one lane (a model on an endpoint, AcquireFor's key):
	// 0 is no cap of its own, only MaxConcurrent's.
	LaneConcurrent func(key string) int
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

// Governor paces requests for a whole swarm. It implements agent.Limiter and
// agent.KeyedLimiter.
//
// Requests are admitted in priority order (interactive/manager before workers
// before background compaction), first come first served within a priority, as
// concurrency slots and rate-limit tokens become available; a lower priority is
// never starved for more than a few admissions.
//
// Lanes. A team runs on several models, often on different endpoints, and each has
// a capacity of its own: one model answering 429 says nothing about the others. So
// what an endpoint answers is kept per lane (a model on an endpoint, the key a
// request is admitted under): a 429 pauses that lane for the server's Retry-After
// (capped) and tightens that lane's rate once per episode, and sustained success
// loosens it again, while the other lanes go on. With one governor-wide pause and
// rate, a single overloaded model held every member of a 30-worker team, whatever
// model it was on. A waiter whose lane is paused or full does not hold up the
// waiters behind it of other lanes. What the person configured (the requests per
// minute and the in-flight cap) stays one budget for the whole swarm.
type Governor struct {
	cfg GovernorConfig

	mu       sync.Mutex
	tokens   float64
	last     time.Time
	rate     float64 // the configured requests per second, the whole swarm's budget
	inflight int
	lanes    map[string]*lane
	qs       [numPrio][]*waiter
	skipped  [numPrio]int
	queued   int
	timer    *time.Timer
	timerAt  time.Time
}

// lane is what the governor knows about one model on one endpoint.
type lane struct {
	key      string
	pause    time.Time
	episode  time.Time // 429s before this instant belong to the current episode
	rate     float64   // effective requests per second (0: the swarm has no rate)
	max      float64
	tokens   float64
	last     time.Time
	burst    float64
	okRun    int
	inflight int
	maxConc  int
}

// NewGovernor builds a governor.
func NewGovernor(cfg GovernorConfig) *Governor {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.MaxPause <= 0 {
		cfg.MaxPause = 60 * time.Second
	}
	g := &Governor{cfg: cfg, last: cfg.Now(), lanes: map[string]*lane{}}
	if cfg.RPM > 0 {
		if cfg.Burst == 0 {
			cfg.Burst = max(cfg.RPM/6, 1)
			g.cfg.Burst = cfg.Burst
		}
		g.rate = float64(cfg.RPM) / 60
		g.tokens = float64(cfg.Burst)
	}
	return g
}

// laneLocked returns the lane of key, making it on first use.
func (g *Governor) laneLocked(key string) *lane {
	if l := g.lanes[key]; l != nil {
		return l
	}
	l := &lane{key: key, rate: g.rate, max: g.rate, tokens: float64(g.cfg.Burst), burst: float64(g.cfg.Burst), last: g.cfg.Now()}
	if g.cfg.LaneConcurrent != nil {
		l.maxConc = g.cfg.LaneConcurrent(key)
	}
	g.lanes[key] = l
	return l
}

type waiter struct {
	prio  int
	lane  *lane
	ready chan error // nil: admitted; non-nil: refused
	dead  bool       // cancelled while queued
	g     *Governor
}

// clampPrio limits a priority index to the supported queue range.
func clampPrio(p int) int {
	switch {
	case p < 0:
		return 0
	case p >= numPrio:
		return numPrio - 1
	}
	return p
}

// Acquire implements agent.Limiter: a request of no particular lane.
func (g *Governor) Acquire(ctx context.Context, prio int) (agent.Release, error) {
	return g.AcquireFor(ctx, "", prio)
}

// AcquireFor implements agent.KeyedLimiter: key names the lane (the model and its endpoint).
func (g *Governor) AcquireFor(ctx context.Context, key string, prio int) (agent.Release, error) {
	if g.cfg.Admit != nil {
		if err := g.cfg.Admit(); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	w := &waiter{prio: clampPrio(prio), ready: make(chan error, 1), g: g}
	g.mu.Lock()
	w.lane = g.laneLocked(key)
	g.qs[w.prio] = append(g.qs[w.prio], w)
	g.queued++
	g.dispatchLocked()
	g.mu.Unlock()

	select {
	case err := <-w.ready:
		if err != nil {
			return nil, err
		}
		return w.release, nil
	case <-ctx.Done():
		g.mu.Lock()
		select {
		case err := <-w.ready:
			if err == nil {
				// Admitted concurrently with cancellation: give the slot back.
				g.inflight--
				w.lane.inflight--
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

func (w *waiter) release(_ *core.Usage, err error) {
	g, l := w.g, w.lane
	var ev map[string]any
	g.mu.Lock()
	g.inflight--
	l.inflight--
	now := g.cfg.Now()
	if err != nil {
		if pe, ok := provider.AsError(err); ok && pe.Kind == provider.ErrRateLimit {
			ev = g.rateLimitedLocked(l, now, pe.RetryAfter)
		}
	} else if l.rate > 0 && l.rate < l.max {
		l.okRun++
		if l.okRun >= 20 { // additive increase after sustained success
			l.okRun = 0
			l.rate = min(l.rate+l.max*0.05, l.max)
		}
	}
	g.dispatchLocked()
	g.mu.Unlock()
	if ev != nil && g.cfg.OnEvent != nil {
		g.cfg.OnEvent("rate-limited", ev)
	}
}

// rateLimitedLocked handles one 429 of lane l. Admission to the lane pauses for the
// Retry-After (capped at MaxPause); the lane's rate is cut once per episode: the
// requests that were in flight when the endpoint started refusing all come back 429
// within moments of each other, and treating each as a fresh signal would collapse
// the rate to its floor after a single burst.
func (g *Governor) rateLimitedLocked(l *lane, now time.Time, retryAfter time.Duration) map[string]any {
	wait := retryAfter
	if wait <= 0 {
		wait = time.Second
	}
	if wait > g.cfg.MaxPause {
		wait = g.cfg.MaxPause
	}
	if until := now.Add(wait); until.After(l.pause) {
		l.pause = until
	}
	if now.Before(l.episode) {
		return nil
	}
	l.episode = now.Add(max(wait, time.Second))
	l.okRun = 0
	if l.rate > 0 {
		l.rate = max(l.rate*0.8, l.max*0.1) // multiplicative decrease
		l.tokens = 0
	}
	ev := map[string]any{"rate_per_min": l.rate * 60, "pause_ms": wait.Milliseconds(), "retry_after_ms": retryAfter.Milliseconds(), "inflight": g.inflight, "queued": g.queued}
	if l.key != "" {
		ev["lane"] = l.key
	}
	return ev
}

// refillLocked adds tokens for elapsed time, to the swarm's bucket and to every lane's.
func (g *Governor) refillLocked(now time.Time) {
	if g.rate <= 0 {
		return
	}
	if dt := now.Sub(g.last).Seconds(); dt > 0 {
		g.tokens = min(g.tokens+dt*g.rate, float64(g.cfg.Burst))
		g.last = now
	}
	for _, l := range g.lanes {
		if dt := now.Sub(l.last).Seconds(); dt > 0 {
			l.tokens = min(l.tokens+dt*l.rate, l.burst)
			l.last = now
		}
	}
}

// laneWait is how long l must wait before it can admit a request (0: now), and whether it waits for a release (full)
// rather than for time to pass.
func (g *Governor) laneWait(l *lane, now time.Time) (wait time.Duration, full bool) {
	if l.maxConc > 0 && l.inflight >= l.maxConc {
		return 0, true
	}
	if now.Before(l.pause) {
		return l.pause.Sub(now), false
	}
	if l.rate > 0 && l.tokens < 1 {
		return time.Duration((1 - l.tokens) / l.rate * float64(time.Second)), false
	}
	return 0, false
}

// nextLocked removes and returns the waiter to admit next: from the best priority
// class, unless a lower class has been passed over agingEvery times, the first
// waiter whose lane can admit it now. soonest is how long until a waiter that cannot
// go yet could (0 when none waits for time to pass).
func (g *Governor) nextLocked(now time.Time) (w *waiter, soonest time.Duration) {
	ready := [numPrio]int{}
	for p := 0; p < numPrio; p++ {
		g.trimLocked(p)
		ready[p] = -1
		for i, x := range g.qs[p] {
			if x.dead {
				continue
			}
			wait, full := g.laneWait(x.lane, now)
			if wait == 0 && !full {
				ready[p] = i
				break
			}
			if !full && (soonest == 0 || wait < soonest) {
				soonest = wait
			}
		}
	}
	best := -1
	for p := 0; p < numPrio; p++ {
		if ready[p] >= 0 {
			best = p
			break
		}
	}
	if best < 0 {
		return nil, soonest
	}
	pick := best
	for p := best + 1; p < numPrio; p++ {
		if ready[p] >= 0 && g.skipped[p] >= agingEvery {
			pick = p
			break
		}
	}
	for p := pick + 1; p < numPrio; p++ {
		if ready[p] >= 0 {
			g.skipped[p]++
		}
	}
	g.skipped[pick] = 0
	q := g.qs[pick]
	i := ready[pick]
	w = q[i]
	copy(q[i:], q[i+1:])
	q[len(q)-1] = nil
	g.qs[pick] = q[:len(q)-1]
	return w, 0
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

// dispatchLocked admits as many waiters as concurrency, tokens and their lanes
// allow, then arms a timer for the next opportunity.
func (g *Governor) dispatchLocked() {
	now := g.cfg.Now()
	g.refillLocked(now)
	for g.queued > 0 {
		if g.cfg.MaxConcurrent > 0 && g.inflight >= g.cfg.MaxConcurrent {
			return // a release will re-dispatch
		}
		if g.rate > 0 && g.tokens < 1 {
			need := (1 - g.tokens) / g.rate
			g.armLocked(now, time.Duration(need*float64(time.Second)))
			return
		}
		w, soonest := g.nextLocked(now)
		if w == nil {
			if !g.anyLiveLocked() {
				g.queued = 0
				return
			}
			if soonest > 0 {
				g.armLocked(now, soonest)
			}
			return // waiting on lanes that are full: a release will re-dispatch
		}
		g.queued--
		if g.rate > 0 {
			g.tokens--
		}
		if w.lane.rate > 0 {
			w.lane.tokens--
		}
		g.inflight++
		w.lane.inflight++
		w.ready <- nil
	}
}

// anyLiveLocked reports whether a waiter that was not cancelled is queued.
func (g *Governor) anyLiveLocked() bool {
	for p := 0; p < numPrio; p++ {
		for _, w := range g.qs[p] {
			if !w.dead {
				return true
			}
		}
	}
	return false
}

// armLocked arms the dispatch timer for a delay of at least one millisecond, unless it is armed already to fire sooner.
// The caller must hold the governor lock; the callback reacquires it before dispatch.
func (g *Governor) armLocked(now time.Time, d time.Duration) {
	if d < time.Millisecond {
		d = time.Millisecond
	}
	at := now.Add(d)
	if g.timer != nil {
		if !g.timerAt.IsZero() && g.timerAt.After(now) && !g.timerAt.After(at) {
			return
		}
		g.timer.Stop()
	}
	g.timerAt = at
	g.timer = time.AfterFunc(d, func() {
		g.mu.Lock()
		g.timerAt = time.Time{}
		g.dispatchLocked()
		g.mu.Unlock()
	})
}

// Stats is a snapshot for dashboards.
type Stats struct {
	InFlight int
	Queued   int
	Rate     float64 // effective requests per minute: the swarm's budget, or the lowest lane's when a lane has been cut
	Paused   bool    // a lane is paused by a 429
	// PausedLanes names the lanes (models) paused now.
	PausedLanes []string
}

// Stats reports current state.
func (g *Governor) Stats() Stats {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.cfg.Now()
	st := Stats{InFlight: g.inflight, Queued: g.queued, Rate: g.rate * 60}
	for _, l := range g.lanes {
		if l.rate > 0 && l.rate*60 < st.Rate {
			st.Rate = l.rate * 60
		}
		if now.Before(l.pause) {
			st.Paused = true
			if l.key != "" {
				st.PausedLanes = append(st.PausedLanes, l.key)
			}
		}
	}
	return st
}
