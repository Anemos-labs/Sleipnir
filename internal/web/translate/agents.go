package translate

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/swarm"
	"github.com/anemos-labs/sleipnir/internal/tui/state"
	"github.com/anemos-labs/sleipnir/internal/web/approvals"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// agentOut is what was last sent about one agent, and a state event held back by the rate limit.
type agentOut struct {
	st      *StateX // the last state sent
	lastT   float64
	pend    *StateX
	use     *UseX
	layers  *[6]int
	service bool
}

// holdInfo is a tool start seen through the sink and not yet in the log: until the log's tool.call of it is folded, the state the
// log implies for the agent is older than the one the sink gave, and is not sent.
type holdInfo struct {
	tid   string
	since float64
}

// openQ is an open question and when it was asked.
type openQ struct {
	q  wire.Question
	at time.Time
}

// stateGap is the least time between two state events of an agent (at most 20 a second, VOCAB.md 14).
const stateGap = 0.05

// agentOutOf returns the record of an agent (UI id).
func (t *Translator) agentOutOf(uid string) *agentOut {
	a := t.d.ags[uid]
	if a == nil {
		a = &agentOut{}
		t.d.ags[uid] = a
	}
	return a
}

// harnessID is the id the State knows an agent (UI id) by.
func (t *Translator) harnessID(uid string) string {
	if h, ok := t.d.hid[uid]; ok {
		return h
	}
	return uid
}

// seen records the harness id of an agent and makes sure the roster names it; it returns the UI id, and "" for an id that is nobody
// of the vocabulary.
func (t *Translator) seen(hid string) string {
	if nonAgent(hid) {
		return ""
	}
	uid := uiID(hid)
	if uid != hid {
		t.d.hid[uid] = hid
	}
	t.ensureRoster(uid)
	return uid
}

// sameState reports whether two state events say the same thing.
func sameState(a, b *StateX) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.S != b.S || a.Doing != b.Doing {
		return false
	}
	if (a.Task == nil) != (b.Task == nil) || (a.Task != nil && *a.Task != *b.Task) {
		return false
	}
	if (a.ReqSince == nil) != (b.ReqSince == nil) || (a.ReqSince != nil && *a.ReqSince != *b.ReqSince) {
		return false
	}
	return true
}

// offerState sends a state event for an agent unless it says what the last one said; one that comes sooner than stateGap after the
// last is held back (a later one replaces it) and sent by the clock. force sends it whatever it says.
func (t *Translator) offerState(uid string, s *StateX, ts float64, force bool) {
	a := t.agentOutOf(uid)
	if a.service {
		return
	}
	if !force && sameState(a.st, s) {
		a.pend = nil
		return
	}
	if !force && a.st != nil && ts-a.lastT < stateGap {
		a.pend = s
		return
	}
	t.sendState(uid, a, s, ts)
}

// sendState journals a state event.
func (t *Translator) sendState(uid string, a *agentOut, s *StateX, ts float64) {
	a.pend = nil
	s.ID = uid
	c := *s
	a.st = &c
	if t.d.history {
		return // the history's states are sent once, at its end
	}
	t.put(s, ts, 0, nil)
	a.lastT = t.j.lastT
}

// flushStates sends the held-back state events whose gap has passed.
func (t *Translator) flushStates(ts float64) {
	for _, uid := range sortedKeys(t.d.ags) {
		if a := t.d.ags[uid]; a.pend != nil && ts-a.lastT >= stateGap {
			t.sendState(uid, a, a.pend, ts)
		}
	}
}

// refreshAgent derives an agent's state from the State and offers it, unless a sink tool start holds it.
func (t *Translator) refreshAgent(hid string, ts float64, force bool) {
	uid := uiID(hid)
	if h, ok := t.d.hold[uid]; ok && !force {
		if ts-h.since < 2 {
			return
		}
		delete(t.d.hold, uid)
	}
	a, ok := t.st.AgentLite(hid)
	if !ok {
		return
	}
	if a.Service || a.Role == swarm.MailmanRoleName {
		t.agentOutOf(uid).service = true
		t.dropRoster(uid)
		return
	}
	t.offerState(uid, t.stateOf(uid, a), ts, force)
}

// stateOf maps an agent of the State to its state event (VOCAB.md 8.3).
func (t *Translator) stateOf(uid string, a state.Agent) *StateX {
	var s, doing string
	switch a.Status {
	case state.StatusStarting, state.StatusThinking:
		s, doing = "think", firstNonEmpty(a.Line, "thinking")
	case state.StatusTool:
		s, doing = "tool", strings.TrimSpace(displayName(a.Tool)+" "+t.summaryLine(a.ToolSummary))
	case state.StatusEditing:
		s, doing = "edit", strings.TrimSpace(displayName(a.Tool)+" "+t.summaryLine(a.ToolSummary))
	case state.StatusWaiting:
		s, doing = "wait", firstNonEmpty(a.Line, "waits")
	case state.StatusAsking:
		s, doing = "ask", "wants to run `"+t.askCmd(uid, a.ID)+"`"
	case state.StatusIdle:
		s, doing = "idle", "waits for work"
		if uid == "mgr" {
			doing = "waits at the prompt"
		}
	case state.StatusStuck:
		s, doing = "stuck", firstNonEmpty(a.Stuck.Note, "stuck")
	case state.StatusDone:
		s, doing = "done", firstNonEmpty(a.Evidence, "done")
	case state.StatusError:
		s, doing = "stuck", "failed: "+firstNonEmpty(t.d.lastErr[uid], a.Stuck.Note, a.EndState, "the run stopped")
	default:
		s, doing = "idle", "waits for work"
	}
	if q := t.openQuestionOf(uid); q != nil && s != "done" {
		s, doing = "ask", "wants to run `"+q.Cmd+"`"
	}
	if s == "idle" || s == "wait" {
		if ts, ok := t.st.Task(a.Task); ok && a.Task != "" && ts.Status == "blocked" && ts.BlockedOn != "" {
			s, doing = "wait", "blocked on "+ts.BlockedOn
		}
	}
	if uid == "mgr" && (s == "idle" || s == "done") && t.team() {
		if open, busy := t.openTasks(), t.workersBusy(); len(open) > 0 || busy {
			s, doing = "wait", "waits for the team"
			if len(open) > 0 {
				if len(open) > 8 {
					open = append(open[:8], "…")
				}
				doing += " (" + strings.Join(open, " ") + ")"
			}
		}
	}
	out := &StateX{State: wire.State{ID: uid, S: s, Doing: line(doing, capDoing)}}
	if a.Task != "" {
		task := a.Task
		out.Task = &task
	}
	if !a.ReqSince.IsZero() && (s == "think" || s == "tool" || s == "edit") {
		rs := t.sessT(a.ReqSince)
		out.ReqSince = &rs
	}
	return out
}

// summaryLine shows a tool summary with paths under the project made relative. A summary the State cut ends in the middle of a
// word, which might be the start of a secret the masker can no longer recognise: that word is dropped.
func (t *Translator) summaryLine(s string) string {
	if root := t.root(); root != "" {
		s = strings.ReplaceAll(s, strings.TrimSuffix(root, "/")+"/", "")
	}
	if cut, ok := strings.CutSuffix(s, "…"); ok {
		if i := strings.LastIndexByte(cut, ' '); i >= 0 {
			s = cut[:i] + " …"
		} else {
			s = "…"
		}
	}
	return s
}

// askCmd is the command of the agent's oldest open question: the bridge's, else the log's.
func (t *Translator) askCmd(uid, hid string) string {
	if q := t.openQuestionOf(uid); q != nil {
		return q.Cmd
	}
	if q, ok := t.st.PendingAsk(hid); ok {
		return firstNonEmpty(q.Command, strings.Join(q.Paths, ", "), q.Summary)
	}
	return "an action that needs approval"
}

// openQuestionOf is the agent's oldest open question from the bridge.
func (t *Translator) openQuestionOf(uid string) *wire.Question {
	for _, id := range t.d.qorder {
		if q := t.d.qs[id]; q != nil && q.q.Agent == uid {
			return &q.q
		}
	}
	return nil
}

// team reports whether the session is a team (a manager and workers).
func (t *Translator) team() bool { return t.st.Session().Swarm }

// openTasks lists the tasks that are neither merged nor failed, by number.
func (t *Translator) openTasks() []string {
	var out []string
	for id, o := range t.d.tasks {
		if o.ev != nil && o.ev.S != "merged" && !o.ev.Failed {
			out = append(out, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return taskLess(out[i], out[j]) })
	return out
}

// workersBusy reports whether a worker is working (its last state is neither idle nor done).
func (t *Translator) workersBusy() bool {
	for uid, a := range t.d.ags {
		if uid == "mgr" || a.service || a.st == nil {
			continue
		}
		if a.st.S != "idle" && a.st.S != "done" {
			return true
		}
	}
	return false
}

// taskLess orders task ids by number (T2 before T10).
func taskLess(a, b string) bool {
	na, nb := taskNum(a), taskNum(b)
	if na != nb {
		return na < nb
	}
	return a < b
}

// taskNum is the number of a task id (T12 is 12), or a large number for an id that is not numbered.
func taskNum(id string) int {
	if len(id) < 2 || id[0] != 'T' {
		return 1 << 30
	}
	n := 0
	for i := 1; i < len(id); i++ {
		c := id[i]
		if c < '0' || c > '9' || n > 1<<20 {
			return 1 << 30
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// sendUse sends an agent's token table when it changed (or always, with force).
func (t *Translator) sendUse(hid string, ts float64, force bool) {
	uid := uiID(hid)
	o := t.agentOutOf(uid)
	if o.service {
		return
	}
	a, ok := t.st.AgentLite(hid)
	if !ok {
		return
	}
	u := &UseX{Use: wire.Use{ID: uid, Rd: a.Tokens.CacheRead, Un: a.Tokens.Input + a.Tokens.CacheWrite, Out: a.Tokens.Output,
		Wr: a.Tokens.CacheWrite, Cost: a.CostUSD, Saved: a.SavedUSD}, SavedPartial: a.UnpricedReadTokens > 0, Unpriced: a.UnpricedReadTokens}
	if !force && o.use != nil && *o.use == *u {
		return
	}
	c := *u
	o.use = &c
	t.put(u, ts, 0, nil)
}

// sendLayers sends an agent's latest prompt by layer when it changed (or always, with force).
func (t *Translator) sendLayers(hid string, ts float64, force bool) {
	uid := uiID(hid)
	o := t.agentOutOf(uid)
	if o.service {
		return
	}
	a, ok := t.st.AgentLite(hid)
	if !ok || (len(a.Stack.Sections) == 0 && a.Stack.Unsectioned == 0) {
		return
	}
	if u := a.Stack.Unsectioned; u > 0 && (t.d.g0 == 0 || u < t.d.g0) {
		t.d.g0 = u
	}
	toks := state.LayerSplit(a, t.d.g0)
	if !force && o.layers != nil && *o.layers == toks {
		return
	}
	o.layers = &toks
	t.put(&wire.Layers{ID: uid, Toks: toks}, ts, 0, nil)
}

// fellBehind reports that sink calls were dropped, and sends every agent's state, token table and layers again.
func (t *Translator) fellBehind(ts float64) {
	t.put(&wire.Say{Who: "sys", Glyph: "⚠", Text: "the page fell behind: some activity rows were skipped"}, ts, 0, nil)
	for _, hid := range t.st.AgentIDs() {
		delete(t.d.hold, uiID(hid))
		t.sendUse(hid, ts, true)
		t.sendLayers(hid, ts, true)
		t.refreshAgent(hid, ts, true)
	}
}

// The bounds of a question's fields: the bridge's limits (internal/web/approvals), four times over for the escapes it writes for
// control and invisible characters (one byte becomes at most four).
const (
	capQuestionCmd   = 4 * approvals.MaxCommand
	capQuestionField = 4 * (16 << 10)
)

// question records an open question of the bridge and sends its ask.
func (t *Translator) question(q wire.Question, now time.Time) {
	q.Agent = uiID(q.Agent)
	// What a question shows is what the person agrees to: the bridge has already made every field whole and visible (or refused to ask),
	// so nothing here may shorten it or fold its lines. The caps only bound what a caller other than the bridge could send, and are the
	// bridge's own limits with room for every byte to be written as an escape.
	q.Cmd = text(q.Cmd, capQuestionCmd)
	q.Why = text(q.Why, capQuestionField)
	q.Scope = text(q.Scope, capQuestionField)
	q.What = text(q.What, capQuestionField)
	q.Rule = text(q.Rule, capQuestionField)
	q.Cwd = text(q.Cwd, capQuestionField)
	if q.ID == "" {
		return
	}
	if _, ok := t.d.qs[q.ID]; !ok {
		t.d.qorder = append(t.d.qorder, q.ID)
	}
	t.d.qs[q.ID] = &openQ{q: q, at: now}
	ts := t.sessT(now)
	t.ensureRoster(q.Agent)
	t.publishRoster()
	t.put(&wire.Ask{Q: q}, ts, 0, nil)
	t.refreshAgent(t.harnessID(q.Agent), ts, false)
}

// answered closes a question and sends its answer; the time it waited is not counted in its agent's running tool call.
func (t *Translator) answered(a wire.Answer, now time.Time) {
	ts := t.sessT(now)
	var who string
	if q := t.d.qs[a.QID]; q != nil {
		who = q.q.Agent
		waited := now.Sub(q.at)
		for _, r := range t.d.runs {
			if r.agent == who && waited > 0 {
				r.waited += waited
			}
		}
		delete(t.d.qs, a.QID)
		t.d.qorder = remove(t.d.qorder, a.QID)
	}
	a.Note = line(a.Note, capReason)
	a.Rule = line(a.Rule, capPath)
	if a.By == "" {
		a.By = "you"
	}
	t.put(&a, ts, 0, nil)
	if who != "" {
		t.refreshAgent(t.harnessID(who), ts, false)
	}
}

// rosterOut is the roster as last published.
type rosterOut struct {
	order []string
	ents  map[string]*wire.RosterEntry
	nextK int
	last  []byte
	dirty bool // an entry may have changed since the roster was last published
}

// ensureRoster adds an agent (UI id) to the roster with what its id says, when it is not there.
func (t *Translator) ensureRoster(uid string) {
	r := &t.d.ros
	if uid == "" || r.ents[uid] != nil || t.agentOutOf(uid).service {
		return
	}
	if code, _ := roleCode(uid); code == swarm.MailmanRole().Short {
		t.agentOutOf(uid).service = true // the harness's mailman: a service, in no roster
		return
	}
	code, nth := roleCode(uid)
	e := &wire.RosterEntry{ID: uid, Code: code, Nth: nth, Leg: -1, Scope: "**"}
	if uid == "mgr" {
		e.Role = "manager"
		r.order = append([]string{uid}, r.order...)
	} else {
		r.nextK++
		e.K, e.Leg = r.nextK, (r.nextK-1)%8
		r.order = append(r.order, uid)
	}
	r.ents[uid] = e
	r.dirty = true
}

// dropRoster removes an agent that turned out to be a service of the harness.
func (t *Translator) dropRoster(uid string) {
	r := &t.d.ros
	if r.ents[uid] == nil || uid == "mgr" {
		return
	}
	delete(r.ents, uid)
	r.order = remove(r.order, uid)
	r.dirty = true
}

// readOnlyRoles are the built-in roles that write no file.
var readOnlyRoles = func() map[string]bool {
	m := map[string]bool{}
	for name, r := range swarm.BuiltinRoles() {
		if r.ReadOnly {
			m[name] = true
		}
	}
	m[swarm.MailmanRoleName] = true
	return m
}()

// syncRoster updates the roster from the State (roles, models, scopes, spawn times) and publishes it when it changed.
func (t *Translator) syncRoster() {
	t.d.ros.dirty = true
	sess := t.st.Session()
	if m := t.d.ros.ents["mgr"]; m != nil {
		m.Model = firstNonEmpty(m.Model, sess.Model)
		m.Scope = "**"
		if sess.Swarm {
			m.Scope = "- (edits no file)"
		}
	}
	for _, a := range t.st.Roster() {
		if nonAgent(a.ID) {
			continue
		}
		uid := uiID(a.ID)
		if a.Service || a.Role == swarm.MailmanRoleName {
			t.agentOutOf(uid).service = true
			t.dropRoster(uid)
			continue
		}
		if uid != a.ID {
			t.d.hid[uid] = a.ID
		}
		t.ensureRoster(uid)
		e := t.d.ros.ents[uid]
		e.Model = firstNonEmpty(a.Model, a.Stack.Model, e.Model)
		if uid == "mgr" {
			e.Model = firstNonEmpty(e.Model, sess.Model)
			e.Scope = "**"
			if sess.Swarm {
				e.Scope = "- (edits no file)"
			}
			continue
		}
		e.Role = firstNonEmpty(a.Role, e.Role)
		e.RO = readOnlyRoles[e.Role]
		switch {
		case len(a.Scope) > 0:
			e.Scope = line(strings.Join(a.Scope, ", "), capPath)
		case e.RO:
			e.Scope = "- (read-only)"
		default:
			e.Scope = "**"
		}
		if !a.Spawned.IsZero() {
			e.Spawn = t.sessT(a.Spawned)
		}
	}
	t.publishRoster()
}

// rosterList is the roster in order: the manager first, then the workers by start order.
func (t *Translator) rosterList() []wire.RosterEntry {
	out := make([]wire.RosterEntry, 0, len(t.d.ros.order))
	for _, id := range t.d.ros.order {
		if e := t.d.ros.ents[id]; e != nil {
			out = append(out, *e)
		}
	}
	return out
}

// publishRoster sends a roster frame when the roster differs from the one last sent. It is called before the events of an agent the
// roster did not name, so that a page knows every agent an event names.
func (t *Translator) publishRoster() {
	if !t.d.ros.dirty {
		return
	}
	t.d.ros.dirty = false
	list := t.rosterList()
	b, err := json.Marshal(list)
	if err != nil || bytes.Equal(b, t.d.ros.last) {
		return
	}
	t.d.ros.last = b
	if t.cfg.Publish != nil {
		t.cfg.Publish(wire.Frame{Type: "roster", Tab: t.cfg.Tab, Data: wire.RosterFrame{Tab: t.cfg.Tab, Roster: list}, Critical: true})
	}
}
