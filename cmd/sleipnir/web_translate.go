package main

// The translator of a tab generation turns what the harness does into the page's events (VOCAB.md). The real one is
// internal/web/translate; until it is linked in (web_translate_real.go sets newTranslator), the host uses stubTranslator, which
// derives only say, sys and state from the session's sink and passes on what the host itself emits (questions, answers, the turn,
// the goal), so that the page and the tests have a working conversation.

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/provider"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/tools"
	"github.com/anemos-labs/sleipnir/internal/web/seam"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// translatorConfig is what the host gives the translator of one tab generation (the fields of translate.Config, plus the session
// the stub reads its roster from).
type translatorConfig struct {
	Tab       string
	Gen       uint64
	Root      string
	StartedAt time.Time
	// Publish sends frames to the pages; it never blocks.
	Publish func(wire.Frame)
	Now     func() time.Time
	// Verify returns the session's --verify command and whether the team is isolated.
	Verify func() (cmd string, isolated bool)
	// Session returns the generation's session (nil while it starts).
	Session func() *session.Session
}

// newTranslator makes the translator of a tab generation. web_translate_real.go replaces it with internal/web/translate.
var newTranslator = func(c translatorConfig) seam.Translator { return newStubTranslator(c) }

// frameClass is the hub class of an "ev" frame of a kind (VOCAB.md 14): critical (never dropped), coalescable with a key (a newer one
// replaces it in a slow page's queue), or ordinary.
func frameClass(tab string, ev wire.Event, firstCkpt bool) (critical, coalescable bool, key string) {
	switch e := ev.(type) {
	case *wire.Use:
		return false, true, "use/" + tab + "/" + e.ID
	case *wire.Layers:
		return false, true, "layers/" + tab + "/" + e.ID
	case *wire.Gov, *wire.Warm, *wire.Plan, *wire.Verdict, *wire.Queue:
		return false, true, wire.KindOf(ev) + "/" + tab
	case *wire.Diff:
		return false, true, "diff/" + tab + "/" + e.File
	case *wire.Ckpt:
		if firstCkpt {
			return true, false, ""
		}
		return false, true, "ckpt/" + tab + "/" + e.CID
	case *wire.More:
		return false, false, ""
	}
	return true, false, ""
}

// evFrame is the "ev" frame of an encoded event of a tab.
func evFrame(tab string, ev wire.Event, raw json.RawMessage, firstCkpt bool) wire.Frame {
	crit, coal, key := frameClass(tab, ev, firstCkpt)
	return wire.Frame{Type: "ev", Tab: tab, Data: wire.EvFrame{Tab: tab, Ev: raw}, Critical: crit, Coalescable: coal, Key: key}
}

// stubJournalEvents bounds the stub's journal; older events are dropped (a snapshot then starts later).
const stubJournalEvents = 20000

// maxMessageBytes bounds one message the stub streams; the rest is dropped.
const maxMessageBytes = 64 << 10

// stubTranslator is the stand-in translator: a journal of the events it emitted, the main agent's text as say and more, tool calls
// and notices as state and sys.
type stubTranslator struct {
	c translatorConfig

	mu      sync.Mutex
	closed  bool
	seq     uint64
	journal []wire.Raw
	ckpts   map[string]bool
	open    map[string]*stubMsg // the open message of each agent
	last    map[string]string   // the last state of each agent ("s|doing")
}

// stubMsg is a message being streamed.
type stubMsg struct {
	mid   string
	bytes int
}

// newStubTranslator returns a stub translator.
func newStubTranslator(c translatorConfig) *stubTranslator {
	if c.Now == nil {
		c.Now = time.Now
	}
	return &stubTranslator{c: c, ckpts: map[string]bool{}, open: map[string]*stubMsg{}, last: map[string]string{}}
}

// nowLocked is the session time now, in seconds rounded to milliseconds, never negative.
func (t *stubTranslator) nowLocked() float64 {
	d := t.c.Now().Sub(t.c.StartedAt).Seconds()
	if d < 0 {
		d = 0
	}
	return math.Round(d*1000) / 1000
}

// Now is the session time now, in seconds.
func (t *stubTranslator) Now() float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.nowLocked()
}

// emitLocked stamps, journals and publishes events.
func (t *stubTranslator) emitLocked(evs ...wire.Event) {
	if t.closed {
		return
	}
	for _, ev := range evs {
		if ev == nil || wire.KindOf(ev) == "" {
			continue
		}
		t.seq++
		wire.Stamp(ev, t.nowLocked(), t.seq)
		raw, err := json.Marshal(ev)
		if err != nil {
			continue
		}
		first := false
		if c, ok := ev.(*wire.Ckpt); ok {
			first = !t.ckpts[c.CID]
			t.ckpts[c.CID] = true
		}
		t.journal = append(t.journal, raw)
		if len(t.journal) > stubJournalEvents {
			t.journal = append([]wire.Raw(nil), t.journal[len(t.journal)-stubJournalEvents:]...)
		}
		if t.c.Publish != nil {
			t.c.Publish(evFrame(t.c.Tab, ev, raw, first))
		}
	}
}

// Emit appends host-originated events and publishes them.
func (t *stubTranslator) Emit(evs ...wire.Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.emitLocked(evs...)
}

// Question records an open question and emits its ask event.
func (t *stubTranslator) Question(q wire.Question) { t.Emit(&wire.Ask{Q: q}) }

// Answered closes a question and emits its answer event.
func (t *stubTranslator) Answered(a wire.Answer) {
	ev := a
	t.Emit(&ev)
}

// Journal returns the retained events, the last seq and the session time now; the stub has no keyframe.
func (t *stubTranslator) Journal() (keyframe, evs []wire.Raw, seq uint64, now float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return []wire.Raw{}, append([]wire.Raw{}, t.journal...), t.seq, t.nowLocked()
}

// Attach follows a session's log; the stub reads nothing from it.
func (t *stubTranslator) Attach(*events.Log, string) func() { return func() {} }

// Close stops the translator; later calls do nothing.
func (t *stubTranslator) Close() {
	t.mu.Lock()
	t.closed = true
	t.mu.Unlock()
}

// Sink is the agent.Sink of the session.
func (t *stubTranslator) Sink() agent.Sink { return stubSink{t} }

// NewSink is the per-agent sink constructor.
func (t *stubTranslator) NewSink(string) agent.Sink { return stubSink{t} }

// uiAgent is an agent as the page names it: the single agent is in the manager's place.
func uiAgent(id string) string {
	if id == "" || id == "main" {
		return "mgr"
	}
	return id
}

// Roster lists the manager (or single agent) and the team's workers, in the order they are named.
func (t *stubTranslator) Roster() []wire.RosterEntry {
	var s *session.Session
	if t.c.Session != nil {
		s = t.c.Session()
	}
	if s == nil {
		return []wire.RosterEntry{}
	}
	mgr := wire.RosterEntry{ID: "mgr", Role: "manager", Code: "mgr", Leg: -1, Scope: "- (edits no file)", Model: s.Model.ID}
	if s.Swarm == nil {
		mgr.Role, mgr.Scope = "agent", "**"
		return []wire.RosterEntry{mgr}
	}
	out := []wire.RosterEntry{mgr}
	snap := s.Swarm.Board.Snapshot()
	ids := make([]string, 0, len(snap.Agents))
	roles := map[string]string{}
	for _, a := range snap.Agents {
		if a.ID == "" || a.ID == s.Swarm.ManagerID() || strings.HasPrefix(a.ID, "mm-") {
			continue
		}
		ids = append(ids, a.ID)
		roles[a.ID] = a.Role
	}
	sort.Strings(ids)
	for i, id := range ids {
		code, nth := id, 0
		if c, n, ok := strings.Cut(id, "-"); ok {
			code = c
			fmt.Sscan(n, &nth)
		}
		ro := false
		if r, ok := s.Roles[roles[id]]; ok {
			ro = r.ReadOnly
		}
		scope := ""
		if ro {
			scope = "- (read-only)"
		}
		out = append(out, wire.RosterEntry{ID: id, Role: roles[id], Code: code, Nth: nth, K: i + 1, Leg: i % 8, RO: ro, Scope: scope, Model: s.Model.ID})
	}
	return out
}

// stubSink turns sink calls into say, more, state and sys events.
type stubSink struct{ t *stubTranslator }

// clip is untrusted text for the page: terminal controls removed, at most n bytes.
func clip(s string, n int) string {
	s = tools.SanitizeForTerminal(s)
	if len(s) > n {
		s = strings.ToValidUTF8(s[:n], "") + "…"
	}
	return s
}

// endLocked closes the open message of an agent.
func (t *stubTranslator) endLocked(agentID string, reset bool) {
	if m := t.open[agentID]; m != nil {
		delete(t.open, agentID)
		t.emitLocked(&wire.More{Mid: m.mid, End: true, Reset: reset})
	}
}

// stateLocked emits an agent's state when it changed.
func (t *stubTranslator) stateLocked(agentID, s, doing string) {
	id := uiAgent(agentID)
	key := s + "|" + doing
	if t.last[id] == key {
		return
	}
	t.last[id] = key
	t.emitLocked(&wire.State{ID: id, S: s, Doing: clip(doing, 120)})
}

// Text streams the main agent's answer: the first delta opens a say, later ones continue it.
func (s stubSink) Text(agentID, delta string) {
	if delta == "" {
		return
	}
	t := s.t
	t.mu.Lock()
	defer t.mu.Unlock()
	if uiAgent(agentID) != "mgr" {
		return
	}
	delta = tools.SanitizeForTerminal(delta)
	m := t.open[agentID]
	if m == nil {
		m = &stubMsg{mid: fmt.Sprintf("m%d", t.seq+1)}
		t.open[agentID] = m
		m.bytes = len(delta)
		t.emitLocked(&wire.Say{Who: "mgr", Text: delta, Stream: true, Rate: 240, Mid: m.mid})
		return
	}
	if m.bytes >= maxMessageBytes {
		return
	}
	m.bytes += len(delta)
	t.emitLocked(&wire.More{Mid: m.mid, Text: delta})
}

// Thinking shows the agent thinking.
func (s stubSink) Thinking(agentID, _ string) {
	s.t.mu.Lock()
	defer s.t.mu.Unlock()
	s.t.stateLocked(agentID, "think", "thinking")
}

// ToolStart ends the agent's message and shows the call it makes.
func (s stubSink) ToolStart(agentID string, call core.Block) {
	t := s.t
	t.mu.Lock()
	defer t.mu.Unlock()
	t.endLocked(agentID, false)
	st := "tool"
	switch call.ToolName {
	case "write", "edit", "apply_patch":
		st = "edit"
	}
	t.stateLocked(agentID, st, toolTitle(call.ToolName)+" "+oneLineCLI(string(call.Input), 80))
}

// ToolEnd shows the agent thinking again.
func (s stubSink) ToolEnd(agentID string, _ core.Block, _ *tools.Result, _ time.Duration) {
	s.t.mu.Lock()
	defer s.t.mu.Unlock()
	s.t.stateLocked(agentID, "think", "thinking")
}

// Response ends the agent's message.
func (s stubSink) Response(agentID string, _ *provider.Response, _ float64) {
	s.t.mu.Lock()
	defer s.t.mu.Unlock()
	s.t.endLocked(agentID, false)
}

// Notice is a sys row in the agent's channel.
func (s stubSink) Notice(agentID, level, msg string) {
	glyph := "◇"
	if level == "warn" || level == "error" {
		glyph = "⚠"
	}
	ch := uiAgent(agentID)
	ag := ""
	if ch != "mgr" {
		ag = ch
	}
	s.t.Emit(&wire.Sys{Ch: ch, Glyph: glyph, Text: clip(msg, 300), Ag: ag})
}

// Reset ends a message whose request is being retried.
func (s stubSink) Reset(agentID string) {
	s.t.mu.Lock()
	defer s.t.mu.Unlock()
	s.t.endLocked(agentID, true)
}

// toolTitle is a harness tool's display name (VOCAB.md 8.2).
func toolTitle(name string) string {
	switch name {
	case "bash", "bash_output", "bash_kill":
		return "Bash"
	case "edit", "apply_patch":
		return "Edit"
	case "web_fetch":
		return "WebFetch"
	case "web_search":
		return "WebSearch"
	case "task":
		return "TaskBoard"
	case "note":
		return "Notes"
	}
	if name == "" {
		return "Tool"
	}
	return strings.ToUpper(name[:1]) + name[1:]
}
