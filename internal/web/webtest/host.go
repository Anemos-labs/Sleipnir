package webtest

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/web/seam"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// Compile-time checks: the fakes implement the interfaces of package seam.
var (
	_ seam.Host          = (*Host)(nil)
	_ seam.Tab           = (*Tab)(nil)
	_ seam.SessionAccess = (*Tab)(nil)
)

// answerFloor is the quiet period after a question appears (and after the tab's previous answer) before an answer is accepted.
const answerFloor = 350 * time.Millisecond

// maxTabs is the most tabs a host runs.
const maxTabs = 16

// Host is an in-memory seam.Host. It holds fake tabs, records every frame published to it and optionally forwards them (the real
// host forwards them to the hub). Its methods are safe for concurrent use.
type Host struct {
	mu       sync.Mutex
	tabs     []*Tab
	active   string
	projects []wire.Project
	frames   []wire.Frame
	pub      func(wire.Frame)
	clock    func() time.Time
	nextTab  int
	runs     int
}

// NewHost returns a host with no tab.
func NewHost() *Host {
	return &Host{clock: time.Now, projects: Projects()}
}

// NewShopHost returns a host with the canned "shop" tab, active.
func NewShopHost() *Host {
	h := NewHost()
	h.addScenario(Shop)
	return h
}

// SetPublisher sets where published frames are forwarded (nil: nowhere). Frames are recorded either way.
func (h *Host) SetPublisher(fn func(wire.Frame)) {
	h.mu.Lock()
	h.pub = fn
	h.mu.Unlock()
}

// SetClock replaces the wall clock that the answer floor of questions uses.
func (h *Host) SetClock(fn func() time.Time) {
	h.mu.Lock()
	h.clock = fn
	h.mu.Unlock()
}

// now is the host's wall clock.
func (h *Host) now() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.clock()
}

// addScenario adds a tab built from a scenario (mk makes a fresh one each time it is called) and makes it active when it is the first.
func (h *Host) addScenario(mk func() *Scenario) *Tab {
	t := newTab(h, mk)
	h.mu.Lock()
	t.sum.Order = len(h.tabs)
	h.tabs = append(h.tabs, t)
	if h.active == "" {
		h.active = t.sum.ID
	}
	h.mu.Unlock()
	return t
}

// AddTab adds an empty tab named name in cwd, as POST /api/sessions does, and publishes the tab frame. It fails with 409 limit at 16
// tabs.
func (h *Host) AddTab(name, cwd string) (*Tab, error) {
	h.mu.Lock()
	if len(h.tabs) >= maxTabs {
		h.mu.Unlock()
		return nil, werr(409, "limit", "16 sessions are open: close one first")
	}
	h.nextTab++
	n := h.nextTab
	h.mu.Unlock()
	if name == "" {
		name = fmt.Sprintf("session-%d", n+1)
	}
	t := h.addScenario(func() *Scenario {
		return &Scenario{
			Tab:    wire.TabSummary{ID: fmt.Sprintf("%s-%d", slug(name), n+1), SID: fmt.Sprintf("20261009-2300%02d-%06x", n%100, 0xabc000+n), Name: name, Cwd: cwd, Gen: 1, CreatedAt: StartedAt + int64(n)*60000},
			Meta:   emptyMeta(cwd),
			Roster: Roster()[:1],
			Now:    0,
		}
	})
	t.Emit(&wire.Say{Who: "sys", Glyph: "◇", Text: "session started in " + cwd})
	h.Publish(Frames("tab", "", wire.TabFrame{Op: "add", Tab: t.Summary()}))
	return t, nil
}

// slug makes a tab id from a name.
func slug(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "tab"
	}
	if len(out) > 30 {
		out = out[:30]
	}
	return out
}

// emptyMeta is the meta of a tab that has just started in cwd with the defaults.
func emptyMeta(cwd string) wire.MetaPatch {
	model, mode, effort, iso := Model, "default", "default", "none"
	sw, run := 4, false
	zero := 0.0
	tr, mm, no := false, false, false
	q := []wire.QueuedLine{}
	rules := []wire.Rule{}
	rm := map[string]string{}
	started := StartedAt
	return wire.MetaPatch{Cwd: &cwd, Model: &model, Mode: &mode, Effort: &effort, Budget: &zero, Swarm: &sw, Isolation: &iso, Mailman: &mm,
		TrustProject: &tr, NoMcp: &no, RoleModels: &rm, Rules: &rules, Queued: &q, Running: &run, StartedAt: &started}
}

// CloseTab removes a tab and publishes the tab frame; closing the last one fails with 409 last, an unknown id with 404.
func (h *Host) CloseTab(id string) error {
	h.mu.Lock()
	idx := -1
	for i, t := range h.tabs {
		if t.sum.ID == id {
			idx = i
		}
	}
	switch {
	case idx < 0:
		h.mu.Unlock()
		return werr(404, "no_session", "no such session")
	case len(h.tabs) == 1:
		h.mu.Unlock()
		return werr(409, "last", "the last session cannot be closed: start another first")
	}
	gone := h.tabs[idx]
	h.tabs = append(h.tabs[:idx:idx], h.tabs[idx+1:]...)
	if h.active == id {
		h.active = h.tabs[0].sum.ID
	}
	h.mu.Unlock()
	h.Publish(Frames("tab", "", wire.TabFrame{Op: "remove", Tab: gone.Summary()}))
	return nil
}

// Tabs lists the live tabs in strip order.
func (h *Host) Tabs() []wire.TabSummary {
	out := []wire.TabSummary{}
	for _, t := range h.tabList() {
		out = append(out, t.Summary())
	}
	return out
}

// tabList copies the tabs.
func (h *Host) tabList() []*Tab {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]*Tab(nil), h.tabs...)
}

// Tab finds a live tab by its id.
func (h *Host) Tab(id string) (seam.Tab, bool) {
	if t := h.FakeTab(id); t != nil {
		return t, true
	}
	return nil, false
}

// FakeTab is Tab with the concrete type, nil when there is no such tab.
func (h *Host) FakeTab(id string) *Tab {
	for _, t := range h.tabList() {
		if t.sum.ID == id {
			return t
		}
	}
	return nil
}

// Active is the tab the page should show first.
func (h *Host) Active() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.active
}

// Projects lists the directories a new session may start in.
func (h *Host) Projects(context.Context) []wire.Project {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]wire.Project{}, h.projects...)
}

// Questions lists the open questions of every tab, oldest first.
func (h *Host) Questions() []wire.OpenQuestion {
	out := []wire.OpenQuestion{}
	for _, t := range h.tabList() {
		out = append(out, t.openQuestions()...)
	}
	return out
}

// Publish records a frame and forwards it to the publisher, if one is set.
func (h *Host) Publish(f wire.Frame) {
	h.mu.Lock()
	h.frames = append(h.frames, f)
	pub := h.pub
	h.mu.Unlock()
	if pub != nil {
		pub(f)
	}
}

// Frames returns a copy of every frame published so far, in order.
func (h *Host) Frames() []wire.Frame {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]wire.Frame(nil), h.frames...)
}

// FramesOf returns the published frames of one type.
func (h *Host) FramesOf(typ string) []wire.Frame {
	var out []wire.Frame
	for _, f := range h.Frames() {
		if f.Type == typ {
			out = append(out, f)
		}
	}
	return out
}

// werr is an error carrying the status, code and sentence of a route's answer.
func werr(status int, code, msg string) *wire.Error {
	return &wire.Error{Status: status, Code: code, Msg: msg}
}

// ---- tabs -----------------------------------------------------------------------------------------------------------------

// Call is one call a route made on a fake tab.
type Call struct {
	Method string
	Arg    any
}

// Tab is an in-memory seam.Tab and seam.SessionAccess. It keeps a journal of UI events like the real tab, answers the requests of
// the HTTP API (docs/WEB-API.md) with its errors, and publishes the acknowledgements the real host would (a say row, a meta patch).
type Tab struct {
	host *Host

	// SessionFn, when set before the tab is used, is what Session returns; the default is nil, as for a tab that restarts.
	SessionFn func() *session.Session

	mu         sync.Mutex
	mk         func() *Scenario
	sum        wire.TabSummary
	meta       wire.MetaPatch
	roster     []wire.RosterEntry
	keyframe   []json.RawMessage
	journal    []json.RawMessage
	seq        uint64
	now        float64
	hist       []string
	open       []openQuestion
	lastAnswer time.Time
	running    bool
	hold       bool
	queue      []wire.QueuedLine
	rules      []wire.Rule
	live       []wire.Event
	pos        int
	cls        Classifier
	calls      []Call
	failures   map[string]error
	goal       string // "", active or paused
	lines      int
}

// openQuestion is a question that has not been answered, with when it was asked.
type openQuestion struct {
	q     wire.Question
	t0    float64
	asked time.Time
}

// newTab builds a tab from a scenario: the journal holds the scenario's history, the continuation waits to be played.
func newTab(h *Host, mk func() *Scenario) *Tab {
	t := &Tab{host: h, mk: mk, failures: map[string]error{}}
	t.load(mk())
	return t
}

// load (re)initialises the tab's state from a scenario.
func (t *Tab) load(sc *Scenario) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sum, t.meta, t.roster = sc.Tab, sc.Meta, append([]wire.RosterEntry(nil), sc.Roster...)
	t.keyframe, t.journal, t.seq, t.now = nil, nil, 0, sc.Now
	for _, e := range sc.Keyframe {
		t.keyframe = append(t.keyframe, raw(e))
	}
	t.hist = append([]string(nil), sc.Hist...)
	t.open, t.queue, t.running, t.hold, t.pos = nil, nil, false, false, 0
	t.cls = Classifier{}
	t.goal = ""
	if sc.Meta.Running != nil {
		t.running = *sc.Meta.Running
	}
	if sc.Meta.Rules != nil {
		t.rules = append([]wire.Rule(nil), *sc.Meta.Rules...)
	}
	t.live = sc.Live
	// the history is journaled without being published: it happened before anyone was connected
	history := sc.History
	for _, e := range history {
		t.journalLocked(e, false)
	}
	for i := range t.open {
		t.open[i].asked = time.Time{} // asked long before anyone connected: answerable at once
	}
	t.now = sc.Now
}

// Summary describes the tab.
func (t *Tab) Summary() wire.TabSummary {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.sum
}

// record notes a call and returns the error a test made it fail with.
func (t *Tab) record(method string, arg any) error {
	t.calls = append(t.calls, Call{Method: method, Arg: arg})
	return t.failures[method]
}

// Calls returns the calls routes made on the tab, oldest first.
func (t *Tab) Calls() []Call {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]Call(nil), t.calls...)
}

// Fail makes the named method of the tab (for example "Send" or "SetMode") return err from now on; nil removes the failure.
func (t *Tab) Fail(method string, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err == nil {
		delete(t.failures, method)
		return
	}
	t.failures[method] = err
}

// HoldTurns makes a message start a turn that stays running until ReleaseTurn, so that queueing, steering and interrupting can be
// exercised.
func (t *Tab) HoldTurns(on bool) {
	t.mu.Lock()
	t.hold = on
	t.mu.Unlock()
}

// ReleaseTurn ends the running turn as it would end by itself, then starts the next queued line.
func (t *Tab) ReleaseTurn() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.endTurnLocked()
	t.drainLocked()
}

// metaFull returns the tab's meta as a patch of every field it has.
func (t *Tab) metaFull() wire.MetaPatch {
	m := t.meta
	q := append([]wire.QueuedLine{}, t.queue...)
	m.Queued = &q
	r := t.running
	m.Running = &r
	rules := append([]wire.Rule{}, t.rules...)
	m.Rules = &rules
	return m
}

// Snapshot returns the tab's state for a late joiner.
func (t *Tab) Snapshot(context.Context) (wire.TabSnapshot, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.record("Snapshot", nil); err != nil {
		return wire.TabSnapshot{}, err
	}
	qs := []wire.Question{}
	for _, o := range t.open {
		qs = append(qs, o.q)
	}
	return wire.TabSnapshot{
		Tab: t.sum, Gen: t.sum.Gen, Seq: t.seq, Now: t.now, Meta: t.metaFull(), Roster: append([]wire.RosterEntry{}, t.roster...),
		Keyframe: nonNil(t.keyframe), Events: nonNil(t.journal), Hist: append([]string{}, t.hist...), Questions: qs,
	}, nil
}

// nonNil returns a copy of s that encodes as [] when empty.
func nonNil(s []json.RawMessage) []json.RawMessage { return append([]json.RawMessage{}, s...) }

// journalLocked stamps an event with the next sequence number, appends it to the journal and, if publish is set, publishes its frame.
// The event's time is the current session time unless it carries one already.
func (t *Tab) journalLocked(e wire.Event, publish bool) {
	t.seq++
	h := wire.HeadOf(e)
	if h.T > t.now {
		t.now = h.T
	}
	wire.Stamp(e, h.T, t.seq)
	switch v := e.(type) {
	case *wire.Ask:
		t.open = append(t.open, openQuestion{q: v.Q, t0: h.T, asked: t.host.nowIfSet()})
	case *wire.Answer:
		for i, o := range t.open {
			if o.q.ID == v.QID {
				t.open = append(t.open[:i:i], t.open[i+1:]...)
				break
			}
		}
	case *wire.Goal:
		switch v.S {
		case "active", "paused":
			t.goal = v.S
		default:
			t.goal = ""
		}
	}
	t.journal = append(t.journal, raw(e))
	if publish {
		t.host.Publish(t.cls.EvFrame(t.sum.ID, e))
	}
	if turn, isTurn := e.(*wire.Turn); isTurn && (turn.S == "start") != t.running {
		t.running = turn.S == "start"
		if publish {
			run := t.running
			t.patchLocked(wire.MetaPatch{Running: &run})
		} else {
			t.meta.Running = &t.running
		}
	}
}

// nowIfSet is the host's wall clock, read without the host's lock being held by the caller of a tab method.
func (h *Host) nowIfSet() time.Time { return h.now() }

// Emit adds host-originated UI events to the tab's journal at the current session time and publishes them.
func (t *Tab) Emit(evs ...wire.Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, e := range evs {
		wire.Stamp(e, t.now, 0)
		t.journalLocked(e, true)
	}
}

// emitLocked is Emit for a caller that holds the tab's lock.
func (t *Tab) emitLocked(evs ...wire.Event) {
	for _, e := range evs {
		wire.Stamp(e, t.now, 0)
		t.journalLocked(e, true)
	}
}

// EmitAt adds an event at session time at (the session clock moves forward to it) and publishes it.
func (t *Tab) EmitAt(at float64, e wire.Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if at > t.now {
		t.now = at
	}
	wire.Stamp(e, t.now, 0)
	t.journalLocked(e, true)
}

// sysLocked emits a status row of the host.
func (t *Tab) sysLocked(glyph, text string) {
	t.emitLocked(&wire.Say{Who: "sys", Glyph: glyph, Text: text})
}

// patchLocked merges a meta patch into the tab's meta and publishes it.
func (t *Tab) patchLocked(p wire.MetaPatch) {
	mergeMeta(&t.meta, p)
	if p.Rules != nil {
		t.rules = append([]wire.Rule(nil), *p.Rules...)
	}
	t.host.Publish(Frames("meta", t.sum.ID, wire.MetaFrame{Tab: t.sum.ID, Patch: p}))
}

// mergeMeta copies the fields set in src into dst.
func mergeMeta(dst *wire.MetaPatch, src wire.MetaPatch) {
	d, s := reflect.ValueOf(dst).Elem(), reflect.ValueOf(src)
	for i := 0; i < s.NumField(); i++ {
		if !s.Field(i).IsNil() {
			d.Field(i).Set(s.Field(i))
		}
	}
}

// TabID names the tab.
func (t *Tab) TabID() string { return t.sum.ID }

// Session returns the current harness session: nil unless SessionFn is set.
func (t *Tab) Session() *session.Session {
	if t.SessionFn != nil {
		return t.SessionFn()
	}
	return nil
}

// Busy says whether a turn runs.
func (t *Tab) Busy() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.running
}

// Exclusive runs fn with the tab's session while no turn runs; it fails with 409 busy when one does.
func (t *Tab) Exclusive(ctx context.Context, fn func(s *session.Session) error) error {
	if t.Busy() {
		return werr(409, "busy", "a turn is running: wait for it to finish")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn(t.Session())
}

// Notify records a note to the agents (see Calls).
func (t *Tab) Notify(text string) {
	t.mu.Lock()
	t.calls = append(t.calls, Call{Method: "Notify", Arg: text})
	t.mu.Unlock()
}

// Meta patches the tab's meta and publishes the patch.
func (t *Tab) Meta(p wire.MetaPatch) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.patchLocked(p)
}

// Access gives the workspace and settings routes the tab's session access (the tab itself).
func (t *Tab) Access() seam.SessionAccess { return t }

// openQuestions lists the tab's open questions.
func (t *Tab) openQuestions() []wire.OpenQuestion {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := []wire.OpenQuestion{}
	for _, o := range t.open {
		out = append(out, wire.OpenQuestion{Tab: t.sum.ID, Q: o.q, T0: o.t0})
	}
	return out
}

// Answer answers an open question as POST /api/questions/{qid}/answer does: 400 bad_choice, 404 no_question, 409 too_soon (with the
// delay in the detail) when the question or the tab's previous answer is younger than the quiet period. It emits the answer event.
func (t *Tab) Answer(qid string, req wire.AnswerRequest) (wire.AnswerResult, error) {
	if req.Choice < 1 || req.Choice > 3 {
		return wire.AnswerResult{}, werr(400, "bad_choice", "choice must be 1, 2 or 3")
	}
	if len(req.Note) > 2000 || (req.Note != "" && req.Choice != 3) {
		return wire.AnswerResult{}, werr(400, "bad_choice", "a note goes with choice 3 and holds at most 2,000 characters")
	}
	now := t.host.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.record("Answer", qid); err != nil {
		return wire.AnswerResult{}, err
	}
	var q *openQuestion
	for i := range t.open {
		if t.open[i].q.ID == qid {
			q = &t.open[i]
		}
	}
	if q == nil {
		return wire.AnswerResult{}, werr(404, "no_question", "no such question")
	}
	arm := q.asked
	if t.lastAnswer.After(arm) {
		arm = t.lastAnswer
	}
	if wait := arm.Add(answerFloor).Sub(now); wait > 0 {
		return wire.AnswerResult{}, &wire.Error{Status: 409, Code: "too_soon", Msg: "too soon: look at the question first", Detail: map[string]int64{"retryAfterMs": wait.Milliseconds() + 1}}
	}
	t.lastAnswer = now
	res := wire.AnswerResult{OK: true}
	ans := &wire.Answer{QID: qid, Choice: req.Choice, Note: req.Note, By: "you"}
	if req.Choice == 2 {
		ans.Rule, res.Rule = q.q.Rule, q.q.Rule
		t.rules = append(t.rules, wire.Rule{Effect: "allow", Rule: q.q.Rule, Origin: "don't ask again"})
		rules := append([]wire.Rule{}, t.rules...)
		t.patchLocked(wire.MetaPatch{Rules: &rules})
	}
	asker := q.q.Agent
	t.emitLocked(ans)
	to := "idle"
	if req.Choice == 3 {
		to = "think"
	}
	task := q.q.Task
	t.emitLocked(&wire.State{ID: asker, S: to, Doing: "continues", Task: &task})
	return res, nil
}

// ---- the seam.Tab methods --------------------------------------------------------------------------------------------------

// Send delivers a message now (a short canned turn runs and ends) or, while a turn runs, queues it.
func (t *Tab) Send(_ context.Context, req wire.MessageRequest) (wire.SendResult, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.record("Send", req); err != nil {
		return wire.SendResult{}, err
	}
	if strings.TrimSpace(req.Text) == "" {
		return wire.SendResult{}, werr(400, "empty", "a message needs text")
	}
	t.lines++
	id := fmt.Sprintf("l%d", t.lines)
	if t.running {
		t.queue = append(t.queue, wire.QueuedLine{ID: id, Text: req.Text})
		q := append([]wire.QueuedLine{}, t.queue...)
		t.patchLocked(wire.MetaPatch{Queued: &q})
		return wire.SendResult{Queued: true, Position: len(t.queue), ID: id}, nil
	}
	t.hist = append(t.hist, req.Text)
	t.startTurnLocked(firstNonEmpty(req.Display, req.Text), req.Text)
	if !t.hold {
		t.endTurnLocked()
		t.drainLocked()
	}
	return wire.SendResult{ID: id}, nil
}

// firstNonEmpty returns the first argument that is not empty.
func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// startTurnLocked echoes the person's line and starts a turn of the manager.
func (t *Tab) startTurnLocked(shown, text string) {
	t.emitLocked(&wire.Say{Who: "you", Text: shown}, &wire.Turn{S: "start"}, &wire.State{ID: "mgr", S: "think", Doing: "reads your message"})
}

// endTurnLocked has the manager answer and ends the turn.
func (t *Tab) endTurnLocked() {
	if !t.running {
		return
	}
	t.now += 1.5
	t.emitLocked(&wire.Say{Who: "mgr", Text: "(fake) understood. Nothing runs behind this answer.", Stream: false},
		&wire.State{ID: "mgr", S: "idle", Doing: "waits for you"}, &wire.Final{}, &wire.Turn{S: "end"})
}

// drainLocked starts the next queued line, if any.
func (t *Tab) drainLocked() {
	for len(t.queue) > 0 && !t.running {
		next := t.queue[0]
		t.queue = t.queue[1:]
		q := append([]wire.QueuedLine{}, t.queue...)
		t.patchLocked(wire.MetaPatch{Queued: &q})
		t.startTurnLocked(next.Text, next.Text)
		if t.hold {
			return
		}
		t.endTurnLocked()
	}
}

// Command runs a slash line the page has no handler for: a custom command answers with its output, anything else is 404
// unknown_command.
func (t *Tab) Command(_ context.Context, req wire.CommandRequest) (wire.CommandResult, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.record("Command", req); err != nil {
		return wire.CommandResult{}, err
	}
	name, _, _ := strings.Cut(strings.TrimSpace(req.Line), " ")
	for _, s := range Slash() {
		if s.Custom && s.Cmd == name {
			return wire.CommandResult{Output: "(fake) " + name + " ran", Sent: false, Title: name}, nil
		}
	}
	return wire.CommandResult{}, werr(404, "unknown_command", fmt.Sprintf("unknown command %s: / lists them", name))
}

// Steer tells the running turn of the manager something without stopping it; 400 empty, 409 idle.
func (t *Tab) Steer(_ context.Context, text string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.record("Steer", text); err != nil {
		return err
	}
	switch {
	case strings.TrimSpace(text) == "":
		return werr(400, "empty", "a steer needs text")
	case !t.running:
		return werr(409, "idle", "no turn is running: send it as a message")
	}
	t.emitLocked(&wire.Steer{To: "mgr", Text: text})
	return nil
}

// Interrupt cancels the running turn and pauses the goal; 409 idle when nothing runs.
func (t *Tab) Interrupt(_ context.Context, target string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.record("Interrupt", target); err != nil {
		return err
	}
	if !t.running {
		return werr(409, "idle", "no turn is running")
	}
	t.emitLocked(&wire.Interrupt{ID: "turn"})
	if t.goal == "active" {
		t.emitLocked(&wire.Goal{S: "paused", Objective: Goal, Paused: "you interrupted it"})
	}
	t.emitLocked(&wire.State{ID: "mgr", S: "idle", Doing: "waits for you"}, &wire.Turn{S: "end"})
	return nil
}

// Rename changes the tab's name; 400 bad_name for an empty name, one over 60 characters or one with control characters.
func (t *Tab) Rename(_ context.Context, name string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.record("Rename", name); err != nil {
		return err
	}
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 60 || strings.ContainsFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return werr(400, "bad_name", "a name is 1 to 60 characters without control characters")
	}
	t.sum.Name = name
	t.host.Publish(Frames("tab", "", wire.TabFrame{Op: "update", Tab: t.sum}))
	return nil
}

// Restart is the restart family: a new generation, with the conversation (restart, model, roles) or without it (new, clear, swarm).
func (t *Tab) Restart(_ context.Context, req wire.RestartRequest) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.record("Restart", req); err != nil {
		return err
	}
	switch req.Kind {
	case "new", "clear", "swarm", "restart", "model", "roles":
	default:
		return werr(400, "bad_flags", "unknown restart kind "+fmt.Sprintf("%q", req.Kind))
	}
	if req.Kind == "swarm" && (req.Swarm == nil || *req.Swarm < 0 || *req.Swarm > 32) {
		return werr(400, "bad_flags", "--swarm wants a number of workers from 0 to 32")
	}
	fresh := req.Fresh || req.Kind == "new" || req.Kind == "clear" || req.Kind == "swarm"
	t.sum.Gen++
	if fresh {
		t.sum.SID = fmt.Sprintf("20261009-2310%02d-%06x", t.sum.Gen%100, 0xbeef00+int(t.sum.Gen))
		t.keyframe, t.journal, t.seq, t.now = nil, nil, 0, 0
		t.open, t.queue, t.goal = nil, nil, ""
	}
	t.running = false
	if req.Swarm != nil {
		n := *req.Swarm
		t.roster = rosterOf(n)
		t.meta.Swarm = &n
	}
	if req.Model != "" {
		m := req.Model
		t.meta.Model = &m
	}
	if len(req.RoleModels) > 0 {
		rm := map[string]string{}
		for k, v := range req.RoleModels {
			rm[k] = v
		}
		t.meta.RoleModels = &rm
	}
	run, q := false, []wire.QueuedLine{}
	t.meta.Running, t.meta.Queued = &run, &q
	t.host.Publish(Frames("reset", t.sum.ID, wire.ResetFrame{Tab: t.sum.ID, Gen: t.sum.Gen}))
	t.host.Publish(Frames("tab", "", wire.TabFrame{Op: "update", Tab: t.sum}))
	t.host.Publish(Frames("roster", t.sum.ID, wire.RosterFrame{Tab: t.sum.ID, Roster: t.roster}))
	t.sysLocked("↺", fmt.Sprintf("restarted (%s): generation %d", req.Kind, t.sum.Gen))
	return nil
}

// rosterOf is the roster of a team of n workers: the manager and the first of the canned workers, then more backends.
func rosterOf(n int) []wire.RosterEntry {
	base := Roster()
	out := []wire.RosterEntry{base[0]}
	for k := 1; k <= n; k++ {
		if k < len(base) {
			out = append(out, base[k])
			continue
		}
		out = append(out, wire.RosterEntry{ID: fmt.Sprintf("be-%d", k-2), Role: "backend", Code: "be", Nth: k - 2, K: k, Leg: (k - 1) % 8, Scope: "**", Model: Model, Spawn: float64(10 + k)})
	}
	return out
}

// Goal sets, pauses, resumes or clears the standing goal, with the API's errors.
func (t *Tab) Goal(_ context.Context, req wire.GoalRequest) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.record("Goal", req); err != nil {
		return err
	}
	switch req.Action {
	case "set":
		if strings.TrimSpace(req.Text) == "" {
			return werr(400, "empty", "/goal needs text")
		}
		t.emitLocked(&wire.Say{Who: "you", Text: "/goal " + req.Text}, &wire.Goal{S: "active", Objective: req.Text, Max: 20},
			&wire.Say{Who: "sys", Glyph: "◇", Text: "goal set; plan:", Plan: true})
	case "pause", "resume", "clear":
		if t.goal == "" {
			return werr(409, "no_goal", "there is no goal")
		}
		switch {
		case req.Action == "pause" && t.goal != "active":
			return werr(409, "not_active", "the goal is not active")
		case req.Action == "resume" && t.goal != "paused":
			return werr(409, "not_paused", "the goal is not paused")
		}
		state := map[string]string{"pause": "paused", "resume": "active", "clear": "cleared"}[req.Action]
		g := &wire.Goal{S: state, Objective: Goal, Max: 20}
		if state == "paused" {
			g.Paused = "you paused it"
		}
		t.emitLocked(g)
	default:
		return werr(400, "bad_request", "goal action must be set, pause, resume or clear")
	}
	return nil
}

// modeNote is the acknowledgment row of a permission mode.
func modeNote(mode string) (glyph, text string) {
	switch mode {
	case "plan":
		return "◇", "mode: plan (read-only)"
	case "bypass":
		return "⚠", "mode: bypass (dangerous: nothing asks except the hard denies)"
	case "yolo":
		return "⚠", "mode: yolo (dangerous: nothing asks at all)"
	}
	return "◇", "mode: " + mode
}

// SetMode changes the permission mode; 400 bad_mode for an unknown one. The route checked the confirmation for bypass and yolo.
func (t *Tab) SetMode(_ context.Context, req wire.ModeRequest) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.record("SetMode", req); err != nil {
		return err
	}
	switch req.Mode {
	case "default", "accept-edits", "plan", "bypass", "yolo":
	default:
		return werr(400, "bad_mode", "mode must be default, accept-edits, plan, bypass or yolo")
	}
	m := req.Mode
	t.patchLocked(wire.MetaPatch{Mode: &m})
	g, text := modeNote(m)
	t.sysLocked(g, text)
	return nil
}

// SetModel changes the manager's model or a role's; 422 model for a reference that is not provider/model.
func (t *Tab) SetModel(_ context.Context, req wire.ModelRequest) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.record("SetModel", req); err != nil {
		return err
	}
	if p, m, ok := strings.Cut(req.Ref, "/"); !ok || p == "" || m == "" {
		return werr(422, "model", fmt.Sprintf("unknown model %q: write it as provider/model", req.Ref))
	}
	if req.Role == "" || req.Role == "manager" {
		ref := req.Ref
		t.patchLocked(wire.MetaPatch{Model: &ref})
		t.sysLocked("◇", "model: "+req.Ref+" (the conversation carries over; the prompt cache starts over)")
		return nil
	}
	rm := map[string]string{}
	if t.meta.RoleModels != nil {
		for k, v := range *t.meta.RoleModels {
			rm[k] = v
		}
	}
	rm[req.Role] = req.Ref
	t.patchLocked(wire.MetaPatch{RoleModels: &rm})
	t.sysLocked("◇", "role "+req.Role+": "+req.Ref)
	return nil
}

// SetEffort changes the reasoning effort; 400 bad_level for an unknown level.
func (t *Tab) SetEffort(_ context.Context, req wire.EffortRequest) (wire.EffortResult, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.record("SetEffort", req); err != nil {
		return wire.EffortResult{}, err
	}
	switch req.Level {
	case "default", "minimal", "low", "medium", "high", "xhigh":
	default:
		return wire.EffortResult{}, werr(400, "bad_level", "effort must be default, minimal, low, medium, high or xhigh")
	}
	l := req.Level
	t.patchLocked(wire.MetaPatch{Effort: &l})
	t.sysLocked("◇", "reasoning effort: "+l+" (closest supported level)")
	return wire.EffortResult{Requested: l, Applied: l}, nil
}

// SetBudget changes the budget; 400 bad_budget for a negative amount.
func (t *Tab) SetBudget(_ context.Context, req wire.BudgetRequest) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.record("SetBudget", req); err != nil {
		return err
	}
	if req.USD < 0 {
		return werr(400, "bad_budget", "a budget is a number of dollars, or off")
	}
	usd := req.USD
	if req.Off {
		usd = 0
	}
	t.patchLocked(wire.MetaPatch{Budget: &usd})
	if req.Off || usd == 0 {
		t.sysLocked("◇", "budget: off")
	} else {
		t.sysLocked("◇", fmt.Sprintf("budget: $%.2f for the turns from now on", usd))
	}
	return nil
}

// Compact folds the manager's thread now and emits the compact event.
func (t *Tab) Compact(_ context.Context, req wire.CompactRequest) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.record("Compact", req); err != nil {
		return err
	}
	t.emitLocked(&wire.Compact{ID: "mgr", From: 41000, To: 12000, Pct: -71})
	return nil
}

// StageLaunch stages flags for the next start of the team; 400 bad_isolation for an unknown isolation.
func (t *Tab) StageLaunch(_ context.Context, p wire.LaunchPatch) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.record("StageLaunch", p); err != nil {
		return err
	}
	var mp wire.MetaPatch
	if p.Isolation != nil {
		if *p.Isolation != "none" && *p.Isolation != "worktree" {
			return werr(400, "bad_isolation", "isolation must be none or worktree")
		}
		mp.Isolation = p.Isolation
		t.sysLocked("◇", "--isolation "+*p.Isolation+" (applies when the team starts again)")
	}
	if p.Verify != nil {
		mp.Verify = p.Verify
		t.sysLocked("◇", fmt.Sprintf("--verify %q", *p.Verify))
	}
	if p.Commit != nil {
		mp.Commit = p.Commit
	}
	if p.Mailman != nil {
		mp.Mailman = p.Mailman
	}
	if p.NoMcp != nil {
		mp.NoMcp = p.NoMcp
	}
	if p.TrustProject != nil {
		mp.TrustProject = p.TrustProject
	}
	t.patchLocked(mp)
	return nil
}

// AddRule adds a session rule ("tests" expands to the tests preset); 400 bad_rule for an empty rule or an unknown effect.
func (t *Tab) AddRule(_ context.Context, req wire.RuleRequest) (wire.RuleResult, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.record("AddRule", req); err != nil {
		return wire.RuleResult{}, err
	}
	effect := firstNonEmpty(req.Effect, "allow")
	rule := strings.TrimSpace(req.Rule)
	if rule == "" || (effect != "allow" && effect != "deny" && effect != "ask") {
		return wire.RuleResult{}, werr(400, "bad_rule", "a rule needs text, e.g. tests or Bash(go test:*)")
	}
	rules := []string{rule}
	origin := firstNonEmpty(req.Origin, "this session")
	if rule == "tests" {
		rules, origin = TestsPreset(), "the tests preset"
	}
	all := append([]wire.Rule{}, t.rules...)
	for _, r := range rules {
		all = append(all, wire.Rule{Effect: effect, Rule: r, Origin: origin})
		t.sysLocked("◇", fmt.Sprintf("%s this session: %s · from %s", effect, r, origin))
	}
	t.patchLocked(wire.MetaPatch{Rules: &all})
	return wire.RuleResult{Added: len(rules)}, nil
}

// RemoveRule removes a session rule; 404 no_rule when it is not there, 409 fixed when it comes from a file.
func (t *Tab) RemoveRule(_ context.Context, req wire.RuleRequest) (wire.RuleResult, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.record("RemoveRule", req); err != nil {
		return wire.RuleResult{}, err
	}
	for i, r := range t.rules {
		if r.Rule != req.Rule || (req.Effect != "" && r.Effect != req.Effect) {
			continue
		}
		if r.Fixed || r.File != "" || r.Origin == "--allow flag" || r.Origin == "project config" || r.Origin == "user config" {
			return wire.RuleResult{}, werr(409, "fixed", "built in or from a file: change it there")
		}
		rest := append(append([]wire.Rule{}, t.rules[:i]...), t.rules[i+1:]...)
		t.patchLocked(wire.MetaPatch{Rules: &rest})
		t.sysLocked("◇", "rule removed: "+req.Rule)
		return wire.RuleResult{Removed: 1}, nil
	}
	return wire.RuleResult{}, werr(404, "no_rule", "no such rule")
}

// Rules lists the rules in force for the tab with their origins.
func (t *Tab) Rules(context.Context) ([]wire.Rule, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.record("Rules", nil); err != nil {
		return nil, err
	}
	return append([]wire.Rule{}, t.rules...), nil
}

// Slash lists the slash commands of the tab: the built-ins, then its custom commands.
func (t *Tab) Slash(context.Context) ([]wire.SlashEntry, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.record("Slash", nil); err != nil {
		return nil, err
	}
	return Slash(), nil
}

// ---- the continuation ----------------------------------------------------------------------------------------------------------

// Step publishes the next event of the continuation and reports whether there was one.
func (t *Tab) Step() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.stepLocked()
}

// stepLocked is Step for a caller that holds the tab's lock.
func (t *Tab) stepLocked() bool {
	if t.pos >= len(t.live) {
		return false
	}
	e := t.live[t.pos]
	t.pos++
	at := wire.HeadOf(e).T
	if at > t.now {
		t.now = at
	}
	wire.Stamp(e, at, 0)
	t.journalLocked(e, true)
	return true
}

// Remaining is the number of events of the continuation that have not been published.
func (t *Tab) Remaining() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.live) - t.pos
}

// NextAt is the session time of the next event of the continuation, and false when there is none.
func (t *Tab) NextAt() (float64, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.pos >= len(t.live) {
		return 0, false
	}
	return wire.HeadOf(t.live[t.pos]).T, true
}

// Reset returns the tab to the snapshot: the history, the open question, the continuation ready to be played again. It publishes
// reset, so that connected pages fetch the snapshot again.
func (t *Tab) Reset() {
	t.load(t.mk())
	t.mu.Lock()
	gen := t.sum.Gen
	t.mu.Unlock()
	t.host.Publish(Frames("reset", t.sum.ID, wire.ResetFrame{Tab: t.sum.ID, Gen: gen}))
}

// Ask opens the canned later question now (the tester wants to run the tests), unless it is already open.
func (t *Tab) Ask() {
	q := LaterQuestion()
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, o := range t.open {
		if o.q.ID == q.ID {
			return
		}
	}
	t.emitLocked(&wire.Ask{Q: q}, &wire.State{ID: q.Agent, S: "ask", Doing: "asks: " + q.Cmd, Task: &q.Task})
}
