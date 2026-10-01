package state

import (
	"fmt"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/tui/state/statetest"
)

// agent.cancel is written by internal/agent (agent.go, run) as the last act of a run whose context was cancelled: phase says what
// the run was doing ("model", "tools" or "between"), cause why ("canceled" or "deadline") and steps how many model answers it had
// finished. The tests here use that shape; the log of a real cancelled session is folded in real_perm_test.go.

func TestACancelledRunIsRecordedWithItsPhaseAndEndsTheRun(t *testing.T) {
	for _, c := range []struct {
		phase, cause string
		steps        int
		text, detail string
	}{
		{CancelModel, CauseCanceled, 8, "sc-1's run was cancelled while it waited for the model", "after 8 steps"},
		{CancelTools, CauseCanceled, 1, "sc-1's run was cancelled while its tools ran", "after 1 step"},
		{CancelBetween, CauseCanceled, 0, "sc-1's run was cancelled", "before its first answer"},
		{CancelModel, CauseDeadline, 3, "sc-1's run was cancelled while it waited for the model", "after 3 steps, a time limit passed"},
		{"", "", 0, "sc-1's run was cancelled", "before its first answer"},
		{"a phase that does not exist", "weather", 2, "sc-1's run was cancelled", "after 2 steps"},
	} {
		t.Run(fmt.Sprintf("%s/%s/%d", c.phase, c.cause, c.steps), func(t *testing.T) {
			b := newB()
			st := New()
			apply(t, st, b.Spawn("sc-1", "scout", "T1", "mgr"))
			apply(t, st, b.Request("sc-1", "sc-1.1", "m", "pk", secShared()))
			apply(t, st, b.Call("sc-1", "c1", "bash", map[string]any{"command": "sleep 99"}))
			if a := agentOf(t, st.Snapshot(), "sc-1"); a.Status != StatusTool || a.InFlight != 1 || a.OpenTools != 1 {
				t.Fatalf("before: %s in flight %d tools %d", a.Status, a.InFlight, a.OpenTools)
			}
			b.Advance(ms(250))
			cancel := b.Cancel("sc-1", c.phase, c.cause, c.steps)
			apply(t, st, cancel)
			sn := st.Snapshot()
			a := agentOf(t, sn, "sc-1")
			if a.Status != StatusIdle || a.InFlight != 0 || a.OpenTools != 0 || a.Tool != "" {
				t.Errorf("the agent after its run was cancelled: %s in flight %d tools %d tool %q", a.Status, a.InFlight, a.OpenTools, a.Tool)
			}
			want := Cancel{Count: 1, Phase: c.phase, Cause: c.cause, Steps: c.steps, At: cancel.TS}
			if a.Cancel != want {
				t.Errorf("cancel %+v, want %+v", a.Cancel, want)
			}
			if tt := sn.Totals; tt.RunsCancelled != 1 || tt.Cancelled != 0 || tt.Errors != 0 {
				t.Errorf("totals: %d runs cancelled, %d requests cancelled, %d errors", tt.RunsCancelled, tt.Cancelled, tt.Errors)
			}
			if a.Errors != 0 {
				t.Errorf("a cancelled run is not an error of the agent: %d", a.Errors)
			}
			last := sn.Feed[len(sn.Feed)-1]
			if last.Kind != FeedCancel || last.Glyph != GlyphEnd || last.Agent != "sc-1" || last.Text != c.text || last.Detail != c.detail {
				t.Errorf("feed line %+v, want %q / %q", last, c.text, c.detail)
			}
		})
	}
}

// What the harness writes when a person presses Ctrl-C while a request is out: the request is reported cancelled, then the run is.
// Together they are one cancellation, which is not a failure, and the agent is idle and can be given work again.
func TestACancelledRequestAndItsRunAreOneCancellationNotAFailure(t *testing.T) {
	b := newB()
	st := New()
	apply(t, st, b.Emit("", events.TypeSessionStart, startPayload(map[string]any{"swarm": false})))
	apply(t, st, b.Request("main", "main.1", "m", "pk", secShared()))
	b.Advance(ms(900))
	apply(t, st, b.Emit("main", events.TypeModelError, map[string]any{"req": "main.1", "error": "provider: Post \"http://x/v1/chat/completions\": context canceled"}))
	apply(t, st, b.Cancel("main", CancelModel, CauseCanceled, 0))
	sn := st.Snapshot()
	a := agentOf(t, sn, "main")
	if a.Status != StatusIdle || a.Errors != 0 || a.InFlight != 0 || a.Cancel.Count != 1 {
		t.Errorf("agent: %s, %d errors, %d in flight, cancel %+v", a.Status, a.Errors, a.InFlight, a.Cancel)
	}
	if tt := sn.Totals; tt.Cancelled != 1 || tt.RunsCancelled != 1 || tt.Errors != 0 {
		t.Errorf("totals %+v", tt)
	}
	if !feedHas(sn, FeedNote, "main", "request cancelled") || !feedHas(sn, FeedCancel, "main", "cancelled while it waited for the model") || feedHas(sn, FeedError, "", "") {
		t.Errorf("feed:\n%s", js(sn.Feed))
	}
	// The next turn works again: the agent is not left cancelled.
	apply(t, st, b.Emit("main", events.TypeUserInput, map[string]any{"text": "try again"}))
	apply(t, st, b.Request("main", "main.2", "m", "pk", secShared()))
	if a := agentOf(t, st.Snapshot(), "main"); a.Status != StatusThinking || a.Cancel.Count != 1 {
		t.Errorf("after the next turn began: %s, cancel %+v", a.Status, a.Cancel)
	}
}

// A cancel that arrives after the agent had ended for good (the manager answered, the worker failed) does not bring it back to idle.
func TestACancelDoesNotBringAnEndedAgentBack(t *testing.T) {
	for _, c := range []struct {
		state string
		want  Status
	}{{"done", StatusDone}, {"failed", StatusError}} {
		t.Run(c.state, func(t *testing.T) {
			b := newB()
			st := New()
			apply(t, st, b.Spawn("w-1", "backend", "T1", "mgr"))
			apply(t, st, b.Request("w-1", "w-1.1", "m", "pk", secShared()))
			apply(t, st, b.Emit("w-1", events.TypeAgentState, map[string]any{"id": "w-1", "state": c.state, "line": "", "task": ""}))
			apply(t, st, b.Cancel("w-1", CancelModel, CauseCanceled, 1))
			a := agentOf(t, st.Snapshot(), "w-1")
			if a.Status != c.want || a.InFlight != 0 || a.Cancel.Count != 1 {
				t.Errorf("after the cancel: %s in flight %d cancel %+v", a.Status, a.InFlight, a.Cancel)
			}
		})
	}
}

func TestEveryCancelledRunIsCountedAndTheLatestIsDescribed(t *testing.T) {
	b := newB()
	st := New()
	apply(t, st, b.Cancel("a-1", CancelModel, CauseCanceled, 4))
	b.Advance(sec(3))
	apply(t, st, b.Cancel("a-2", CancelTools, CauseCanceled, 1))
	last := b.Cancel("a-1", CancelBetween, CauseDeadline, 9)
	apply(t, st, last)
	apply(t, st, b.Cancel("", CancelModel, CauseCanceled, 0)) // a run of nobody the State tracks: counted, and in the feed
	sn := st.Snapshot()
	a1 := agentOf(t, sn, "a-1")
	if sn.Totals.RunsCancelled != 4 || a1.Cancel != (Cancel{Count: 2, Phase: CancelBetween, Cause: CauseDeadline, Steps: 9, At: last.TS}) || agentOf(t, sn, "a-2").Cancel.Count != 1 {
		t.Errorf("totals %d, a-1 %+v", sn.Totals.RunsCancelled, a1.Cancel)
	}
	if !feedHas(sn, FeedCancel, "", "a run was cancelled while it waited for the model") {
		t.Errorf("feed:\n%s", js(sn.Feed))
	}
}

// The stuck guard's two phases as the agent writes them: a nudge carries the note that was given to the model, the stop carries the
// error that ended the run. Each is counted in its own column and the latest phase and note are kept.
func TestTheStuckGuardsNudgeAndStopAreCountedSeparately(t *testing.T) {
	b := newB()
	st := New()
	apply(t, st, b.Spawn("w-1", "backend", "T1", "mgr"))
	apply(t, st, b.Emit("w-1", events.TypeAgentStuck, map[string]any{"phase": "nudge", "note": "[harness] bash has now failed the same way 4 times"}))
	n := agentOf(t, st.Snapshot(), "w-1").Stuck
	if n.Phase != "nudge" || n.Nudges != 1 || n.Stops != 0 || !n.Active || n.Note != "[harness] bash has now failed the same way 4 times" {
		t.Errorf("after the nudge: %+v", n)
	}
	apply(t, st, b.Emit("w-1", events.TypeAgentStuck, map[string]any{"phase": "nudge", "note": "again"}))
	apply(t, st, b.Emit("w-1", events.TypeAgentStuck, map[string]any{"phase": "stop", "error": "agent w-1: agent stuck: bash failed the same way 8 times"}))
	s := agentOf(t, st.Snapshot(), "w-1").Stuck
	if s.Phase != "stop" || s.Nudges != 2 || s.Stops != 1 || s.Note != "agent w-1: agent stuck: bash failed the same way 8 times" {
		t.Errorf("after the stop: %+v", s)
	}
}

// user.input opens a run (a person's message, or the task a worker is handed), and a run starts with a repetition guard that has seen
// nothing: a worker that was told it was stuck, or whose run the guard ended, is not stuck in the next one. (Without this a chat
// whose agent was stopped by the guard would show "stuck" through every later turn until a tool call succeeded.)
func TestANewRunStartsWithoutTheLastOnesStuckWarning(t *testing.T) {
	for _, origin := range []string{"", "task"} {
		t.Run("origin="+origin, func(t *testing.T) {
			b := newB()
			st := New()
			apply(t, st, b.Request("main", "main.1", "m", "pk", secShared()))
			apply(t, st, b.Emit("main", events.TypeAgentStuck, map[string]any{"phase": "nudge", "note": "n"}))
			apply(t, st, b.Emit("main", events.TypeAgentStuck, map[string]any{"phase": "stop", "error": "e"}))
			if a := agentOf(t, st.Snapshot(), "main"); a.Status != StatusStuck || !a.Stuck.Active {
				t.Fatalf("after the stop: %s %+v", a.Status, a.Stuck)
			}
			input := map[string]any{"text": "try something else"}
			if origin != "" {
				input["origin"] = origin
			}
			apply(t, st, b.Emit("main", events.TypeUserInput, input))
			a := agentOf(t, st.Snapshot(), "main")
			if a.Status == StatusStuck || a.Stuck.Active {
				t.Errorf("a new run is still stuck: %s %+v", a.Status, a.Stuck)
			}
			if a.Stuck.Nudges != 1 || a.Stuck.Stops != 1 || a.Stuck.Phase != "stop" {
				t.Errorf("what happened is still on record: %+v", a.Stuck)
			}
		})
	}
}

// A request that is cut off because its run was stopped did not fail. The error text says so in the transport's own words: Go's
// for a cancelled context, and the provider adapters' (internal/provider/openaichat and anthropic write "provider: network: request
// cancelled", which is what a person's Ctrl-C during a request really leaves in the log). Anything else that merely mentions
// cancelling is a failure.
func TestARequestCutOffWithItsRunIsNotAFailureWhateverTheTransportCallsIt(t *testing.T) {
	for _, c := range []struct {
		text      string
		cancelled bool
	}{
		{"provider: network: request cancelled", true},
		{"context canceled", true},
		{`provider: network: Post "http://127.0.0.1:1/v1/chat/completions": context canceled`, true},
		{"CONTEXT CANCELED", true},
		{"context cancelled", true},
		{"provider: server (http 500): upstream request cancelled by the gateway", false},
		{"provider: rate_limit (http 429): slow down", false},
		{"provider: network: connection reset by peer", false},
		{"", false},
	} {
		t.Run(c.text, func(t *testing.T) {
			b := newB()
			st := New()
			apply(t, st, b.Request("main", "main.1", "m", "pk", secShared()))
			apply(t, st, b.Emit("main", events.TypeModelError, map[string]any{"req": "main.1", "error": c.text}))
			sn := st.Snapshot()
			a := agentOf(t, sn, "main")
			if c.cancelled {
				if sn.Totals.Cancelled != 1 || sn.Totals.Errors != 0 || a.Errors != 0 || a.Status != StatusIdle || !feedHas(sn, FeedNote, "main", "request cancelled") {
					t.Errorf("cancelled: totals %+v agent %s errors %d", sn.Totals, a.Status, a.Errors)
				}
				return
			}
			if sn.Totals.Cancelled != 0 || sn.Totals.Errors != 1 || a.Errors != 1 || a.Status != StatusError || !feedHas(sn, FeedError, "main", "model request failed") {
				t.Errorf("a failure: totals %+v agent %s errors %d", sn.Totals, a.Status, a.Errors)
			}
		})
	}
}

// The transport's wording is not something to rely on, and the harness says outright that the run was cancelled: a request that
// failed, with nothing of its agent's in between, and then agent.cancel in the phase "model", failed because of the cancellation.
// The counts, the agent and the line of the feed become what they would have been had the request been recognised at once.
func TestARequestThatFailedAndThenItsRunWasCancelledWasCancelled(t *testing.T) {
	b := newB()
	st := New()
	apply(t, st, b.Request("main", "main.1", "m", "pk", secShared()))
	apply(t, st, b.Response("main", "main.1", "m", 10, 0, 0, 5, 0))
	apply(t, st, b.Request("main", "main.2", "m", "pk", secShared()))
	apply(t, st, b.Emit("w-1", events.TypeModelError, map[string]any{"req": "w-1.9", "error": "provider: server (http 500): boom"})) // another agent's, a real one
	apply(t, st, b.Emit("main", events.TypeModelError, map[string]any{"req": "main.2", "error": "provider: transport: the operation was aborted"}))
	sn := st.Snapshot()
	if a := agentOf(t, sn, "main"); a.Status != StatusError || a.Errors != 1 || sn.Totals.Errors != 2 || sn.Totals.Cancelled != 0 {
		t.Fatalf("before the cancel: %s errors %d, totals %+v", a.Status, a.Errors, sn.Totals)
	}
	apply(t, st, b.Cancel("main", CancelModel, CauseCanceled, 1))
	sn = st.Snapshot()
	a := agentOf(t, sn, "main")
	if a.Status != StatusIdle || a.Errors != 0 || a.Cancel.Count != 1 || sn.Totals.Errors != 1 || sn.Totals.Cancelled != 1 || sn.Totals.RunsCancelled != 1 {
		t.Errorf("after the cancel: %s errors %d cancel %+v totals %+v", a.Status, a.Errors, a.Cancel, sn.Totals)
	}
	if !feedHas(sn, FeedNote, "main", "request cancelled") || feedHas(sn, FeedError, "main", "") || !feedHas(sn, FeedError, "w-1", "boom") {
		t.Errorf("the line of the failure is corrected, and only that one:\n%s", js(sn.Feed))
	}
	var corrected FeedLine
	for _, l := range sn.Feed {
		if l.Kind == FeedNote && l.Agent == "main" {
			corrected = l
		}
	}
	if corrected.Seq == 0 || corrected.Glyph != GlyphInfo || corrected.Detail != "main.2" {
		t.Errorf("the corrected line: %+v", corrected)
	}
}

// A failure is a failure unless the run's cancellation comes straight after it.
func TestAFailedRequestIsNotReclassifiedWhenItWasNotTheEndOfTheRun(t *testing.T) {
	fail := func(b *statetest.Builder, req string) events.Event {
		return b.Emit("main", events.TypeModelError, map[string]any{"req": req, "error": "provider: transport: the operation was aborted"})
	}
	for _, c := range []struct {
		name string
		run  func(b *statetest.Builder, st *State)
		want int // failures left
	}{
		{"the cancel names another phase", func(b *statetest.Builder, st *State) {
			apply(t, st, fail(b, "main.1"), b.Cancel("main", CancelBetween, CauseCanceled, 1))
		}, 1},
		{"the agent went on with a new request", func(b *statetest.Builder, st *State) {
			apply(t, st, fail(b, "main.1"), b.Request("main", "main.2", "m", "pk", secShared()), b.Cancel("main", CancelModel, CauseCanceled, 1))
		}, 1},
		{"the agent went on with a tool call", func(b *statetest.Builder, st *State) {
			apply(t, st, fail(b, "main.1"), b.Call("main", "c1", "read", map[string]any{"path": "x"}), b.Cancel("main", CancelModel, CauseCanceled, 1))
		}, 1},
		{"a new input began another run", func(b *statetest.Builder, st *State) {
			apply(t, st, fail(b, "main.1"), b.Emit("main", events.TypeUserInput, map[string]any{"text": "again"}), b.Cancel("main", CancelModel, CauseCanceled, 1))
		}, 1},
		{"a compactor's request failed, not the run's", func(b *statetest.Builder, st *State) {
			apply(t, st, b.Emit("main", events.TypeModelRequest, statetestSide("main", "main.c1")))
			apply(t, st, b.Emit("main", events.TypeModelError, map[string]any{"req": "main.c1", "error": "provider: transport: the operation was aborted"}), b.Cancel("main", CancelModel, CauseCanceled, 1))
		}, 1},
		{"nothing failed", func(b *statetest.Builder, st *State) {
			apply(t, st, b.Request("main", "main.1", "m", "pk", secShared()), b.Cancel("main", CancelModel, CauseCanceled, 0))
		}, 0},
		{"the failure is reclassified only once", func(b *statetest.Builder, st *State) {
			apply(t, st, fail(b, "main.1"), b.Cancel("main", CancelModel, CauseCanceled, 1), b.Cancel("main", CancelModel, CauseCanceled, 1))
		}, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := newB()
			st := New()
			c.run(b, st)
			sn := st.Snapshot()
			if sn.Totals.Errors != c.want || sn.Totals.Errors+sn.Totals.Cancelled > 1 {
				t.Errorf("totals %+v, want %d failures", sn.Totals, c.want)
			}
		})
	}
}
