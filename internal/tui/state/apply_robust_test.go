package state

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/tui/state/statetest"
)

// allTypes is every event type the State has a handler for or knowingly ignores, and a few it does not know.
var allTypes = []string{
	events.TypeLogOpen, events.TypeLogCorrupt, events.TypeSessionStart, events.TypeSessionEnd, events.TypeAgentSpawn, events.TypeAgentState,
	events.TypeAgentEnd, events.TypeAgentStuck, events.TypeAgentSnapshot, events.TypeAgentRestore, events.TypeTurnAppend, events.TypeModelRequest,
	events.TypeModelResponse, events.TypeModelError, events.TypeToolCall, events.TypeToolResult, events.TypeToolJob, events.TypePermAsk,
	events.TypePermDecide, events.TypeLayerCommit, events.TypeCachePlan, events.TypeCacheAnomaly, events.TypeCompactPlan, events.TypeCompactPatch,
	events.TypeCompactCommit, events.TypeCompactReject, events.TypeRecall, events.TypeBoardOp, events.TypeMailSend, events.TypeMailRoute,
	events.TypeMailDeliver, events.TypeMailAck, events.TypeLease, events.TypeGovernor, events.TypeSwarmHold, events.TypeSwarmUnfinished,
	events.TypeSwarmWake, events.TypeSwarmWakePaused, events.TypeSwarmWakeLimit, events.TypeMailDigest, events.TypeMailDirect, events.TypeMailBatch,
	events.TypeMailmanState, events.TypeWorkspaceCreate, events.TypeWorkspaceRemove, events.TypeWorkspacePrune, events.TypeWorkspaceCommit,
	events.TypeWorkspaceReset, events.TypeMergeQueued, events.TypeMergeMerged, events.TypeMergeConflict, events.TypeMergeVerifyFail,
	events.TypeMergeRolledBack, events.TypeMergeRejected, events.TypeMergeFastFwd, events.TypeTaskMerge, events.TypeSwarmIntegration,
	events.TypeUserInput, events.TypeUserSteer, events.TypeOutcome, events.TypeAgentCancel,
	"agent.assign", "agent.panic", "agent.abandon", "mail.drop", "notice", "swarm.budget", "swarm.shutdown", "supervisor.panic", "sink.panic",
	"tool.panic", "tool.timeout", "tool.spill", "tool.budget", "hook.run", "x.never.heard.of.it", "",
}

// garbage is data that no producer writes: what a payload is not allowed to be trusted not to be.
var garbage = []string{
	``, `null`, `{}`, `[]`, `""`, `"a string"`, `123`, `-1`, `true`, `not json`, `{`, `{"a":`, `[[[[[[[[[[`, `{"seq":1e999}`,
	`{"usage":"x","sections":"y","input":[1,2],"files":7,"deps":"d","origins":3,"text":["a"],"tokens":"9","id":{"a":1},"name":5,"agent":[],"error":null}`,
	`{"usage":{"input_tokens":1e30,"cache_read_tokens":-5,"output_tokens":"many","cache_write_5m_tokens":9223372036854775807},"cost_usd":-1}`,
	`{"sections":[{"name":5,"tokens":"x"},null,7,{"tokens":1e99}],"breakpoints":[1,"a",{"ttl":-9,"label":[]}],"manifest":{"system":[1,2,3],"tools":{}}}`,
	`{"task":"T1","status":{"a":1},"rev":-3,"files":[null,1,"a"],"deps":{"x":1},"title":["t"],"tasks":[1,2,3],"evicted":["a"],"notes":"n"}`,
	`{"models":{"m":{"input_per_m":"x"},"n":5,"o":null,"p":{"input_per_m":1e309}}}`,
	`{"id":"\u0000\u001b[2J","agent":"‮","to":"a\nb","from":"\r","text":" "}`,
	`{"decision":{},"warm":"yes","yes":1,"position":-5,"hunks":"many","n":-9,"limit_ms":-1,"duration_ms":"x","exit":"x","pause_ms":1e30,"rate_per_min":-1}`,
	"\xff\xfe\x00\x01{\"a\":1}",
}

func TestGarbagePayloadsNeverPanicAndNeverBreakTheSnapshot(t *testing.T) {
	b := newB()
	st := New()
	agents := []string{"a", "", "swarm", "harness", "w\x1b[31m-1", strings.Repeat("long", 300)}
	for _, typ := range allTypes {
		for _, g := range garbage {
			for _, ag := range agents {
				b.Advance(ms(1))
				apply(t, st, b.Raw(ag, typ, g))
			}
		}
	}
	sn := st.Snapshot()
	bs, err := json.Marshal(sn)
	if err != nil {
		t.Fatalf("the snapshot of a state that was fed garbage does not marshal: %v", err)
	}
	if strings.Contains(string(bs), `\u001b`) || strings.Contains(string(bs), "NaN") || strings.Contains(string(bs), "Inf") {
		t.Error("an escape character, NaN or Inf got through")
	}
	checkBounds(t, st)
	if s := st.Stats(); s.Events != len(allTypes)*len(garbage)*len(agents) || s.Bad == 0 || s.Unknown == 0 || s.Panics != 0 {
		t.Errorf("stats %+v", s)
	}
	// And the same again with nothing at all in the envelope: no seq, no time, no type.
	apply(t, st, events.Event{}, events.Event{Type: "x"}, events.Event{Data: json.RawMessage(`{"a":1}`)})
}

// The barrier is what keeps a bug in a handler from taking the UI down with it: the State is sabotaged (an agent table that cannot
// be written to) so that a handler panics, and Apply must come back, count it, say what it was, and go on with the next event.
func TestAHandlerThatPanicsIsContainedAndRecorded(t *testing.T) {
	st := New()
	b := newB()
	st.Apply(b.Emit("", events.TypeSessionStart, startPayload(nil)))
	st.agents = nil // a write to it panics
	st.Apply(b.Spawn("a", "backend", "T1", "mgr"))
	s := st.Stats()
	if s.Panics != 1 || !strings.Contains(s.LastPanic, "nil map") || !strings.Contains(s.LastPanic, "event 2, agent.spawn") || len(s.LastPanic) > maxPanicText+5 {
		t.Fatalf("stats %+v", s)
	}
	st.agents = map[string]*agentState{}
	st.Apply(b.Spawn("b", "backend", "T2", "mgr")) // and the next event is folded
	if _, ok := st.Snapshot().Agent("b"); !ok || st.Stats().Panics != 1 || st.Stats().Events != 3 {
		t.Errorf("the State did not go on: %+v", st.Stats())
	}
}

func TestADuplicateOrStaleEventIsIgnored(t *testing.T) {
	b := newB()
	st := New()
	e1 := b.Request("a", "a.1", "m", "pk", secShared())
	e2 := b.Response("a", "a.1", "m", 100, 900, 0, 10, 0.01)
	apply(t, st, e1, e2)
	before := js(st.Snapshot().Totals) + js(st.Snapshot().Agents)
	apply(t, st, e2, e1, e2) // the same events again, in any order
	if after := js(st.Snapshot().Totals) + js(st.Snapshot().Agents); after != before {
		t.Errorf("a duplicate changed the state:\n%s\n%s", before, after)
	}
	if s := st.Stats(); s.Stale != 3 || s.Events != 2 || s.LastSeq != e2.Seq {
		t.Errorf("stats %+v", s)
	}
	if tot := st.Snapshot().Totals; tot.Responses != 1 || tot.Savings.PricedReadTokens != 0 {
		t.Errorf("counted twice: %+v", tot)
	}
	// An event with a lower seq than one already applied is a replay, whatever its content.
	apply(t, st, events.Event{Seq: 1, TS: b.Now(), Agent: "zz", Type: events.TypeToolCall, Data: json.RawMessage(`{"id":"x","name":"bash"}`)})
	if _, ok := st.Snapshot().Agent("zz"); ok || st.Stats().Stale != 4 {
		t.Error("a stale event created an agent")
	}
	// Events with no seq are applied as they come.
	for i := 0; i < 3; i++ {
		apply(t, st, events.Event{TS: b.Now(), Agent: "q", Type: events.TypeToolCall, Data: json.RawMessage(fmt.Sprintf(`{"id":"x%d","name":"bash"}`, i))})
	}
	if a := agentOf(t, st.Snapshot(), "q"); a.ToolCalls != 3 || a.OpenTools != 3 {
		t.Errorf("unsequenced events: %+v", a)
	}
}

func TestEventsOutOfCausalOrderAreStillCounted(t *testing.T) {
	// A result before its call, a response before its request, an end before the spawn, a delivery before the send: seqs increase,
	// but the story does not make sense. The state takes what it can from each.
	b := newB()
	st := New()
	apply(t, st, b.Emit("", events.TypeSessionStart, startPayload(nil)))
	apply(t, st, b.Result("a", "c1", "bash", true, 12))
	apply(t, st, b.Response("a", "a.1", "m", 100, 900, 0, 10, 0.01))
	apply(t, st, b.Emit("a", events.TypeAgentEnd, map[string]any{"id": "a", "state": "failed", "evidence": "x"}))
	apply(t, st, b.Spawn("a", "backend", "T1", "mgr"))
	apply(t, st, b.Request("a", "a.2", "m", "pk", secShared()))
	apply(t, st, b.Call("a", "c1", "bash", map[string]any{"command": "late"}))
	a := agentOf(t, st.Snapshot(), "a")
	if a.ToolErrors != 1 || a.Requests != 1 || a.Tokens.CacheRead != 900 || a.Role != "backend" {
		t.Errorf("%+v", a)
	}
	if a.Status != StatusTool || a.Tool != "bash" {
		t.Errorf("after all of that the agent is running a tool: %s %q", a.Status, a.Tool)
	}
	if sv := st.Snapshot().Totals.Savings; sv.PricedReadTokens != 900 {
		t.Errorf("savings %+v", sv)
	}
	// The response of a request that was never seen is a sample and a count of its own.
	if a.Hits.Len() != 1 {
		t.Errorf("history %+v", a.Hits)
	}
}

func TestMissingAndWrongTypedFieldsLeaveTheRestOfTheEventInPlace(t *testing.T) {
	b := newB()
	st := New()
	apply(t, st, b.Emit("", events.TypeSessionStart, startPayload(nil)))
	// Wrong type in one field: the others are applied.
	apply(t, st, b.Raw("a", events.TypeModelResponse, `{"req":"a.1","model":"m","usage":{"input_tokens":"many","cache_read_tokens":500,"output_tokens":7},"cost_usd":"free","stop":"tool_use"}`))
	a := agentOf(t, st.Snapshot(), "a")
	if a.Tokens != (Tokens{CacheRead: 500, Output: 7}) || a.CostUSD != 0 || a.Requests != 1 {
		t.Errorf("%+v", a)
	}
	// No payload at all: a response is still a response.
	apply(t, st, b.Raw("a", events.TypeModelResponse, ``))
	if tot := st.Snapshot().Totals; tot.Responses != 2 {
		t.Errorf("responses %d", tot.Responses)
	}
	// What an event is for must be there: a tool call with no name is not a call.
	bad := st.Stats().Bad
	apply(t, st, b.Raw("a", events.TypeToolCall, `{"id":"c1"}`))
	apply(t, st, b.Raw("a", events.TypeToolCall, `{"id":"c1","name":""}`))
	if st.Stats().Bad != bad+2 || agentOf(t, st.Snapshot(), "a").ToolCalls != 0 {
		t.Errorf("a nameless tool call: bad %d", st.Stats().Bad)
	}
	// An agent.spawn with no id of its own names the agent it came from, and with neither it names nobody.
	apply(t, st, b.Raw("b", events.TypeAgentSpawn, `{"role":"backend"}`))
	if a := agentOf(t, st.Snapshot(), "b"); a.Role != "backend" {
		t.Errorf("%+v", a)
	}
	n := len(st.Snapshot().Agents)
	apply(t, st, b.Raw("swarm", events.TypeAgentSpawn, `{"role":"backend"}`))
	if len(st.Snapshot().Agents) != n {
		t.Error("an agent.spawn from the swarm runtime with no id created an agent")
	}
	// A message with no id is kept but cannot be updated.
	apply(t, st, b.Raw("a", events.TypeMailSend, `{"from":"a","to":"b","text":"no id"}`))
	apply(t, st, b.Raw("b", events.TypeMailDeliver, `{}`))
	if m := st.Snapshot().Mail; m.Counts.Sent != 1 || m.Counts.Delivered != 1 || len(m.Recent) != 2 {
		t.Errorf("mail %s", js(m))
	}
	// A request with no req id or sections is a request with nothing to show.
	apply(t, st, b.Raw("c", events.TypeModelRequest, `{}`))
	if c := agentOf(t, st.Snapshot(), "c"); c.Stack.Req != "" || len(c.Stack.Sections) != 0 || c.Status != StatusThinking {
		t.Errorf("%+v", c)
	}
}

func TestUnknownEventTypesAreCountedByNameAndTheNamesAreBounded(t *testing.T) {
	b := newB()
	st := New()
	for i := 0; i < 3*MaxUnknownTypes; i++ {
		apply(t, st, b.Emit("", fmt.Sprintf("future.event.%d", i), map[string]any{"x": 1}))
	}
	for i := 0; i < 5; i++ {
		apply(t, st, b.Emit("", "future.event.0", nil))
	}
	s := st.Stats()
	if s.Unknown != 3*MaxUnknownTypes+5 || len(s.UnknownTypes) != MaxUnknownTypes || s.UnknownTypes["future.event.0"] != 6 {
		t.Errorf("unknown %d, %d names, first %d", s.Unknown, len(s.UnknownTypes), s.UnknownTypes["future.event.0"])
	}
	if _, ok := s.UnknownTypes["future.event.150"]; ok {
		t.Error("a name beyond the bound was kept")
	}
	// The events the package knowingly ignores are not unknown.
	st2 := New()
	for _, typ := range []string{events.TypeTurnAppend, events.TypeAgentSnapshot, events.TypeAgentRestore, events.TypeCachePlan, events.TypeRecall, events.TypeOutcome, "tool.spill", "tool.budget", "hook.run"} {
		apply(t, st2, b.Emit("a", typ, map[string]any{}))
	}
	if st2.Stats().Unknown != 0 || st2.Stats().Events != 9 {
		t.Errorf("stats %+v", st2.Stats())
	}
}

func TestPayloadsOverTheDecodeLimitAreIgnoredAndToolCallsAreExempt(t *testing.T) {
	b := newB()
	st := New()
	huge := `{"req":"a.1","agent":"a","model":"m","pad":"` + strings.Repeat("x", maxPayload) + `"}`
	apply(t, st, b.Raw("a", events.TypeModelRequest, huge))
	if s := st.Stats(); s.TooBig != 1 {
		t.Errorf("stats %+v", s)
	}
	if _, ok := st.Snapshot().Agent("a"); ok {
		t.Error("an oversized payload created an agent")
	}
	// A tool call carries what the model asked to write, which can be large; only its few short fields are read.
	call := `{"id":"c1","name":"write","input":{"path":"big.txt","content":"` + strings.Repeat("y", 3<<20) + `"}}`
	apply(t, st, b.Raw("a", events.TypeToolCall, call))
	if a := agentOf(t, st.Snapshot(), "a"); a.Tool != "write" || a.ToolSummary != "big.txt" || a.Status != StatusEditing {
		t.Errorf("%+v", a)
	}
	if st.Stats().TooBig != 1 {
		t.Error("the tool call was refused as too big")
	}
	// A long array inside the limit is cut to what is kept.
	var sb strings.Builder
	sb.WriteString(`{"req":"b.1","agent":"b","model":"m","sections":[`)
	for i := 0; i < 20000; i++ {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(`{"name":"s","tokens":1}`)
	}
	sb.WriteString(`],"manifest":{"system":[`)
	sb.WriteString(strings.Repeat(`"h",`, 5000) + `"h"]}}`)
	apply(t, st, b.Raw("b", events.TypeModelRequest, sb.String()))
	s := agentOf(t, st.Snapshot(), "b").Stack
	if len(s.Sections) != MaxSections || len(s.SystemHashes) > 4 {
		t.Errorf("%d sections, %d system hashes", len(s.Sections), len(s.SystemHashes))
	}
	deps := `{"op":"create","task":"T1","status":"todo","rev":1,"title":"t","deps":[` + strings.Repeat(`"T9",`, 50000) + `"T9"],"files":[` + strings.Repeat(`"f",`, 50000) + `"f"]}`
	apply(t, st, b.Raw("mgr", events.TypeBoardOp, deps))
	if tk := taskByID(t, st.Snapshot(), "T1"); len(tk.Deps) != MaxDeps || len(tk.Files) != MaxFiles {
		t.Errorf("%d deps %d files", len(tk.Deps), len(tk.Files))
	}
}

func TestStringsAndNumbersFromPayloadsAreBounded(t *testing.T) {
	b := newB()
	st := New()
	long := strings.Repeat("ab cd\n", 50000)
	apply(t, st, b.Emit("", events.TypeSessionStart, map[string]any{"model": long, "provider": long, "cwd": long, "version": long, "root": long}))
	apply(t, st, b.Emit(long, events.TypeModelRequest, map[string]any{"req": long, "agent": long, "role": long, "model": long, "prefix_key": long, "cache_key": long,
		"sections": []map[string]any{{"name": long, "hash": long, "tokens": 5}}}))
	apply(t, st, b.Emit("mgr", events.TypeBoardOp, taskOp("create", long, "todo", long, 1, map[string]any{"title": long, "line": long, "result": long, "evidence": long, "role": long})))
	apply(t, st, b.Emit("a", events.TypeMailSend, map[string]any{"id": long, "from": long, "to": long, "kind": long, "text": long}))
	apply(t, st, b.Emit("a", events.TypeUserInput, map[string]any{"text": long}))
	apply(t, st, b.Emit("a", events.TypeToolCall, map[string]any{"id": long, "name": long, "input": map[string]any{"command": long}}))
	apply(t, st, b.Emit("a", events.TypeLease, map[string]any{"action": "acquire", "agent": long, "path": long}))
	apply(t, st, b.Emit("a", events.TypePermAsk, map[string]any{"tool": long, "reason": long, "command": long, "paths": []string{long, long, long, long, long, long, long}, "role": long}))
	apply(t, st, b.Emit("a", events.TypePermDecide, map[string]any{"tool": long, "reason": long, "command": long, "allow": true, "by": long, "remember": long}))
	apply(t, st, b.Emit("a", events.TypeAgentCancel, map[string]any{"phase": long, "cause": long, "steps": 1 << 40}))
	bs, err := json.Marshal(st.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case string:
			if n := len([]rune(x)); n > textLong {
				t.Errorf("a string of %d runes got into the snapshot: %.40q", n, x)
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		case map[string]any:
			for k, e := range x {
				if len([]rune(k)) > textID {
					t.Errorf("a key of %d runes", len(k))
				}
				walk(e)
			}
		}
	}
	var v any
	if err := json.Unmarshal(bs, &v); err != nil {
		t.Fatal(err)
	}
	walk(v)
	if len(bs) > 1<<20 {
		t.Errorf("the snapshot of a few events with huge strings is %d bytes", len(bs))
	}

	// Numbers: negative and huge counts are clamped, and sums cannot overflow.
	st2 := New()
	for i := 0; i < 50; i++ {
		apply(t, st2, b.Raw("a", events.TypeModelResponse, `{"req":"a.1","model":"m","usage":{"input_tokens":-5,"cache_read_tokens":9223372036854775807,"cache_write_5m_tokens":9223372036854775807,"cache_write_1h_tokens":9223372036854775807,"output_tokens":-1},"cost_usd":1e300,"expected_read":-7,"missed":9223372036854775807}`))
	}
	sn := st2.Snapshot()
	tok := sn.Totals.Tokens
	if tok.Input != 0 || tok.Output != 0 || tok.CacheRead != 50*maxTokens || tok.CacheWrite != 100*maxTokens || tok.CacheRead < 0 || sn.Totals.CostUSD != 50*1e12 {
		t.Errorf("tokens %+v cost %v", tok, sn.Totals.CostUSD)
	}
	if r := sn.Totals.HitRatio(); math.IsNaN(r) || r < 0 || r > 1 {
		t.Errorf("hit ratio %v", r)
	}
	if h := agentOf(t, sn, "a").Hits.Ratios; len(h) != 50 || h[0] < 0 || h[0] > 1 {
		t.Errorf("ratios %v", h[:1])
	}
	if a := agentOf(t, sn, "a"); a.Stack.Prompt != smallCount*0+a.Stack.Prompt || a.Stack.Prompt > smallCount || a.Stack.Missed > smallCount || a.Stack.ExpectedRead != 0 {
		t.Errorf("stack %+v", a.Stack)
	}
	if _, err := json.Marshal(sn); err != nil {
		t.Errorf("the snapshot does not marshal: %v", err)
	}
}

func TestTimesAreTolerated(t *testing.T) {
	b := newB()
	st := New()
	t0 := b.Now()
	apply(t, st, b.Request("a", "a.1", "m", "pk", secShared()))
	// A year that cannot be right is not a time: it neither advances the clock nor stamps anything.
	apply(t, st, events.Event{Seq: 100, TS: time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC), Agent: "a", Type: events.TypeToolCall, Data: json.RawMessage(`{"id":"x","name":"bash"}`)})
	apply(t, st, events.Event{Seq: 101, TS: time.Date(1800, 1, 1, 0, 0, 0, 0, time.UTC), Agent: "a", Type: events.TypeToolResult, Data: json.RawMessage(`{"id":"x","name":"bash"}`)})
	if !st.Clock().Equal(t0) {
		t.Errorf("clock %v", st.Clock())
	}
	// A clock that steps back does not move the State's clock back.
	apply(t, st, events.Event{Seq: 102, TS: t0.Add(sec(10)), Agent: "a", Type: events.TypeToolCall, Data: json.RawMessage(`{"id":"y","name":"bash"}`)})
	apply(t, st, events.Event{Seq: 103, TS: t0.Add(sec(5)), Agent: "a", Type: events.TypeToolResult, Data: json.RawMessage(`{"id":"y","name":"bash","ms":1}`)})
	if !st.Clock().Equal(t0.Add(sec(10))) {
		t.Errorf("clock %v", st.Clock())
	}
	a := agentOf(t, st.Snapshot(), "a")
	if !a.LastActive.Equal(t0.Add(sec(10))) {
		t.Errorf("LastActive %v went backwards", a.LastActive)
	}
	// An event with no timestamp takes the clock.
	apply(t, st, events.Event{Seq: 104, Agent: "a", Type: events.TypeModelResponse, Data: json.RawMessage(`{"req":"a.1","model":"m","usage":{"input_tokens":1}}`)})
	if e := st.Snapshot().TTL; len(e) == 0 || e[len(e)-1].Last.Before(t0) {
		t.Errorf("ttl %+v", e)
	}
	// Nothing at all has no clock.
	if !New().Clock().IsZero() || !New().Snapshot().Clock.IsZero() {
		t.Error("an empty State has no clock")
	}
}

func TestOnlyAgentsGetRows(t *testing.T) {
	b := newB()
	st := New()
	for _, who := range []string{"swarm", "harness", "curator", ""} {
		apply(t, st, b.Call(who, "c", "bash", map[string]any{}))
		apply(t, st, b.Request(who, who+".1", "m", "pk", secShared()))
		apply(t, st, b.Emit(who, events.TypeLease, map[string]any{"action": "acquire", "path": "/x"}))
	}
	sn := st.Snapshot()
	if len(sn.Agents) != 0 || len(sn.Activity.Rows) != 0 {
		t.Errorf("agents %v", agentIDs(sn))
	}
	// They are still counted: the totals are of the session, not of its agents.
	if sn.Totals.ToolCalls != 4 || sn.Totals.Requests != 4 {
		t.Errorf("totals %+v", sn.Totals)
	}
}

func TestResetForgetsEverythingButTheOptions(t *testing.T) {
	st := NewWith(Options{DefaultTTL: 42 * time.Second})
	evs := genEvents(3000, 5)
	apply(t, st, evs...)
	if len(st.Snapshot().Agents) == 0 {
		t.Fatal("setup")
	}
	st.Reset()
	sn := st.Snapshot()
	fresh := NewWith(Options{DefaultTTL: 42 * time.Second}).Snapshot()
	sn.Stats.Resets = 0
	if js(sn) != js(fresh) {
		t.Errorf("after Reset:\n%.400s", js(sn))
	}
	if st.Stats().Resets != 1 || st.LastSeq() != 0 {
		t.Errorf("stats %+v", st.Stats())
	}
	// It takes the same events again, which it would have called duplicates before.
	apply(t, st, evs...)
	if len(st.Snapshot().Agents) == 0 {
		t.Error("nothing applied after a reset")
	}
	apply(t, st, b2Request())
	for _, e := range st.Snapshot().TTL {
		if e.Kind == "prefix" && e.Key == "RESETPFX" && e.TTLSeconds != 300 {
			// session.start of the generated log recorded a TTL: only a default would be 42
			t.Logf("ttl %+v", e)
		}
	}
}

func b2Request() events.Event {
	return events.Event{TS: statetest.Epoch, Agent: "zz", Type: events.TypeModelRequest, Data: json.RawMessage(`{"req":"zz.1","model":"none","prefix_key":"RESETPFX","sections":[{"name":"shared","tokens":1}]}`)}
}

func TestASnapshotIsImmutableAndSharesNothing(t *testing.T) {
	evs := genEvents(4000, 7)
	st := New()
	apply(t, st, evs[:2000]...)
	sn := st.Snapshot()
	before := js(sn)
	apply(t, st, evs[2000:]...)
	if js(sn) != before {
		t.Fatal("a snapshot changed when the State went on folding")
	}
	// And the other way: a caller that scribbles on a snapshot does not reach the State.
	fresh := st.Snapshot()
	want := js(fresh)
	mut := st.Snapshot()
	for i := range mut.Agents {
		a := &mut.Agents[i]
		for j := range a.Hits.Ratios {
			a.Hits.Ratios[j] = 99
		}
		for j := range a.Hits.Marks {
			a.Hits.Marks[j].Text = "x"
		}
		for j := range a.Stack.Sections {
			a.Stack.Sections[j].Name = "x"
		}
		for j := range a.Leases {
			a.Leases[j] = "x"
		}
		for j := range a.Scope {
			a.Scope[j] = "x"
		}
		for j := range a.Compacts {
			a.Compacts[j].Reason = "x"
		}
		for j := range a.Anomalies {
			a.Anomalies[j].Note = "x"
		}
		a.Stack.SystemHashes = append(a.Stack.SystemHashes, "x")
	}
	for i := range mut.Feed {
		mut.Feed[i].Text = "x"
	}
	for i := range mut.Board.Tasks {
		for j := range mut.Board.Tasks[i].Deps {
			mut.Board.Tasks[i].Deps[j] = "x"
		}
		for j := range mut.Board.Tasks[i].Files {
			mut.Board.Tasks[i].Files[j] = "x"
		}
	}
	for i := range mut.Mail.Recent {
		mut.Mail.Recent[i].Summary = "x"
		for j := range mut.Mail.Recent[i].Origins {
			mut.Mail.Recent[i].Origins[j] = "x"
		}
	}
	for i := range mut.Merge.Recent {
		for j := range mut.Merge.Recent[i].Files {
			mut.Merge.Recent[i].Files[j] = "x"
		}
	}
	for i := range mut.Merge.Waiting {
		mut.Merge.Waiting[i].Reason = "x"
	}
	for i := range mut.TTL {
		for j := range mut.TTL[i].Agents {
			mut.TTL[i].Agents[j] = "x"
		}
	}
	for i := range mut.Prefixes {
		for j := range mut.Prefixes[i].Agents {
			mut.Prefixes[i].Agents[j] = "x"
		}
	}
	for i := range mut.Activity.Rows {
		for j := range mut.Activity.Rows[i].Levels {
			mut.Activity.Rows[i].Levels[j] = 9
			mut.Activity.Rows[i].Marks[j] = 9
		}
	}
	for i := range mut.Models {
		mut.Models[i].ID = "x"
	}
	for i := range mut.Anomalies {
		mut.Anomalies[i].Note = "x"
	}
	for i := range mut.Leases.Held {
		mut.Leases.Held[i].Path = "x"
	}
	for i := range mut.Perms.Pending {
		mut.Perms.Pending[i].Summary = "x"
		for j := range mut.Perms.Pending[i].Paths {
			mut.Perms.Pending[i].Paths[j] = "x"
		}
	}
	for i := range mut.Perms.Recent {
		mut.Perms.Recent[i].Reason = "x"
		for j := range mut.Perms.Recent[i].Ask.Paths {
			mut.Perms.Recent[i].Ask.Paths[j] = "x"
		}
	}
	for i := range mut.Board.Alerts {
		mut.Board.Alerts[i].Text = "x"
	}
	for k := range mut.Stats.UnknownTypes {
		mut.Stats.UnknownTypes[k] = -1
	}
	if got := js(st.Snapshot()); got != want {
		t.Fatalf("writing to a snapshot reached the State")
	}
}
