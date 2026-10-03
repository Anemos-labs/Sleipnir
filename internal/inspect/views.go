package inspect

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// This file turns the model into the JSON views the API serves. Everything here
// runs under s.mu (read) and never mutates the model.

// stateLocked classifies the session as empty, ended, live, or idle from event state and recent
// timestamps; the caller must hold the session lock.
func (s *Session) stateLocked(now time.Time) string {
	switch {
	case s.events == 0:
		return StateEmpty
	case s.meta.ended:
		return StateEnded
	}
	recent := func(t time.Time) bool { return !t.IsZero() && now.Sub(t) < s.opts.LiveWindow }
	if recent(s.mtime) || recent(s.meta.last) {
		return StateLive
	}
	return StateIdle
}

func (s *Session) metaLocked(now time.Time) SessionMeta {
	m := &s.meta
	end := m.last
	if m.ended && !m.endTS.IsZero() {
		end = m.endTS
	}
	root := ""
	if m.root != "" {
		root = filepath.Base(m.root)
	}
	dur := int64(0)
	if !m.first.IsZero() && end.After(m.first) {
		dur = end.Sub(m.first).Milliseconds()
	}
	return SessionMeta{
		ID: s.idLocked(), Name: s.nameLocked(), Model: m.model, Models: append([]string(nil), m.models...),
		Provider: m.provider, Dialect: m.dialect, Renderer: m.renderer, Version: m.version,
		Swarm: m.swarm || s.swarm.nSpawns > 1, Root: root, Goal: m.goal, SharedHash: m.sharedHash,
		ReconTokens: m.reconTokens, Start: m.first, End: end, Ended: m.ended, DurationMs: dur, EndCostUSD: m.endCost,
		EndReason: m.endReason, Isolation: m.isolation, Mailman: m.mailman,
	}
}

// Summary is everything the header and overview need. It is cheap: totals are
// maintained incrementally and only the downsampled series walks the window.
func (s *Session) Summary() Summary {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.summaryLocked()
}

// summaryLocked assembles inspection totals, recent series, cost, swarm, RL, and warning views
// under the session lock.
func (s *Session) summaryLocked() Summary {
	now := s.opts.Now()
	state := s.stateLocked(now)
	out := Summary{
		Session: s.metaLocked(now), State: state, Rev: s.rev, Log: s.logMetaLocked(),
		Totals: s.tot, Compaction: s.compTotalsLocked(), Anomalies: s.anomTot,
	}
	out.Totals.Agents = len(s.agents)
	out.Cache = s.cacheStatsLocked()
	out.Cost = s.costReportLocked()
	out.Swarm = s.swarmTotalsLocked()
	out.Series = s.seriesLocked(480)
	out.RL = s.rlSummaryLocked()
	out.Warnings = s.warningsLocked(state, out)
	return out
}

// compTotalsLocked copies compaction counters and recorded compactor cost; the caller must hold
// the session lock.
func (s *Session) compTotalsLocked() CompactionTotals {
	c := &s.comp
	return CompactionTotals{
		Commits: c.commits, Fork: c.fork, Mask: c.mask, Emergency: c.emergency, Fallbacks: c.fallbacks,
		Rejects: c.rejects, Held: c.held, Folded: c.folded, SpineAdded: c.spineAdded, Net: c.net,
		CompactorUSD: s.costAgg.compUSD, Rebases: c.rebases,
	}
}

func (s *Session) cacheStatsLocked() CacheStats {
	c := &s.cache
	all := c.mainPrompt + c.sidePrompt
	st := CacheStats{
		HitRatio:       ratio(c.mainRead+c.sideRead, all),
		MainHitRatio:   ratio(c.mainRead, c.mainPrompt),
		SideHitRatio:   ratio(c.sideRead, c.sidePrompt),
		SteadyHitRatio: ratio(c.steadyRead, c.steadyPrompt),
		RebaseHitRatio: ratio(c.rebaseRead, c.rebasePrompt),
		SteadyRequests: c.steadyN, RebaseRequests: c.rebaseN,
		ColdStarts: c.cold, FirstRequests: c.first, WarmFirst: c.warmFirst,
		MaxContext: c.ctxMax, ExpectedRead: c.expected, ActualRead: c.actual,
	}
	if c.ctxN > 0 {
		st.AvgContext = float64(c.ctxSum) / float64(c.ctxN)
		st.AvgContextNaive = float64(c.naiveCtxSum) / float64(c.ctxN)
		if st.AvgContextNaive > 0 {
			st.ContextReduction = 1 - st.AvgContext/st.AvgContextNaive
		}
	}
	st.NoReadReports = !c.anyRead && s.tot.Main >= 3 && c.expected > 0
	return st
}

// costReportLocked computes modeled savings and reported-cost divergence only when all successful
// requests have reported prices; the caller must hold the session lock.
func (s *Session) costReportLocked() CostReport {
	c := &s.costAgg
	r := CostReport{
		Reported: c.reported, ReportedRequests: c.reportedN, GatewayRequests: c.gatewayN,
		Actual: c.actual, NoCache: c.noCache, Naive: c.naive, CompactorUSD: c.compUSD,
		Prices: s.pricesUsedLocked(), Assumptions: s.assumptionsLocked(),
	}
	r.SavedNoCache = c.noCache.Total - c.actual.Total
	r.SavedNoCachePct = pct(r.SavedNoCache, c.noCache.Total)
	r.SavedNaive = c.naive.Total - c.actual.Total
	r.SavedNaivePct = pct(r.SavedNaive, c.naive.Total)
	if c.reportedN == s.tot.Requests-s.tot.Pending-s.tot.Failed && c.actual.Total > 0 {
		r.Divergence = (c.reported - c.actual.Total) / c.actual.Total
	}
	return r
}

// swarmTotalsLocked combines stored counters with current running/waiting agent counts; the caller
// must hold the session lock.
func (s *Session) swarmTotalsLocked() SwarmTotals {
	w := &s.swarm
	t := SwarmTotals{
		Agents: len(s.agents), Spawns: w.nSpawns, BoardOps: w.boardOps, MailSent: w.mailSent, MailDelivered: w.mailDeliv,
		LeaseEvents: w.nLease, Alerts: w.alerts, Tasks: len(s.board.tasks), PeakInFlight: w.peakInflight,
	}
	now := s.opts.Now()
	live := s.stateLocked(now)
	for _, a := range s.agents {
		if st := s.agentStateLocked(a, now, live); st == "running" || st == "waiting" {
			t.Running++
		}
	}
	return t
}

// seriesLocked downsamples the retained window into at most max groups of
// consecutive requests, so overview charts stay small however long the session.
func (s *Session) seriesLocked(max int) Series {
	var rows []*req
	for _, r := range s.reqs {
		if r.done {
			rows = append(rows, r)
		}
	}
	out := Series{Window: len(rows), Group: 1, Points: []SeriesPoint{}}
	if len(rows) == 0 {
		return out
	}
	g := (len(rows) + max - 1) / max
	out.Group = g
	start := s.meta.first
	for i := 0; i < len(rows); i += g {
		end := min(i+g, len(rows))
		pt := SeriesPoint{I: i, N: end - i, T: rows[end-1].t.Sub(start).Milliseconds()}
		for _, r := range rows[i:end] {
			pt.Prompt += int64(r.prompt)
			pt.In += int64(r.usage.InputTokens)
			pt.Read += int64(r.usage.CacheReadTokens)
			pt.Write += int64(r.usage.CacheWriteTokens())
			pt.Out += int64(r.usage.OutputTokens)
			pt.USD += r.cost
			pt.Priced += r.priced
			pt.NoCache += r.noCache
			pt.Naive += r.naive
			if r.anomaly {
				pt.Anom++
			}
			if r.rebase == "commit" {
				pt.Commits++
			}
		}
		pt.Hit = ratio(pt.Read, pt.Prompt)
		out.Points = append(out.Points, pt)
	}
	return out
}

func (s *Session) warningsLocked(state string, sum Summary) []string {
	var w []string
	add := func(f string, a ...any) { w = append(w, fmt.Sprintf(f, a...)) }
	if s.events > 0 && state == StateIdle && !s.meta.ended {
		add("The log has no session.end and has not been written to recently: the run was interrupted, or this is an old log.")
	}
	if s.torn > 0 && state != StateLive {
		add("The log ends with %d bytes of an incomplete line (the writer stopped mid-write); they were ignored.", s.torn)
	}
	if s.bad > 0 {
		add("%d lines were not valid events and were skipped.", s.bad)
	}
	if s.badPay > 0 {
		add("%d events had payloads the inspector could not read.", s.badPay)
	}
	if s.reloads > 0 {
		add("The log was truncated or replaced %d time(s); the model was rebuilt from the start.", s.reloads)
	}
	if s.blobs != nil && !s.blobs.available() {
		add("No blobs/ directory next to the log: layer sizes for G0 (constitution + tools) and the hot tail are unknown, the thread is shown as the remainder, and layer text is unavailable.")
	}
	for _, p := range sum.Cost.Prices {
		if p.Source == "fallback" {
			add("No price is known for model %q: conservative fallback prices are used, so dollar figures for it are estimates.", p.Model)
		}
	}
	if sum.Cache.NoReadReports {
		add("No request reported a cache read although the guard expected a cached prefix: caching may be off, or the endpoint may not report cache usage.")
	}
	if s.dropped > 0 {
		add("Per-request detail covers the newest %d of %d requests; totals cover the whole log.", len(s.reqs), s.tot.Requests)
	}
	if s.tot.Failed > 0 {
		add("%d request(s) failed without a response.", s.tot.Failed)
	}
	if d := sum.Cost.Divergence; d > 0.15 || d < -0.15 {
		add("Recorded costs differ from table prices by %+.0f%%: the gateway's prices differ from the table, so the comparisons use table prices.", d*100)
	}
	return w
}

// ---- agents -----------------------------------------------------------------------

// agentStateLocked prioritizes failure, open requests, waits, completion, and recent activity to
// derive display state under the session lock.
func (s *Session) agentStateLocked(a *agent, now time.Time, sessionState string) string {
	switch {
	case a.ended && a.endState == "failed":
		return "failed"
	case a.openReq > 0 || len(a.open) > 0:
		if a.waiting {
			return "waiting"
		}
		return "running"
	case a.waiting:
		return "waiting"
	case a.ended && a.endState == "idle":
		return "idle"
	case a.ended, s.meta.ended:
		return "done"
	case sessionState == StateLive && now.Sub(a.last) < 30*time.Second:
		return "running"
	}
	return "idle"
}

func (s *Session) agentViewLocked(a *agent, now time.Time, sessionState string) AgentView {
	main := 0
	v := AgentView{
		ID: a.id, Role: a.role, Model: a.model, Parent: a.parent, Task: a.task,
		State: s.agentStateLocked(a, now, sessionState), Line: a.line, Started: a.spawned, Last: a.last,
		Requests: a.nMain + a.nSide, Main: a.nMain, Side: a.nSide,
		Input: int64(a.usage.InputTokens), CacheRead: int64(a.usage.CacheReadTokens), CacheWrite: int64(a.usage.CacheWriteTokens()),
		Output: int64(a.usage.OutputTokens), HitRatio: ratio(int64(a.usage.CacheReadTokens), a.prompt),
		CostUSD: a.usd, Context: a.ctxLast, MaxContext: a.ctxMax, Compactions: a.commits, Folded: a.netFolded,
		Anomalies: a.anomalies, ToolCalls: a.toolCalls, ToolErrors: a.toolErrs, Turns: a.turns,
		Ended: a.ended, EndState: a.endState, Evidence: a.evidence,
	}
	if a.ctxN > 0 {
		v.AvgContext = float64(a.ctxSum) / float64(a.ctxN)
	}
	if v.State == "failed" || v.State == "done" {
		v.Line = ""
	}
	// Sparklines: up to 48 evenly spaced points of the newest main requests.
	var rows []*req
	for _, r := range a.reqs {
		if r.done && r.kind == "main" {
			rows = append(rows, r)
		}
	}
	if n := len(rows); n > 0 {
		step := (n + 47) / 48
		for i := n - 1; i >= 0 && len(v.SparkCtx) < 48; i -= step {
			v.SparkCtx = append([]int{rows[i].prompt}, v.SparkCtx...)
			v.SparkHit = append([]float64{rows[i].hit}, v.SparkHit...)
		}
	}
	_ = main
	return v
}

// Agents lists every agent in the order it first appeared.
func (s *Session) Agents() []AgentView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.agentsLocked()
}

// agentsLocked builds agent views in retained agent order using one current timestamp; the caller
// must hold the session lock.
func (s *Session) agentsLocked() []AgentView {
	now := s.opts.Now()
	st := s.stateLocked(now)
	out := make([]AgentView, 0, len(s.order))
	for _, id := range s.order {
		if a := s.agents[id]; a != nil {
			out = append(out, s.agentViewLocked(a, now, st))
		}
	}
	return out
}

// Agent returns one agent's detail. id is only ever used as a map key.
func (s *Session) Agent(id string) (AgentDetail, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a := s.agents[id]
	if a == nil {
		return AgentDetail{}, false
	}
	now := s.opts.Now()
	d := AgentDetail{Agent: s.agentViewLocked(a, now, s.stateLocked(now)), Rebases: a.commits}
	for name, t := range a.tools {
		ts := ToolStat{Name: name, Calls: max(t.calls, t.done), Errors: t.errors, Truncated: t.truncated, TotalMs: t.totalMs, MaxMs: t.maxMs, Chars: t.chars}
		if t.done > 0 {
			ts.AvgMs = float64(t.totalMs) / float64(t.done)
		}
		d.Tools = append(d.Tools, ts)
	}
	sort.Slice(d.Tools, func(i, j int) bool {
		if d.Tools[i].Calls != d.Tools[j].Calls {
			return d.Tools[i].Calls > d.Tools[j].Calls
		}
		return d.Tools[i].Name < d.Tools[j].Name
	})
	d.Compactions = []Compaction{}
	for _, c := range s.comp.list {
		if c.Agent == id {
			d.Compactions = append(d.Compactions, c.Compaction.clone())
		}
	}
	if len(d.Compactions) > 200 {
		d.Compactions = d.Compactions[len(d.Compactions)-200:]
	}
	d.Anomalies = []Anomaly{}
	for _, an := range s.anoms {
		if an.Agent == id {
			d.Anomalies = append(d.Anomalies, s.explainLocked(an))
		}
	}
	return d, true
}

// clone copies the slices so a view never aliases live model state.
func (c Compaction) clone() Compaction {
	c.Steps = append([]CompStep(nil), c.Steps...)
	c.Rejects = append([]string(nil), c.Rejects...)
	c.Warnings = append([]string(nil), c.Warnings...)
	if c.Next != nil {
		n := *c.Next
		c.Next = &n
	}
	if c.Warm != nil {
		w := *c.Warm
		c.Warm = &w
	}
	return c
}

// ---- requests ------------------------------------------------------------------------

func (s *Session) layerTokensLocked(r *req) ([7]int, bool) {
	var l [7]int
	if r.kind != "main" {
		return l, false
	}
	sum := 0
	for _, sec := range r.sections {
		if idx, ok := sectionLayer[sec.name]; ok {
			l[idx] = sec.tokens
			sum += sec.tokens
		}
	}
	known := r.g0Tok >= 0
	if known {
		l[0] = r.g0Tok
		sum += r.g0Tok
	}
	if r.hotTok > 0 {
		l[6] = r.hotTok
		sum += r.hotTok
	}
	if r.done {
		l[5] = max(r.prompt-sum, 0)
	}
	return l, known
}

// changedNames maps the low six change-mask bits to layer names in layer order.
func changedNames(mask uint8) []string {
	var out []string
	for i := 0; i < 6; i++ {
		if mask&(1<<i) != 0 {
			out = append(out, LayerKeys[i])
		}
	}
	return out
}

// reqViewLocked projects request usage, latency, cache, cost, and layer metadata into the
// inspection view under the session lock.
func (s *Session) reqViewLocked(r *req) Req {
	v := Req{
		ID: r.id, Seq: r.seq, Rev: r.rev, Agent: r.agent.id, Role: firstNonEmpty(r.role, r.agent.role), Kind: r.kind, Model: r.model,
		TS: r.t.UnixMilli(), T: max(r.t.Sub(s.meta.first).Milliseconds(), 0), Done: r.done, Failed: r.failed, Err: r.err,
		In: r.usage.InputTokens, Read: r.usage.CacheReadTokens, Write: r.usage.CacheWriteTokens(), Out: r.usage.OutputTokens,
		Prompt: r.prompt, Hit: r.hit, Expected: r.expected, Anomaly: r.anomaly, AnomalyKind: r.anomKind, USD: r.cost, Gateway: r.gateway, Priced: r.priced, NoCache: r.noCache,
		Naive: r.naive, NaiveCtx: r.naiveCtx, TTFB: r.ttfb, TotalMs: r.total, Stop: r.stop, Cold: r.cold, First: r.isFirst,
		Rebase: r.rebase, Changed: changedNames(r.changed), Undeclared: r.undeclared,
		ThreadFrom: r.threadFrom, ThreadTo: r.threadTo, Epoch: r.epoch, WireHash: shortHash(r.wire),
	}
	v.Layers, v.G0Known = s.layerTokensLocked(r)
	return v
}

// shortHash retains at most 12 bytes of a hash for display.
func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// Requests returns request rows. With Tail it returns the newest Limit requests
// (initial load); otherwise it returns requests created or changed after the
// Since revision, oldest change first, so paging by the returned Rev is exact.
func (s *Session) Requests(q RequestQuery) RequestPage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	src := s.reqs
	if q.Agent != "" {
		a := s.agents[q.Agent]
		if a == nil {
			return RequestPage{Requests: []Req{}, Rev: s.rev, Retained: len(s.reqs), Dropped: s.dropped}
		}
		src = a.reqs
	}
	match := func(r *req) bool {
		switch q.Kind {
		case "":
			return true
		case "side":
			return r.kind != "main"
		default:
			return r.kind == q.Kind
		}
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 1000
	}
	limit = min(limit, 20000)
	page := RequestPage{Retained: len(s.reqs), Dropped: s.dropped, Rev: s.rev, Requests: []Req{}}

	var sel []*req
	if q.Tail {
		for i := len(src) - 1; i >= 0 && len(sel) < limit; i-- {
			if match(src[i]) {
				sel = append(sel, src[i])
			}
		}
		for i, j := 0, len(sel)-1; i < j; i, j = i+1, j-1 {
			sel[i], sel[j] = sel[j], sel[i]
		}
		total := 0
		for _, r := range src {
			if match(r) {
				total++
			}
		}
		page.Total = total
		page.More = total > len(sel)
	} else {
		for _, r := range src {
			if r.rev > q.Since && match(r) {
				sel = append(sel, r)
			}
		}
		sort.SliceStable(sel, func(i, j int) bool { return sel[i].rev < sel[j].rev })
		page.Total = len(sel)
		if len(sel) > limit {
			sel = sel[:limit]
			page.More = true
			page.Rev = sel[len(sel)-1].rev
		}
	}
	for _, r := range sel {
		page.Requests = append(page.Requests, s.reqViewLocked(r))
	}
	return page
}

// ---- compactions and anomalies --------------------------------------------------------

// Compactions lists compaction episodes, oldest first.
func (s *Session) Compactions() CompactionReport {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rep := CompactionReport{Totals: s.compTotalsLocked(), Compactions: []Compaction{}}
	list := s.comp.list
	if len(list) > 2000 {
		list = list[len(list)-2000:]
	}
	for _, c := range list {
		rep.Compactions = append(rep.Compactions, c.Compaction.clone())
	}
	return rep
}

// Anomalies lists guard findings with the evidence the log holds for each,
// followed by layer changes with no declared rebase that the guard did not flag.
func (s *Session) Anomalies() AnomalyReport {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rep := AnomalyReport{Totals: s.anomTot, Anomalies: []Anomaly{}, Checked: s.checked}
	flagged := map[string]bool{}
	for _, an := range s.anoms {
		if an.Req != "" {
			flagged[an.Req] = true
		}
		rep.Anomalies = append(rep.Anomalies, s.explainLocked(an))
	}
	for _, r := range s.reqs {
		if !r.undeclared || flagged[r.id] {
			continue
		}
		an := &anomaly{agent: r.agent}
		an.Seq, an.T, an.Agent, an.Kind, an.Severity, an.Req = r.seq, r.t, r.agent.id, "undeclared", "warning", r.id
		if r.first >= 0 {
			an.Diverged = layerKey(r.first)
		}
		rep.Anomalies = append(rep.Anomalies, s.explainLocked(an))
	}
	sort.SliceStable(rep.Anomalies, func(i, j int) bool { return rep.Anomalies[i].Seq < rep.Anomalies[j].Seq })
	for i := range rep.Anomalies {
		rep.Anomalies[i].N = i + 1
	}
	return rep
}

// layerKey maps the six rendered layer positions to their API names and returns empty for unknown
// positions.
func layerKey(i int) string {
	switch i {
	case 0:
		return "tools"
	case 1:
		return "shared"
	case 2:
		return "role"
	case 3:
		return "notes"
	case 4:
		return "spine"
	case 5:
		return "thread"
	}
	return ""
}

// ---- swarm -----------------------------------------------------------------------------

// Swarm summarises coordination: agents, the reconstructed board, mail, leases,
// spawns, per-minute activity and what the request stream says about the governor.
func (s *Session) Swarm() SwarmReport {
	s.mu.RLock()
	defer s.mu.RUnlock()
	w := &s.swarm
	rep := SwarmReport{
		Totals: s.swarmTotalsLocked(), Agents: s.agentsLocked(), Tasks: s.board.views(),
		TaskCount: map[string]int{}, BoardOps: map[string]int{}, MailKinds: map[string]int{},
		Pairs: []MailPair{}, Mail: []MailView{}, Spawns: []SpawnView{}, Leases: []LeaseView{}, Minutes: []MinutePoint{},
		Alerts: w.alerts,
	}
	rep.Tasksrc = "none: the log has no board activity"
	if s.board.touched || len(rep.Tasks) > 0 {
		rep.Tasksrc = "reconstructed from task and spawn tool calls, board ops and agent.spawn; board.op events themselves carry only the operation"
		if s.board.exact {
			rep.Tasksrc = "replayed from the board.op events, which carry the full task state"
		}
	}
	for _, t := range rep.Tasks {
		rep.TaskCount[t.Status]++
	}
	for k, v := range w.boardBy {
		rep.BoardOps[k] = v
	}
	for k, v := range w.mailKinds {
		rep.MailKinds[k] = v
	}
	for k, n := range w.pairs {
		rep.Pairs = append(rep.Pairs, MailPair{From: k[0], To: k[1], N: n})
	}
	sort.Slice(rep.Pairs, func(i, j int) bool {
		if rep.Pairs[i].N != rep.Pairs[j].N {
			return rep.Pairs[i].N > rep.Pairs[j].N
		}
		if rep.Pairs[i].From != rep.Pairs[j].From {
			return rep.Pairs[i].From < rep.Pairs[j].From
		}
		return rep.Pairs[i].To < rep.Pairs[j].To
	})
	if len(rep.Pairs) > 50 {
		rep.Pairs = rep.Pairs[:50]
	}
	for i := len(w.mail) - 1; i >= 0 && len(rep.Mail) < 100; i-- {
		rep.Mail = append(rep.Mail, w.mail[i].MailView)
	}
	for i := len(w.spawns) - 1; i >= 0 && len(rep.Spawns) < 200; i-- {
		rep.Spawns = append(rep.Spawns, w.spawns[i])
	}
	for i := len(w.leases) - 1; i >= 0 && len(rep.Leases) < 100; i-- {
		rep.Leases = append(rep.Leases, w.leases[i])
	}
	keys := make([]int64, 0, len(w.minutes))
	for k := range w.minutes {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	peak, total := 0, 0
	for _, k := range keys {
		m := w.minutes[k]
		rep.Minutes = append(rep.Minutes, MinutePoint{T: k * 60000, Board: m.board, MailSent: m.mailSent, MailDeliv: m.mailDeliv,
			Spawns: m.spawns, Leases: m.leases, Requests: m.requests})
		peak = max(peak, m.requests)
		total += m.requests
	}
	if len(rep.Minutes) > 1440 {
		rep.Minutes = rep.Minutes[len(rep.Minutes)-1440:]
	}
	gov := GovernorStats{
		PeakInFlight: w.peakInflight, InFlight: w.inflight, PeakRPM: peak, Requests: w.requests, Retries: w.retries,
		RateLimited: w.rateLimited, Events: w.govEvents, Errors: map[string]int{},
	}
	if len(keys) > 0 {
		gov.AvgRPM = float64(total) / float64(len(keys))
	}
	for k, v := range w.errKinds {
		gov.Errors[k] = v
	}
	if w.govLast != nil {
		gov.Last = map[string]any{}
		for k, v := range w.govLast {
			gov.Last[k] = v
		}
	}
	rep.Governor = gov
	rep.Isolation, rep.Mailman, rep.Supervision = s.isolationLocked(), s.mailmanLocked(), s.supervisionLocked()
	return rep
}

// prevMainLocked returns the agent's previous main request in the retained window.
func (s *Session) prevMainLocked(r *req) *req {
	rs := r.agent.reqs
	i := sort.Search(len(rs), func(i int) bool { return rs[i].seq >= r.seq })
	for j := i - 1; j >= 0; j-- {
		if rs[j].kind == "main" {
			return rs[j]
		}
	}
	return nil
}

// nextMainLocked finds the next main request after r in its agent's sequence-sorted history; the
// caller must hold the session lock.
func (s *Session) nextMainLocked(r *req) *req {
	rs := r.agent.reqs
	i := sort.Search(len(rs), func(i int) bool { return rs[i].seq > r.seq })
	for j := i; j < len(rs); j++ {
		if rs[j].kind == "main" {
			return rs[j]
		}
	}
	return nil
}

// fmtDur renders a duration for explanations.
func fmtDur(d time.Duration) string {
	switch {
	case d >= time.Hour:
		return fmt.Sprintf("%.1f h", d.Hours())
	case d >= time.Minute:
		return fmt.Sprintf("%.1f min", d.Minutes())
	case d >= time.Second:
		return fmt.Sprintf("%.1f s", d.Seconds())
	}
	return fmt.Sprintf("%d ms", d.Milliseconds())
}

var _ = strings.TrimSpace

// Digest is a cheap one-line summary (no series, no cost report).
func (s *Session) Digest() Digest {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := s.opts.Now()
	m := s.metaLocked(now)
	c := &s.costAgg
	all := s.cache.mainPrompt + s.cache.sidePrompt
	return Digest{
		Model: m.Model, Provider: m.Provider, State: s.stateLocked(now), Swarm: m.Swarm, Start: m.Start,
		DurationMs: m.DurationMs, Requests: s.tot.Requests, Agents: len(s.agents), Commits: s.comp.commits,
		Anomalies: s.anomTot.Total, HitRatio: ratio(s.cache.mainRead+s.cache.sideRead, all),
		CostUSD: c.reported, SavedPct: pct(c.noCache.Total-c.actual.Total, c.noCache.Total), Goal: oneLine(m.Goal, 120),
	}
}

// Episode returns the RL episode.json next to the log, if any.
func (s *Session) Episode() *EpisodeInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.episode
}

// Updated is the log file's modification time at the last poll.
func (s *Session) Updated() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.mtime
}
