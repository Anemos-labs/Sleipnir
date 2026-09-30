package reward

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/cost"
	"github.com/reee344/sleipnir/internal/rl"
)

// synthStepGap spaces steps whose timestamps were not recorded. It must exceed
// the modelled time to first byte, otherwise an agent's next request would be
// issued before its own previous write became readable and every step of a
// hand-built episode would look cold.
const synthStepGap = 30 * time.Second

// Bill is a token and cost tally under the target model. ITE is in input-token
// equivalents (1 = one plain input token); the four *ITE fields split it.
type Bill struct {
	Requests int   `json:"requests"`
	Uncached int64 `json:"uncached"`
	Read     int64 `json:"read"`
	Write5m  int64 `json:"write_5m"`
	Write1h  int64 `json:"write_1h"`
	Output   int64 `json:"output"`

	UncachedITE float64 `json:"uncached_ite"`
	ReadITE     float64 `json:"read_ite"`
	WriteITE    float64 `json:"write_ite"`
	OutputITE   float64 `json:"output_ite"`
	ITE         float64 `json:"ite"`
}

func (b *Bill) add(o Bill) {
	b.Requests += o.Requests
	b.Uncached += o.Uncached
	b.Read += o.Read
	b.Write5m += o.Write5m
	b.Write1h += o.Write1h
	b.Output += o.Output
	b.UncachedITE += o.UncachedITE
	b.ReadITE += o.ReadITE
	b.WriteITE += o.WriteITE
	b.OutputITE += o.OutputITE
	b.ITE += o.ITE
}

// Input is the prompt side of the bill in tokens.
func (b Bill) Input() int64 { return b.Uncached + b.Read + b.Write5m + b.Write1h }

// Usage converts the tally to a core.Usage, e.g. to price it in dollars.
func (b Bill) Usage() core.Usage {
	return core.Usage{
		InputTokens: int(b.Uncached), CacheReadTokens: int(b.Read),
		CacheWrite5mTokens: int(b.Write5m), CacheWrite1hTokens: int(b.Write1h), OutputTokens: int(b.Output),
	}
}

// StepBill is one replayed request.
type StepBill struct {
	Bill
	Agent  string        `json:"agent"`
	Step   string        `json:"step"`
	Kind   string        `json:"kind"`
	Role   string        `json:"role"`
	At     time.Duration `json:"at_ns"` // offset from the first request of the episode
	Prompt int           `json:"prompt_tokens"`
	Hit    bool          `json:"hit"`
	TTFB   time.Duration `json:"ttfb_ns"`
	// Cold reports a request that read nothing although it has a prefix a
	// previous request of this episode had already written.
	Cold bool `json:"cold,omitempty"`
}

// Repriced is the counterfactual bill of an episode under a target model.
type Repriced struct {
	Target  string       `json:"target"`
	Weights cost.Weights `json:"weights"` // Write5m/Write1h are the effective write weights
	// Bill is the total: ITE (input-token equivalents, output included),
	// Requests and the token classes.
	Bill
	USD float64 `json:"usd,omitempty"` // total priced with the target's dollar prices

	PerAgent map[string]Bill `json:"per_agent,omitempty"`
	PerRole  map[string]Bill `json:"per_role,omitempty"`
	PerKind  map[string]Bill `json:"per_kind,omitempty"`
	// Steps lists the requests in replay order (by time across all agents).
	Steps []StepBill `json:"steps,omitempty"`
	// Recorded prices the recorded usage of the episode at the target's weights:
	// what the run actually consumed, valued as the target values it. The
	// difference to Bill is the effect of the target's cache rules alone.
	Recorded Bill `json:"recorded"`
	// SharedTokens is the size assumed for each shared prefix id.
	SharedTokens map[string]int `json:"shared_tokens,omitempty"`
	Warnings     []string       `json:"warnings,omitempty"`

	pos map[[2]int]int // (agent, step) -> index into Steps
}

// StepOf returns the replayed bill of agent ai's step si (indexes into
// ep.Agents and Agent.Steps).
func (r Repriced) StepOf(ai, si int) (StepBill, bool) {
	i, ok := r.pos[[2]int{ai, si}]
	if !ok {
		return StepBill{}, false
	}
	return r.Steps[i], true
}

// Reprice replays an episode's recorded requests through the target model's
// cache rules and returns what the episode would have cost there.
//
// The target is what the policy will be deployed on, which need not be what it
// was rolled out on (a self-hosted vLLM has unknown prices and a different
// cache), so nothing about the recorded billing is trusted: only the request
// recipes are. Each request contributes its total prompt size
// (Prompt.Tokens, else the recorded input tokens), its position in the agent's
// append-only chain, the shared prefix it carries (Prompt.SharedPrefix) and the
// time it was issued (Step.At); the output size is the recorded usage.
// Requests are replayed in time order across all agents, so a worker that
// starts before the primer's first byte pays a cold write, exactly as in sim.
func Reprice(ep *rl.Episode, target cost.Model) (Repriced, error) {
	return RepriceWith(ep, target, RepriceOptions{})
}

// RepriceWith is Reprice with explicit cache-model options.
func RepriceWith(ep *rl.Episode, target cost.Model, o RepriceOptions) (Repriced, error) {
	if ep == nil {
		return Repriced{}, errors.New("reward: reprice: nil episode")
	}
	w := target.Price.Weights()
	w5, w1 := writeWeights(target)
	for name, v := range map[string]float64{"read": w.Read, "write5m": w5, "write1h": w1, "output": w.Output} {
		if !finite(v) || v < 0 {
			return Repriced{}, fmt.Errorf("reward: reprice: target %q has an invalid %s price weight %v", target.ID, name, v)
		}
	}
	w.Write5m, w.Write1h = w5, w1

	out := Repriced{Target: target.ID, Weights: w, PerAgent: map[string]Bill{}, PerRole: map[string]Bill{}, PerKind: map[string]Bill{}}

	infos, warns := collectSteps(ep)
	out.Warnings = append(out.Warnings, warns...)
	sharedSizes, warns := inferSharedTokens(ep, infos, o)
	out.Warnings = append(out.Warnings, warns...)
	if len(sharedSizes) > 0 {
		out.SharedTokens = sharedSizes
	}
	reqs := planRequests(ep, infos, sharedSizes, target)

	sort.SliceStable(reqs, func(i, j int) bool {
		a, b := reqs[i], reqs[j]
		if a.at != b.at {
			return a.at < b.at
		}
		if a.ai != b.ai {
			return a.ai < b.ai
		}
		return a.si < b.si
	})

	cache := NewCache(target, o)
	out.pos = make(map[[2]int]int, len(reqs))
	seenPrefix := map[[16]byte]bool{}
	for _, r := range reqs {
		key := ""
		if target.Cache.KeyRouting {
			// The harness sends the shared-prefix id as the routing key, so agents
			// that share a prefix land on the engine that holds it.
			key = r.route
		}
		res := cache.do(cache.Route(key), r.at, r.nodes, r.tail, r.bp, reqInfo{latency: r.latency, out: r.out})
		b := Bill{
			Requests: 1, Uncached: int64(res.Uncached), Read: int64(res.Read),
			Write5m: int64(res.Write5m), Write1h: int64(res.Write1h), Output: int64(r.out),
		}
		fillITE(&b, w)
		sb := StepBill{
			Bill: b, Agent: ep.Agents[r.ai].ID, Step: r.step.ID, Kind: r.step.Kind, Role: stepRole(&ep.Agents[r.ai], r.step),
			At: r.at, Prompt: r.prompt, Hit: res.Hit, TTFB: res.TTFB,
		}
		if n := len(r.nodes); n > 0 && !res.Hit && seenPrefix[r.nodes[0].key] {
			sb.Cold = true
		}
		if len(r.nodes) > 0 {
			seenPrefix[r.nodes[0].key] = true
		}
		out.pos[[2]int{r.ai, r.si}] = len(out.Steps)
		out.Steps = append(out.Steps, sb)
		out.Bill.add(b)
		addTo(out.PerAgent, sb.Agent, b)
		addTo(out.PerRole, sb.Role, b)
		addTo(out.PerKind, kindName(r.step), b)
	}
	// Recorded usage at target prices, in agent/step order.
	for ai := range ep.Agents {
		for si := range ep.Agents[ai].Steps {
			u := ep.Agents[ai].Steps[si].Usage
			b := Bill{Requests: 1, Uncached: int64(max(u.InputTokens, 0)), Read: int64(max(u.CacheReadTokens, 0)),
				Write5m: int64(max(u.CacheWrite5mTokens, 0)), Write1h: int64(max(u.CacheWrite1hTokens, 0)), Output: int64(max(u.OutputTokens, 0))}
			fillITE(&b, w)
			out.Recorded.add(b)
		}
	}
	out.USD = target.Price.USD(out.Bill.Usage())
	if !finite(out.ITE) {
		return Repriced{}, errors.New("reward: reprice: non-finite total")
	}
	return out, nil
}

func fillITE(b *Bill, w cost.Weights) {
	b.UncachedITE = float64(b.Uncached)
	b.ReadITE = w.Read * float64(b.Read)
	b.WriteITE = w.Write5m*float64(b.Write5m) + w.Write1h*float64(b.Write1h)
	b.OutputITE = w.Output * float64(b.Output)
	b.ITE = b.UncachedITE + b.ReadITE + b.WriteITE + b.OutputITE
}

func addTo(m map[string]Bill, key string, b Bill) {
	cur := m[key]
	cur.add(b)
	m[key] = cur
}

func kindName(s *rl.Step) string {
	if s.Kind == "" {
		return rl.KindMain
	}
	return s.Kind
}

// ---- step collection ------------------------------------------------------------------

// stepInfo is what replay needs to know about one step, independent of time.
type stepInfo struct {
	ai, si int
	st     *rl.Step
	prompt int
	out    int
	at     time.Duration
	// gen reports that the step starts a new append-only chain: the agent's first
	// request, a rebase (segment or epoch change), a change of shared prefix, or a
	// prompt that shrank without a declared rebase.
	gen bool
}

func promptTokens(st *rl.Step) int {
	if st.Prompt.Tokens > 0 {
		return st.Prompt.Tokens
	}
	if n := st.Usage.TotalInput(); n > 0 {
		return n
	}
	return 0
}

func outputTokens(st *rl.Step) int {
	if st.Usage.OutputTokens > 0 {
		return st.Usage.OutputTokens
	}
	if st.Usage != (core.Usage{}) {
		return 0 // usage was recorded and says no output
	}
	// No usage at all: estimate from the completion so an unrecorded step is not
	// free. The estimator is a fixed ratio, so this is deterministic.
	est := core.NewBytesEstimator()
	n := 0
	for _, b := range st.Completion.Turn.Blocks {
		switch b.Kind {
		case core.BlockText, core.BlockThinking:
			n += est.Tokens(b.Text)
		case core.BlockToolUse:
			n += est.Tokens(b.ToolName) + est.Tokens(string(b.Input))
		}
	}
	return n
}

func isFork(st *rl.Step) bool { return st.Kind == rl.KindCompactor || st.Kind == rl.KindMailman }
func isMain(st *rl.Step) bool { return st.Kind == rl.KindMain || st.Kind == "" }

// collectSteps flattens the episode into stepInfo in agent/step order and
// assigns every step an offset on a common timeline.
func collectSteps(ep *rl.Episode) ([]stepInfo, []string) {
	var warns []string
	var origin time.Time
	for ai := range ep.Agents {
		for si := range ep.Agents[ai].Steps {
			if at := ep.Agents[ai].Steps[si].At; !at.IsZero() && (origin.IsZero() || at.Before(origin)) {
				origin = at
			}
		}
	}
	if origin.IsZero() {
		origin = ep.StartedAt
	}

	var infos []stepInfo
	cursor := time.Duration(0) // end of the timeline built so far, for agents with no timestamps
	unknownSize := 0
	for ai := range ep.Agents {
		a := &ep.Agents[ai]
		start := len(infos)
		stamped := 0
		for si := range a.Steps {
			st := &a.Steps[si]
			in := stepInfo{ai: ai, si: si, st: st, prompt: promptTokens(st), out: outputTokens(st)}
			if in.prompt == 0 {
				unknownSize++
			}
			if !st.At.IsZero() && !origin.IsZero() {
				in.at = st.At.Sub(origin)
				stamped++
			} else {
				in.at = -1
			}
			infos = append(infos, in)
		}
		mine := infos[start:]
		if len(mine) == 0 {
			continue
		}
		fillTimes(mine, stamped, cursor)
		for _, in := range mine {
			if in.at > cursor {
				cursor = in.at
			}
		}
		cursor += synthStepGap
	}
	if unknownSize > 0 {
		warns = append(warns, fmt.Sprintf("%d step(s) have no recorded prompt size and are priced as empty prompts", unknownSize))
	}
	markGenerations(infos)
	return infos, warns
}

// fillTimes gives unstamped steps a time: leading ones count back from the first
// stamped step, the rest follow their predecessor, and an agent with no
// timestamps at all starts after everything laid out so far (a manager's
// workers therefore find its shared prefix warm, as the warm gate arranges).
func fillTimes(steps []stepInfo, stamped int, cursor time.Duration) {
	gap := func(i int) time.Duration {
		g := synthStepGap
		if l := time.Duration(steps[i].st.LatencyMs) * time.Millisecond; l > g {
			g = l
		}
		return g
	}
	if stamped == 0 {
		t := cursor
		for i := range steps {
			steps[i].at = t
			t += gap(i)
		}
		return
	}
	first := -1
	for i := range steps {
		if steps[i].at >= 0 {
			first = i
			break
		}
	}
	for i := first - 1; i >= 0; i-- {
		steps[i].at = steps[i+1].at - gap(i)
		if steps[i].at < 0 {
			steps[i].at = 0
		}
	}
	for i := first + 1; i < len(steps); i++ {
		if steps[i].at < 0 {
			steps[i].at = steps[i-1].at + gap(i-1)
		}
	}
}

// ---- shared prefix sizes -------------------------------------------------------------

// inferSharedTokens sizes each shared prefix id. Sources, best first: the
// caller's pins; an inlined prompt (exact bytes of tools, system and the first
// SharedMessages messages, scaled to the recorded prompt size); the cache read a
// later agent's first request got (its ExpectedRead, else its recorded cache
// read), which can only be the shared prefix; and, when several agents carry the
// id, the smallest first prompt among them (an upper bound: it also contains the
// agent's own assignment). An id carried by a single agent needs no size, since
// nothing else can read its entry.
func inferSharedTokens(ep *rl.Episode, infos []stepInfo, o RepriceOptions) (map[string]int, []string) {
	type acc struct {
		agents  map[int]bool
		minP    int
		inline  int
		cand    int
		hasInl  bool
		hasCand bool
	}
	by := map[string]*acc{}
	for _, in := range infos {
		id := in.st.Prompt.SharedPrefix
		if id == "" || !isMain(in.st) {
			continue
		}
		a := by[id]
		if a == nil {
			a = &acc{agents: map[int]bool{}, minP: math.MaxInt}
			by[id] = a
		}
		a.agents[in.ai] = true
		if in.prompt > 0 && in.prompt < a.minP && in.gen {
			a.minP = in.prompt
		}
		if in.st.Inline != nil && in.prompt > 0 {
			if n := inlineSharedTokens(in.st); n > 0 && (!a.hasInl || n < a.inline) {
				a.inline, a.hasInl = n, true
			}
		}
		if in.gen {
			for _, c := range []int{in.st.Cache.ExpectedRead, in.st.Usage.CacheReadTokens} {
				if c > 0 && (!a.hasCand || c < a.cand) {
					a.cand, a.hasCand = c, true
				}
			}
		}
	}
	sizes := map[string]int{}
	var warns []string
	for _, id := range sortedKeys(by) {
		a := by[id]
		s := 0
		switch pin, ok := o.SharedTokens[id]; {
		case ok:
			s = pin
		case a.hasInl:
			s = a.inline
		case a.hasCand:
			s = a.cand
		case len(a.agents) > 1 && a.minP != math.MaxInt:
			s = a.minP
			warns = append(warns, fmt.Sprintf("shared prefix %q: size not recorded, assuming %d tokens (smallest first prompt); pin it with reprice.shared_tokens", id, s))
		}
		if a.minP != math.MaxInt && s > a.minP {
			s = a.minP // a prefix cannot exceed any prompt that contains it
		}
		if len(a.agents) < 2 && !a.hasInl && o.SharedTokens[id] == 0 {
			s = 0 // a single agent's chain needs no separate shared node
		}
		if s > 0 {
			sizes[id] = s
		}
	}
	return sizes, warns
}

// markGenerations sets stepInfo.gen: the steps that begin a new append-only
// chain. Forks and other non-main calls never do.
func markGenerations(infos []stepInfo) {
	type state struct {
		seen       bool
		seg, epoch int
		prefix     string
		prompt     int
	}
	states := map[int]*state{}
	for k := range infos {
		in := &infos[k]
		if !isMain(in.st) {
			continue
		}
		s := states[in.ai]
		if s == nil {
			s = &state{}
			states[in.ai] = s
		}
		in.gen = !s.seen || in.st.Segment != s.seg || in.st.Epoch != s.epoch ||
			in.st.Prompt.SharedPrefix != s.prefix || in.prompt < s.prompt
		s.seen, s.seg, s.epoch, s.prefix, s.prompt = true, in.st.Segment, in.st.Epoch, in.st.Prompt.SharedPrefix, in.prompt
	}
}

// inlineSharedTokens sizes a step's shared prefix from its inlined prompt: the
// share of the prompt's bytes taken by tools, system blocks and the first
// SharedMessages messages, applied to the recorded token count.
func inlineSharedTokens(st *rl.Step) int {
	p := st.Inline
	total := st.Prompt.Tokens
	if p == nil || total <= 0 {
		return 0
	}
	size := func(v any) int {
		b, err := core.MarshalStable(v)
		if err != nil {
			return 0
		}
		return len(b)
	}
	shared, all := 0, 0
	if len(p.Tools) > 0 {
		n := size(p.Tools)
		shared += n
		all += n
	}
	for _, b := range p.System {
		n := size(b)
		shared += n
		all += n
	}
	for i, m := range p.Messages {
		n := size(m.Blocks) + len(m.Role) + 12
		if i < st.Prompt.SharedMessages {
			shared += n
		}
		all += n
	}
	if all == 0 || shared == 0 {
		return 0
	}
	return int(float64(total) * float64(shared) / float64(all))
}

// ---- chains and requests ---------------------------------------------------------------

// plannedReq is one request ready to be replayed: its chain nodes, the
// ephemeral tail, and its timing.
type plannedReq struct {
	ai, si  int
	step    *rl.Step
	nodes   []node
	tail    int
	bp      []int
	out     int
	prompt  int
	at      time.Duration
	latency time.Duration
	route   string
}

// generation is one agent's current append-only chain. The shared node (when
// there is one) is element 0, so a request's nodes are simply a prefix of nodes
// and no per-request copies are made.
type generation struct {
	id        int
	nodes     []node
	hasShared bool
	prompt    int
}

func (g *generation) last() [16]byte {
	if n := len(g.nodes); n > 0 {
		return g.nodes[n-1].key
	}
	return [16]byte{}
}

// planRequests turns the flattened steps into requests. It works per agent in
// step order, independent of time, so a fork (compactor) always sees the chain
// state of the main request that precedes it in the agent's own order.
func planRequests(ep *rl.Episode, infos []stepInfo, shared map[string]int, target cost.Model) []plannedReq {
	maxBP := target.Cache.MaxBreakpoints
	if maxBP <= 0 {
		maxBP = defaultMaxBreakpoints
	}
	reqs := make([]plannedReq, 0, len(infos))
	type agentState struct {
		g   *generation
		gen int
	}
	states := make([]agentState, len(ep.Agents))

	for k := range infos {
		in := &infos[k]
		st := in.st
		as := &states[in.ai]
		prefix := st.Prompt.SharedPrefix
		p := in.prompt
		r := plannedReq{ai: in.ai, si: in.si, step: st, out: in.out, prompt: p, at: in.at,
			latency: time.Duration(st.LatencyMs) * time.Millisecond}
		r.route = routeKey(prefix, in.ai)

		sharedNode := func() (node, bool) {
			s := shared[prefix]
			if prefix == "" || s <= 0 {
				return node{}, false
			}
			if s > p {
				s = p
			}
			return node{key: chainKey([16]byte{}, "shared:"+prefix), cum: s, long: true}, true
		}

		switch {
		case isMain(st):
			if in.gen || as.g == nil {
				g := &generation{id: as.gen, prompt: p}
				as.gen++
				if sn, ok := sharedNode(); ok {
					g.nodes = append(g.nodes, sn)
					g.hasShared = true
				}
				if p > lastCum(g.nodes) {
					g.nodes = append(g.nodes, node{key: chainKey(g.last(), privateID(in.ai, g.id, len(g.nodes))), cum: p})
				}
				as.g = g
			} else if g := as.g; p > g.prompt {
				g.nodes = append(g.nodes, node{key: chainKey(g.last(), privateID(in.ai, g.id, len(g.nodes))), cum: p})
				g.prompt = p
			}
			g := as.g
			r.nodes = g.nodes[:len(g.nodes):len(g.nodes)]
			r.tail = p - lastCum(r.nodes)
			r.bp = breakpoints(r.nodes, g.hasShared, maxBP)
		case isFork(st) && as.g != nil:
			// A fork reuses the agent's own prefix and appends a one-shot
			// instruction: read the chain up to what fits, bill the rest uncached.
			g := as.g
			m := len(g.nodes)
			for m > 0 && g.nodes[m-1].cum > p {
				m--
			}
			r.nodes = g.nodes[:m:m]
			r.tail = p - lastCum(r.nodes)
			r.bp = breakpoints(r.nodes, g.hasShared && m > 0, maxBP)
		default:
			// Standalone request (recon, a fork with no chain yet): only the shared
			// prefix, if any, is common with other requests.
			if sn, ok := sharedNode(); ok {
				r.nodes = []node{sn}
			}
			r.tail = p - lastCum(r.nodes)
			r.bp = breakpoints(r.nodes, len(r.nodes) > 0, maxBP)
		}
		if r.tail < 0 {
			r.tail = 0
		}
		reqs = append(reqs, r)
	}
	return reqs
}

func routeKey(prefix string, ai int) string {
	if prefix != "" {
		return "p:" + prefix
	}
	return "a:" + strconv.Itoa(ai)
}

func privateID(ai, gen, idx int) string {
	return "a" + strconv.Itoa(ai) + "/g" + strconv.Itoa(gen) + "/n" + strconv.Itoa(idx)
}

func lastCum(nodes []node) int {
	if n := len(nodes); n > 0 {
		return nodes[n-1].cum
	}
	return 0
}

// breakpoints places explicit-cache markers the way the kv planner does: the
// rolling one at the end of the newest persistent segment first, then the end of
// the shared pin, then the end of the agent's first private segment (notes and
// spine), within the provider's limit. Automatic caches ignore them.
func breakpoints(nodes []node, hasShared bool, limit int) []int {
	n := len(nodes)
	if n == 0 {
		return nil
	}
	var cands []int
	add := func(i int) {
		if i < 0 || i >= n {
			return
		}
		for _, c := range cands {
			if c == i {
				return
			}
		}
		cands = append(cands, i)
	}
	add(n - 1)
	firstPrivate := 0
	if hasShared {
		add(0)
		firstPrivate = 1
	}
	add(firstPrivate)
	if len(cands) > limit {
		cands = cands[:limit]
	}
	sort.Ints(cands)
	return cands
}
