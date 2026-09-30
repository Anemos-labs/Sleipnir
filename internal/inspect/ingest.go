package inspect

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/events"
)

const (
	maxAgents    = 5000
	maxOpenCalls = 4000
	maxAnomalies = 5000
	maxComps     = 20000
	maxOutcomes  = 200
)

// apply folds one event into the model. The caller holds s.mu for writing.
// Unknown event types are counted and otherwise ignored, so newer producers
// never break an older inspector.
func (s *Session) apply(ev *events.Event, off int64) {
	s.events++
	s.rev++
	if ev.Seq > s.lastSeq {
		s.lastSeq = ev.Seq
	}
	if n := len(s.index); n == 0 || (s.events%indexEvery == 1 && ev.Seq > s.index[n-1].seq) {
		s.index = append(s.index, indexEntry{seq: ev.Seq, off: off})
	}
	if len(s.types) < 256 || s.types[ev.Type] > 0 {
		s.types[ev.Type]++
	}
	ts := ev.TS
	if ts.IsZero() {
		ts = s.meta.last
	}
	if s.meta.first.IsZero() {
		s.meta.first = ts
	}
	if ts.After(s.meta.last) {
		s.meta.last = ts
	}
	if ev.V > 0 && s.meta.schema == 0 {
		s.meta.schema = ev.V
	}
	if s.meta.id == "" && ev.Session != "" {
		s.meta.id = ev.Session
	}

	switch ev.Type {
	case events.TypeSessionStart:
		s.onSessionStart(ev.Data)
	case events.TypeSessionEnd:
		var p struct {
			CostUSD float64 `json:"cost_usd"`
		}
		_ = json.Unmarshal(ev.Data, &p)
		s.meta.ended, s.meta.endTS, s.meta.endCost = true, ts, p.CostUSD
	case events.TypeAgentSpawn:
		s.onSpawn(ev.Data, ts)
	case events.TypeAgentEnd:
		s.onAgentEnd(ev.Data, ts)
	case events.TypeAgentState:
		s.onAgentState(ev, ts)
	case events.TypeModelRequest:
		s.onRequest(ev, ts)
	case events.TypeModelResponse:
		s.onResponse(ev, ts)
	case events.TypeModelError:
		s.onModelError(ev, ts)
	case events.TypeToolCall:
		s.onToolCall(ev, ts)
	case events.TypeToolResult:
		s.onToolResult(ev, ts)
	case events.TypeTurnAppend:
		s.tot.Turns++
		if a := s.agentFor(ev.Agent, ts); a != nil {
			a.turns++
		}
	case events.TypeUserInput:
		if s.meta.goal == "" {
			var p struct{ Text, Origin string }
			_ = json.Unmarshal(ev.Data, &p)
			if p.Origin == "" || p.Origin == "user" { // a task the harness handed out is not the goal
				s.meta.goal = oneLine(p.Text, 300)
			}
		}
		s.agentFor(ev.Agent, ts)
	case events.TypeLayerCommit:
		s.onLayerCommit(ev, ts)
	case events.TypeCompactPlan:
		s.onCompactPlan(ev, ts)
	case events.TypeCompactPatch:
		s.onCompactPatch(ev, ts)
	case events.TypeCompactCommit:
		s.onCompactCommit(ev, ts)
	case events.TypeCompactReject:
		s.onCompactReject(ev, ts)
	case events.TypeCacheAnomaly:
		s.onAnomaly(ev, ts)
	case events.TypeBoardOp:
		s.onBoardOp(ev, ts)
	case events.TypeMailSend:
		s.onMailSend(ev.Data, ev.Agent, ts)
	case events.TypeMailDeliver:
		s.onMailDeliver(ev.Data, ts)
	case events.TypeLease:
		s.onLease(ev.Data, ev.Agent, ts)
	case events.TypeGovernor:
		s.onGovernor(ev.Data)
	case events.TypeOutcome:
		s.onOutcome(ev, ts)
	}
}

// agentFor returns the agent for an id, creating it on first sight. The empty
// id (kernel events) and "swarm" (the runtime's own events) are not agents.
func (s *Session) agentFor(id string, ts time.Time) *agent {
	if id == "" || id == "swarm" {
		return nil
	}
	a := s.agents[id]
	if a == nil {
		if len(s.agents) >= maxAgents {
			return nil
		}
		a = &agent{id: id, spawned: ts, tools: map[string]*toolAgg{}, open: map[string]*openCall{}}
		s.agents[id] = a
		s.order = append(s.order, id)
	}
	if ts.After(a.last) {
		a.last = ts
	}
	return a
}

func (s *Session) noteModel(m string) {
	if m == "" {
		return
	}
	for _, x := range s.meta.models {
		if x == m {
			return
		}
	}
	if len(s.meta.models) < 24 {
		s.meta.models = append(s.meta.models, m)
	}
	if s.meta.model == "" {
		s.meta.model = m
	}
}

func (s *Session) onSessionStart(raw json.RawMessage) {
	var p struct {
		Version, Model, Provider, Dialect, Root, Renderer string
		Swarm                                             bool
		ReconTokens                                       int    `json:"recon_tokens"`
		SharedHash                                        string `json:"shared_hash"`
	}
	if json.Unmarshal(raw, &p) != nil {
		s.badPay++
		return
	}
	m := &s.meta
	m.version, m.provider, m.dialect, m.renderer = p.Version, p.Provider, p.Dialect, p.Renderer
	m.root, m.swarm, m.reconTokens, m.sharedHash = p.Root, p.Swarm, p.ReconTokens, p.SharedHash
	if p.Model != "" {
		m.model = p.Model
		s.noteModel(p.Model)
	}
}

func (s *Session) onSpawn(raw json.RawMessage, ts time.Time) {
	var p struct{ ID, Role, Model, Task, By, Parent string }
	if json.Unmarshal(raw, &p) != nil || p.ID == "" {
		s.badPay++
		return
	}
	a := s.agentFor(p.ID, ts)
	if a == nil {
		return
	}
	a.hasSpawn, a.spawned = true, ts
	a.role, a.model, a.task = p.Role, p.Model, p.Task
	a.parent = firstNonEmpty(p.Parent, p.By)
	s.noteModel(p.Model)
	w := &s.swarm
	w.nSpawns++
	w.minute(ts).spawns++
	w.spawns = append(w.spawns, SpawnView{T: ts, ID: p.ID, Role: p.Role, Parent: a.parent, Task: p.Task, Model: p.Model})
	if len(w.spawns) > maxSpawns {
		w.spawns = w.spawns[len(w.spawns)-maxSpawns:]
	}
	s.board.onSpawn(p.ID, p.Role, p.Task, a.parent, ts)
	if p.Role != "" && p.Role != "manager" {
		s.meta.swarm = true
	}
}

func (s *Session) onAgentEnd(raw json.RawMessage, ts time.Time) {
	var p struct{ ID, State, Evidence string }
	if json.Unmarshal(raw, &p) != nil || p.ID == "" {
		s.badPay++
		return
	}
	a := s.agentFor(p.ID, ts)
	if a == nil {
		return
	}
	a.ended, a.endState, a.evidence, a.line = true, p.State, oneLine(p.Evidence, 240), ""
	s.board.onAgentEnd(p.ID, p.State, ts)
}

func (s *Session) onAgentState(ev *events.Event, ts time.Time) {
	var p struct{ ID, State, Line string }
	_ = json.Unmarshal(ev.Data, &p)
	a := s.agentFor(firstNonEmpty(p.ID, ev.Agent), ts)
	if a == nil {
		return
	}
	if p.Line != "" {
		a.line = oneLine(p.Line, 160)
	}
	switch p.State {
	case "idle", "failed", "done":
		a.ended, a.endState = true, p.State
	case "running", "waiting":
		a.ended, a.endState = false, ""
	}
}

func (s *Session) onLayerCommit(ev *events.Event, ts time.Time) {
	var p struct{ Scope, Reason string }
	_ = json.Unmarshal(ev.Data, &p)
	switch p.Scope {
	case "shared-sync":
		if a := s.agentFor(ev.Agent, ts); a != nil {
			a.rebasePending = "sync"
			s.comp.rebases++
		}
	case "shared-epoch":
		s.comp.epochs++
		for _, a := range s.agents {
			if a.rebasePending == "" {
				a.rebasePending = "epoch"
			}
		}
	}
}

// ---- requests -------------------------------------------------------------------

func (s *Session) onRequest(ev *events.Event, ts time.Time) {
	var p struct {
		Req      string `json:"req"`
		Agent    string `json:"agent"`
		Role     string `json:"role"`
		Kind     string `json:"kind"`
		Model    string `json:"model"`
		Provider string `json:"provider"`
		Dialect  string `json:"dialect"`
		Sections []struct {
			Name   string `json:"name"`
			Hash   string `json:"hash"`
			Tokens int    `json:"tokens"`
			BP     bool   `json:"bp"`
		} `json:"sections"`
		ThreadFrom   int64             `json:"thread_from"`
		ThreadTo     int64             `json:"thread_to"`
		Hot          string            `json:"hot"`
		CacheKey     string            `json:"cache_key"`
		PrefixKey    string            `json:"prefix_key"`
		Breakpoints  []json.RawMessage `json:"breakpoints"`
		SharedBlocks int               `json:"shared_blocks"`
		SharedTokens int               `json:"shared_tokens"`
		Renderer     string            `json:"renderer"`
		WireHash     string            `json:"wire_hash"`
		Manifest     struct {
			Tools  string   `json:"tools"`
			System []string `json:"system"`
			Wire   string   `json:"wire"`
		} `json:"manifest"`
	}
	if json.Unmarshal(ev.Data, &p) != nil || p.Req == "" {
		s.badPay++
		return
	}
	a := s.agentFor(firstNonEmpty(p.Agent, ev.Agent), ts)
	if a == nil {
		return
	}
	kind := p.Kind
	if kind == "" {
		kind = "main"
	}
	r := &req{
		id: p.Req, seq: ev.Seq, rev: s.rev, agent: a, role: p.Role, kind: kind, model: p.Model,
		provider: p.Provider, t: ts, threadFrom: p.ThreadFrom, threadTo: p.ThreadTo, hotHash: p.Hot,
		cacheKey: p.CacheKey, prefixKey: p.PrefixKey, breakpoints: len(p.Breakpoints),
		sharedBlocks: p.SharedBlocks, sharedTokens: p.SharedTokens, renderer: p.Renderer,
		wire: firstNonEmpty(p.WireHash, p.Manifest.Wire), toolsHash: p.Manifest.Tools, systemHashes: p.Manifest.System,
		g0Tok: -1, hotTok: -1, first: -1,
	}
	for _, sec := range p.Sections {
		r.sections = append(r.sections, secRec{name: sec.Name, hash: sec.Hash, tokens: sec.Tokens, bp: sec.BP})
	}
	r.g0Sig = p.Manifest.Tools + "|" + strings.Join(p.Manifest.System, ",")
	s.noteModel(p.Model)
	if s.meta.provider == "" {
		s.meta.provider = p.Provider
	}
	if s.meta.dialect == "" {
		s.meta.dialect = p.Dialect
	}
	if s.meta.renderer == "" {
		s.meta.renderer = p.Renderer
	}
	s.rl.kinds[kind]++
	if r.wire != "" {
		s.rl.wireHashes++
	}
	if p.Renderer != "" {
		s.rl.noteRenderer(p.Renderer)
	}
	if kind == "main" {
		if p.Role != "" && a.role == "" {
			a.role = p.Role
		}
	}
	if a.model == "" {
		a.model = p.Model
	}
	a.ended, a.endState = false, ""

	// A request that reuses the id of one still open supersedes it (a resumed
	// session restarts its counters).
	if old := s.byID[r.id]; old != nil && !old.done && !old.failed {
		s.closeRequest(old, "superseded")
	}
	s.byID[r.id] = r
	s.reqs = append(s.reqs, r)
	a.reqs = append(a.reqs, r)
	s.tot.Requests++
	s.tot.Pending++
	a.openReq++
	s.swarm.requests++
	s.swarm.minute(ts).requests++
	s.swarm.inflight++
	if s.swarm.inflight > s.swarm.peakInflight {
		s.swarm.peakInflight = s.swarm.inflight
	}

	if kind == "main" {
		s.tot.Main++
		a.nMain++
		r.isFirst = a.mainSeen == 0
		a.mainSeen++
		s.resolveG0Hot(r)
		s.compareLayers(r, a.prevMain)
		r.rebase, a.rebasePending = a.rebasePending, ""
		r.epoch = a.commits
		// The guard reports drift just before the request it concerns.
		guardFlagged := a.pendingDrift != nil
		if an := a.pendingDrift; an != nil {
			an.Req = r.id
			a.pendingDrift = nil
			r.anomaly, r.anomKind = true, "drift"
		}
		if r.hadPrev {
			s.checked++
			if r.first >= 0 && r.rebase == "" {
				r.undeclared = true
				if !guardFlagged {
					s.anomTot.Undeclared++
				}
			}
		}
		r.foldedAtReq = a.foldedCum
		r.afterCommit, a.lastCommit = a.lastCommit, nil
		a.prevMain = r
	} else {
		s.tot.Side++
		a.nSide++
		if ep := a.ep; ep != nil {
			ep.Calls++
			if len(ep.reqIDs) < 16 {
				ep.reqIDs = append(ep.reqIDs, r.id)
			}
		}
	}
	s.evictLocked()
}

// closeRequest ends a request without a response.
func (s *Session) closeRequest(r *req, why string) {
	if r.done || r.failed {
		return
	}
	r.failed, r.err = true, why
	s.tot.Pending--
	s.tot.Failed++
	r.agent.failedReqs++
	if r.agent.openReq > 0 {
		r.agent.openReq--
	}
	if s.swarm.inflight > 0 {
		s.swarm.inflight--
	}
	r.rev = s.rev
}

// resolveG0Hot sizes the layers the log does not record (G0, G6) from their
// blobs, using the bytes-per-token ratio revealed by a recorded layer. Without
// blobs they stay unknown and the UI says so.
func (s *Session) resolveG0Hot(r *req) {
	if s.blobs == nil {
		return
	}
	for _, sec := range r.sections {
		if sec.tokens >= 64 {
			if n, ok := s.blobSizeLocked(sec.hash); ok && n > 0 {
				r.ratio = float64(n) / float64(sec.tokens)
				break
			}
		}
	}
	ratio := r.ratio
	if ratio <= 0 {
		ratio = 3.6 // BytesEstimator's starting point
	}
	toks := func(bytes int64) int { return int((float64(bytes) + ratio - 1) / ratio) }
	if r.toolsHash != "" || len(r.systemHashes) > 0 {
		var total int64
		ok := true
		if r.toolsHash != "" {
			n, found := s.blobSizeLocked(r.toolsHash)
			total, ok = total+n, ok && found
		}
		for _, h := range r.systemHashes {
			n, found := s.blobSizeLocked(h)
			total, ok = total+n, ok && found
		}
		if ok {
			r.g0Tok = toks(total)
		}
	}
	if r.hotHash == "" {
		r.hotTok = 0
	} else if n, ok := s.blobSizeLocked(r.hotHash); ok {
		r.hotTok = toks(n)
	}
}

// compareLayers records which layers' own bytes differ from the agent's
// previous main request, and where the first difference is: a change at layer k
// invalidates everything after it in a prefix cache.
func (s *Session) compareLayers(r *req, prev *req) {
	r.first = -1
	if prev == nil {
		return
	}
	r.hadPrev = true
	var ch uint8
	if r.g0Sig != prev.g0Sig {
		ch |= 1 << 0
	}
	for name, idx := range sectionLayer {
		if r.sectionHash(name) != prev.sectionHash(name) {
			ch |= 1 << idx
		}
	}
	// The thread is append-only between rebases: a different first turn, or a
	// last turn that went backwards, means turns were removed or rewritten.
	if r.threadFrom != prev.threadFrom || r.threadTo < prev.threadTo {
		ch |= 1 << 5
	}
	r.changed = ch
	for i := 0; i < 6; i++ {
		if ch&(1<<i) != 0 {
			r.first = i
			r.anyChange = true
			break
		}
	}
}

func (r *req) sectionHash(name string) string {
	for _, sec := range r.sections {
		if sec.name == name {
			return sec.hash
		}
	}
	return ""
}

func (s *Session) onResponse(ev *events.Event, ts time.Time) {
	var p struct {
		Req          string     `json:"req"`
		Model        string     `json:"model"`
		Provider     string     `json:"provider"`
		Usage        core.Usage `json:"usage"`
		CostUSD      *float64   `json:"cost_usd"`
		GatewayCost  bool       `json:"gateway_cost"`
		ExpectedRead int        `json:"expected_read"`
		Anomaly      bool       `json:"anomaly"`
		Stop         string     `json:"stop"`
		TTFBMs       int64      `json:"ttfb_ms"`
		TotalMs      int64      `json:"total_ms"`
		Side         bool       `json:"side"`
		Tokens       string     `json:"tokens"`
	}
	if json.Unmarshal(ev.Data, &p) != nil || p.Req == "" {
		s.badPay++
		return
	}
	r := s.byID[p.Req]
	if r == nil || r.done || r.failed && r.err == "superseded" {
		// A response whose request we never saw (a log that starts mid-session) or
		// a duplicate id: record it as a request of its own.
		a := s.agentFor(ev.Agent, ts)
		if a == nil {
			return
		}
		kind := "main"
		if p.Side {
			kind = "compactor"
		}
		r = &req{id: p.Req, seq: ev.Seq, rev: s.rev, agent: a, kind: kind, model: p.Model, provider: p.Provider,
			t: ts.Add(-time.Duration(p.TotalMs) * time.Millisecond), g0Tok: -1, hotTok: -1, first: -1}
		if kind == "main" {
			r.isFirst = a.mainSeen == 0
			a.mainSeen++
			s.tot.Main++
			a.nMain++
			r.epoch = a.commits
			r.foldedAtReq = a.foldedCum
		} else {
			s.tot.Side++
			a.nSide++
		}
		s.tot.Requests++
		s.tot.Pending++
		a.openReq++
		s.swarm.inflight++ // balanced by the decrement below: the request is answered in the same breath
		s.byID[r.id] = r
		s.reqs = append(s.reqs, r)
		a.reqs = append(a.reqs, r)
		s.evictLocked()
	}
	a := r.agent
	if r.failed { // a response after a model.error: the retry succeeded
		r.failed, r.err = false, ""
		s.tot.Failed--
		s.tot.Pending++
		a.openReq++
		s.swarm.inflight++
	}
	if ts.After(a.last) {
		a.last = ts
	}
	model := firstNonEmpty(p.Model, r.model, a.model, s.meta.model)
	if r.model == "" {
		r.model = model
	}
	s.noteModel(model)
	pi := s.priceFor(model)
	pi.requests++

	u := p.Usage
	r.usage, r.done, r.side = u, true, p.Side || r.kind != "main"
	r.prompt = u.TotalInput()
	r.hit = u.HitRatio()
	r.expected = p.ExpectedRead
	if p.Anomaly {
		r.anomaly = true
		if r.anomKind == "" {
			r.anomKind = "low_hit"
		}
	}
	r.ttfb, r.total, r.stop = p.TTFBMs, p.TotalMs, p.Stop
	r.rev = s.rev
	r.priced = pi.bill(u).Total
	r.cost = r.priced
	if p.CostUSD != nil {
		r.cost, r.reported, r.gateway = *p.CostUSD, true, p.GatewayCost
		s.costAgg.reported += r.cost
		s.costAgg.reportedN++
		if p.GatewayCost {
			s.costAgg.gatewayN++
		}
	}
	if p.Tokens != "" {
		s.rl.tokenTraces++
	}
	s.tot.Pending--
	if a.openReq > 0 {
		a.openReq--
	}
	if s.swarm.inflight > 0 {
		s.swarm.inflight--
	}
	a.nDone++

	read, write := u.CacheReadTokens, u.CacheWriteTokens()
	s.tot.Input += int64(u.InputTokens)
	s.tot.CacheRead += int64(read)
	s.tot.CacheWrite += int64(write)
	s.tot.Output += int64(u.OutputTokens)
	s.tot.Prompt += int64(r.prompt)
	a.usage = a.usage.Add(u)
	a.prompt += int64(r.prompt)
	a.usd += r.cost
	if read > 0 {
		s.cache.anyRead = true
	}

	actual := pi.bill(u)
	nc := pi.noCache(u)
	s.costAgg.actual.add(actual)
	s.costAgg.noCache.add(nc)
	a.noCache += nc.Total
	r.noCache = nc.Total

	c := &s.cache
	if r.kind != "main" {
		c.sideRead += int64(read)
		c.sidePrompt += int64(r.prompt)
		s.costAgg.compUSD += r.cost
		if ep := a.ep; ep != nil {
			ep.CompactorUSD += r.cost
		}
		return
	}

	c.mainRead += int64(read)
	c.mainPrompt += int64(r.prompt)
	a.hitRead += int64(read)
	a.hitAll += int64(r.prompt)
	if r.isFirst {
		c.first++
		if read > 0 {
			c.warmFirst++
		}
	}
	if read == 0 && r.prompt > 0 {
		c.cold++
		r.cold = true
	}
	switch {
	case r.rebase != "":
		c.rebaseRead += int64(read)
		c.rebasePrompt += int64(r.prompt)
		c.rebaseN++
	case !r.isFirst:
		c.steadyRead += int64(read)
		c.steadyPrompt += int64(r.prompt)
		c.steadyN++
	}
	if r.expected > 0 {
		c.expected += int64(r.expected)
		c.actual += int64(read)
	}
	a.ctxLast = r.prompt
	if r.prompt > a.ctxMax {
		a.ctxMax = r.prompt
	}
	a.ctxSum += int64(r.prompt)
	a.ctxN++
	c.ctxSum += int64(r.prompt)
	c.ctxN++
	if r.prompt > c.ctxMax {
		c.ctxMax = r.prompt
	}

	nctx, nb := s.naiveBill(a, r, pi)
	r.naiveCtx, r.naive = int(nctx), nb.Total
	s.costAgg.naive.add(nb)
	a.naive += nb.Total
	a.naiveCtxSum += nctx
	c.naiveCtxSum += nctx
	c.lastAnyT = r.t
	if r.g0Tok > 0 {
		c.lastAnyG0 = r.g0Tok
	}

	if cm := r.afterCommit; cm != nil && cm.Next == nil {
		cm.Next = &CompNext{Req: r.id, Hit: r.hit, Rewrite: u.InputTokens + write, Prompt: r.prompt}
	}
}

func (s *Session) onModelError(ev *events.Event, ts time.Time) {
	var p struct {
		Req     string `json:"req"`
		Kind    string `json:"kind"`
		Error   string `json:"error"`
		Attempt int    `json:"attempt"`
		DelayMs int64  `json:"delay_ms"`
	}
	if json.Unmarshal(ev.Data, &p) != nil {
		s.badPay++
		return
	}
	w := &s.swarm
	if p.Attempt > 0 || p.DelayMs > 0 { // a retry notice: the request goes on
		s.tot.Retries++
		w.retries++
		if p.Kind != "" && (len(w.errKinds) < 16 || w.errKinds[p.Kind] > 0) {
			w.errKinds[p.Kind]++
		}
		if p.Kind == "rate_limit" {
			s.tot.RateLimited++
			w.rateLimited++
		}
		return
	}
	if r := s.byID[p.Req]; r != nil && !r.done {
		if p.Error != "" && !r.failed {
			s.closeRequest(r, oneLine(p.Error, 300))
		}
	}
	if a := s.agentFor(ev.Agent, ts); a != nil {
		a.last = ts
	}
}

// evictLocked keeps the per-request window bounded. Totals are already exact;
// what goes is row-level detail for the oldest requests.
func (s *Session) evictLocked() {
	max := s.opts.MaxRequests
	if len(s.reqs) <= max+max/4 {
		return
	}
	drop := len(s.reqs) - max
	for _, r := range s.reqs[:drop] {
		if s.byID[r.id] == r {
			delete(s.byID, r.id)
		}
		a := r.agent
		if len(a.reqs) > 0 && a.reqs[0] == r {
			a.reqs = a.reqs[1:]
		}
		if !r.done && !r.failed {
			// Never forget an in-flight request's accounting: it stays in byID.
			s.byID[r.id] = r
		}
	}
	s.dropped += drop
	keep := make([]*req, len(s.reqs)-drop)
	copy(keep, s.reqs[drop:])
	s.reqs = keep
}

// ---- tools -------------------------------------------------------------------------

func (s *Session) onToolCall(ev *events.Event, ts time.Time) {
	var p struct {
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	}
	if json.Unmarshal(ev.Data, &p) != nil || p.Name == "" {
		s.badPay++
		return
	}
	a := s.agentFor(ev.Agent, ts)
	if a == nil {
		return
	}
	a.toolCalls++
	s.tot.ToolCalls++
	agg := a.tool(p.Name)
	agg.calls++
	a.lastTool, a.lastToolAt = p.Name, ts
	a.line = activity(p.Name, p.Input)
	if p.Name == "wait" {
		a.waiting = true
	}
	bc := parseBoardCall(p.Name, p.Input)
	if p.ID != "" {
		if len(a.open) >= maxOpenCalls {
			for k := range a.open {
				delete(a.open, k)
				break
			}
		}
		a.open[p.ID] = &openCall{name: p.Name, t: ts, board: bc}
	}
	s.board.onToolCall(a.id, p.ID, bc)
}

func (a *agent) tool(name string) *toolAgg {
	t := a.tools[name]
	if t == nil {
		if len(a.tools) >= 200 {
			name = "(other)"
			if t = a.tools[name]; t != nil {
				return t
			}
		}
		t = &toolAgg{}
		a.tools[name] = t
	}
	return t
}

func (s *Session) onToolResult(ev *events.Event, ts time.Time) {
	var p struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Error     bool   `json:"error"`
		Chars     int64  `json:"chars"`
		Truncated bool   `json:"truncated"`
		Ms        int64  `json:"ms"`
	}
	if json.Unmarshal(ev.Data, &p) != nil {
		s.badPay++
		return
	}
	a := s.agentFor(ev.Agent, ts)
	if a == nil {
		return
	}
	oc := a.open[p.ID]
	name := firstNonEmpty(p.Name, "")
	if oc != nil {
		delete(a.open, p.ID)
		name = firstNonEmpty(name, oc.name)
	} else {
		// A result whose call we never saw still counts as a call.
		a.toolCalls++
		s.tot.ToolCalls++
		a.tool(name).calls++
	}
	if name == "" {
		name = "(unknown)"
	}
	agg := a.tool(name)
	agg.totalMs += p.Ms
	if p.Ms > agg.maxMs {
		agg.maxMs = p.Ms
	}
	agg.chars += p.Chars
	agg.done++
	if p.Error {
		agg.errors++
		a.toolErrs++
		s.tot.ToolErrors++
	}
	if p.Truncated {
		agg.truncated++
	}
	if name == "wait" {
		a.waiting = false
	}
	if len(a.open) == 0 && a.line != "" && a.openReq == 0 {
		a.line = ""
	}
	s.board.onToolResult(a.id, p.ID)
}

// activity turns a tool call into the short status line the swarm board shows.
func activity(name string, input json.RawMessage) string {
	var in struct {
		Path, FilePath, Command string
		File                    string `json:"file"`
		Action                  string
	}
	if len(input) > 0 && len(input) < 1<<16 {
		_ = json.Unmarshal(input, &in)
	}
	path := firstNonEmpty(in.Path, in.FilePath, in.File)
	if i := strings.LastIndexAny(path, `/\`); i >= 0 {
		path = path[i+1:]
	}
	switch name {
	case "read":
		return "reading " + firstNonEmpty(path, "a file")
	case "edit", "write":
		return "editing " + firstNonEmpty(path, "a file")
	case "apply_patch":
		return "applying a patch"
	case "bash":
		return "running `" + oneLine(in.Command, 40) + "`"
	case "grep", "glob":
		return "searching"
	case "recall":
		return "recalling earlier context"
	case "wait":
		return "waiting for the team"
	case "web_fetch", "web_search":
		return "browsing"
	case "task":
		if in.Action != "" {
			return "task " + oneLine(in.Action, 20)
		}
	}
	return oneLine(name, 40)
}

// ---- anomalies -------------------------------------------------------------------

func (s *Session) onAnomaly(ev *events.Event, ts time.Time) {
	var p struct {
		Kind         string `json:"kind"`
		Diverged     string `json:"diverged"`
		SharedBlocks int    `json:"shared_blocks"`
		Req          string `json:"req"`
		ExpectedRead int    `json:"expected_read"`
		ActualRead   int    `json:"actual_read"`
	}
	if json.Unmarshal(ev.Data, &p) != nil {
		s.badPay++
		return
	}
	a := s.agentFor(ev.Agent, ts)
	if a == nil {
		return
	}
	an := &anomaly{agent: a}
	an.Seq, an.T, an.Agent, an.Kind = ev.Seq, ts, a.id, p.Kind
	an.Diverged, an.SharedBlocks, an.Req = p.Diverged, p.SharedBlocks, p.Req
	an.Expected, an.Actual = p.ExpectedRead, p.ActualRead
	an.Severity = "warning"
	s.anomTot.Total++
	a.anomalies++
	switch p.Kind {
	case "drift":
		an.Severity = "error"
		s.anomTot.Drift++
		a.pendingDrift = an
	case "low_hit":
		s.anomTot.LowHit++
		if r := s.byID[p.Req]; r != nil {
			r.anomaly, r.anomKind = true, "low_hit"
		}
	}
	an.N = s.anomTot.Total
	s.anoms = append(s.anoms, an)
	if len(s.anoms) > maxAnomalies {
		s.anoms = s.anoms[len(s.anoms)-maxAnomalies:]
	}
}

// ---- swarm ----------------------------------------------------------------------

func (s *Session) onBoardOp(ev *events.Event, ts time.Time) {
	var p struct{ Op string }
	if json.Unmarshal(ev.Data, &p) != nil {
		s.badPay++
		return
	}
	w := &s.swarm
	w.boardOps++
	w.minute(ts).board++
	op := p.Op
	if op == "" {
		op = "(unknown)"
	}
	if len(w.boardBy) < 32 || w.boardBy[op] > 0 {
		w.boardBy[op]++
	}
	if op == "alert" {
		w.alerts++
	}
	s.board.onBoardOp(ev.Agent, op, ev.Data, ts)
}

// ---- RL --------------------------------------------------------------------------

type rlState struct {
	outcomes    []Outcome
	kinds       map[string]int
	wireHashes  int
	tokenTraces int
	renderers   []string
}

func (r *rlState) noteRenderer(v string) {
	for _, x := range r.renderers {
		if x == v {
			return
		}
	}
	if len(r.renderers) < 8 {
		r.renderers = append(r.renderers, v)
	}
}

func (s *Session) onOutcome(ev *events.Event, ts time.Time) {
	var p struct {
		Kind    string  `json:"kind"`
		Score   float64 `json:"score"`
		Pass    bool    `json:"pass"`
		Version string  `json:"verifier_version"`
	}
	if json.Unmarshal(ev.Data, &p) != nil {
		s.badPay++
		return
	}
	s.rl.outcomes = append(s.rl.outcomes, Outcome{Seq: ev.Seq, T: ts, Agent: ev.Agent, Kind: oneLine(p.Kind, 40), Score: p.Score, Pass: p.Pass, Version: oneLine(p.Version, 40)})
	if len(s.rl.outcomes) > maxOutcomes {
		s.rl.outcomes = s.rl.outcomes[len(s.rl.outcomes)-maxOutcomes:]
	}
}
