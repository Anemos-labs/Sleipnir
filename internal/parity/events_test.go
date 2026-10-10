package parity

// The events dimension. An event of the session's log (internal/events) reaches a person through one of the two interfaces only if
// the code of that interface does something with its type. This file lists, from the source and not by hand, the event types each
// interface has code for and compares the two lists (Differences.Check); what differs is entered, with its reason, in
// contract/events.json.
//
// The terminal interface reads events in one place: the State of internal/tui/state, which the chat program and the cockpit fold
// the log into (internal/tui/app only hands events to it). The web interface reads them in internal/web/translate, which folds the
// same events into a State of its own as well, and in the log cache of the Workspace pane (internal/web/wsvc). So a type counts as
// handled by the web interface when the translator has code for it, and also when the translator shows what the State makes of it
// without naming it (an agent's state, the governor's gauge): that route is not claimed but measured, by feeding a recording one
// event of the type and one of an unknown type and comparing what the translator sends the page (the probes below).

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/web/translate"
)

// ---- reading the code ---------------------------------------------------------------------------------------------------------

// repoRoot is the root of the repository, found from the contract directory.
func repoRoot() string { return filepath.Join(Dir(), "..", "..", "..") }

// nonTestFiles lists the Go files of a directory of the repository (not its subdirectories) that are not tests, in order.
func nonTestFiles(t testing.TB, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(filepath.Join(repoRoot(), dir))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		if n := e.Name(); !e.IsDir() && strings.HasSuffix(n, ".go") && !strings.HasSuffix(n, "_test.go") {
			out = append(out, filepath.Join(repoRoot(), dir, n))
		}
	}
	return out
}

// parseFile parses a Go file without resolving objects.
func parseFile(t testing.TB, path string) *ast.File {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// constTable maps "package.Name" to the value of every string constant declared in the directories it was loaded from.
type constTable map[string]string

// loadConsts reads the string constants (a name bound to a string literal) of the packages in the directories.
func loadConsts(t testing.TB, dirs ...string) constTable {
	t.Helper()
	tbl := constTable{}
	for _, dir := range dirs {
		for _, path := range nonTestFiles(t, dir) {
			f := parseFile(t, path)
			for _, d := range f.Decls {
				g, ok := d.(*ast.GenDecl)
				if !ok || g.Tok != token.CONST {
					continue
				}
				for _, s := range g.Specs {
					vs := s.(*ast.ValueSpec)
					if len(vs.Names) != len(vs.Values) {
						continue
					}
					for i, n := range vs.Names {
						if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
							if v, err := strconv.Unquote(lit.Value); err == nil {
								tbl[f.Name.Name+"."+n.Name] = v
							}
						}
					}
				}
			}
		}
	}
	return tbl
}

// importNames maps the name a file uses for each package it imports to that package's name (the last element of its path).
func importNames(f *ast.File) map[string]string {
	m := map[string]string{}
	for _, im := range f.Imports {
		p, err := strconv.Unquote(im.Path.Value)
		if err != nil {
			continue
		}
		base := p[strings.LastIndex(p, "/")+1:]
		name := base
		if im.Name != nil {
			name = im.Name.Name
		}
		m[name] = base
	}
	return m
}

// value is the string an expression stands for: a literal, a constant of the file's own package or a constant of an imported one.
func (c constTable) value(f *ast.File, e ast.Expr) (string, bool) {
	switch x := e.(type) {
	case *ast.ParenExpr:
		return c.value(f, x.X)
	case *ast.BasicLit:
		if x.Kind != token.STRING {
			return "", false
		}
		v, err := strconv.Unquote(x.Value)
		return v, err == nil
	case *ast.Ident:
		v, ok := c[f.Name.Name+"."+x.Name]
		return v, ok
	case *ast.SelectorExpr:
		if id, ok := x.X.(*ast.Ident); ok {
			if pkg, ok := importNames(f)[id.Name]; ok {
				v, ok := c[pkg+"."+x.Sel.Name]
				return v, ok
			}
		}
	}
	return "", false
}

// isTypeOfEvent reports whether an expression reads the Type of something (e.Type, ev.Type): the shape of every dispatch on an
// event's type.
func isTypeOfEvent(e ast.Expr) bool {
	sel, ok := e.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "Type"
}

// handling is what the code of one interface does with the type of an event.
type handling struct {
	// handled are the types with code of their own: a case of a switch on an event's Type whose body does something, or a comparison
	// of an event's Type with the type.
	handled map[string]bool
	// ignored are the types a switch on an event's Type names with an empty body: known, and deliberately nothing.
	ignored map[string]bool
}

// scanHandling reads the non-test Go files of the directories and collects what they do with event types. A type is named by the
// constant of package events, by any other constant that holds its name, or by its literal string; all are the same type.
func scanHandling(t testing.TB, tbl constTable, dirs ...string) handling {
	t.Helper()
	h := handling{handled: map[string]bool{}, ignored: map[string]bool{}}
	for _, dir := range dirs {
		for _, path := range nonTestFiles(t, dir) {
			f := parseFile(t, path)
			ast.Inspect(f, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.SwitchStmt:
					if x.Tag == nil || !isTypeOfEvent(x.Tag) {
						return true
					}
					for _, c := range x.Body.List {
						cc := c.(*ast.CaseClause)
						for _, e := range cc.List {
							if v, ok := tbl.value(f, e); ok {
								if len(cc.Body) == 0 {
									h.ignored[v] = true
								} else {
									h.handled[v] = true
								}
							}
						}
					}
				case *ast.BinaryExpr:
					if x.Op != token.EQL && x.Op != token.NEQ {
						return true
					}
					for _, pair := range [][2]ast.Expr{{x.X, x.Y}, {x.Y, x.X}} {
						if isTypeOfEvent(pair[0]) {
							if v, ok := tbl.value(f, pair[1]); ok {
								h.handled[v] = true
							}
						}
					}
				}
				return true
			})
		}
	}
	return h
}

// declaredTypes are the event types of package events: the value of each Type* constant, with its name.
func declaredTypes(tbl constTable) map[string]string {
	out := map[string]string{}
	for k, v := range tbl {
		if name, ok := strings.CutPrefix(k, "events."); ok && strings.HasPrefix(name, "Type") {
			out[v] = name
		}
	}
	return out
}

// The directories whose code each interface reads events in.
var (
	terminalEventDirs = []string{"internal/tui/state", "internal/tui/app"}
	webEventDirs      = []string{"internal/web/translate", "internal/web/wsvc"}
	// constDirs are the packages whose string constants name event types (package events, the State's own, and the producers that
	// export theirs).
	constDirs = []string{"internal/events", "internal/tui/state", "internal/swarm", "internal/workspace"}
)

// ---- the contract ---------------------------------------------------------------------------------------------------------------

// eventsContract is contract/events.json: the differences of the dimension and the two lists that explain the rest.
type eventsContract struct {
	Differences
	// ViaState are the types the web interface shows through the State it shares with the terminal, without naming them: the types
	// that the translator's output changes for, as measured by the probes. The list must be exactly that set.
	ViaState []Entry `json:"via_state,omitempty"`
	// Ignored are the types that neither interface shows anything for: the State names them with an empty case ("known, and nothing
	// the UI shows") and the translator has no code for them.
	Ignored []Entry `json:"ignored,omitempty"`
}

// eventInput is everything the comparison reads from the code and from the probes.
type eventInput struct {
	declared map[string]string // event type -> the name of its constant in package events
	term     handling
	web      handling
	// surfaced says, for each type a probe exists for, whether the translator's output changed when it was fed an event of the type.
	surfaced map[string]bool
}

// entrySet checks a list of entries of the contract, the way Differences.Check checks its own, and returns the items.
func entrySet(list string, es []Entry, add func(string, ...any)) map[string]bool {
	out := map[string]bool{}
	for _, e := range es {
		switch {
		case e.Item == "":
			add("an entry under %s has no item", list)
			continue
		case len(strings.TrimSpace(e.Reason)) < minReason:
			add("%s under %s needs a reason (at least %d characters)", quote(e.Item), list, minReason)
		}
		if out[e.Item] {
			add("%s is listed twice under %s", quote(e.Item), list)
		}
		out[e.Item] = true
	}
	return out
}

// check compares the two interfaces and returns every problem found, in a stable order.
func (c eventsContract) check(in eventInput) []string {
	var out []string
	add := func(format string, args ...any) { out = append(out, "events: "+fmt.Sprintf(format, args...)) }

	// Every event type must be named by the code of one interface at least: a type nobody has looked at is a type added without
	// asking whether anyone can see it.
	universe := map[string]bool{}
	for v := range in.declared {
		universe[v] = true
	}
	for _, s := range []map[string]bool{in.term.handled, in.term.ignored, in.web.handled, in.web.ignored} {
		for v := range s {
			universe[v] = true
		}
	}
	named := func(v string) bool {
		return in.term.handled[v] || in.term.ignored[v] || in.web.handled[v] || in.web.ignored[v]
	}
	for _, v := range sorted(universe) {
		if !named(v) {
			add("%s (events.%s) is named by the code of neither interface: handle it in the State (internal/tui/state) and the translator (internal/web/translate), or name it in the State's list of events that show nothing and list it under ignored in contract/events.json", quote(v), in.declared[v])
		}
	}

	// The types the terminal has code for and the translator has none for: shown by the web interface only if the probe says so.
	surfaced := map[string]bool{}
	for _, v := range sorted(in.term.handled) {
		if in.web.handled[v] {
			continue
		}
		s, probed := in.surfaced[v]
		switch {
		case !probed:
			add("%s is handled by the terminal interface and not named by the translator, and no probe tells whether the page shows it through the State: add a probe for it to eventProbes in events_test.go", quote(v))
		case s:
			surfaced[v] = true
		}
	}
	via := entrySet("via_state", c.ViaState, add)
	for _, v := range sorted(surfaced) {
		if !via[v] {
			add("%s reaches the page through the shared State (the translator's output changes when it is fed an event of the type): list it under via_state in contract/events.json, with what the page shows of it", quote(v))
		}
	}
	for _, e := range c.ViaState {
		if e.Item != "" && !surfaced[e.Item] {
			add("%s under via_state is stale: the translator's output does not change when it is fed an event of the type, so the page does not show it through the State; remove it, and list it under terminal_only or gaps", quote(e.Item))
		}
	}

	termList := sorted(in.term.handled)
	webSet := map[string]bool{}
	for v := range in.web.handled {
		webSet[v] = true
	}
	for v := range surfaced {
		webSet[v] = true
	}
	out = append(out, c.Differences.Check("events", termList, sorted(webSet))...)

	// The types nobody shows anything for.
	ign := entrySet("ignored", c.Ignored, add)
	nothing := map[string]bool{}
	for v := range universe {
		if named(v) && !in.term.handled[v] && !webSet[v] {
			nothing[v] = true
		}
	}
	for _, v := range sorted(nothing) {
		if !ign[v] {
			add("%s is known to the code of an interface that shows nothing for it, and the other interface has no code for it: list it under ignored in contract/events.json, with the reason that nobody shows it", quote(v))
		}
	}
	for _, e := range c.Ignored {
		if e.Item != "" && !nothing[e.Item] {
			add("%s under ignored is stale: an interface shows something for it now (or it no longer exists); remove it", quote(e.Item))
		}
	}
	return out
}

// ---- the probes -------------------------------------------------------------------------------------------------------------------

// pev is an event of a probe's log.
type pev struct {
	agent, typ string
	data       any
}

// probeCase is one way of feeding the translator an event of a type: the events that must come before it (the same in the control),
// and the event itself.
type probeCase struct {
	setup []pev
	ev    pev
}

// controlType is a type no code knows: the control of a probe carries the probed event with it.
const controlType = "zz.control"

// The times of a probe's log, from a fixed start: the recording's events 10 ms apart, the setup of a case after them, the event
// under test half a second after that, and a last event of an unknown type eight seconds later, whose arrival makes the translator's
// clock pass the rate limits of what it holds back (a state event 50 ms, the governor's gauge 5 s).
const (
	probeStep    = 10 * time.Millisecond
	probeSetupAt = 200 * time.Millisecond
	probeEventAt = 500 * time.Millisecond
	probeEndAt   = 8 * time.Second
)

// probeBoardOp is a board.op of one task in the board's full form.
func probeBoardOp(op, task, status, owner string, rev uint64) map[string]any {
	return map[string]any{"op": op, "version": rev + 1, "task": task, "status": status, "owner": owner, "line": "", "result": "", "evidence": "",
		"attempts": 1, "rev": rev, "files": []string{"api/" + task + "/**"}, "closure": nil, "blocked_on": "", "verification_failures": 0,
		"title": "catalogue", "role": "backend", "deps": []string{}}
}

// probeRecording is a small team's log up to the moment the event under test arrives: a manager that has answered once, a task and a
// worker that has claimed it and has a model request in flight, in an isolated team.
func probeRecording() []pev {
	req := func(id string) map[string]any {
		return map[string]any{"req": id, "kind": "main", "model": "m1", "prefix_key": "pk1",
			"sections": []map[string]any{{"name": "shared", "tokens": 1000, "hash": "h1"}, {"name": "role", "tokens": 200}}}
	}
	return []pev{
		{"", "session.start", map[string]any{"model": "m1", "swarm": true, "root": "/work/p", "isolation": "worktree"}},
		{"mgr", "user.input", map[string]any{"text": "Build"}},
		{"swarm", "agent.spawn", map[string]any{"id": "mgr", "role": "manager", "model": "m1"}},
		{"mgr", "model.request", req("r1")},
		{"mgr", "model.response", map[string]any{"req": "r1", "usage": map[string]any{"input_tokens": 500, "output_tokens": 50}, "stop": "tool_use"}},
		{"mgr", "board.op", probeBoardOp("create", "T1", "todo", "", 0)},
		{"swarm", "agent.spawn", map[string]any{"id": "be-1", "role": "backend", "task": "T1", "model": "m1", "by": "mgr", "parent": "mgr"}},
		{"be-1", "board.op", probeBoardOp("claim", "T1", "doing", "be-1", 1)},
		{"be-1", "model.request", req("r2")},
	}
}

// probeAsk is the permission question of the worker that the decision probes answer.
var probeAsk = pev{"be-1", "perm.ask", map[string]any{"tool": "bash", "command": "npm i", "role": "backend", "reason": "installs a package"}}

// eventProbes are the cases for every type the terminal has code for and the translator names nowhere. The payloads are the shapes
// the producers write (and the recordings under internal/web/translate/testdata hold); a case that shows no change is meant to show
// none: it is what makes the type a difference.
var eventProbes = map[string][]probeCase{
	"agent.abandon": {{ev: pev{"swarm", "agent.abandon", map[string]any{"id": "be-1"}}}},
	"agent.state":   {{ev: pev{"be-1", "agent.state", map[string]any{"id": "be-1", "state": "failed", "line": "", "task": "T1"}}}},
	"agent.stuck": {
		{ev: pev{"be-1", "agent.stuck", map[string]any{"guard": "repeat", "note": "bash failed the same way 4 times", "phase": "nudge"}}},
		{ev: pev{"be-1", "agent.stuck", map[string]any{"note": "the answer was sent back", "phase": "plan"}}},
	},
	"compact.patch":  {{ev: pev{"be-1", "compact.patch", map[string]any{"reason": "thread over its limit", "stage": "request", "thread_from": 1, "thread_to": 7}}}},
	"compact.plan":   {{ev: pev{"be-1", "compact.plan", map[string]any{"decision": "start", "mode": "fork", "warm": true, "reason": "thread over its limit"}}}},
	"compact.reject": {{ev: pev{"be-1", "compact.reject", map[string]any{"stage": "model_patch", "reason": "the patch was unusable", "fallback": "mechanical"}}}},
	"governor":       {{ev: pev{"", "governor", map[string]any{"action": "rate-limited", "rate_per_min": 30, "pause_ms": 1000, "inflight": 3, "queued": 2}}}},
	"layer.commit": {
		{ev: pev{"be-1", "layer.commit", map[string]any{"scope": "shared-epoch", "reason": "the shared prefix was re-written"}}},
		{ev: pev{"be-1", "layer.commit", map[string]any{"scope": "shared-sync", "reason": "the agent took the new prefix"}}},
	},
	"log.corrupt":        {{ev: pev{"", "log.corrupt", map[string]any{"corrupt_lines": 2, "first_corrupt_line": 5}}}},
	"log.open":           {{ev: pev{"", "log.open", map[string]any{"schema": 1}}}},
	"merge.fast_forward": {{ev: pev{"swarm", "merge.fast_forward", map[string]any{"branch": "main", "to": "abcdef0123456789"}}}},
	"merge.rolled_back": {{
		setup: []pev{{"be-1", "merge.queued", map[string]any{"position": 1, "task": "T1: catalogue"}}, {"be-1", "merge.verify_failed", map[string]any{"task": "T1: catalogue", "cmd": "go test ./...", "exit_code": 1}}},
		ev:    pev{"be-1", "merge.rolled_back", map[string]any{"task": "T1: catalogue", "tip": "abcdef0123456789"}},
	}},
	"perm.ask": {{ev: probeAsk}},
	"perm.decide": {
		{setup: []pev{probeAsk}, ev: pev{"be-1", "perm.decide", map[string]any{"tool": "bash", "command": "npm i", "role": "backend", "allow": true, "by": "user"}}},
		{ev: pev{"be-1", "perm.decide", map[string]any{"tool": "bash", "command": "rm -rf /", "role": "backend", "allow": false, "by": "policy", "reason": "denied by a rule"}}},
	},
	"sink.panic":        {{ev: pev{"swarm", "sink.panic", map[string]any{"agent": "be-1", "panic": "index out of range"}}}},
	"swarm.hold":        {{ev: pev{"mgr", "swarm.hold", map[string]any{"reason": "T1 is unfinished"}}}},
	"swarm.integration": {{ev: pev{"swarm", "swarm.integration", map[string]any{"applied": true, "branch": "sleipnir/s1/_integration", "files": []string{"a.go"}, "tip": "abcdef0123456789"}}}},
	"swarm.shutdown":    {{ev: pev{"", "swarm.shutdown", map[string]any{}}}},
	"swarm.unfinished":  {{ev: pev{"mgr", "swarm.unfinished", map[string]any{"reason": "T1 is unfinished"}}}},
	"swarm.wake":        {{ev: pev{"mgr", "swarm.wake", map[string]any{"n": 1, "note": "be-1 finished"}}}},
	"swarm.wake.paused": {{ev: pev{"mgr", "swarm.wake.paused", map[string]any{}}}},
	"swarm.wake_limit":  {{ev: pev{"be-1", "swarm.wake_limit", map[string]any{"limit": 3, "task": "T1"}}}},
	"tool.job":          {{ev: pev{"be-1", "tool.job", map[string]any{"id": "j1", "agent": "be-1", "command": "sleep 60", "status": "finished", "exit": 1, "duration_ms": 60000}}}},
	"tool.timeout":      {{ev: pev{"be-1", "tool.timeout", map[string]any{"id": "c1", "name": "bash", "limit_ms": 600000}}}},
	"workspace.commit":  {{ev: pev{"be-1", "workspace.commit", map[string]any{"commit": "abcdef0123456789"}}}},
	"workspace.create":  {{ev: pev{"be-1", "workspace.create", map[string]any{"base": "abcdef0123456789", "mode": "worktree", "path": "/work/trees/be-1"}}}},
	"workspace.prune":   {{ev: pev{"", "workspace.prune", map[string]any{"kept": 0, "live": 0, "removed": 1}}}},
	"workspace.remove":  {{ev: pev{"be-1", "workspace.remove", map[string]any{"force": true, "path": "/work/trees/be-1"}}}},
	"workspace.reset":   {{ev: pev{"be-1", "workspace.reset", map[string]any{"reason": "the worker starts again"}}}},
}

// probeJournal is what the translator sends the page for the recording, the case's setup, the event (or, with control, an event of an
// unknown type) and the last event: the journal of a Replay of that log, one encoded event per element.
func probeJournal(t testing.TB, pc probeCase, control bool) []string {
	t.Helper()
	start := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	ev := pc.ev
	if control {
		ev.typ = controlType
	}
	type timed struct {
		pev
		at time.Time
	}
	var log []timed
	for i, e := range probeRecording() {
		log = append(log, timed{e, start.Add(time.Duration(i) * probeStep)})
	}
	for i, e := range pc.setup {
		log = append(log, timed{e, start.Add(probeSetupAt + time.Duration(i)*probeStep)})
	}
	log = append(log, timed{ev, start.Add(probeEventAt)}, timed{pev{"", controlType, map[string]any{}}, start.Add(probeEndAt)})
	var buf []byte
	for i, e := range log {
		raw, err := json.Marshal(e.data)
		if err != nil {
			t.Fatal(err)
		}
		line, err := json.Marshal(events.Event{Seq: uint64(i + 1), TS: e.at, Session: "probe", Agent: e.agent, Type: e.typ, Data: raw})
		if err != nil {
			t.Fatal(err)
		}
		buf = append(append(buf, line...), '\n')
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), buf, 0o600); err != nil {
		t.Fatal(err)
	}
	raws, err := translate.Replay(context.Background(), dir, translate.Config{Tab: "probe", Root: "/work/p"})
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, len(raws))
	for i, r := range raws {
		out[i] = string(r)
	}
	return out
}

// surfacedTypes runs every probe and says, for each type, whether any of its cases changed what the translator sends the page.
func surfacedTypes(t testing.TB) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for typ, cases := range eventProbes {
		for _, pc := range cases {
			if pc.ev.typ != typ {
				t.Fatalf("the probe for %q carries an event of type %q", typ, pc.ev.typ)
			}
			if strings.Join(probeJournal(t, pc, false), "\n") != strings.Join(probeJournal(t, pc, true), "\n") {
				out[typ] = true
			}
		}
		if _, ok := out[typ]; !ok {
			out[typ] = false
		}
	}
	return out
}

// ---- the tests ----------------------------------------------------------------------------------------------------------------------

// realEventInput reads the code and runs the probes.
func realEventInput(t testing.TB) eventInput {
	t.Helper()
	tbl := loadConsts(t, constDirs...)
	return eventInput{
		declared: declaredTypes(tbl),
		term:     scanHandling(t, tbl, terminalEventDirs...),
		web:      scanHandling(t, tbl, webEventDirs...),
		surfaced: surfacedTypes(t),
	}
}

// loadEventsContract reads contract/events.json.
func loadEventsContract(t testing.TB) eventsContract {
	t.Helper()
	var c eventsContract
	if err := Load("events", &c); err != nil {
		t.Fatal(err)
	}
	return c
}

// The event types the terminal interface has code for and the ones the web interface has code for agree, except where
// contract/events.json says why not. A type added to package events, or a handler added to one interface and not the other, fails
// until it is built on the other side or entered there with its reason.
func TestEventsParity(t *testing.T) {
	c := loadEventsContract(t)
	for _, p := range c.check(realEventInput(t)) {
		t.Error(p)
	}
	for _, l := range c.GapLines("events") {
		t.Log(l)
	}
}

// The enumeration reads the constants of package events from the source: it finds the types the package is known to declare,
// whatever file they are in, and the State's own, and it reads the handling of both interfaces (the sets are not empty and hold what
// the code is known to do).
func TestEventsAreReadFromTheSource(t *testing.T) {
	in := realEventInput(t)
	for _, typ := range []string{"session.start", "agent.state", "log.corrupt", "mail.digest", "swarm.handover", "outcome"} {
		if _, ok := in.declared[typ]; !ok {
			t.Errorf("event type %q (a constant of package events) was not found", typ)
		}
	}
	if len(in.declared) < 60 {
		t.Errorf("only %d event types were found in package events", len(in.declared))
	}
	for _, tc := range []struct {
		what string
		ok   bool
	}{
		{"the State handles a type it names by the constant of package events (model.response)", in.term.handled["model.response"]},
		{"the State handles a type it names by a literal (notice)", in.term.handled["notice"]},
		{"the State handles a type named by a constant of its own package (goal.state)", in.term.handled["goal.state"]},
		{"the State knows a type and shows nothing for it (cache.plan)", in.term.ignored["cache.plan"] && !in.term.handled["cache.plan"]},
		{"the translator handles a type by the constant of package events (mail.send)", in.web.handled["mail.send"]},
		{"the translator handles a type named by a constant of another package (verify.run)", in.web.handled["verify.run"]},
		{"the translator has no code of its own for a type it shows through the State (agent.state)", !in.web.handled["agent.state"] && !in.web.ignored["agent.state"]},
	} {
		if !tc.ok {
			t.Errorf("not found in the source: %s", tc.what)
		}
	}
}

// The probes tell a type that changes what the page shows from one that does not: an agent's state event is seen, an event of a type
// no code knows is not, and the same probe run twice sends the same.
func TestEventProbesTellOneTypeFromAnother(t *testing.T) {
	pc := eventProbes["agent.state"][0]
	a, b := probeJournal(t, pc, false), probeJournal(t, pc, false)
	if strings.Join(a, "\n") != strings.Join(b, "\n") {
		t.Fatal("the same probe sent two different journals")
	}
	if strings.Join(a, "\n") == strings.Join(probeJournal(t, pc, true), "\n") {
		t.Error("an agent.state that fails the worker changes nothing the page is sent: the probe cannot see a change")
	}
	none := probeCase{ev: pev{"be-1", controlType, map[string]any{"id": "be-1", "state": "failed"}}}
	if strings.Join(probeJournal(t, none, false), "\n") != strings.Join(probeJournal(t, none, true), "\n") {
		t.Error("an event of an unknown type changes what the page is sent")
	}
	// Every probe uses the type it is filed under.
	for typ, cases := range eventProbes {
		for _, pc := range cases {
			if pc.ev.typ != typ {
				t.Errorf("a probe filed under %q carries %q", typ, pc.ev.typ)
			}
		}
	}
}

// cloneInput copies an eventInput, so that a test can add to it.
func cloneInput(in eventInput) eventInput {
	cp := func(m map[string]bool) map[string]bool {
		out := make(map[string]bool, len(m))
		for k, v := range m {
			out[k] = v
		}
		return out
	}
	decl := make(map[string]string, len(in.declared))
	for k, v := range in.declared {
		decl[k] = v
	}
	return eventInput{declared: decl, term: handling{cp(in.term.handled), cp(in.term.ignored)}, web: handling{cp(in.web.handled), cp(in.web.ignored)}, surfaced: cp(in.surfaced)}
}

// asText joins problems, for a test to look for a phrase in.
func asText(ps []string) string { return strings.Join(ps, "\n") }

// The comparison fails for what a developer does wrong. Each case starts from the real code and the real contract and adds one
// synthetic event type.
func TestEventsParityFailsForANewType(t *testing.T) {
	c := loadEventsContract(t)
	base := realEventInput(t)
	if p := c.check(base); len(p) > 0 {
		t.Fatalf("the real comparison is not clean, the meta-tests start from it:\n%s", asText(p))
	}
	const x = "zz.new"
	for _, tc := range []struct {
		name  string
		edit  func(*eventInput)
		wants []string
	}{
		{"a type added to package events and handled by nobody",
			func(in *eventInput) { in.declared[x] = "TypeZZNew" },
			[]string{`"zz.new" (events.TypeZZNew) is named by the code of neither interface`}},
		{"a type the State handles and the translator does not, without a probe",
			func(in *eventInput) { in.declared[x] = "TypeZZNew"; in.term.handled[x] = true },
			[]string{`"zz.new" is handled by the terminal interface and not named by the translator, and no probe`}},
		{"a type the State handles and the page does not show",
			func(in *eventInput) { in.declared[x] = "TypeZZNew"; in.term.handled[x] = true; in.surfaced[x] = false },
			[]string{`"zz.new" exists in the terminal interface and not in the web interface`, "contract/events.json"}},
		{"a type the State handles and the page shows through it",
			func(in *eventInput) { in.declared[x] = "TypeZZNew"; in.term.handled[x] = true; in.surfaced[x] = true },
			[]string{`"zz.new" reaches the page through the shared State`, "via_state"}},
		{"a type only the translator handles",
			func(in *eventInput) { in.declared[x] = "TypeZZNew"; in.web.handled[x] = true },
			[]string{`"zz.new" exists in the web interface and not in the terminal interface`}},
		{"a type the State knows and shows nothing for, in no list of the contract",
			func(in *eventInput) { in.declared[x] = "TypeZZNew"; in.term.ignored[x] = true },
			[]string{`"zz.new" is known to the code of an interface that shows nothing for it`, "ignored"}},
		{"a type the State lists as showing nothing and the translator handles",
			func(in *eventInput) { in.term.ignored[x] = true; in.web.handled[x] = true },
			[]string{`"zz.new" exists in the web interface and not in the terminal interface`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := cloneInput(base)
			tc.edit(&in)
			got := asText(c.check(in))
			if got == "" {
				t.Fatal("nothing was reported")
			}
			for _, want := range tc.wants {
				if !strings.Contains(got, want) {
					t.Errorf("missing %q in:\n%s", want, got)
				}
			}
		})
	}
}

// Every entry of the contract is needed: leaving any one out makes the comparison fail, so that the file never keeps an exception
// that has gone, and an exception cannot be moved to another heading unnoticed.
func TestEventsContractHasNoDeadEntry(t *testing.T) {
	c := loadEventsContract(t)
	in := realEventInput(t)
	without := func(es []Entry, i int) []Entry { return append(append([]Entry(nil), es[:i]...), es[i+1:]...) }
	for i, e := range c.TerminalOnly {
		d := c
		d.TerminalOnly = without(c.TerminalOnly, i)
		if len(d.check(in)) == 0 {
			t.Errorf("terminal_only %q can be removed and nothing fails", e.Item)
		}
	}
	for i, e := range c.WebOnly {
		d := c
		d.WebOnly = without(c.WebOnly, i)
		if len(d.check(in)) == 0 {
			t.Errorf("web_only %q can be removed and nothing fails", e.Item)
		}
	}
	for i, e := range c.Gaps {
		d := c
		d.Gaps = without(c.Gaps, i)
		if len(d.check(in)) == 0 {
			t.Errorf("gap %q can be removed and nothing fails", e.Item)
		}
	}
	for i, e := range c.ViaState {
		d := c
		d.ViaState = without(c.ViaState, i)
		if len(d.check(in)) == 0 {
			t.Errorf("via_state %q can be removed and nothing fails", e.Item)
		}
	}
	for i, e := range c.Ignored {
		d := c
		d.Ignored = without(c.Ignored, i)
		if len(d.check(in)) == 0 {
			t.Errorf("ignored %q can be removed and nothing fails", e.Item)
		}
	}
}

// A claim in via_state that the probe does not bear out, and an entry that nobody needs, fail.
func TestEventsViaStateIsMeasured(t *testing.T) {
	c := loadEventsContract(t)
	in := realEventInput(t)
	d := c
	d.ViaState = append(append([]Entry(nil), c.ViaState...), Entry{"log.open", "the page is claimed to show the schema through the State"})
	if got := asText(d.check(in)); !strings.Contains(got, `"log.open" under via_state is stale`) {
		t.Errorf("a via_state claim that the probe does not support was accepted:\n%s", got)
	}
	e := cloneInput(in)
	e.surfaced["log.open"] = true // the translator starts to show it
	if got := asText(c.check(e)); !strings.Contains(got, `"log.open" reaches the page through the shared State`) {
		t.Errorf("a type that became visible through the State was not asked for:\n%s", got)
	}
	f := cloneInput(in)
	f.surfaced["agent.state"] = false // the translator stops showing it
	got := asText(c.check(f))
	for _, want := range []string{`"agent.state" under via_state is stale`, `"agent.state" exists in the terminal interface and not in the web interface`} {
		if !strings.Contains(got, want) {
			t.Errorf("a type that stopped being visible through the State: missing %q in:\n%s", want, got)
		}
	}
}
