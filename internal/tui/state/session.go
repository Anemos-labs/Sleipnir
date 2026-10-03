package state

import (
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
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

// onLogCorrupt adds a bounded corrupt-line count from a valid event payload.
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
	ss.Ended, ss.EndedAt, ss.EndReason, ss.EndCostUSD = false, time.Time{}, "", 0 // a resumed session goes on after the end of the run before
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
	s.idleAgents("", t)
	s.line(e.Seq, t, "", FeedSession, GlyphInfo, "session ended ("+firstOf(ss.EndReason, "other")+")", fmtUSD(ss.EndCostUSD))
}

// idleAgents ends the run of every agent that is still working but keep: whatever it was doing has no more to do, because the session is
// over, or was picked up again by another process. A worker whose task the manager had accepted, and that was still in the last turn of its
// run when the session ended (the manager is quicker than the worker's closing message), is done.
func (s *State) idleAgents(keep string, t time.Time) {
	for _, a := range s.agents {
		if a.ID != keep && a.Status.Active() {
			s.endRun(a, runIdle, t)
			s.settleDone(a)
		}
	}
	s.dropAllAsks() // nobody is left to answer: a question of an agent that is not tracked, or of none, ends with the session
}

// onRestore is an agent brought back from a snapshot: the session is resumed. Its log may hold a run that was cut short (a killed process
// ends nothing). Recovered workers stay idle until their tasks restart.
func (s *State) onRestore(e events.Event, t time.Time) {
	ss := &s.sess
	ss.Ended, ss.EndedAt, ss.EndReason, ss.EndCostUSD = false, time.Time{}, "", 0 // its session.start waits for a goal
	s.idleAgents(e.Agent, t)
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
		a.failed = failure{}
		if a.Stuck.Active {
			// user.input opens a run (a person's message, or a task the harness hands a worker) and a run starts with a repetition
			// guard that has seen nothing: what an earlier run was told about repeating a call is over with it.
			a.Stuck.Active = false
			a.refresh()
		}
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

// onUserSteer decodes and sanitizes steering text before adding an input entry to the event feed.
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

// newLeaseState initializes the index of currently held leases.
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

// root prefers the session repository root and falls back to its working directory.
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

// dropLease removes the first matching path from an agent's lease display order.
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

// slot60 maps any signed second count into a nonnegative slot in a 60-entry ring.
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
	pending   []PermAsk
	recent    ring[PermDecision]
	asked     int
	allowed   int
	denied    int
	byUser    int
	byNoOne   int
	byPolicy  int
	canceled  int
	abandoned int
}

// permWire is the payload of perm.ask and perm.decide (internal/session permaudit.go, from perm.Audit): the request the permission
// engine could not simply allow, and for perm.decide what came of it. There is no id in it: the event's agent, the tool, the command
// and the paths are what tell one question from another and pair it with its answer. The producer cuts command at 400 characters
// and paths at five of 200; allow is always written, by is one of the PermBy* words, remember is "session" or "project" and absent
// for an answer that is not kept.
type permWire struct {
	Tool     string   `json:"tool"`
	Reason   string   `json:"reason"`
	Command  string   `json:"command"`
	Paths    []string `json:"paths"`
	Role     string   `json:"role"`
	Allow    *bool    `json:"allow"`
	By       string   `json:"by"`
	Remember string   `json:"remember"`
}

// ask is what a permWire says about the question, in the form the State keeps it: bounded, one line, paths relative to the project.
// The same function reads a perm.ask and the perm.decide that answers it, so that the two come out equal where the log means the same.
func (s *State) ask(w *permWire, e events.Event, t time.Time) PermAsk {
	q := PermAsk{Seq: e.Seq, T: t, Agent: clip(e.Agent, textID), Role: clip(w.Role, textID), Tool: clip(w.Tool, textID),
		Command: clean(w.Command, textPath), Reason: clean(w.Reason, textLine)}
	root := s.root()
	for _, p := range w.Paths {
		if len(q.Paths) >= MaxPermPaths {
			break
		}
		q.Paths = append(q.Paths, relPath(root, clean(p, textPath)))
	}
	what := q.Command
	if what == "" {
		what = strings.Join(q.Paths, ", ")
	}
	q.Summary = clean(firstOf(strings.TrimSpace(q.Tool+" "+what), "a permission request"), textLine)
	return q
}

// answers reports whether the decision (read like a question, without its sequence number) is the answer to the pending question:
// the same agent, tool, command and paths.
func (q PermAsk) answers(d PermAsk) bool {
	return q.Agent == d.Agent && q.Tool == d.Tool && q.Command == d.Command && slices.Equal(q.Paths, d.Paths)
}

// detach returns the question with paths of its own, for a snapshot that must share nothing with the State.
func (q PermAsk) detach() PermAsk {
	q.Paths = copyOf(q.Paths)
	return q
}

func (s *State) onPermAsk(e events.Event, t time.Time) {
	var w permWire
	if !s.decode(e.Data, maxPayload, &w) {
		return
	}
	q := s.ask(&w, e, t)
	ps := &s.perms
	if len(ps.pending) >= MaxPending {
		// More questions unanswered than anyone can be shown: the oldest makes room, and is counted as not kept.
		s.forgetAsk(0)
		s.stats.Dropped++
	}
	ps.pending = append(ps.pending, q)
	ps.asked++
	if a := s.agent(e.Agent, t); a != nil {
		a.Role = firstOf(a.Role, q.Role)
		a.asks++
		if a.run == runIdle || a.run == runDone || a.run == runError {
			a.run = runThinking // a tool call is held at a question: it is working, whatever said it had stopped is out of date
		}
		a.active(t)
		a.refresh()
	}
	s.line(e.Seq, t, q.Agent, FeedPerm, GlyphAsk, firstOf(q.Agent, "an agent")+" asks permission: "+q.Summary, q.Reason)
}

func (s *State) onPermDecide(e events.Event, t time.Time) {
	var w permWire
	if !s.decode(e.Data, maxPayload, &w) {
		return
	}
	if w.Allow == nil {
		s.bad() // a decision that does not say what it decided: it is shown as a refusal, never as a consent
	}
	d := PermDecision{Seq: e.Seq, T: t, Allow: w.Allow != nil && *w.Allow, By: clip(w.By, textID), Remember: clip(w.Remember, textID),
		Reason: clean(w.Reason, textLine)}
	d.Ask = s.ask(&w, e, t)
	ps := &s.perms
	if at := slices.IndexFunc(ps.pending, func(q PermAsk) bool { return q.answers(d.Ask) }); at >= 0 {
		d.Ask, d.Asked = ps.pending[at], true
		d.WaitedMs = max(t.Sub(d.Ask.T).Milliseconds(), 0)
		s.forgetAsk(at)
		if a := s.agents[d.Ask.Agent]; a != nil {
			a.active(t)
			a.refresh()
		}
	} else {
		// There is no question event to point at (a refusal by policy is never asked), and the reason the decision carries is the
		// decision's, not the question's.
		d.Ask.Seq, d.Ask.T, d.Ask.Reason = 0, time.Time{}, ""
	}
	if d.Allow {
		ps.allowed++
	} else {
		ps.denied++
	}
	switch d.By {
	case PermByUser:
		ps.byUser++
	case PermByNoOne:
		ps.byNoOne++
	case PermByPolicy:
		ps.byPolicy++
	case PermByCanceled:
		ps.canceled++
	}
	ps.recent.push(d)
	glyph, word := GlyphOK, "allowed"
	if !d.Allow {
		glyph, word = GlyphFail, "denied"
	}
	s.line(e.Seq, t, d.Ask.Agent, FeedPerm, glyph, word+": "+d.Ask.Summary, decisionDetail(&d))
}

// decisionDetail is the small print of a decision's line in the feed: who decided, and why when it was not a person's yes.
func decisionDetail(d *PermDecision) string {
	var by string
	switch d.By {
	case PermByUser:
		by = "by you"
	case PermByNoOne:
		by = "no one to ask"
	case PermByPolicy:
		by = "by policy"
	case PermByCanceled:
		by = "cancelled while it waited"
	case "":
		by = "by unknown"
	default:
		by = "by " + d.By
	}
	if d.Remember != "" {
		by += ", kept for the " + d.Remember
	}
	if d.Reason != "" && !d.Allow {
		by += ": " + d.Reason
	}
	return by
}

// forgetAsk takes the pending question at index i off the list, and off its agent's count of questions it waits on.
func (s *State) forgetAsk(i int) {
	ps := &s.perms
	q := ps.pending[i]
	ps.pending = slices.Delete(ps.pending, i, i+1)
	if a := s.agents[q.Agent]; a != nil && a.asks > 0 {
		a.asks--
		a.refresh()
	}
}

// dropAsks forgets the questions an agent can no longer be waiting for, because its run ended: they are counted as abandoned, and
// an answer that comes for one later is recorded as a decision that was not asked.
func (s *State) dropAsks(a *agentState) {
	ps := &s.perms
	for i := len(ps.pending) - 1; i >= 0; i-- {
		if ps.pending[i].Agent == a.ID {
			s.forgetAsk(i)
			ps.abandoned++
		}
	}
	a.asks = 0
}

// dropAllAsks forgets every pending question: the session is over.
func (s *State) dropAllAsks() {
	ps := &s.perms
	ps.abandoned += len(ps.pending)
	ps.pending = nil
	for _, a := range s.agents {
		if a.asks > 0 {
			a.asks = 0
			a.refresh()
		}
	}
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
