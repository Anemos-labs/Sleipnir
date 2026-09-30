package sim

import (
	"fmt"
	"strings"
	"time"

	"github.com/reee344/sleipnir/internal/kv"
)

// pool hands out task indexes in order. The k-th task taken always has the same
// spec, whichever agent takes it and whenever (common random numbers).
type pool struct{ next, total int }

func (p *pool) take() (int, bool) {
	if p.next < p.total {
		p.next++
		return p.next - 1, true
	}
	return 0, false
}

// waves splits the task list into sequential swarm launches separated by idle
// gaps. A gap longer than the cache lifetime makes every launch a cold start.
func waves(w Workload) (n, per int) {
	n = w.Waves
	if n < 1 {
		n = 1
	}
	per = (w.Tasks + n - 1) / n
	return n, per
}

func gap(w Workload) time.Duration {
	if w.WaveGap > 0 {
		return w.WaveGap
	}
	return 12 * time.Minute
}

// managerBill prices one manager request. Both policies pay for a manager; the
// layered one also carries the shared pin and a larger hot block. briefOut is the
// output the manager spends writing an assignment.
func managerBill(r *runner, now time.Duration, layered bool, mgrThread *[]seg, mgrTok *int, eng int, briefOut int) time.Duration {
	*mgrThread = append(*mgrThread, seg{id: fmt.Sprintf("mgr:%d", len(*mgrThread)), tokens: 300})
	*mgrTok += 300
	var segs []seg
	var bp []int
	if layered {
		segs = []seg{{"G0", r.w.ConstTokens, false}, {"G1", r.w.SharedTokens, false}, {"G2:mgr", 2000, false}, {"G3:mgr", r.w.AssignTokens, false}}
		segs = append(segs, *mgrThread...)
		if r.p.HotMode == kv.HotInline || r.p.HotMode == kv.HotTurnScoped {
			segs = append(segs, seg{"hot:mgr", 2000, true})
		}
		bp = layeredBP(r.p, segs)
	} else {
		segs = []seg{{"G0", r.w.ConstTokens, false}, {"goal", r.w.AssignTokens, false}, {"mgr-explore", r.w.ExploreTokens, false}}
		segs = append(segs, *mgrThread...)
		bp = []int{0, len(segs) - 1}
	}
	return r.bill(now, eng, segs, bp, 260+briefOut)
}

// firstDispatch is when the manager's dispatch response has finished streaming
// and the workers can start: nobody runs before the plan that spawns them.
func firstDispatch(start, mttfb time.Duration) time.Duration { return start + mttfb + 8*time.Second }

// ---- naive policy ------------------------------------------------------------------

// NaiveOptions configures the baseline harness.
type NaiveOptions struct {
	// CompactAt summarises a subagent's history once it exceeds this many tokens
	// (0 disables). Summary replaces history; everything after is a fresh write.
	CompactAt int
}

// Naive models a conventional harness: every subagent has its own growing
// history cached end to end, is briefed by the manager, and must explore the
// project to learn it. System prompt and tools are shared (as in real harnesses);
// project knowledge is not.
func Naive(w Workload, p Provider, o NaiveOptions) Result {
	name := "naive whole-history"
	if o.CompactAt > 0 {
		name = fmt.Sprintf("naive + summary compaction @%dk", o.CompactAt/1000)
	}
	r := newRunner(w, p, name)
	var mgrThread []seg
	mgrTok := 0
	nWaves, per := waves(w)
	start := time.Duration(0)
	for wave := 0; wave < nWaves; wave++ {
		tp := &pool{next: wave * per, total: min((wave+1)*per, w.Tasks)}
		t0 := firstDispatch(start, managerBill(r, start, false, &mgrThread, &mgrTok, r.cache.route("mgr"), 0))

		var actors []*actor
		for i := 0; i < w.Agents; i++ {
			id := fmt.Sprintf("w%02d", i)
			var (
				hist       []seg
				histTok    int
				taskNo     int
				active     bool
				sp         *taskSpec
				step       int // orientation steps first, then work
				totalSteps int
			)
			a := &actor{id: id, next: t0}
			a.step = func(now time.Duration) (bool, time.Duration) {
				if !active {
					k, ok := tp.take()
					if !ok {
						return true, 0
					}
					taskNo++
					active = true
					hist, histTok = nil, 0
					sp = w.spec(k)
					step, totalSteps = 0, w.ExploreSteps+len(sp.work)
				}
				segs := []seg{{"G0", w.ConstTokens, false}, {fmt.Sprintf("brief:w%d:%s:%d", wave, id, taskNo), w.BriefTokens, false}}
				segs = append(segs, hist...)
				bp := []int{0, len(segs) - 1}
				var st stepSpec
				res := 0
				if step < w.ExploreSteps {
					st = sp.orient[step%len(sp.orient)]
					res = int(float64(w.ExploreTokens) / float64(w.ExploreSteps) * st.f)
				} else {
					st = sp.work[step-w.ExploreSteps]
					res = st.res
				}
				ttfb := r.bill(now, r.cache.route(id), segs, bp, st.out)
				hist = append(hist,
					seg{fmt.Sprintf("o:w%d:%s:%d:%d", wave, id, taskNo, len(hist)), st.out, false},
					seg{fmt.Sprintf("r:w%d:%s:%d:%d", wave, id, taskNo, len(hist)), res, false})
				histTok += st.out + res
				step++
				done := step >= totalSteps
				next := now + ttfb + st.lat
				if !done && o.CompactAt > 0 && histTok > o.CompactAt {
					// Summarise everything into one message; nothing carries over.
					csegs := append([]seg{{"G0", w.ConstTokens, false}, {fmt.Sprintf("brief:w%d:%s:%d", wave, id, taskNo), w.BriefTokens, false}}, hist...)
					csegs = append(csegs, seg{"compact-instr", 1500, true})
					c := r.bill(now+ttfb, r.cache.route(id), csegs, []int{0, len(csegs) - 2}, 2000)
					r.res.Compactions++
					taskNo += 1000 // new history identity
					hist = []seg{{fmt.Sprintf("summary:w%d:%s:%d", wave, id, taskNo), 2000, false}}
					histTok = 2000
					next += c
				}
				if done {
					active = false
					r.res.Steps += totalSteps
					managerBill(r, next, false, &mgrThread, &mgrTok, r.cache.route("mgr"), w.BriefTokens)
				}
				return false, next
			}
			actors = append(actors, a)
		}
		end := runActors(actors)
		r.end = end
		start = end + gap(w)
	}
	return r.finish()
}

// ---- layered policy ------------------------------------------------------------------

// LayeredOptions toggles Sleipnir's mechanisms for ablations.
type LayeredOptions struct {
	Compaction bool
	// ColdMask enables the deterministic mask-only commit when the cache is cold.
	ColdMask bool
	Gate     bool
	Affinity bool
	// Shards spreads agents over routing keys (0: derived from agent count).
	Shards int
	// Planner drives compaction decisions (zero value = defaults).
	Planner kv.Planner
	// RetainedFrac / SpineFrac describe what a model compaction patch achieves.
	RetainedFrac, SpineFrac float64
	// MaskedResult is the size a masked tool result shrinks to (a placeholder).
	MaskedResult int
	// HotEvery is the number of steps between persisted hot notices when the
	// provider's route persists the hot tail (kv.HotPersist); default 3.
	HotEvery int
}

// DefaultLayered enables everything.
func DefaultLayered() LayeredOptions {
	return LayeredOptions{Compaction: true, ColdMask: true, Gate: true, Affinity: true, RetainedFrac: 0.22, SpineFrac: 0.03, MaskedResult: 40, HotEvery: 3, Planner: kv.DefaultPlanner()}
}

// SpineBound and InstructionBound mirror kv.DefaultApplyPolicy: the spine is
// evicted behind a pointer line once it exceeds MaxSpineTokens (down to 70%).
const (
	spineBound = 3000
	// notesGrowth is what each model commit adds to the notes layer (compactor
	// notes ops and preserved user text).
	notesGrowth = 120
)

type compJob struct {
	readyAt   time.Duration
	snapTok   int // thread tokens covered
	snapSegs  int // number of thread segments covered
	spine, rt int
	held      int // boundaries at which the ready patch was held
}

// Layered models Sleipnir: shared/role pins, per-agent notes and spine, a
// planner-driven compacting thread, a small hot tail, a warm gate and routing
// affinity. It follows the code: breakpoints come from kv.PlanMarks, decisions
// from the real kv.Planner (fed the same state the agent feeds it, with warmth
// judged by the entry lifetime like the agent does, and the planner's default
// horizon instead of an oracle for the remaining work), masking hides the bulky
// old results one by one so the cache keeps the prefix before the first, and a
// route with preserved thinking persists the hot tail in the thread on change.
func Layered(w Workload, p Provider, o LayeredOptions) Result {
	name := "sleipnir layered"
	if !o.Compaction {
		name += " − compaction"
	} else if !o.ColdMask {
		name += " − cold masking"
	}
	if !o.Gate {
		name += " − warm gate"
	}
	if !o.Affinity && p.Engines > 1 {
		name += " − affinity"
	}
	if p.HotMode == kv.HotPersist {
		name += " (persisted hot)"
	}
	r := newRunner(w, p, name)
	pl := o.Planner
	shards := o.Shards
	if shards == 0 {
		shards = (w.Agents + 11) / 12
	}
	if shards < 1 {
		shards = 1
	}
	hotEvery := o.HotEvery
	if hotEvery < 1 {
		hotEvery = 3
	}
	route := func(agentIdx int, id string) int {
		if !o.Affinity {
			return r.cache.route("")
		}
		return r.cache.route(fmt.Sprintf("G%d", agentIdx%shards))
	}
	gate := newPrimerGate(o.Gate, p.TTL)
	// A cold prefix has to be primed once per routing key: with affinity that is
	// once per shard, without it every request may land on a different engine and
	// the gate can only serialise on the prefix itself. Providers that route by
	// nothing (Anthropic's per-workspace cache) share one shared-level key.
	gateKeys := func(agentIdx int) (shared, role string) {
		if !o.Affinity || p.Engines <= 1 {
			return "G", "G|R:worker"
		}
		return fmt.Sprintf("G/%d", agentIdx%shards), fmt.Sprintf("G/%d|R:worker", agentIdx%shards)
	}
	horizon := pl.HorizonTurns
	if horizon == 0 {
		horizon = kv.DefaultPlanner().HorizonTurns
	}

	var mgrThread []seg
	mgrTok := 0
	nWaves, per := waves(w)
	start := time.Duration(0)
	for wave := 0; wave < nWaves; wave++ {
		tp := &pool{next: wave * per, total: min((wave+1)*per, w.Tasks)}
		mttfb := managerBill(r, start, true, &mgrThread, &mgrTok, route(0, "mgr"), w.AssignTokens)
		// The manager's request primes the constitution and shared pin on its own
		// engine; other shards still have to prime theirs.
		mShared, mRole := gateKeys(0)
		gate.touch(mShared, start, mttfb)
		gate.touch(mRole, start, mttfb)
		t0 := firstDispatch(start, mttfb)

		var actors []*actor
		for i := 0; i < w.Agents; i++ {
			i := i
			id := fmt.Sprintf("w%02d", i)
			var (
				thread     []seg
				threadTok  int
				taskNo     int
				active     bool
				notesVer   int
				spineVer   int
				notesTok   int
				spineTok   int
				epoch      int
				lastReq    time.Duration
				haveReq    bool
				job        *compJob
				sp         *taskSpec
				si         int
				total      int
				sinceMask  int
				sinceHot   int
				hotSeq     int
				pendingHot bool
			)
			a := &actor{id: id, next: t0}
			a.step = func(now time.Duration) (bool, time.Duration) {
				if !active {
					k, ok := tp.take()
					if !ok {
						return true, 0
					}
					taskNo++
					active = true
					thread, threadTok, notesVer, spineVer, epoch = nil, 0, 0, 0, 0
					notesTok, spineTok, job, haveReq = w.AssignTokens, 0, nil, false
					sinceMask, sinceHot, hotSeq, pendingHot = 1<<20, 0, 0, w.HotTokens > 0
					sp = w.spec(k)
					total = w.OrientSteps + len(sp.work)
					si = 0
				}
				sharedKey, roleKey := gateKeys(i)
				if wait := gate.wait(now, sharedKey, roleKey); wait > 0 {
					return false, now + wait
				}
				// Boundary: commit or start compaction.
				if o.Compaction {
					warm := haveReq
					if p.TTL > 0 {
						warm = haveReq && !pl.IsCold(lastReq-lastReq+lastReqAt(lastReq, haveReq), now, p.TTL)
					}
					prefix := w.ConstTokens + w.SharedTokens + w.RoleTokens
					state := func(threadNow int) kv.State {
						maskable := 0
						if len(thread) > 4 {
							maskable = maskableTokens(thread[:len(thread)-4], w.MaskMinTokens(), o.MaskedResult)
						}
						return kv.State{
							PrefixTokens: prefix, NotesTokens: notesTok, SpineTokens: spineTok,
							ThreadTokens: threadNow, PromptTokens: prefix + notesTok + spineTok + threadNow,
							ContextWindow: w.ContextWindow, Warm: warm, Remaining: 0, W: p.W, Write: p.Write, Explicit: p.Explicit,
							MaskableTokens: maskable, SinceMask: sinceMask,
						}
					}
					_ = horizon
					if job != nil && job.readyAt <= now {
						st := state(threadTok)
						tail := thread[job.snapSegs:]
						tailTok, tailPrior := 0, 0
						for k, sg := range tail {
							tailTok += sg.tokens
							if k < len(tail)-2 { // the newest exchange has not been sent yet
								tailPrior += sg.tokens
							}
						}
						spineAfter, evicted := spineTok+job.spine, false
						if spineAfter > spineBound {
							spineAfter, evicted = spineBound*7/10, true
						}
						out := kv.Outcome{SnapTokens: job.snapTok, SpineAdded: job.spine, RetainedTokens: job.rt, SpineAfter: spineAfter,
							SpineRewritten: evicted, NotesChanged: true, NotesAfter: notesTok + notesGrowth, TailTokens: tailPrior}
						job.held++
						if stale, _ := pl.Stale(job.snapTok+0, threadTok, job.held); stale {
							job = nil // dropped; a new one may start below
						} else if d := pl.ShouldCommit(st, out); d.Yes {
							epoch++
							thread = append([]seg{{fmt.Sprintf("ret:%s:%d:%d", id, taskNo, epoch), job.rt, false}}, tail...)
							threadTok = job.rt + tailTok
							spineTok = spineAfter
							spineVer++
							notesVer++
							notesTok += notesGrowth
							r.res.Compactions++
							job = nil
							pendingHot = pendingHot || p.HotMode == kv.HotPersist
						}
					}
					if job == nil && len(thread) >= 6 {
						d := pl.ShouldStart(state(threadTok))
						switch {
						case d.Yes && d.Mode == kv.ModeMask && o.ColdMask:
							// Cold cache: hide the bulky old results, newest four segments
							// verbatim. The cache keeps everything before the first masked one.
							epoch++
							keep := len(thread) - 4
							changed := 0
							for k := 0; k < keep; k++ {
								if sg := thread[k]; strings.HasPrefix(sg.id, "r:") && sg.tokens >= w.MaskMinTokens() {
									thread[k] = seg{id: fmt.Sprintf("m:%s:%d", sg.id, epoch), tokens: o.MaskedResult}
									changed++
								}
							}
							if changed > 0 {
								threadTok = 0
								for _, sg := range thread {
									threadTok += sg.tokens
								}
								r.res.MaskCommits++
							}
							sinceMask = 0
						case d.Yes && d.Mode == kv.ModeFork:
							// The compactor is a fork: the agent's own prefix, read from cache,
							// plus an instruction and a short answer.
							csegs := layeredSegs(w, id, notesVer, spineVer, notesTok, spineTok, thread, 0)
							csegs = append(csegs, seg{"compact-instr", 1400, true})
							ttfb := r.bill(now, route(i, id), csegs, layeredBP(p, csegs), 700)
							job = &compJob{readyAt: now + ttfb + 14*time.Second, snapTok: threadTok, snapSegs: len(thread),
								spine: int(float64(threadTok) * o.SpineFrac), rt: int(float64(threadTok) * o.RetainedFrac)}
						}
					}
				}
				hot := 0
				if p.HotMode != kv.HotPersist {
					hot = w.HotTokens
				} else if pendingHot && si > 0 || pendingHot && len(thread) == 0 {
					// Persist on change: the board rides in the user turn as a frozen
					// block, first with the task and then every few steps.
					if sinceHot >= hotEvery || len(thread) == 0 {
						hotSeq++
						thread = append(thread, seg{fmt.Sprintf("hotn:%s:%d:%d:%d", id, taskNo, epoch, hotSeq), w.HotTokens, false})
						threadTok += w.HotTokens
						sinceHot = 0
					}
				}
				segs := layeredSegs(w, id, notesVer, spineVer, notesTok, spineTok, thread, hot)
				var st stepSpec
				res := 0
				if si < w.OrientSteps {
					// A shared pin means little orientation is needed: a small share of
					// what a cold agent has to explore.
					st = sp.orient[si%len(sp.orient)]
					res = int(float64(w.ExploreTokens) * w.OrientFrac * st.f)
				} else {
					st = sp.work[si-w.OrientSteps]
					res = st.res
				}
				ttfb := r.bill(now, route(i, id), segs, layeredBP(p, segs), st.out)
				gate.touch(sharedKey, now, ttfb)
				gate.touch(roleKey, now, ttfb)
				lastReq, haveReq = now, true
				sinceMask++
				sinceHot++
				thread = append(thread,
					seg{fmt.Sprintf("o:%s:%d:%d:%d", id, taskNo, epoch, len(thread)), st.out, false},
					seg{fmt.Sprintf("r:%s:%d:%d:%d", id, taskNo, epoch, len(thread)), res, false})
				threadTok += st.out + res
				si++
				next := now + ttfb + st.lat
				if si >= total {
					active = false
					r.res.Steps += total
					managerBill(r, next, true, &mgrThread, &mgrTok, route(0, "mgr"), w.AssignTokens)
				}
				return false, next
			}
			actors = append(actors, a)
		}
		end := runActors(actors)
		r.end = end
		start = end + gap(w)
	}
	return r.finish()
}

// lastReqAt is the start of the agent's previous request (zero when none).
func lastReqAt(t time.Duration, have bool) time.Duration {
	if !have {
		return 0
	}
	return t
}

// maskableTokens is what masking the bulky results among segs would save.
func maskableTokens(segs []seg, minTokens, masked int) int {
	n := 0
	for _, sg := range segs {
		if strings.HasPrefix(sg.id, "r:") && sg.tokens >= minTokens {
			n += sg.tokens - masked
		}
	}
	return n
}

func layeredSegs(w Workload, id string, notesVer, spineVer, notesTok, spineTok int, thread []seg, hot int) []seg {
	segs := []seg{
		{"G0", w.ConstTokens, false},
		{"G1", w.SharedTokens, false},
		{"G2:worker", w.RoleTokens, false},
		{fmt.Sprintf("G3:%s:%d", id, notesVer), notesTok, false},
	}
	if spineTok > 0 {
		segs = append(segs, seg{fmt.Sprintf("G4:%s:%d", id, spineVer), spineTok, false})
	}
	segs = append(segs, thread...)
	if hot > 0 {
		segs = append(segs, seg{"hot:" + id, hot, true})
	}
	return segs
}

// layeredBP places the cache markers with the same planner the renderer uses
// (kv.PlanMarks), on the simulator's segments: "G0" ends the constitution,
// "G1" the shared pin, "G2:*" the role pin, "G3:*" the notes, "G4:*" is the
// spine, everything else is thread. Ephemeral segments (the inline hot tail)
// are not part of the cached prompt. Providers that cache automatically get none.
func layeredBP(p Provider, segs []seg) []int {
	if !p.Explicit {
		return nil
	}
	var blocks []kv.PlanBlock
	var at []int
	for i, s := range segs {
		if s.ephemeral {
			continue
		}
		pb := kv.PlanBlock{Tokens: s.tokens}
		switch {
		case s.id == "G0":
			pb.End, pb.LayerTokens = "const", s.tokens
		case s.id == "G1":
			pb.End, pb.LayerTokens = "shared", s.tokens
		case strings.HasPrefix(s.id, "G2:"):
			pb.End, pb.LayerTokens = "role", s.tokens
		case strings.HasPrefix(s.id, "G3:"):
			pb.End, pb.LayerTokens = "notes", s.tokens
		}
		blocks = append(blocks, pb)
		at = append(at, i)
	}
	caps := kv.Caps{MaxBreakpoints: p.MaxBP, LookbackBlocks: 20, MinPrefixTokens: p.MinPrefix}
	var bp []int
	for _, m := range kv.PlanMarks(blocks, -1, caps, kv.DefaultPolicy()) {
		bp = append(bp, at[m.Block])
	}
	return bp
}

// Comparison is a set of policies run on one workload and provider.
type Comparison struct {
	Workload Workload
	Provider Provider
	Results  []Result
}

// Compare runs the standard set of policies, including ablations.
func Compare(w Workload, p Provider) Comparison {
	c := Comparison{Workload: w, Provider: p}
	c.Results = append(c.Results,
		Naive(w, p, NaiveOptions{}),
		Naive(w, p, NaiveOptions{CompactAt: 100_000}),
	)
	full := DefaultLayered()
	c.Results = append(c.Results, Layered(w, p, full))
	nc := full
	nc.Compaction = false
	c.Results = append(c.Results, Layered(w, p, nc))
	nm := full
	nm.ColdMask = false
	c.Results = append(c.Results, Layered(w, p, nm))
	ng := full
	ng.Gate = false
	c.Results = append(c.Results, Layered(w, p, ng))
	if p.Engines > 1 {
		na := full
		na.Affinity = false
		c.Results = append(c.Results, Layered(w, p, na))
	}
	return c
}

// Baseline returns the plain naive result.
func (c Comparison) Baseline() Result { return c.Results[0] }

// Find returns the result whose policy name has the given prefix.
func (c Comparison) Find(prefix string) (Result, bool) {
	for _, r := range c.Results {
		if len(r.Policy) >= len(prefix) && r.Policy[:len(prefix)] == prefix {
			return r, true
		}
	}
	return Result{}, false
}

// Table renders the comparison.
func (c Comparison) Table() string {
	b := c.Baseline()
	var s string
	s += fmt.Sprintf("workload: %d workers, %d tasks in %d wave(s), ~%.0f work steps/task, shared pin %dk, role pin %dk, orientation %dk (naive) vs %.0f%% (pinned), seed %d\n",
		c.Workload.Agents, c.Workload.Tasks, max(1, c.Workload.Waves), c.Workload.StepsMean, c.Workload.SharedTokens/1000, c.Workload.RoleTokens/1000,
		c.Workload.ExploreTokens/1000, c.Workload.OrientFrac*100, c.Workload.Seed)
	s += fmt.Sprintf("provider: %s\n\n", c.Provider.Name)
	s += fmt.Sprintf("%-40s %9s %7s %6s %8s %8s %7s %8s %9s\n", "policy", "cost(ITE)", "vs base", "hit%", "avg ctx", "requests", "peakRPM", "wall", "fork/mask")
	for _, r := range c.Results {
		delta := (r.ITE/b.ITE - 1) * 100
		s += fmt.Sprintf("%-40s %8.1fM %+6.0f%% %5.0f%% %7.0fk %8d %7.0f %7.0fm %4d/%-4d\n", r.Policy, r.ITE/1e6, delta, r.HitRatio()*100, r.AvgContext/1000, r.Requests, r.PeakRPM, r.WallSeconds/60, r.Compactions, r.MaskCommits)
	}
	return s
}
