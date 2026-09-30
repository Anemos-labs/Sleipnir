package state

import (
	"math"
	"strconv"
	"time"

	"github.com/reee344/sleipnir/internal/events"
)

func (s *State) onLogOpen(e events.Event) {
	var p struct {
		Schema int `json:"schema"`
	}
	if !s.decode(e.Data, maxPayload, &p) {
		return
	}
	s.sess.Schema = clampInt(p.Schema)
	if e.V > 0 && s.sess.Schema == 0 {
		s.sess.Schema = clampInt(e.V)
	}
}

func (s *State) onLogCorrupt(e events.Event) {
	var p struct {
		CorruptLines int `json:"corrupt_lines"`
	}
	if s.decode(e.Data, maxPayload, &p) {
		s.stats.Corrupt += clampInt(p.CorruptLines)
	}
}

// startWire is session.start (internal/session/session.go Run).
type startWire struct {
	Version     string               `json:"version"`
	Model       string               `json:"model"`
	Provider    string               `json:"provider"`
	Dialect     string               `json:"dialect"`
	Swarm       bool                 `json:"swarm"`
	Root        string               `json:"root"`
	Cwd         string               `json:"cwd"`
	Renderer    string               `json:"renderer"`
	Mode        string               `json:"mode"`
	PermMode    string               `json:"permission_mode"`
	Isolation   string               `json:"isolation"`
	Mailman     bool                 `json:"mailman"`
	Resumed     bool                 `json:"resumed"`
	ReconTokens int64                `json:"recon_tokens"`
	SharedHash  string               `json:"shared_hash"`
	Models      map[string]modelWire `json:"models"`
}

func (s *State) onSessionStart(e events.Event, t time.Time) {
	var p startWire
	if !s.decode(e.Data, maxPayload, &p) {
		return
	}
	ss := &s.sess
	ss.Started = t
	ss.Version, ss.Model, ss.Provider, ss.Dialect = clip(p.Version, textID), clip(p.Model, textID), clip(p.Provider, textID), clip(p.Dialect, textID)
	ss.Root, ss.Cwd = clip(p.Root, textPath), clip(p.Cwd, textPath)
	ss.Renderer, ss.Mode = clip(p.Renderer, textID), clip(firstOf(p.Mode, p.PermMode), textID)
	ss.Swarm = ss.Swarm || p.Swarm
	ss.Isolation, ss.Mailman, ss.Resumed = clip(p.Isolation, textID), p.Mailman, p.Resumed
	ss.ReconTokens, ss.SharedHash = clampTokens(p.ReconTokens), short(p.SharedHash)
	s.recordModels(p.Models)
	s.line(e.Seq, t, "", FeedSession, GlyphInfo, "session started: "+firstOf(ss.Model, "model unknown"), firstOf(ss.Provider, ""))
}

func (s *State) onSessionEnd(e events.Event, t time.Time) {
	var p struct {
		CostUSD float64 `json:"cost_usd"`
		Reason  string  `json:"reason"`
	}
	if !s.decode(e.Data, maxPayload, &p) {
		return
	}
	ss := &s.sess
	ss.Ended, ss.EndedAt, ss.EndReason, ss.EndCostUSD = true, t, clip(p.Reason, textID), usd(p.CostUSD)
	// Whatever was still working has no more to do: the session is over. A worker whose task the manager had accepted, and that was
	// still in the last turn of its run when the session ended (the manager is quicker than the worker's closing message), is done.
	for _, a := range s.agents {
		if a.Status.Active() {
			a.endRun(runIdle, t)
			s.settleDone(a)
		}
	}
	s.line(e.Seq, t, "", FeedSession, GlyphInfo, "session ended ("+firstOf(ss.EndReason, "other")+")", fmtUSD(ss.EndCostUSD))
}

func (s *State) onUserInput(e events.Event, t time.Time) {
	var p struct {
		Text   string `json:"text"`
		Origin string `json:"origin"`
	}
	if !s.decode(e.Data, maxPayload, &p) {
		return
	}
	if a := s.agent(e.Agent, t); a != nil {
		a.active(t)
	}
	if p.Origin != "" && p.Origin != "user" {
		return // a task the harness handed out is not something a person said
	}
	text := clean(p.Text, textLong)
	if s.sess.Goal == "" {
		s.sess.Goal = text
	}
	s.line(e.Seq, t, e.Agent, FeedInput, GlyphInput, text, "")
}

func (s *State) onUserSteer(e events.Event, t time.Time) {
	var p struct {
		Text string `json:"text"`
	}
	if !s.decode(e.Data, maxPayload, &p) {
		return
	}
	s.line(e.Seq, t, e.Agent, FeedInput, GlyphInput, "steering: "+clean(p.Text, textLine), "")
}

func (s *State) onNotice(e events.Event, t time.Time) {
	var p struct {
		Level   string `json:"level"`
		Msg     string `json:"msg"`
		Message string `json:"message"`
	}
	if !s.decode(e.Data, maxPayload, &p) {
		return
	}
	glyph := GlyphInfo
	if p.Level == "warn" || p.Level == "error" {
		glyph = GlyphWarn
	}
	s.line(e.Seq, t, e.Agent, FeedNote, glyph, firstOf(clean(p.Msg, textLine), clean(p.Message, textLine), "notice"), "")
}

// ---- leases, the governor, the budget, permissions, supervision -------------------------------------------------------

// leaseState is the lease table.
type leaseState struct {
	held      map[string]Lease
	conflicts int
	scope     int
	overlaps  int
}

func newLeaseState() leaseState { return leaseState{held: map[string]Lease{}} }

// relPath shows a path relative to the project root when it lies under it.
func relPath(root, p string) string {
	if root == "" || len(p) <= len(root) || p[:len(root)] != root {
		return p
	}
	if c := p[len(root)]; c == '/' || c == '\\' {
		return p[len(root)+1:]
	}
	return p
}

func (s *State) root() string { return firstOf(s.sess.Root, s.sess.Cwd) }

func (s *State) onLease(e events.Event, t time.Time) {
	var p struct {
		Action string `json:"action"`
		Agent  string `json:"agent"`
		Path   string `json:"path"`
		Holder string `json:"holder"`
	}
	if !s.decode(e.Data, maxPayload, &p) {
		return
	}
	id := clip(firstOf(p.Agent, e.Agent), textID)
	path := relPath(s.root(), clip(p.Path, textPath))
	a := s.agent(id, t)
	switch p.Action {
	case "acquire":
		if path == "" || a == nil {
			return // a lease is shown against an agent: one that is not tracked has none to show it on
		}
		if old, ok := s.leases.held[path]; ok && old.Agent != id {
			if o := s.agents[old.Agent]; o != nil {
				o.dropLease(path)
			}
		}
		if _, ok := s.leases.held[path]; !ok && len(s.leases.held) >= MaxLeases {
			s.evictLease()
		}
		s.leases.held[path] = Lease{Agent: id, Path: path, Seq: e.Seq, Since: t}
		if a != nil {
			a.addLease(path, s)
		}
	case "release":
		if a != nil {
			for _, lp := range a.leaseOrder {
				if l, ok := s.leases.held[lp]; ok && l.Agent == id {
					delete(s.leases.held, lp)
				}
			}
			a.leaseOrder = nil
		}
	case "conflict":
		s.leases.conflicts++
		s.line(e.Seq, t, id, FeedLease, GlyphWarn, id+" wanted "+firstOf(path, "a file")+", which is leased to "+clip(p.Holder, textID), "")
	case "scope":
		s.leases.scope++
		s.line(e.Seq, t, id, FeedLease, GlyphWarn, id+" tried to write outside its task's scope", path)
	case "overlap":
		s.leases.overlaps++
		s.line(e.Seq, t, id, FeedLease, GlyphWarn, id+" and "+clip(p.Holder, textID)+" edit the same file in separate trees: expect a merge conflict", path)
	}
	if a != nil {
		a.active(t)
	}
}

// addLease remembers that the agent holds the path, keeping at most MaxLeasesPerAgent (the oldest is let go of).
func (a *agentState) addLease(path string, s *State) {
	for _, p := range a.leaseOrder {
		if p == path {
			return
		}
	}
	if len(a.leaseOrder) >= MaxLeasesPerAgent {
		oldest := a.leaseOrder[0]
		a.leaseOrder = append(a.leaseOrder[:0], a.leaseOrder[1:]...)
		if l, ok := s.leases.held[oldest]; ok && l.Agent == a.ID {
			delete(s.leases.held, oldest)
		}
	}
	a.leaseOrder = append(a.leaseOrder, path)
}

func (a *agentState) dropLease(path string) {
	for i, p := range a.leaseOrder {
		if p == path {
			a.leaseOrder = append(a.leaseOrder[:i], a.leaseOrder[i+1:]...)
			return
		}
	}
}

// evictLease drops the oldest lease of the table (the lowest seq, ties by path).
func (s *State) evictLease() {
	var victim *Lease
	for _, l := range s.leases.held {
		l := l
		if victim == nil || l.Seq < victim.Seq || (l.Seq == victim.Seq && l.Path < victim.Path) {
			victim = &l
		}
	}
	if victim == nil {
		return
	}
	if a := s.agents[victim.Agent]; a != nil {
		a.dropLease(victim.Path)
	}
	delete(s.leases.held, victim.Path)
}

// govState is the governor's and the endpoint's side.
type govState struct {
	episodes    int
	rate        float64
	pauseUntil  time.Time
	lastAt      time.Time
	inflight    int
	queued      int
	retries     int
	rateLimited int
	reqs        [60]struct {
		sec int64
		n   uint16
	}
}

func slot60(sec int64) int { return int(((sec % 60) + 60) % 60) }

// noteRequest counts a model request in its second, for the requests-per-minute figure.
func (g *govState) noteRequest(t time.Time) {
	sec := t.Unix()
	c := &g.reqs[slot60(sec)]
	if c.sec != sec {
		c.sec, c.n = sec, 0
	}
	if c.n < 1<<16-1 {
		c.n++
	}
}

// rpm is how many requests were sent in the 60 seconds ending at now.
func (g *govState) rpm(now time.Time) int {
	end := now.Unix()
	n := 0
	for i := range g.reqs {
		if c := g.reqs[i]; c.sec > end-60 && c.sec <= end {
			n += int(c.n)
		}
	}
	return n
}

func (s *State) onGovernor(e events.Event, t time.Time) {
	var p struct {
		Action     string  `json:"action"`
		RatePerMin float64 `json:"rate_per_min"`
		PauseMs    int64   `json:"pause_ms"`
		Inflight   int     `json:"inflight"`
		Queued     int     `json:"queued"`
	}
	if !s.decode(e.Data, maxPayload, &p) {
		return
	}
	g := &s.gov
	g.episodes++
	g.lastAt = t
	g.rate = min(usd(p.RatePerMin), 1e9)
	if p.PauseMs > 0 {
		g.pauseUntil = t.Add(time.Duration(min(p.PauseMs, 3_600_000)) * time.Millisecond)
	}
	g.inflight, g.queued = clampInt(p.Inflight), clampInt(p.Queued)
	s.line(e.Seq, t, "", FeedRetry, GlyphWarn, "the endpoint is rate limiting: the swarm slowed to "+fmtRate(g.rate)+" requests a minute", "paused "+fmtDur(p.PauseMs))
}

func (s *State) onBudget(e events.Event, t time.Time) {
	var p struct {
		BudgetUSD float64 `json:"budget_usd"`
		SpentUSD  float64 `json:"spent_usd"`
	}
	if !s.decode(e.Data, maxPayload, &p) {
		return
	}
	s.totals.BudgetUSD, s.totals.BudgetSpentUSD, s.totals.BudgetSpent = usd(p.BudgetUSD), usd(p.SpentUSD), true
	s.line(e.Seq, t, "", FeedBudget, GlyphWarn, "the swarm budget of "+fmtUSD(s.totals.BudgetUSD)+" is spent: running workers were stopped", fmtUSD(s.totals.BudgetSpentUSD)+" spent")
}

// permState is the permission dialog's state.
type permState struct {
	pending []PermAsk
	recent  ring[PermDecision]
	asked   int
	allowed int
	denied  int
}

func (s *State) onPermAsk(e events.Event, t time.Time) {
	var p struct {
		ID      string `json:"id"`
		Agent   string `json:"agent"`
		Tool    string `json:"tool"`
		Summary string `json:"summary"`
		Command string `json:"command"`
		Path    string `json:"path"`
	}
	if !s.decode(e.Data, maxPayload, &p) {
		return
	}
	ask := PermAsk{ID: clip(p.ID, textID), Seq: e.Seq, T: t, Agent: clip(firstOf(p.Agent, e.Agent), textID), Tool: clip(p.Tool, textID),
		Summary: clean(firstOf(p.Summary, p.Command, p.Path, p.Tool, "a permission"), textLine)}
	if len(s.perms.pending) >= MaxPending {
		s.perms.pending = append(s.perms.pending[:0], s.perms.pending[1:]...)
	}
	s.perms.pending = append(s.perms.pending, ask)
	s.perms.asked++
	s.line(e.Seq, t, ask.Agent, FeedPerm, "?", ask.Agent+" asks to "+ask.Summary, "")
}

func (s *State) onPermDecide(e events.Event, t time.Time) {
	var p struct {
		ID       string `json:"id"`
		Agent    string `json:"agent"`
		Tool     string `json:"tool"`
		Allow    *bool  `json:"allow"`
		Allowed  *bool  `json:"allowed"`
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}
	if !s.decode(e.Data, maxPayload, &p) {
		return
	}
	allow := false
	switch {
	case p.Allow != nil:
		allow = *p.Allow
	case p.Allowed != nil:
		allow = *p.Allowed
	default:
		switch p.Decision {
		case "allow", "allowed", "approve", "approved", "yes":
			allow = true
		}
	}
	id, agent, tool := clip(p.ID, textID), clip(firstOf(p.Agent, e.Agent), textID), clip(p.Tool, textID)
	at := -1
	for i, a := range s.perms.pending {
		if (id != "" && a.ID == id) || (id == "" && (agent == "" || a.Agent == agent) && (tool == "" || a.Tool == tool)) {
			at = i
			break
		}
	}
	if at < 0 && id == "" && len(s.perms.pending) > 0 {
		at = 0 // an answer that names nothing answers the oldest question
	}
	var ask PermAsk
	if at >= 0 {
		ask = s.perms.pending[at]
		s.perms.pending = append(s.perms.pending[:at], s.perms.pending[at+1:]...)
	} else {
		ask = PermAsk{ID: id, Agent: agent, Tool: tool, Summary: firstOf(tool, "a permission")}
	}
	s.perms.recent.push(PermDecision{Ask: ask, Seq: e.Seq, T: t, Allow: allow, Reason: clean(p.Reason, textShort)})
	glyph, word := GlyphOK, "allowed"
	if allow {
		s.perms.allowed++
	} else {
		s.perms.denied++
		glyph, word = GlyphFail, "denied"
	}
	s.line(e.Seq, t, ask.Agent, FeedPerm, glyph, word+": "+ask.Summary, clean(p.Reason, textShort))
}

func (s *State) onSupervision(e events.Event, t time.Time) {
	var p struct {
		Reason string `json:"reason"`
		Note   string `json:"note"`
		N      int    `json:"n"`
	}
	if !s.decode(e.Data, maxPayload, &p) {
		return
	}
	sup := &s.sup
	switch e.Type {
	case events.TypeSwarmHold:
		sup.Holds++
		sup.LastHold = clean(p.Reason, textLine)
		s.line(e.Seq, t, e.Agent, FeedNote, GlyphWarn, "the manager answered while work was unfinished and was sent back to it", sup.LastHold)
	case events.TypeSwarmUnfinished:
		sup.Unfinished++
		s.line(e.Seq, t, e.Agent, FeedNote, GlyphWarn, "the run ended with work unfinished", clean(p.Reason, textShort))
	case events.TypeSwarmWake:
		sup.Wakes++
		sup.LastWake = clean(p.Note, textLine)
		s.line(e.Seq, t, e.Agent, FeedNote, GlyphInfo, "the idle manager was woken", sup.LastWake)
	case events.TypeSwarmWakePaused:
		sup.WakePaused++
		s.line(e.Seq, t, e.Agent, FeedNote, GlyphWarn, "the manager will not be woken again until you write to it", "")
	case events.TypeSwarmWakeLimit:
		sup.WakeLimits++
		s.line(e.Seq, t, e.Agent, FeedNote, GlyphWarn, "peer mail no longer wakes "+clip(e.Agent, textID), "")
	default:
		s.line(e.Seq, t, "", FeedNote, GlyphWarn, "the swarm shut down with agents still running", "")
	}
}

// fmtRate writes a request rate as a whole number.
func fmtRate(r float64) string { return strconv.FormatFloat(math.Round(r), 'f', 0, 64) }
