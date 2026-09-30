package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/reee344/sleipnir/internal/agent"
	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/kv"
	"github.com/reee344/sleipnir/internal/provider/mock"
	"github.com/reee344/sleipnir/internal/tools"
)

// callN numbers a model's calls so that every tool_use in a thread has an id of its own.
func callN(c *mock.Call, name, args string) mock.ToolCall {
	return mock.ToolCall{ID: fmt.Sprintf("c%d", assistantTurns(c)), Name: name, Args: args}
}

func assistantTurns(c *mock.Call) int {
	n := 0
	for _, m := range c.Messages {
		if m.Role == "assistant" {
			n++
		}
	}
	return n
}

func stuckEvents(r *rig) (nudges, stops int) {
	for _, e := range r.log.OfType(events.TypeAgentStuck) {
		var p struct{ Phase string }
		_ = json.Unmarshal(e.Data, &p)
		switch p.Phase {
		case "nudge":
			nudges++
		case "stop":
			stops++
		}
	}
	return
}

// A model that repeats one refused call is told so at the fourth failure and stopped at the
// eighth, with the last results in the thread: it does not spend two hundred requests on it.
func TestARunThatRepeatsOneFailingCallIsNudgedThenStopped(t *testing.T) {
	refusing := fakeTool{name: "flaky", run: func(json.RawMessage) *tools.Result {
		return tools.Errorf("permission denied: approval required")
	}}
	r := newRig(t, rigOpts{noCompact: true, tools: []fakeTool{refusing}, steps: 100}, func(c *mock.Call) mock.Reply {
		return mock.Reply{ToolCalls: []mock.ToolCall{callN(c, "flaky", `{"path":"/x"}`)}}
	})
	res, err := r.agent.Run(context.Background(), "go")
	if !errors.Is(err, agent.ErrStuck) {
		t.Fatalf("err = %v, want ErrStuck", err)
	}
	if res.Steps != 8 {
		t.Fatalf("the run took %d steps, want 8 (the eighth identical failure ends it)", res.Steps)
	}
	if !strings.Contains(err.Error(), "flaky failed the same way 8 times") {
		t.Errorf("err = %v", err)
	}

	snap := r.agent.Thread().Snapshot()
	if verr := kv.Validate(snap.Turns); verr != nil {
		t.Fatalf("the thread is not valid after a stop: %v", verr)
	}
	// The last turn is the eighth results turn: every call has its answer.
	last := snap.Turns[len(snap.Turns)-1]
	if last.Role != core.RoleUser || last.Blocks[0].Kind != core.BlockToolResult {
		t.Fatalf("the run must end with the results of its last call in the thread: %+v", last)
	}
	// The nudge rode with the fourth results, once, as text after the results.
	notes := 0
	for i, turn := range snap.Turns {
		for _, b := range turn.Blocks {
			if b.Kind == core.BlockText && strings.HasPrefix(b.Text, "[harness] flaky has now failed the same way 4 times") {
				notes++
				if turn.Role != core.RoleUser || turn.Blocks[0].Kind != core.BlockToolResult {
					t.Errorf("turn %d: the note is not with the tool results: %+v", i, turn)
				}
			}
		}
	}
	if notes != 1 {
		t.Fatalf("%d notes in the thread, want 1", notes)
	}
	if n, s := stuckEvents(r); n != 1 || s != 1 {
		t.Fatalf("agent.stuck events: %d nudges and %d stops, want 1 and 1", n, s)
	}
}

// Work that is not repetition goes on: the same call that succeeds every time (a poll), and a
// failing call that fails differently each time (a model trying things).
func TestSuccessAndVariedFailureAreNotAStuckLoop(t *testing.T) {
	var n int
	tl := []fakeTool{
		{name: "poll", readOnly: true, run: func(json.RawMessage) *tools.Result { return &tools.Result{Text: "still running"} }},
		{name: "try", run: func(json.RawMessage) *tools.Result { n++; return tools.Errorf("attempt %d failed", n) }},
	}
	r := newRig(t, rigOpts{noCompact: true, tools: tl, steps: 100}, func(c *mock.Call) mock.Reply {
		switch k := assistantTurns(c); {
		case k < 30:
			return mock.Reply{ToolCalls: []mock.ToolCall{callN(c, "poll", `{"job":"1"}`)}}
		case k < 60:
			return mock.Reply{ToolCalls: []mock.ToolCall{callN(c, "try", `{}`)}}
		}
		return mock.Reply{Text: "done"}
	})
	res, err := r.agent.Run(context.Background(), "go")
	if err != nil || res.Text != "done" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if n, s := stuckEvents(r); n != 0 || s != 0 {
		t.Fatalf("agent.stuck events for work that was not repeating: %d, %d", n, s)
	}
}

// Interleaving other calls does not hide a loop: eight identical failures among the last twenty
// calls end the run.
func TestAFailureRepeatedBetweenOtherCallsIsStillALoop(t *testing.T) {
	tl := []fakeTool{
		{name: "flaky", run: func(json.RawMessage) *tools.Result { return tools.Errorf("no") }},
		{name: "look", readOnly: true, run: func(json.RawMessage) *tools.Result { return &tools.Result{Text: "same"} }},
	}
	r := newRig(t, rigOpts{noCompact: true, tools: tl, steps: 100}, func(c *mock.Call) mock.Reply {
		if assistantTurns(c)%2 == 0 {
			return mock.Reply{ToolCalls: []mock.ToolCall{callN(c, "flaky", `{}`)}}
		}
		return mock.Reply{ToolCalls: []mock.ToolCall{callN(c, "look", `{}`)}}
	})
	res, err := r.agent.Run(context.Background(), "go")
	if !errors.Is(err, agent.ErrStuck) {
		t.Fatalf("err = %v", err)
	}
	if res.Steps != 15 { // the eighth failure is the fifteenth call: flaky, look, flaky, ...
		t.Fatalf("steps = %d, want 15", res.Steps)
	}
}

// A command that exits with a status other than 0 is not a tool error (the model is meant to read what it printed), so the guard
// used to count nothing for it: a model that ran one failing command until the step limit was never told. The same command
// failing the same way is a loop whatever the tool calls it; what it prints about how long it took is not a difference.
func TestAFailingCommandRepeatedIsALoopToo(t *testing.T) {
	var n int
	failing := fakeTool{name: "bash", run: func(json.RawMessage) *tools.Result {
		n++
		return &tools.Result{
			Text: fmt.Sprintf("--- FAIL: TestList (0.%02ds)\nFAIL\tx\t%d.004s\n[exit code 1]", n, n),
			Meta: map[string]any{"exit_code": 1, "timed_out": false},
		}
	}}
	r := newRig(t, rigOpts{noCompact: true, tools: []fakeTool{failing}, steps: 100}, func(c *mock.Call) mock.Reply {
		return mock.Reply{ToolCalls: []mock.ToolCall{callN(c, "bash", `{"command":"go test ./..."}`)}}
	})
	res, err := r.agent.Run(context.Background(), "go")
	if !errors.Is(err, agent.ErrStuck) {
		t.Fatalf("err = %v, want ErrStuck (steps %d)", err, res.Steps)
	}
	if res.Steps != 8 || !strings.Contains(err.Error(), "bash failed the same way 8 times") {
		t.Fatalf("steps = %d, err = %v", res.Steps, err)
	}
	if nudges, stops := stuckEvents(r); nudges != 1 || stops != 1 {
		t.Fatalf("agent.stuck events: %d nudges and %d stops, want 1 and 1", nudges, stops)
	}
}

// Failing commands that fail differently (a model fixing one test after another) and commands that succeed are not repetition.
func TestFailingCommandsThatDifferAreNotALoop(t *testing.T) {
	var n int
	cmd := fakeTool{name: "bash", run: func(json.RawMessage) *tools.Result {
		n++
		if n > 40 {
			return &tools.Result{Text: "ok\n[exit code 0]", Meta: map[string]any{"exit_code": 0}}
		}
		return &tools.Result{Text: fmt.Sprintf("--- FAIL: Test%c (0.01s)\n[exit code 1]", 'A'+n%26), Meta: map[string]any{"exit_code": 1}}
	}}
	r := newRig(t, rigOpts{noCompact: true, tools: []fakeTool{cmd}, steps: 100}, func(c *mock.Call) mock.Reply {
		if assistantTurns(c) < 60 {
			return mock.Reply{ToolCalls: []mock.ToolCall{callN(c, "bash", `{"command":"go test ./..."}`)}}
		}
		return mock.Reply{Text: "done"}
	})
	res, err := r.agent.Run(context.Background(), "go")
	if err != nil || res.Text != "done" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if nudges, stops := stuckEvents(r); nudges != 0 || stops != 0 {
		t.Fatalf("agent.stuck events for commands that were not repeating: %d, %d", nudges, stops)
	}
}
