package traj

import (
	"errors"
	"fmt"
	"sort"
	"strconv"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/rl"
)

// Options tells Episode what the log cannot: which task and sample the run was,
// and which model is the policy under training.
type Options struct {
	// TaskID and Sample identify the episode ("<task>/<sample>"). TaskID defaults
	// to the task's id, then to the log's session id.
	TaskID string
	Sample int
	// Group is the GRPO group: episodes of one task and one policy snapshot.
	// It defaults to "<task>@<checkpoint or model>".
	Group string
	// Policy names the model being trained. A step is trainable only when its model
	// equals Policy.Model; every other model is treated as a teacher (RoleModels is
	// descriptive and copied through). With no Policy.Model nothing is trainable
	// and every model is a teacher: a policy must be named to train on a run.
	Policy rl.PolicyRef
	// Task overrides the run's task.json. It supplies the environment, licence and
	// the verifier command used to recognise self-run checks.
	Task *rl.Task
	// Harness is copied into the episode; Renderer, Roles and Agents are filled from
	// the log when left empty.
	Harness rl.HarnessRef

	// StrictTokens applies the literal contract of docs/TRAINING-DATA.md: a
	// segment whose consecutive token traces violate the strict prefix property
	// (P[i+1] starts with P[i] ++ C[i]) loses all its token traces and the episode
	// is flagged token_mismatch. The default keeps every individually consistent
	// trace, because each per-step sample is still exactly what the endpoint saw;
	// the violation is counted in Signals["token_prefix_breaks"] and the exporter
	// refuses to pack such a segment into one sequence.
	StrictTokens bool
}

// ErrNoRequests is returned for a log without a single model request.
var ErrNoRequests = errors.New("traj: the log contains no model requests")

// Signal names beyond the rl vocabulary that traj derives.
const (
	SigRequestErrors = "request_errors" // requests that produced no usable step: errors, kills, cancels, unreadable completions
	SigRequestRetry  = "request_retries"
	SigPrefixBreaks  = "token_prefix_breaks"
)

// Episode builds the canonical episode of the run: agents, steps with exact
// prompt references, segments, observations, edges, signals, outcome and cost. It
// carries no rewards; the reward package fills those.
//
// Partial data is preferred to none: a torn log yields an episode flagged
// truncated, and steps whose request never got a response are dropped and
// counted in Signals["request_errors"]. Only a log with no model request at all
// is an error.
func (r *Run) Episode(o Options) (*rl.Episode, error) {
	if len(r.reqOrder) == 0 {
		return nil, ErrNoRequests
	}
	b := &builder{run: r, o: o, v: r.buildView(), agents: map[string]*agentInfo{}}
	return b.build()
}

// agentInfo collects everything known about one agent while an episode is built.
type agentInfo struct {
	id, role, parent, model, task string
	firstSeq                      uint64
	spawned                       bool
	reqs                          []*reqInfo  // every request, kept or not, in request order
	steps                         []*stepInfo // kept steps in request order
	main                          []*stepInfo // kept main-kind steps in request order
	segs                          []rl.Segment
	state                         string // from agent.end
	ended                         bool
}

// stepInfo is a kept step while it is being assembled.
type stepInfo struct {
	q    *reqInfo
	p    *respInfo
	turn core.Turn
	tok  *core.TokenTrace // individually consistent trace, or nil
	msgs []core.Hash      // full message hash list, nil when the manifest chain is broken
	a    *agentInfo
	step rl.Step
}

type builder struct {
	run *Run
	o   Options
	v   *view

	task   *rl.Task
	agents map[string]*agentInfo
	order  []*agentInfo
	steps  map[string]*stepInfo
	mains  []*stepInfo // all kept main steps by request order

	epochSeqs []uint64 // seqs of shared-epoch layer commits, ascending

	requestErrors int
	tokenMismatch bool
	prefixBreaks  int

	runs    []toolRun
	edges   []edge
	signals map[string]float64
}

func (b *builder) build() (*rl.Episode, error) {
	r := b.run
	b.task = b.o.Task
	if b.task == nil {
		b.task = r.task
	}
	for _, l := range b.v.layers {
		if l.scope == "shared-epoch" {
			b.epochSeqs = append(b.epochSeqs, l.seq)
		}
	}
	b.collectAgents()
	b.buildSteps()
	if len(b.steps) == 0 {
		return nil, fmt.Errorf("traj: none of the %d requests produced a usable step (%s)", len(r.reqOrder), b.whyNoSteps())
	}
	for _, a := range b.order {
		b.segmentize(a)
		b.checkTokenChains(a)
		b.observations(a)
	}
	shared := b.sharedPrefix()
	for _, st := range b.steps {
		b.finishStep(st, shared)
	}
	b.buildEdges()
	b.deriveSignals()

	ep := &rl.Episode{Schema: rl.SchemaEpisode}
	b.identify(ep)
	for _, a := range b.order {
		ep.Agents = append(ep.Agents, b.agentOut(a))
	}
	for _, e := range b.edges {
		ep.Edges = append(ep.Edges, e.Edge)
	}
	ep.StartedAt, ep.EndedAt = b.span()
	ep.Signals = b.signals
	ep.Outcome = b.outcome()
	ep.Cost = b.cost()
	b.provenance(ep)
	b.flags(ep)
	return ep, nil
}

// identify fills the episode identity, policy, harness and environment.
func (b *builder) identify(ep *rl.Episode) {
	task := b.o.TaskID
	if task == "" && b.task != nil {
		task = b.task.ID
	}
	if task == "" && len(b.run.evs) > 0 {
		task = b.run.evs[0].Session
	}
	if task == "" {
		task = "run"
	}
	ep.TaskID, ep.Sample = task, b.o.Sample
	ep.ID = task + "/" + strconv.Itoa(b.o.Sample)
	ep.Group = b.o.Group
	if ep.Group == "" {
		snap := b.o.Policy.Checkpoint
		if snap == "" {
			snap = b.o.Policy.Model
		}
		ep.Group = task + "@" + snap
	}
	ep.Policy = b.o.Policy
	if len(ep.Policy.Sampling) == 0 {
		for _, st := range b.mains {
			if len(st.q.params) > 0 {
				ep.Policy.Sampling = st.q.params
				break
			}
		}
	}
	ep.Harness = b.o.Harness
	if ep.Harness.Renderer == "" {
		for _, q := range b.run.reqOrder {
			if q.renderer != "" {
				ep.Harness.Renderer = q.renderer
				break
			}
		}
	}
	roles := map[string]bool{}
	for _, a := range b.order {
		if a.role != "" {
			roles[a.role] = true
		}
	}
	if len(ep.Harness.Roles) == 0 {
		for role := range roles {
			ep.Harness.Roles = append(ep.Harness.Roles, role)
		}
		sort.Strings(ep.Harness.Roles)
	}
	if ep.Harness.Agents == 0 {
		ep.Harness.Agents = len(b.order)
	}
	if t := b.task; t != nil {
		ep.Env.Repo = t.Repo.URL
		if ep.Env.Repo == "" {
			ep.Env.Repo = t.Repo.Path
		}
		ep.Env.Commit = t.Repo.Commit
		ep.Env.Network = t.Network
		lim := map[string]string{}
		if t.Budget.Steps > 0 {
			lim["steps"] = strconv.Itoa(t.Budget.Steps)
		}
		if t.Budget.Requests > 0 {
			lim["requests"] = strconv.Itoa(t.Budget.Requests)
		}
		if t.Budget.ITE > 0 {
			lim["ite"] = strconv.FormatFloat(t.Budget.ITE, 'f', -1, 64)
		}
		if t.Budget.WallS > 0 {
			lim["wall_s"] = strconv.Itoa(t.Budget.WallS)
		}
		if t.Budget.ContextWindow > 0 {
			lim["context_window"] = strconv.Itoa(t.Budget.ContextWindow)
		}
		if len(lim) > 0 {
			ep.Env.Limits = lim
		}
	}
}

// collectAgents creates the agent table from agent.spawn events and, for runs
// that never spawn (a single agent), from the requests themselves.
func (b *builder) collectAgents() {
	for _, sp := range b.v.spawns {
		a, ok := b.agents[sp.id]
		if ok {
			continue // a repeated spawn (a reused worker) does not redefine it
		}
		a = &agentInfo{id: sp.id, role: sp.role, parent: sp.parent, model: sp.model, task: sp.task, firstSeq: sp.seq, spawned: true}
		b.agents[sp.id] = a
		b.order = append(b.order, a)
	}
	for _, q := range b.run.reqOrder {
		id := q.agent
		if id == "" {
			id = "agent"
		}
		if _, ok := b.agents[id]; !ok {
			a := &agentInfo{id: id, firstSeq: q.seq}
			b.agents[id] = a
			b.order = append(b.order, a)
		}
	}
	for _, e := range b.v.ends {
		if a, ok := b.agents[e.id]; ok {
			a.state, a.ended = e.state, true
		}
	}
}

func (b *builder) agentOf(q *reqInfo) *agentInfo {
	if q.agent == "" {
		return b.agents["agent"]
	}
	return b.agents[q.agent]
}

// buildSteps turns every request that got a usable response into a step.
func (b *builder) buildSteps() {
	r := b.run
	b.steps = map[string]*stepInfo{}
	for _, q := range r.reqOrder {
		a := b.agentOf(q)
		a.reqs = append(a.reqs, q)
		p, ok := r.resps[q.id]
		if !ok {
			b.requestErrors++
			continue
		}
		turn, mm := r.completionOf(p)
		if mm != nil {
			b.requestErrors++
			continue
		}
		st := &stepInfo{q: q, p: p, turn: turn, a: a}
		if tr, tmm, present := r.traceOf(p); tmm == nil && present {
			if tr.Consistent() {
				st.tok = tr
			} else {
				b.tokenMismatch = true
			}
		}
		if msgs, err := r.msgHashes(q.id); err == nil {
			st.msgs = msgs
		}
		st.step = b.stepOf(st)
		a.steps = append(a.steps, st)
		b.steps[q.id] = st
		if q.kind == rl.KindMain {
			a.main = append(a.main, st)
			b.mains = append(b.mains, st)
		}
		if a.role == "" && q.kind == rl.KindMain {
			a.role = q.role
		}
		if a.model == "" && q.kind == rl.KindMain {
			a.model = q.model
		}
	}
}

// stepOf fills the parts of a step that come straight from the request and
// response events.
func (b *builder) stepOf(st *stepInfo) rl.Step {
	q, p := st.q, st.p
	role := q.role
	if role == "" {
		if q.kind == rl.KindMain {
			role = st.a.role
		} else {
			role = q.kind
		}
	}
	wire := q.man.Wire
	if wire == "" {
		wire = q.wire
	}
	model := q.model
	if model == "" {
		model = p.model
	}
	hit := p.hitRatio
	if hit == 0 {
		hit = p.usage.HitRatio()
	}
	lat := p.totalMs
	if lat <= 0 && !p.ts.IsZero() && !q.ts.IsZero() && p.ts.After(q.ts) {
		lat = p.ts.Sub(q.ts).Milliseconds()
	}
	return rl.Step{
		ID: q.id, Kind: q.kind, Role: role, Model: model, At: q.ts,
		Prompt:     rl.PromptRef{Req: q.id, WireHash: wire, Tokens: p.usage.TotalInput()},
		Completion: rl.Completion{Turn: st.turn, Stop: p.stop},
		Tokens:     st.tok,
		Usage:      p.usage,
		Cache:      rl.CacheInfo{HitRatio: hit, ExpectedRead: p.expected, Anomaly: p.anomaly},
		LatencyMs:  lat,
	}
}

// finishStep sets the fields that need the whole episode: trainability and the
// shared-prefix reference.
func (b *builder) finishStep(st *stepInfo, sp sharedInfo) {
	st.step.Trainable = b.o.Policy.Model != "" && st.step.Model == b.o.Policy.Model
	if sp.ok && st.q.man.Tools == sp.tools && equalHashes(st.q.man.System, sp.system) {
		st.step.Prompt.SharedPrefix = sp.id
		st.step.Prompt.SharedMessages = commonPrefix(st.msgs, sp.leading)
	}
}

func equalHashes(a, b []core.Hash) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// commonPrefix is the length of the common leading run of a and b.
func commonPrefix(a, b []core.Hash) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}

// sharedInfo describes the prefix every agent's first prompt has in common.
type sharedInfo struct {
	ok      bool
	id      string
	tools   core.Hash
	system  []core.Hash
	leading []core.Hash // the leading messages common to all agents (may be empty)
}

// sharedPrefix finds the tools, system blocks and leading messages that are
// byte-identical across the first prompts of all agents, from manifest hashes
// alone. With a single agent the prefix is tools and system only: one prompt is
// trivially "shared" with itself, which would say nothing.
func (b *builder) sharedPrefix() sharedInfo {
	var firsts []*stepInfo
	for _, a := range b.order {
		if len(a.steps) == 0 {
			continue
		}
		first := a.steps[0]
		if len(a.main) > 0 {
			first = a.main[0]
		}
		if first.q.noManifest {
			continue
		}
		firsts = append(firsts, first)
	}
	if len(firsts) == 0 {
		return sharedInfo{}
	}
	sp := sharedInfo{ok: true, tools: firsts[0].q.man.Tools, system: firsts[0].q.man.System}
	for _, f := range firsts[1:] {
		if f.q.man.Tools != sp.tools || !equalHashes(f.q.man.System, sp.system) {
			return sharedInfo{}
		}
	}
	if len(firsts) >= 2 && firsts[0].msgs != nil {
		lead := firsts[0].msgs
		for _, f := range firsts[1:] {
			if f.msgs == nil {
				lead = nil
				break
			}
			lead = lead[:commonPrefix(lead, f.msgs)]
		}
		sp.leading = append([]core.Hash(nil), lead...)
	}
	key := string(sp.tools)
	for _, h := range sp.system {
		key += "," + string(h)
	}
	key += "|"
	for _, h := range sp.leading {
		key += string(h) + ","
	}
	sp.id = "sp-" + core.HashString(key).Short()
	return sp
}

// epochAt is the number of shared-layer epochs committed before seq.
func (b *builder) epochAt(seq uint64) int {
	return sort.Search(len(b.epochSeqs), func(i int) bool { return b.epochSeqs[i] >= seq })
}

// segmentize splits an agent's steps into segments of append-only prompts.
//
// The manifest chain is the ground truth for where a segment ends: a main step
// continues its predecessor's segment exactly when its message list extends the
// predecessor's persistent messages (all of them, or all but the last when the
// predecessor carried an ephemeral hot tail) and its model, tools and system
// blocks are unchanged. Events only label the reason. Side calls (compactor,
// mailman, recon) are forks of the agent's current prompt and belong to whatever
// segment is current when they are requested.
func (b *builder) segmentize(a *agentInfo) {
	if len(a.steps) == 0 {
		return
	}
	segs := []rl.Segment{{Index: 0, From: 0, Epoch: b.epochAt(a.steps[0].q.seq), Reason: "start"}}
	var prev *stepInfo
	for i, st := range a.steps {
		if st.q.kind == rl.KindMain {
			if prev != nil && !b.continues(prev, st) {
				segs[len(segs)-1].To = i - 1
				segs = append(segs, rl.Segment{Index: len(segs), From: i, Epoch: b.epochAt(st.q.seq), Reason: b.reason(a, prev.q.seq, st.q.seq)})
			}
			prev = st
		}
		st.step.Segment = segs[len(segs)-1].Index
		st.step.Epoch = b.epochAt(st.q.seq)
	}
	segs[len(segs)-1].To = len(a.steps) - 1
	a.segs = segs
}

// continues reports whether cur's prompt is an append-only extension of prev's.
func (b *builder) continues(prev, cur *stepInfo) bool {
	if prev.msgs == nil || cur.msgs == nil || prev.q.model != cur.q.model {
		return false
	}
	if prev.q.man.Tools != cur.q.man.Tools || !equalHashes(prev.q.man.System, cur.q.man.System) {
		return false
	}
	persist := len(prev.msgs)
	if prev.q.hot != "" && persist > 0 {
		// The hot tail is appended to the last message of the request and replaced
		// on the next one, so that message changes by design.
		persist--
	}
	return len(cur.msgs) >= persist && commonPrefix(prev.msgs, cur.msgs) >= persist
}

// reason labels a rebase using the events between two requests of an agent.
func (b *builder) reason(a *agentInfo, from, to uint64) string {
	for _, c := range b.v.commits {
		if c.agent == a.id && c.seq > from && c.seq < to {
			return "compact"
		}
	}
	for _, l := range b.v.layers {
		if l.seq <= from || l.seq >= to {
			continue
		}
		switch l.scope {
		case "agent":
			if l.agent == a.id {
				return "compact"
			}
		case "shared-epoch":
			return "epoch"
		case "shared-sync":
			if l.agent == a.id {
				return "epoch"
			}
		}
	}
	return "strip"
}

// strictPrefix reports whether next's prompt starts with prev's prompt followed
// by prev's completion, token for token.
func strictPrefix(prev, next *core.TokenTrace) bool {
	n := len(prev.PromptIDs) + len(prev.CompletionIDs)
	if len(prev.PromptIDs) == 0 || len(next.PromptIDs) < n {
		return false
	}
	for i, id := range prev.PromptIDs {
		if next.PromptIDs[i] != id {
			return false
		}
	}
	for i, id := range prev.CompletionIDs {
		if next.PromptIDs[len(prev.PromptIDs)+i] != id {
			return false
		}
	}
	return true
}

// checkTokenChains verifies the prefix property along each segment's main steps.
// A violation never fabricates or repairs anything: it is counted, and in strict
// mode the segment's traces are dropped.
func (b *builder) checkTokenChains(a *agentInfo) {
	var prev *stepInfo
	broken := map[int]bool{}
	for _, st := range a.main {
		if prev != nil && prev.step.Segment == st.step.Segment && prev.tok != nil && st.tok != nil && !strictPrefix(prev.tok, st.tok) {
			b.prefixBreaks++
			broken[st.step.Segment] = true
		}
		prev = st
	}
	if !b.o.StrictTokens || len(broken) == 0 {
		return
	}
	for _, st := range a.main {
		if broken[st.step.Segment] && st.tok != nil {
			st.tok, st.step.Tokens = nil, nil
		}
	}
	b.tokenMismatch = true
}

// agentOut assembles the rl.Agent for a.
func (b *builder) agentOut(a *agentInfo) rl.Agent {
	out := rl.Agent{ID: a.id, Role: a.role, Parent: a.parent, Model: a.model, Segments: a.segs, Status: b.status(a), Steps: make([]rl.Step, 0, len(a.steps))}
	for _, st := range a.steps {
		out.Steps = append(out.Steps, st.step)
	}
	return out
}

// status maps how an agent ended onto the rl vocabulary.
func (b *builder) status(a *agentInfo) string {
	if a.ended {
		switch a.state {
		case "idle", "done", "finished", "ok":
			return "done"
		case "failed", "error":
			return "error"
		case "blocked":
			return "blocked"
		case "budget":
			return "budget"
		case "killed", "cancelled", "canceled", "stopped":
			return "killed"
		}
	}
	if cleanFinish(a) {
		return "done"
	}
	if n := len(a.reqs); n > 0 {
		if _, ok := b.run.resps[a.reqs[n-1].id]; !ok && a.reqs[n-1].kind == rl.KindMain {
			return "killed"
		}
	}
	return ""
}

// cleanFinish reports whether the agent's last main step is a final answer: no
// tool calls and a normal stop.
func cleanFinish(a *agentInfo) bool {
	if len(a.main) == 0 {
		return false
	}
	last := a.main[len(a.main)-1]
	return len(last.turn.ToolCalls()) == 0 && (last.p.stop == core.StopEnd || last.p.stop == "")
}

// whyNoSteps explains an empty result with the first few reasons found.
func (b *builder) whyNoSteps() string {
	if ms := b.run.Verify(); len(ms) > 0 {
		return mismatchSummary(ms, 3)
	}
	return "no request has a response"
}
