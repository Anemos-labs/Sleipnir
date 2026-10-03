// Package sim is a deterministic cost simulator for prompt-cache policies.
//
// It replays one synthetic agentic-coding workload (a manager plus workers doing
// tool-heavy tasks over a shared project) under different context policies and
// prices every request against an explicit model of the provider's cache: exact
// prefix chains, TTL or LRU eviction, entries readable only after the first
// response byte, per-engine caches with optional routing affinity.
//
// It exists to answer "what does layering buy us?" and to tune the planner,
// not to predict a bill. Every parameter is an assumption, printed with the
// result; calibrate them against real traces (the event log records exactly the
// quantities the simulator consumes) before quoting absolute numbers.
package sim

import (
	"container/heap"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"time"

	"github.com/anemos-labs/sleipnir/internal/cost"
	"github.com/anemos-labs/sleipnir/internal/kv"
)

// Provider models a provider's cache and prices.
type Provider struct {
	Name string
	// Weights are relative to plain input = 1.
	W     cost.Weights
	Write float64 // write weight for cache misses that get stored
	// TTL is the entry lifetime (0: pure LRU by capacity).
	TTL time.Duration
	// Explicit: entries are stored only at breakpoints (max MaxBP); otherwise the
	// provider stores every prefix it processes.
	Explicit bool
	MaxBP    int
	// CapacityTokens per engine (0 = unbounded).
	CapacityTokens int
	Engines        int
	// MinPrefix: shorter prefixes are never cached.
	MinPrefix int
	// TTFB models time to first byte: base + per uncached token.
	TTFBBase    time.Duration
	TTFBPerToks time.Duration // per 1000 uncached tokens
	// HotMode is how the route delivers the hot tail (kv.ResolveHot): inline (the
	// uncached tail, rebuilt every request), persisted on change (models that
	// enforce preserved thinking on a provider without turn-scoped system
	// messages) or turn-scoped (priced like inline: it follows the last marker).
	HotMode kv.HotMode
}

// WithHot returns p with the hot tail delivered the given way.
func (p Provider) WithHot(m kv.HotMode) Provider {
	p.HotMode = m
	if m == kv.HotPersist {
		p.Name += ", preserved thinking (hot board persisted on change)"
	}
	return p
}

// AnthropicPreserved is Anthropic() on a model that enforces preserved thinking
// (Fable 5.1, Opus 5.5, Sonnet 5.5): an inline hot tail would void every thinking
// signature, so the board is persisted on change.
func AnthropicPreserved() Provider { return Anthropic().WithHot(kv.HotPersist) }

// Anthropic returns an explicit-breakpoint provider with 5-minute TTL.
func Anthropic() Provider {
	return Provider{Name: "explicit-cache (Anthropic-like: read 0.1x, write 1.25x, TTL 5m)",
		W: cost.Weights{Read: 0.1, Write5m: 1.25, Output: 5}, Write: 1.25,
		TTL: 5 * time.Minute, Explicit: true, MaxBP: 4, Engines: 1, MinPrefix: 1024,
		TTFBBase: 1500 * time.Millisecond, TTFBPerToks: 60 * time.Millisecond}
}

// Marketplace returns an automatic-prefix-cache provider with no write premium,
// per-engine LRU capacity, and k engines.
func Marketplace(engines int) Provider {
	if engines < 1 {
		engines = 1
	}
	return Provider{Name: fmt.Sprintf("auto-cache (marketplace-like: read 0.25x, no write premium, %d engine(s))", engines),
		W: cost.Weights{Read: 0.25, Write5m: 1, Output: 2}, Write: 1,
		TTL: time.Hour, Explicit: false, Engines: engines, CapacityTokens: 3_000_000, MinPrefix: 64,
		TTFBBase: 1200 * time.Millisecond, TTFBPerToks: 40 * time.Millisecond}
}

// Workload parameterises the synthetic swarm.
type Workload struct {
	Agents int // concurrent workers
	Tasks  int // total tasks
	Seed   int64
	// Waves splits the tasks into sequential swarm launches separated by WaveGap
	// of idleness (default 12 minutes: longer than a 5-minute cache lifetime, so
	// each launch starts cold). 0 or 1 means a single launch.
	Waves   int
	WaveGap time.Duration

	// Prompt structure (tokens).
	ConstTokens  int // tools + constitution
	SharedTokens int // project knowledge
	RoleTokens   int
	AssignTokens int
	HotTokens    int // per request, Sleipnir only

	// Task shape.
	StepsMean, StepsStd float64
	OutMean             float64 // assistant tokens per step
	ResultMedian        float64 // tool result tokens (lognormal median)
	ResultSigma         float64
	ResultCap           int

	// Without a shared pin, a fresh agent must learn the project by reading:
	// ExploreSteps extra tool calls returning ExploreTokens in total, on top of the
	// task's own work steps, and a manager-written brief of BriefTokens.
	ExploreTokens int
	ExploreSteps  int
	BriefTokens   int
	// With the shared pin, a few targeted reads remain: OrientSteps tool calls,
	// each returning OrientFrac of the whole exploration budget (2 x 12% by
	// default, about a quarter of what a cold agent reads).
	OrientSteps int
	OrientFrac  float64

	// Timing.
	StepSeconds    float64 // mean model+tool latency per step
	LongToolProb   float64 // chance a step is a long-running command
	LongToolSecond float64

	ContextWindow int
}

// DefaultWorkload is an illustrative agentic-coding profile.
func DefaultWorkload(agents int) Workload {
	return Workload{
		Agents: agents, Tasks: agents * 2, Seed: 1,
		ConstTokens: 9000, SharedTokens: 8000, RoleTokens: 10000, AssignTokens: 500, HotTokens: 700,
		StepsMean: 32, StepsStd: 10, OutMean: 240, ResultMedian: 1100, ResultSigma: 1.1, ResultCap: 30000,
		ExploreTokens: 30000, ExploreSteps: 10, BriefTokens: 1800, OrientSteps: 2, OrientFrac: 0.12,
		StepSeconds: 22, LongToolProb: 0.06, LongToolSecond: 330,
		ContextWindow: 1_000_000,
	}
}

// Result is what a policy run produced.
type Result struct {
	Policy   string
	Provider string
	Requests int
	Steps    int // agent steps executed (including orientation)
	// Token accounting (all requests, including compaction calls).
	InputUncached, CacheRead, CacheWrite, Output int64
	// ITE is the total cost in input-token equivalents.
	ITE          float64
	Compactions  int
	MaskCommits  int
	AvgContext   float64 // mean prompt tokens per request: a proxy for attention load
	PeakContext  int
	PeakRPM      float64
	WallSeconds  float64
	ColdRequests int // requests with no cache read at all
}

// HitRatio is cache_read / total input.
func (r Result) HitRatio() float64 {
	t := float64(r.InputUncached + r.CacheRead + r.CacheWrite)
	if t == 0 {
		return 0
	}
	return float64(r.CacheRead) / t
}

// ---- cache model ----------------------------------------------------------------

type seg struct {
	id     string
	tokens int
	// ephemeral segments (the hot tail) are billed uncached and never stored.
	ephemeral bool
}

type entry struct {
	last   time.Duration
	tokens int // size of the prefix ending here, for capacity accounting
}

type engine struct {
	entries map[uint64]*entry
	tokens  int
}

type cacheSim struct {
	p       Provider
	engines []*engine
	rr      int
	pins    map[string]int
}

// newCache initializes independent simulated cache engines and affinity pins, using at least one
// engine.
func newCache(p Provider) *cacheSim {
	c := &cacheSim{p: p, pins: map[string]int{}}
	n := p.Engines
	if n < 1 {
		n = 1
	}
	for i := 0; i < n; i++ {
		c.engines = append(c.engines, &engine{entries: map[uint64]*entry{}})
	}
	return c
}

// route assigns requests round-robin and pins nonempty affinity keys to their first assigned
// engine.
func (c *cacheSim) route(affinityKey string) int {
	if len(c.engines) == 1 {
		return 0
	}
	if affinityKey != "" {
		if e, ok := c.pins[affinityKey]; ok {
			return e
		}
		c.rr++
		c.pins[affinityKey] = c.rr % len(c.engines)
		return c.pins[affinityKey]
	}
	c.rr++
	return c.rr % len(c.engines)
}

// chainHash deterministically extends the simulator's noncryptographic prefix hash with an item
// ID.
func chainHash(prev uint64, id string) uint64 {
	h := prev*1099511628211 + 14695981039346656037
	for i := 0; i < len(id); i++ {
		h = (h ^ uint64(id[i])) * 1099511628211
	}
	return h
}

// request prices one prompt at time now and stores the prefixes it processed.
// segs are in prefix order; bp lists indexes of segments after which an
// explicit-cache breakpoint sits. Stored entries become readable only at
// now+ttfb, the moment the first response byte streams back.
func (c *cacheSim) request(eng int, now time.Duration, segs []seg, bp []int) (unc, read, write int, hit bool, ttfb time.Duration) {
	e := c.engines[eng]
	type node struct {
		h   uint64
		cum int
		idx int
	}
	var nodes []node
	var h uint64
	cum := 0
	all := 0
	for i, s := range segs {
		all += s.tokens
		if s.ephemeral {
			continue
		}
		h = chainHash(h, s.id)
		cum += s.tokens
		nodes = append(nodes, node{h, cum, i})
	}
	fresh := func(n node) *entry {
		en, ok := e.entries[n.h]
		if !ok || en.last > now || n.cum < c.p.MinPrefix {
			return nil
		}
		if c.p.TTL > 0 && now-en.last > c.p.TTL {
			return nil
		}
		return en
	}
	// The longest fresh entry on the chain wins. An entry at a deeper node exists
	// only if the whole prefix before it matched, so no continuity check is needed.
	hitTok, hitAt := 0, -1
	for i, n := range nodes {
		if fresh(n) != nil {
			hitTok, hitAt = n.cum, i
		}
	}
	for i := 0; i <= hitAt; i++ {
		if en := fresh(nodes[i]); en != nil {
			en.last = now // a read refreshes the entry
		}
	}
	bpSet := map[int]bool{}
	for _, i := range bp {
		bpSet[i] = true
	}
	lastStore := hitTok
	var toStore []node
	for _, n := range nodes {
		if n.cum <= hitTok {
			continue
		}
		if c.p.Explicit && !bpSet[n.idx] {
			continue
		}
		if n.cum < c.p.MinPrefix {
			continue
		}
		toStore = append(toStore, n)
		lastStore = n.cum
	}
	read = hitTok
	write = lastStore - hitTok
	unc = all - read - write
	if unc < 0 {
		unc = 0
	}
	ttfb = c.p.TTFBBase + time.Duration(float64(unc+write)/1000*float64(c.p.TTFBPerToks))
	ready := now + ttfb
	prev := hitTok
	for _, n := range toStore {
		if en, ok := e.entries[n.h]; ok {
			// Two requests can write the same prefix concurrently; it becomes
			// readable at the earlier first byte. A stale entry is simply rewritten.
			if en.last <= now || ready < en.last {
				en.last = ready
			}
		} else {
			e.entries[n.h] = &entry{last: ready, tokens: n.cum - prev}
			e.tokens += n.cum - prev
		}
		prev = n.cum
	}
	if c.p.CapacityTokens > 0 && e.tokens > c.p.CapacityTokens {
		type kv struct {
			k  uint64
			en *entry
		}
		list := make([]kv, 0, len(e.entries))
		for k, en := range e.entries {
			list = append(list, kv{k, en})
		}
		sort.Slice(list, func(i, j int) bool { return list[i].en.last < list[j].en.last })
		for _, x := range list {
			if e.tokens <= c.p.CapacityTokens*9/10 {
				break
			}
			delete(e.entries, x.k)
			e.tokens -= x.en.tokens
		}
	}
	return unc, read, write, hitTok > 0, ttfb
}

// ---- event loop -------------------------------------------------------------------

type actor struct {
	id   string
	next time.Duration
	step func(now time.Duration) (done bool, next time.Duration)
	idx  int
}

type queue []*actor

// Len returns the number of actors currently held by the scheduler heap.
func (q queue) Len() int { return len(q) }

// Less orders actors by their next scheduled event time.
func (q queue) Less(i, j int) bool { return q[i].next < q[j].next }

// Swap exchanges heap entries and updates both actors' recorded heap indexes.
func (q queue) Swap(i, j int) { q[i], q[j] = q[j], q[i]; q[i].idx, q[j].idx = i, j }

// Push appends an actor and records its index; the heap package restores ordering.
func (q *queue) Push(x any) { a := x.(*actor); a.idx = len(*q); *q = append(*q, a) }

// Pop removes the final actor after the heap package moves the selected entry there.
func (q *queue) Pop() any { o := *q; n := len(o); a := o[n-1]; *q = o[:n-1]; return a }

// durSec converts fractional seconds to a duration, truncating fractional nanoseconds.
func durSec(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }

// runner is shared bookkeeping for a simulation run.
type runner struct {
	w      Workload
	p      Provider
	cache  *cacheSim
	res    Result
	sumCtx float64
	reqs   []time.Duration
	end    time.Duration
}

// newRunner creates a simulator with a fresh provider cache and labeled result totals.
func newRunner(w Workload, p Provider, name string) *runner {
	return &runner{w: w, p: p, cache: newCache(p), res: Result{Policy: name, Provider: p.Name}}
}

// bill records one request.
func (r *runner) bill(now time.Duration, eng int, segs []seg, bp []int, outTokens int) (ttfb time.Duration) {
	unc, rd, wr, hit, ttfb := r.cache.request(eng, now, segs, bp)
	total := unc + rd + wr
	r.res.Requests++
	r.res.InputUncached += int64(unc)
	r.res.CacheRead += int64(rd)
	r.res.CacheWrite += int64(wr)
	r.res.Output += int64(outTokens)
	r.res.ITE += float64(unc) + r.p.W.Read*float64(rd) + r.p.Write*float64(wr) + r.p.W.Output*float64(outTokens)
	r.sumCtx += float64(total)
	if total > r.res.PeakContext {
		r.res.PeakContext = total
	}
	if !hit {
		r.res.ColdRequests++
	}
	r.reqs = append(r.reqs, now)
	return ttfb
}

// finish computes average context, peak requests in a one-minute window, and simulated wall time
// before returning results.
func (r *runner) finish() Result {
	if r.res.Requests > 0 {
		r.res.AvgContext = r.sumCtx / float64(r.res.Requests)
	}
	sort.Slice(r.reqs, func(i, j int) bool { return r.reqs[i] < r.reqs[j] })
	best, lo := 0, 0
	for hi := range r.reqs {
		for r.reqs[hi]-r.reqs[lo] > time.Minute {
			lo++
		}
		if hi-lo+1 > best {
			best = hi - lo + 1
		}
	}
	r.res.PeakRPM = float64(best)
	r.res.WallSeconds = r.end.Seconds()
	return r.res
}

// stepSpec is one tool-using step of a task.
type stepSpec struct {
	out int           // assistant tokens
	res int           // tool result tokens (work steps)
	f   float64       // size factor (orientation steps)
	lat time.Duration // model + tool latency
}

// taskSpec is everything random about one task, drawn from the seed and the
// task's index alone. Policies that finish tasks in a different order therefore
// still face exactly the same work, so differences between them are the policy
// and not the dice (common random numbers). work is the task itself; orient is
// what an agent reads to get its bearings, sized by each policy.
type taskSpec struct {
	work   []stepSpec
	orient []stepSpec
}

func (w Workload) spec(k int) *taskSpec {
	rng := rand.New(rand.NewSource(w.Seed*7919 + int64(k)*104729 + 1))
	draw := func() stepSpec {
		x := math.Exp(math.Log(w.ResultMedian) + rng.NormFloat64()*w.ResultSigma)
		if x > float64(w.ResultCap) {
			x = float64(w.ResultCap)
		}
		if x < 30 {
			x = 30
		}
		sec := w.StepSeconds * (0.4 + 1.2*rng.Float64())
		if rng.Float64() < w.LongToolProb {
			sec = w.LongToolSecond * (0.7 + 0.6*rng.Float64())
		}
		return stepSpec{out: int(w.OutMean * (0.5 + rng.Float64())), res: int(x), f: 0.6 + 0.8*rng.Float64(), lat: durSec(sec)}
	}
	n := int(math.Round(w.StepsMean + rng.NormFloat64()*w.StepsStd))
	if n < 6 {
		n = 6
	}
	t := &taskSpec{}
	for i := 0; i < n; i++ {
		t.work = append(t.work, draw())
	}
	orient := max(w.ExploreSteps, w.OrientSteps, 1)
	for i := 0; i < orient; i++ {
		t.orient = append(t.orient, draw())
	}
	return t
}

// runActors drives actors to completion.
func runActors(actors []*actor) time.Duration {
	q := queue{}
	for _, a := range actors {
		heap.Push(&q, a)
	}
	var last time.Duration
	for q.Len() > 0 {
		a := heap.Pop(&q).(*actor)
		done, next := a.step(a.next)
		if a.next > last {
			last = a.next
		}
		if !done {
			a.next = next
			heap.Push(&q, a)
		}
	}
	return last
}
