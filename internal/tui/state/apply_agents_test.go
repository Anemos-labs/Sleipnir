package state

import (
	"fmt"
	"strings"
	"testing"

	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/tui/state/statetest"
)

func TestSessionStartAndEnd(t *testing.T) {
	b := newB()
	b.Emit("", events.TypeLogOpen, map[string]any{"schema": 1})
	b.Advance(ms(5))
	startAt := b.Now()
	b.Emit("", events.TypeSessionStart, startPayload(nil))
	b.Advance(sec(1))
	b.Emit("mgr", events.TypeUserInput, map[string]any{"text": "  fix\nthe\tbug \x1b[31m now  "})
	b.Emit("mgr", events.TypeUserInput, map[string]any{"text": "a second thing"})
	b.Emit("mgr", events.TypeUserInput, map[string]any{"text": "a kickoff the harness wrote", "origin": "task"})
	b.Advance(sec(2))
	endAt := b.Now()
	b.Emit("", events.TypeSessionEnd, map[string]any{"cost_usd": 1.25, "reason": "completed"})
	sn := fold(t, b.Events()...).Snapshot()

	s := sn.Session
	want := Session{ID: "test", Version: "1.2.3", Model: "m", Provider: "prov", Dialect: "openai-chat", Cwd: "/work/proj/sub", Root: "/work/proj",
		Renderer: "sleipnir-kv/2", Swarm: true, Schema: 1, ReconTokens: 25, SharedHash: "0123456789ab", Started: startAt,
		Ended: true, EndedAt: endAt, EndReason: "completed", EndCostUSD: 1.25, Goal: "fix the bug [31m now"}
	if js(s) != js(want) {
		t.Errorf("session:\n%s\nwant:\n%s", js(s), js(want))
	}
	if s.Mode != "" {
		t.Errorf("Mode = %q: session.start does not carry the permission mode, so it is unknown", s.Mode)
	}
	if sn.Elapsed(endAt) != 3*sec(1) {
		t.Errorf("Elapsed = %v", sn.Elapsed(endAt))
	}
	if sn.Elapsed(startAt.Add(-sec(1))) != 0 {
		t.Error("Elapsed before the start must be 0")
	}
	if !sn.First.Equal(statetest.Epoch) {
		t.Errorf("First = %v", sn.First)
	}

	// A producer that does write the mode is believed.
	b2 := newB()
	b2.Emit("", events.TypeSessionStart, startPayload(map[string]any{"mode": "accept-edits"}))
	if m := fold(t, b2.Events()...).Snapshot().Session.Mode; m != "accept-edits" {
		t.Errorf("Mode = %q", m)
	}
}

func TestSingleAgentSessionIsNotASwarm(t *testing.T) {
	b := newB()
	b.Emit("", events.TypeSessionStart, startPayload(map[string]any{"swarm": false}))
	b.Request("main", "main.1", "m", "pk", secShared())
	sn := fold(t, b.Events()...).Snapshot()
	if sn.Session.Swarm {
		t.Error("a session that says swarm:false and has no agent.spawn is a single agent")
	}
	if got := sn.Main(); got != "main" {
		t.Errorf("Main = %q", got)
	}
	// The first agent.spawn makes it a swarm, whatever session.start said.
	st := fold(t, b.Events()...)
	apply(t, st, b.Spawn("be-1", "backend", "T1", "mgr"))
	if !st.Snapshot().Session.Swarm {
		t.Error("an agent.spawn means a swarm")
	}
}

func TestAgentStatusFollowsTheEvents(t *testing.T) {
	b := newB()
	st := New()
	status := func(want Status) {
		t.Helper()
		st.mu.RLock()
		got := st.agents["be-1"].Status
		st.mu.RUnlock()
		if got != want {
			t.Fatalf("status = %s, want %s", got, want)
		}
	}
	step := func(e events.Event) { t.Helper(); apply(t, st, e) }

	step(b.Spawn("be-1", "backend", "T1", "mgr"))
	status(StatusStarting)
	step(b.Request("be-1", "be-1.1", "m", "pk", secShared()))
	status(StatusThinking)
	step(b.Response("be-1", "be-1.1", "m", 10, 0, 0, 5, 0))
	status(StatusThinking) // the response asked for tools (stop tool_use): it is about to run them
	b.Advance(ms(10))
	step(b.Call("be-1", "c1", "bash", map[string]any{"command": "go test ./...\nsecond line"}))
	status(StatusTool)
	sn := st.Snapshot()
	if a := agentOf(t, sn, "be-1"); a.Tool != "bash" || a.ToolSummary != "go test ./... second line" || a.OpenTools != 1 {
		t.Errorf("tool = %q %q open %d", a.Tool, a.ToolSummary, a.OpenTools)
	}
	step(b.Call("be-1", "c2", "edit", map[string]any{"path": "internal/a.go"}))
	status(StatusEditing)
	if a := agentOf(t, st.Snapshot(), "be-1"); a.Tool != "edit" || a.ToolSummary != "internal/a.go" || a.OpenTools != 2 {
		t.Errorf("the newest open call is the one shown: %q %q open %d", a.Tool, a.ToolSummary, a.OpenTools)
	}
	b.Advance(ms(5))
	step(b.Result("be-1", "c2", "edit", false, 5))
	status(StatusTool) // bash is still running
	b.Advance(ms(1200))
	step(b.Result("be-1", "c1", "bash", false, 1200))
	status(StatusThinking) // nothing runs: the results are being turned into the next request
	step(b.Call("be-1", "c3", "wait", map[string]any{"timeout_sec": 30}))
	status(StatusWaiting)
	step(b.Result("be-1", "c3", "wait", false, 400))
	status(StatusThinking)

	// The swarm's own word about it.
	step(b.Emit("be-1", events.TypeAgentState, map[string]any{"id": "be-1", "state": "idle", "line": "", "task": "T1"}))
	status(StatusIdle)
	step(b.Request("be-1", "be-1.2", "m", "pk", secShared()))
	status(StatusThinking) // work again: what said it had stopped is out of date
	step(b.Emit("be-1", events.TypeAgentState, map[string]any{"id": "be-1", "state": "failed", "line": "budget limit reached", "task": "T1"}))
	status(StatusError)
	if a := agentOf(t, st.Snapshot(), "be-1"); a.Line != "budget limit reached" {
		t.Errorf("Line = %q", a.Line)
	}
	step(b.Emit("be-1", events.TypeAgentState, map[string]any{"id": "be-1", "state": "running", "line": "starting", "task": "T1"}))
	status(StatusStarting)
	step(b.Emit("be-1", events.TypeAgentState, map[string]any{"id": "be-1", "state": "done", "line": ""}))
	status(StatusDone)
}

func TestAStuckAgentShowsItUntilACallSucceeds(t *testing.T) {
	b := newB()
	st := New()
	step := func(e events.Event) { t.Helper(); apply(t, st, e) }
	step(b.Spawn("w-1", "backend", "T1", "mgr"))
	step(b.Request("w-1", "w-1.1", "m", "pk", secShared()))
	for i := 0; i < 4; i++ {
		id := fmt.Sprintf("c%d", i)
		step(b.Call("w-1", id, "nosuch", map[string]any{}))
		b.Advance(ms(1))
		step(b.Result("w-1", id, "nosuch", true, 1))
	}
	step(b.Emit("w-1", events.TypeAgentStuck, map[string]any{"phase": "nudge", "note": "[harness] nosuch has now failed the same way 4 times"}))
	a := agentOf(t, st.Snapshot(), "w-1")
	if a.Status != StatusStuck || a.Stuck.Phase != "nudge" || a.Stuck.Nudges != 1 || !a.Stuck.Active || !strings.Contains(a.Stuck.Note, "failed the same way 4 times") {
		t.Fatalf("after the nudge: %s %+v", a.Status, a.Stuck)
	}
	if a.ToolErrors != 4 || a.ToolCalls != 4 {
		t.Errorf("tool calls %d errors %d", a.ToolCalls, a.ToolErrors)
	}
	// A failing call does not get it out of it; a thinking turn does not either.
	step(b.Request("w-1", "w-1.2", "m", "pk", secShared()))
	if s := agentOf(t, st.Snapshot(), "w-1").Status; s != StatusStuck {
		t.Errorf("status = %s while it still has not got out of the loop", s)
	}
	step(b.Call("w-1", "d1", "nosuch", map[string]any{}))
	step(b.Result("w-1", "d1", "nosuch", true, 1))
	if s := agentOf(t, st.Snapshot(), "w-1").Status; s != StatusStuck {
		t.Errorf("status = %s after another failure", s)
	}
	step(b.Call("w-1", "d2", "read", map[string]any{"path": "x"}))
	step(b.Result("w-1", "d2", "read", false, 1))
	a = agentOf(t, st.Snapshot(), "w-1")
	if a.Status == StatusStuck || a.Stuck.Active {
		t.Errorf("a successful call gets it out of it: %s %+v", a.Status, a.Stuck)
	}
	if a.Stuck.Nudges != 1 || a.Stuck.Phase != "nudge" {
		t.Errorf("the record of what happened stays: %+v", a.Stuck)
	}
	// The stop: the run is ended for it.
	step(b.Emit("w-1", events.TypeAgentStuck, map[string]any{"phase": "stop", "error": "agent w-1: agent stuck: nosuch failed the same way 8 times"}))
	a = agentOf(t, st.Snapshot(), "w-1")
	if a.Status != StatusStuck || a.Stuck.Stops != 1 || a.Stuck.Phase != "stop" {
		t.Errorf("after the stop: %s %+v", a.Status, a.Stuck)
	}
	step(b.Emit("w-1", events.TypeAgentEnd, map[string]any{"id": "w-1", "state": "failed", "evidence": "edited 0 files"}))
	a = agentOf(t, st.Snapshot(), "w-1")
	if a.Status != StatusError || a.Stuck.Active || a.EndState != "failed" || a.Evidence != "edited 0 files" {
		t.Errorf("after the end: %s %+v %q %q", a.Status, a.Stuck, a.EndState, a.Evidence)
	}
	// It shows on the gantt and in the feed.
	sn := st.Snapshot()
	if !anyMark(rowOf(t, sn, "w-1"), ActStuck) {
		t.Errorf("no stuck marker on the gantt: %+v", rowOf(t, sn, "w-1").Marks)
	}
	if !feedHas(sn, FeedStuck, "w-1", "stuck") {
		t.Errorf("no stuck line in the feed: %v", sn.Feed)
	}
}

func TestAWorkerIsDoneWhenItsTaskIsAcceptedInEitherOrder(t *testing.T) {
	for _, acceptFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("acceptFirst=%v", acceptFirst), func(t *testing.T) {
			b := newB()
			st := New()
			apply(t, st, b.Spawn("w-1", "scout", "T1", "mgr"))
			apply(t, st, b.Emit("mgr", events.TypeBoardOp, taskOp("create", "T1", "todo", "", 1, map[string]any{"title": "Survey"})))
			apply(t, st, b.Emit("mgr", events.TypeBoardOp, taskOp("assign", "T1", "doing", "w-1", 2, nil)))
			apply(t, st, b.Request("w-1", "w-1.1", "m", "pk", secShared()))
			accept := func() events.Event {
				return b.Emit("mgr", events.TypeBoardOp, taskOp("finish", "T1", "done", "w-1", 3, nil))
			}
			end := func() events.Event {
				return b.Emit("w-1", events.TypeAgentState, map[string]any{"id": "w-1", "state": "idle", "line": "", "task": ""})
			}
			if acceptFirst {
				apply(t, st, accept(), end())
			} else {
				apply(t, st, end(), accept())
			}
			if s := agentOf(t, st.Snapshot(), "w-1").Status; s != StatusDone {
				t.Errorf("status = %s, want done: its task was accepted", s)
			}
			// A worker whose task was not accepted is idle, not done.
			apply(t, st, b.Spawn("w-2", "scout", "T2", "mgr"))
			apply(t, st, b.Emit("w-2", events.TypeAgentState, map[string]any{"id": "w-2", "state": "idle"}))
			if s := agentOf(t, st.Snapshot(), "w-2").Status; s != StatusIdle {
				t.Errorf("w-2 status = %s", s)
			}
		})
	}
}

// The manager is quicker than a worker's closing message: its task is accepted and the session ends while the worker is still in
// the last request of its run (which is then cancelled). The worker is done, not idle and not failed, whichever order the last
// events come in.
func TestAWorkerThatWasStillFinishingWhenTheSessionEndedIsDone(t *testing.T) {
	b := newB()
	st := New()
	apply(t, st, b.Emit("", events.TypeSessionStart, startPayload(nil)))
	apply(t, st, b.Spawn("w-1", "scout", "T1", "mgr"))
	apply(t, st, b.Emit("mgr", events.TypeBoardOp, taskOp("create", "T1", "todo", "", 1, map[string]any{"title": "Survey"})))
	apply(t, st, b.Emit("mgr", events.TypeBoardOp, taskOp("assign", "T1", "doing", "w-1", 2, nil)))
	apply(t, st, b.Request("w-1", "w-1.1", "m", "pk", secShared()))
	apply(t, st, b.Emit("mgr", events.TypeBoardOp, taskOp("finish", "T1", "done", "w-1", 3, nil)))
	apply(t, st, b.Request("w-1", "w-1.2", "m", "pk", secShared())) // its closing message, in flight when everything stops
	apply(t, st, b.Emit("mgr", events.TypeSessionEnd, map[string]any{"cost_usd": 0.5, "reason": "other"}))
	if a := agentOf(t, st.Snapshot(), "w-1"); a.Status != StatusDone || a.InFlight != 0 {
		t.Errorf("status %s with %d requests in flight", a.Status, a.InFlight)
	}
	// Its request is then reported cancelled, as the harness does: not a failure of the worker.
	apply(t, st, b.Emit("w-1", events.TypeModelError, map[string]any{"req": "w-1.2", "error": "context canceled"}))
	apply(t, st, b.Emit("w-1", events.TypeAgentEnd, map[string]any{"id": "w-1", "state": "idle", "evidence": "no files edited"}))
	sn := st.Snapshot()
	if a := agentOf(t, sn, "w-1"); a.Status != StatusDone || a.Errors != 0 || sn.Totals.Errors != 0 || sn.Totals.Cancelled != 1 {
		t.Errorf("status %s, %d errors, totals %+v", a.Status, a.Errors, sn.Totals)
	}
	if !feedHas(sn, FeedNote, "w-1", "cancelled") || feedHas(sn, FeedError, "w-1", "") {
		t.Error("a cancelled request is a note in the feed, not an error")
	}
}

func TestAgentCounters(t *testing.T) {
	b := newB()
	st := New()
	apply(t, st, b.Emit("", events.TypeSessionStart, startPayload(nil)))
	apply(t, st, b.Spawn("w-1", "backend", "T1", "mgr"))
	b.Advance(sec(1))
	apply(t, st, b.Request("w-1", "w-1.1", "m", "pk", secShared()))
	b.Advance(sec(2))
	apply(t, st, b.Response("w-1", "w-1.1", "m", 100, 900, 50, 20, 0.001))
	apply(t, st, b.Request("w-1", "w-1.2", "m", "pk", secShared()))
	b.Advance(sec(1))
	apply(t, st, b.Response("w-1", "w-1.2", "m", 10, 1100, 0, 30, 0.0005))
	// A retry is an attempt repeated inside one request; a failure ends it.
	apply(t, st, b.Request("w-1", "w-1.3", "m", "pk", secShared()))
	apply(t, st, b.Emit("w-1", events.TypeModelError, map[string]any{"req": "w-1.3", "kind": "rate_limit", "status": 429, "attempt": 1, "delay_ms": 1500}))
	apply(t, st, b.Emit("w-1", events.TypeModelError, map[string]any{"req": "w-1.3", "kind": "server", "status": 503, "attempt": 2, "delay_ms": 3000}))
	b.Advance(sec(3))
	end := b.Now()
	apply(t, st, b.Emit("w-1", events.TypeModelError, map[string]any{"req": "w-1.3", "error": "provider: the endpoint went away"}))
	apply(t, st, b.Call("w-1", "c1", "read", map[string]any{"path": "a"}))
	apply(t, st, b.Result("w-1", "c1", "read", false, 3))
	apply(t, st, b.Call("w-1", "c2", "bash", map[string]any{"command": "x"}))
	apply(t, st, b.Result("w-1", "c2", "bash", true, 3))

	sn := st.Snapshot()
	a := agentOf(t, sn, "w-1")
	if a.Tokens != (Tokens{Input: 110, CacheRead: 2000, CacheWrite: 50, Output: 50}) {
		t.Errorf("tokens %+v", a.Tokens)
	}
	if !near(a.CostUSD, 0.0015) {
		t.Errorf("cost %v", a.CostUSD)
	}
	if a.Requests != 2 || a.Errors != 1 || a.Retries != 2 || a.ToolCalls != 2 || a.ToolErrors != 1 {
		t.Errorf("requests %d errors %d retries %d calls %d tool errors %d", a.Requests, a.Errors, a.Retries, a.ToolCalls, a.ToolErrors)
	}
	if a.Status != StatusThinking {
		// the failed request ended its run with an error, and the tool calls that followed are work again
		t.Errorf("status = %s", a.Status)
	}
	if !a.LastActive.After(end.Add(-ms(1))) {
		t.Errorf("LastActive %v", a.LastActive)
	}
	tot := sn.Totals
	if tot.Requests != 3 || tot.Main != 3 || tot.Responses != 2 || tot.Errors != 1 || tot.Retries != 2 || tot.RateLimited != 1 || tot.ToolCalls != 2 || tot.ToolErrors != 1 {
		t.Errorf("totals %+v", tot)
	}
	if sn.Governor.Retries != 2 || sn.Governor.RateLimited != 1 {
		t.Errorf("governor %+v", sn.Governor)
	}
	for _, want := range []struct {
		kind FeedKind
		frag string
	}{{FeedRetry, "rate_limit (http 429)"}, {FeedRetry, "server (http 503)"}, {FeedError, "the endpoint went away"}, {FeedToolErr, "bash x"}, {FeedTool, "read a"}} {
		if !feedHas(sn, want.kind, "w-1", want.frag) {
			t.Errorf("feed lacks a %s line with %q", want.kind, want.frag)
		}
	}
}

func TestAgentsAreOrderedManagerFirstThenByIDThenServices(t *testing.T) {
	b := newB()
	st := New()
	for _, id := range []string{"be-10", "be-2", "fe-1", "be-1", "sc-3"} {
		apply(t, st, b.Spawn(id, strings.TrimSuffix(strings.Split(id, "-")[0], "x"), "", "mgr"))
	}
	apply(t, st, b.Emit("swarm", events.TypeAgentSpawn, map[string]any{"id": "mm-1", "role": "mailman", "service": true}))
	apply(t, st, b.Emit("swarm", events.TypeAgentSpawn, map[string]any{"id": "mgr", "role": "manager"}))
	sn := st.Snapshot()
	want := []string{"mgr", "be-1", "be-2", "be-10", "fe-1", "sc-3", "mm-1"}
	if got := agentIDs(sn); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("order %v, want %v", got, want)
	}
	if got := st.Agents(); len(got) != len(want) || got[0].ID != "mgr" {
		t.Errorf("State.Agents = %v", got)
	}
	if st.Main() != "mgr" || sn.Main() != "mgr" {
		t.Errorf("Main = %q %q", st.Main(), sn.Main())
	}
	// Focus: an unknown id falls back to the main agent; cycling skips the service agents and wraps.
	for _, c := range []struct {
		focus string
		delta int
		want  string
	}{{"", 0, "mgr"}, {"nobody", 0, "mgr"}, {"be-2", 0, "be-2"}, {"mgr", 1, "be-1"}, {"sc-3", 1, "mgr"}, {"mgr", -1, "sc-3"}, {"be-2", -2, "mgr"}, {"mgr", 11, "sc-3"}, {"mgr", 12, "mgr"}, {"mm-1", 1, "be-1"}} {
		if got := sn.Cycle(c.focus, c.delta); got != c.want {
			t.Errorf("Cycle(%q, %d) = %q, want %q", c.focus, c.delta, got, c.want)
		}
	}
	if got := sn.Resolve("be-10"); got != "be-10" {
		t.Errorf("Resolve = %q", got)
	}
	if a, ok := sn.Focused("zzz"); !ok || a.ID != "mgr" {
		t.Errorf("Focused = %v %v", a.ID, ok)
	}
	// With no manager, the first agent by id is main; with only a service agent there is none.
	only := fold(t, b.Emit("swarm", events.TypeAgentSpawn, map[string]any{"id": "mm-1", "service": true})).Snapshot()
	if only.Main() != "" || only.Cycle("", 1) != "" {
		t.Errorf("a snapshot with only a service agent has no main agent: %q", only.Main())
	}
	if (&Snapshot{}).Main() != "" {
		t.Error("the empty snapshot has no main agent")
	}
}

// A gauge of how full an agent's context is needs the window of its model (session.start records it, after any override) and the
// prompt of its latest answered request: the board's own figure is 0 in every log the harness writes.
func TestContextFillIsThePromptOverTheWindowOfTheAgentsModel(t *testing.T) {
	b := newB()
	st := New()
	models := map[string]any{"m": map[string]any{"source": "given", "context": 20000, "input_per_m": 4.0, "output_per_m": 20.0,
		"cache_read_per_m": 0.4, "cache_write_5m_per_m": 5.0, "cache_write_1h_per_m": 8.0, "ttl_s": 300}}
	apply(t, st, b.Emit("", events.TypeSessionStart, startPayload(map[string]any{"models": models})))
	apply(t, st, b.Spawn("a", "backend", "T1", "mgr"))
	fill := func(id string) (float64, bool) {
		sn := st.Snapshot()
		return sn.ContextFill(agentOf(t, sn, id))
	}
	if _, ok := fill("a"); ok {
		t.Error("an agent that has sent nothing has no fill")
	}
	apply(t, st, b.Request("a", "a.1", "m", "pk", secShared()))
	if _, ok := fill("a"); ok {
		t.Error("a request that has not been answered has no fill")
	}
	apply(t, st, b.Response("a", "a.1", "m", 1000, 4000, 0, 10, 0.01))
	if f, ok := fill("a"); !ok || f != 0.25 {
		t.Errorf("a prompt of 5000 tokens in a window of 20000: %v %v", f, ok)
	}
	if m, ok := st.Snapshot().ModelOf(agentOf(t, st.Snapshot(), "a")); !ok || m.ID != "m" || m.ContextTokens != 20000 {
		t.Errorf("model %+v %v", m, ok)
	}
	apply(t, st, b.Request("a", "a.2", "m", "pk", secShared()))
	apply(t, st, b.Response("a", "a.2", "m", 30000, 4000, 0, 10, 0.01))
	if f, ok := fill("a"); !ok || f != 1 {
		t.Errorf("a prompt over the window counts as full: %v %v", f, ok)
	}
	// An agent on a model nothing has heard of has no window; one the price table has, has the table's.
	apply(t, st, b.Request("b", "b.1", "mystery-9", "pk", secShared()))
	apply(t, st, b.Response("b", "b.1", "mystery-9", 100, 0, 0, 1, 0))
	if f, ok := fill("b"); ok {
		t.Errorf("a model of unknown window: %v", f)
	}
	apply(t, st, b.Request("c", "c.1", "mock-1", "pk", secShared()))
	apply(t, st, b.Response("c", "c.1", "mock-1", 100000, 0, 0, 1, 0))
	if f, ok := fill("c"); !ok || f != 0.1 {
		t.Errorf("mock-1 has a window of a million in the table: %v %v", f, ok)
	}
}

func TestAnAgentIsCreatedOnFirstSightAndLaterEventsFillItIn(t *testing.T) {
	// The real log writes an agent's first board and state events before its agent.spawn (seq 3 and 4, then 5 in the demo log).
	b := newB()
	st := New()
	apply(t, st, b.Emit("mgr", events.TypeBoardOp, map[string]any{"op": "agent", "agent": "mgr", "role": "manager", "state": "running", "task": "", "line": "", "ctx_tokens": 1234, "version": 1}))
	apply(t, st, b.Emit("mgr", events.TypeAgentState, map[string]any{"id": "mgr", "state": "running", "line": "planning", "task": ""}))
	a := agentOf(t, st.Snapshot(), "mgr")
	if a.Role != "manager" || a.Status != StatusStarting || a.Line != "planning" || a.CtxTokens != 1234 {
		t.Fatalf("before the spawn: %+v", a)
	}
	apply(t, st, b.Emit("swarm", events.TypeAgentSpawn, map[string]any{"id": "mgr", "role": "manager", "model": "big-model"}))
	a = agentOf(t, st.Snapshot(), "mgr")
	if a.Model != "big-model" || a.SpawnSeq != 3 || a.Spawned.IsZero() || a.Parent != "" {
		t.Errorf("after the spawn: %+v", a)
	}
	if len(st.Agents()) != 1 {
		t.Errorf("one agent, got %v", agentIDs(st.Snapshot()))
	}
}

func TestAgentsAreCappedAndTheOldestFinishedOneMakesRoom(t *testing.T) {
	b := newB()
	st := New()
	for i := 0; i < MaxAgents; i++ {
		b.Advance(ms(1))
		apply(t, st, b.Spawn(fmt.Sprintf("w-%d", i), "backend", "", "mgr"))
	}
	if n := len(st.Agents()); n != MaxAgents {
		t.Fatalf("%d agents", n)
	}
	// All of them are active (starting): a new one does not fit and is dropped, counted.
	apply(t, st, b.Spawn("late-1", "backend", "", "mgr"))
	if n := len(st.Agents()); n != MaxAgents || st.Stats().Dropped != 1 {
		t.Fatalf("%d agents, %d dropped", n, st.Stats().Dropped)
	}
	if _, ok := st.Snapshot().Agent("late-1"); ok {
		t.Error("the new agent must have been dropped while every agent is active")
	}
	// Two finish, w-7 after w-3: the one that has been quiet the longest makes room.
	b.Advance(sec(1))
	apply(t, st, b.Emit("w-3", events.TypeAgentState, map[string]any{"id": "w-3", "state": "idle"}))
	b.Advance(sec(1))
	apply(t, st, b.Emit("w-7", events.TypeAgentState, map[string]any{"id": "w-7", "state": "idle"}))
	b.Advance(sec(1))
	apply(t, st, b.Spawn("late-2", "backend", "", "mgr"))
	sn := st.Snapshot()
	if len(sn.Agents) != MaxAgents {
		t.Fatalf("%d agents", len(sn.Agents))
	}
	if _, ok := sn.Agent("w-3"); ok {
		t.Error("w-3 finished first and must have been evicted")
	}
	if _, ok := sn.Agent("w-7"); !ok {
		t.Error("w-7 must still be there")
	}
	if _, ok := sn.Agent("late-2"); !ok {
		t.Error("late-2 must have been admitted")
	}
}

func TestCompactorSideRequestsAreNotMainRequests(t *testing.T) {
	b := newB()
	st := New()
	apply(t, st, b.Emit("", events.TypeSessionStart, startPayload(nil)))
	apply(t, st, b.Request("w-1", "w-1.1", "m", "pk", secShared()))
	apply(t, st, b.Response("w-1", "w-1.1", "m", 100, 0, 0, 10, 0.01))
	side := statetestSide("w-1", "w-1.c1")
	apply(t, st, b.Emit("w-1", events.TypeModelRequest, side))
	if a := agentOf(t, st.Snapshot(), "w-1"); a.InFlight != 1 || a.Stack.Req != "w-1.1" {
		t.Errorf("a side request is in flight but is not the agent's prompt: inflight %d stack %q", a.InFlight, a.Stack.Req)
	}
	apply(t, st, b.Emit("w-1", events.TypeModelResponse, map[string]any{"req": "w-1.c1", "model": "m", "side": true, "cost_usd": 0.002, "stop": "end_turn",
		"usage": map[string]any{"input_tokens": 5, "cache_read_tokens": 500, "output_tokens": 50}}))
	sn := st.Snapshot()
	a := agentOf(t, sn, "w-1")
	if a.Requests != 1 || a.SideRequests != 1 || a.Hits.Len() != 1 || a.InFlight != 0 {
		t.Errorf("requests %d side %d history %d inflight %d", a.Requests, a.SideRequests, a.Hits.Len(), a.InFlight)
	}
	if a.Tokens.CacheRead != 500 || !near(a.CostUSD, 0.012) {
		t.Errorf("the side request is paid for by the agent: %+v %v", a.Tokens, a.CostUSD)
	}
	tot := sn.Totals
	if tot.Requests != 2 || tot.Main != 1 || tot.Side != 1 || tot.Responses != 2 {
		t.Errorf("totals %+v", tot)
	}
	if a.Status != StatusThinking {
		t.Errorf("a compactor's answer must not end the run of the agent: status %s", a.Status)
	}
}

// tool.call carries the name as the model wrote it and, when the harness ran another tool because it repaired the name, as: the
// tool that ran (internal/agent exec.go). The agent is shown doing that tool, with the summary that tool's input has; tool.result
// still carries the name as written, and does not rename it.
func TestTheToolThatRanIsTheOneShownWhenAModelsNameWasRepaired(t *testing.T) {
	b := newB()
	st := New()
	apply(t, st, b.Emit("w-1", events.TypeToolCall, map[string]any{"id": "c1", "name": "read<|channel|>commentary", "as": "read", "input": map[string]any{"path": "src/a.go"}}))
	apply(t, st, b.Emit("w-1", events.TypeToolCall, map[string]any{"id": "c2", "name": "functions.grep", "as": "grep", "input": map[string]any{"pattern": "TODO", "path": "src"}}))
	if a := agentOf(t, st.Snapshot(), "w-1"); a.Tool != "grep" || a.ToolSummary != "TODO src" || a.OpenTools != 2 || a.Status != StatusTool {
		t.Errorf("shown doing %q %q (%d open, %s)", a.Tool, a.ToolSummary, a.OpenTools, a.Status)
	}
	b.Advance(ms(5))
	apply(t, st, b.Emit("w-1", events.TypeToolResult, map[string]any{"id": "c2", "name": "functions.grep", "error": false, "chars": 3, "ms": 4}))
	apply(t, st, b.Emit("w-1", events.TypeToolResult, map[string]any{"id": "c1", "name": "read<|channel|>commentary", "error": false, "chars": 3, "ms": 5}))
	sn := st.Snapshot()
	if !feedHas(sn, FeedTool, "w-1", "read src/a.go") || !feedHas(sn, FeedTool, "w-1", "grep TODO src") {
		t.Errorf("feed:\n%s", js(sn.Feed))
	}
	for _, l := range sn.Feed {
		if strings.Contains(l.Text, "<|") || strings.Contains(l.Text, "functions.") {
			t.Errorf("the feed shows the name as the model wrote it: %q", l.Text)
		}
	}
	// A call without as is what its name says.
	apply(t, st, b.Emit("w-1", events.TypeToolCall, map[string]any{"id": "c3", "name": "bash", "input": map[string]any{"command": "ls"}}))
	if a := agentOf(t, st.Snapshot(), "w-1"); a.Tool != "bash" || a.ToolSummary != "ls" {
		t.Errorf("shown doing %q %q", a.Tool, a.ToolSummary)
	}
}
