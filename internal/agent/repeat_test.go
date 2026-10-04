package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/cost"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/kv"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/tools"
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

// A model that is refused and has nobody to ask tries another command each time: the refusals count together. It is told at the fifth,
// once, and the run ends at the tenth with the last results in the thread (glm-5.3-flash made 23 of them in five minutes before this).
func TestARunWhoseEveryWayIsRefusedIsNudgedThenStopped(t *testing.T) {
	refusing := fakeTool{name: "bash", run: func(in json.RawMessage) *tools.Result {
		return tools.Errorf("permission denied: approval required: accept-edits mode: %s%s", in, perm.NoOneToAsk)
	}}
	r := newRig(t, rigOpts{noCompact: true, tools: []fakeTool{refusing}, steps: 100}, func(c *mock.Call) mock.Reply {
		return mock.Reply{ToolCalls: []mock.ToolCall{callN(c, "bash", fmt.Sprintf(`{"command":"go test ./p%d"}`, assistantTurns(c)))}}
	})
	res, err := r.agent.Run(context.Background(), "fix it and run the tests")
	if !errors.Is(err, agent.ErrStuck) || !strings.Contains(err.Error(), "10 of its last 10 actions were refused and this run has no one to ask") {
		t.Fatalf("err = %v, want ErrStuck after ten refusals", err)
	}
	if res.Steps != 10 {
		t.Fatalf("the run took %d steps, want 10", res.Steps)
	}
	snap := r.agent.Thread().Snapshot()
	if verr := kv.Validate(snap.Turns); verr != nil {
		t.Fatalf("the thread is not valid after a stop: %v", verr)
	}
	notes := 0
	for _, turn := range snap.Turns {
		for _, b := range turn.Blocks {
			if b.Kind == core.BlockText && strings.HasPrefix(b.Text, "[harness] 5 of your last 5 actions were refused") {
				notes++
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

// A failing test run followed by an edit of the test alone reaches the model as a note in the results of that edit, once. An edit of the
// code instead says nothing.
func TestEditingOnlyTheTestAfterAFailingRunIsNotedToTheModel(t *testing.T) {
	for _, tc := range []struct {
		edit string
		want bool
	}{{"cache_test.go", true}, {"cache.go", false}} {
		failing := fakeTool{name: "bash", run: func(json.RawMessage) *tools.Result {
			return &tools.Result{Text: "--- FAIL: TestEvict\n[exit code 1]", Meta: map[string]any{"exit_code": 1, "timed_out": false}}
		}}
		editor := fakeTool{name: "edit", run: func(json.RawMessage) *tools.Result { return &tools.Result{Text: "edited"} }}
		var sawNote bool
		r := newRig(t, rigOpts{noCompact: true, tools: []fakeTool{failing, editor}, steps: 10}, func(c *mock.Call) mock.Reply {
			if strings.Contains(c.LastUser(), "[harness] The last test run failed and you changed only test files") {
				sawNote = true
			}
			switch assistantTurns(c) {
			case 0:
				return mock.Reply{ToolCalls: []mock.ToolCall{callN(c, "bash", `{"command":"go test ./..."}`)}}
			case 1:
				return mock.Reply{ToolCalls: []mock.ToolCall{callN(c, "edit", `{"path":"`+tc.edit+`"}`)}}
			}
			return mock.Reply{Text: "done"}
		})
		if _, err := r.agent.Run(context.Background(), "fix the cache"); err != nil {
			t.Fatal(err)
		}
		if sawNote != tc.want {
			t.Errorf("editing %s: the model saw the note: %v, want %v", tc.edit, sawNote, tc.want)
		}
	}
}

// An answer given while the plan has open steps is sent back once, with a note that says what to do; the second answer ends the run (a
// model that no longer means its plan is not held to it for ever).
func TestARunWithOpenPlanStepsIsSentBackOnce(t *testing.T) {
	open := 2
	var notes int
	r := newRig(t, rigOpts{noCompact: true, steps: 10, planOpen: func(string) int { return open }}, func(c *mock.Call) mock.Reply {
		if strings.Contains(c.LastUser(), "[harness] Your plan still has 2 open step(s)") {
			notes++
		}
		return mock.Reply{Text: "all done"}
	})
	res, err := r.agent.Run(context.Background(), "do the thing")
	if err != nil {
		t.Fatal(err)
	}
	if notes != 1 || res.Steps != 2 {
		t.Errorf("the model saw the note %d times in %d steps; want once, in two", notes, res.Steps)
	}
	if n := len(r.log.OfType(events.TypeAgentStuck)); n != 1 {
		t.Errorf("agent.stuck events: %d", n)
	}
	// With nothing open, the first answer ends the run.
	open = 0
	notes = 0
	if res, err := r.agent.Run(context.Background(), "and again"); err != nil || res.Steps != 1 || notes != 0 {
		t.Errorf("a finished plan: %+v %v %d", res, err, notes)
	}
}

// An answer given after code was changed and before any test ran is sent back once with the project's test command; a test run, an edit of
// documentation only, or a project with no test command is not nagged.
func TestAnAnswerAfterAnEditWithNoTestRunIsSentBackToRunTheTests(t *testing.T) {
	type step struct{ tool, args string }
	for _, tc := range []struct {
		name  string
		hint  string
		steps []step
		want  int // the number of times the model saw the note
	}{
		{"edit then answer", "go test ./...", []step{{"edit", `{"path":"a.go"}`}}, 1},
		{"edit, test, answer", "go test ./...", []step{{"edit", `{"path":"a.go"}`}, {"bash", `{"command":"go test ./..."}`}}, 0},
		{"test, edit, answer", "go test ./...", []step{{"bash", `{"command":"go test ./..."}`}, {"edit", `{"path":"a.go"}`}}, 1},
		{"a doc only", "go test ./...", []step{{"edit", `{"path":"README.md"}`}}, 0},
		{"no test command known", "", []step{{"edit", `{"path":"a.go"}`}}, 0},
		{"no edit at all", "go test ./...", nil, 0},
	} {
		editor := fakeTool{name: "edit", run: func(json.RawMessage) *tools.Result { return &tools.Result{Text: "edited"} }}
		bash := fakeTool{name: "bash", run: func(json.RawMessage) *tools.Result {
			return &tools.Result{Text: "ok\n[exit code 0]", Meta: map[string]any{"exit_code": 0, "timed_out": false}}
		}}
		var saw int
		r := newRig(t, rigOpts{noCompact: true, tools: []fakeTool{editor, bash}, steps: 12, verifyHint: tc.hint}, func(c *mock.Call) mock.Reply {
			if strings.Contains(c.LastUser(), "[harness] You changed code and have not run the tests since. Run `go test ./...`") {
				saw++
			}
			if n := assistantTurns(c); n < len(tc.steps) {
				return mock.Reply{ToolCalls: []mock.ToolCall{callN(c, tc.steps[n].tool, tc.steps[n].args)}}
			}
			return mock.Reply{Text: "done"}
		})
		if _, err := r.agent.Run(context.Background(), "change it"); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if saw != tc.want {
			t.Errorf("%s: the model saw the note %d times, want %d", tc.name, saw, tc.want)
		}
	}
}

func TestVerificationHintUsesToolOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name       string
		editError  bool
		runTest    bool
		testError  bool
		testFailed bool
		want       int
	}{
		{name: "refused edit", editError: true},
		{name: "refused test after edit", runTest: true, testError: true, want: 1},
		{name: "executed failing test", runTest: true, testFailed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			editor := fakeTool{name: "edit", run: func(json.RawMessage) *tools.Result {
				if tc.editError {
					return tools.Errorf("permission denied: edit was refused")
				}
				return &tools.Result{Text: "edited"}
			}}
			bash := fakeTool{name: "bash", run: func(json.RawMessage) *tools.Result {
				if tc.testError {
					return tools.Errorf("permission denied: command was refused")
				}
				exit := 0
				if tc.testFailed {
					exit = 1
				}
				return &tools.Result{Text: "test output", Meta: map[string]any{"exit_code": exit}}
			}}
			var notes int
			r := newRig(t, rigOpts{noCompact: true, tools: []fakeTool{editor, bash}, steps: 8, verifyHint: "go test ./..."}, func(c *mock.Call) mock.Reply {
				if strings.Contains(c.LastUser(), "[harness] You changed code and have not run the tests since.") {
					notes++
				}
				switch assistantTurns(c) {
				case 0:
					return mock.Reply{ToolCalls: []mock.ToolCall{callN(c, "edit", `{"path":"a.go"}`)}}
				case 1:
					if tc.runTest {
						return mock.Reply{ToolCalls: []mock.ToolCall{callN(c, "bash", `{"command":"go test ./..."}`)}}
					}
				}
				return mock.Reply{Text: "finished"}
			})
			if _, err := r.agent.Run(context.Background(), "change the implementation and verify it"); err != nil {
				t.Fatal(err)
			}
			if notes != tc.want {
				t.Fatalf("verification reminders = %d, want %d", notes, tc.want)
			}
		})
	}
}

// A server whose window is smaller than the prompt reads only that much and reports only that much: when the prompt grows and the tokens
// reported do not, the harness says so once, with the window it saw, for a model whose window it was never told.
func TestAServerThatCutsThePromptOffIsNoticedOnce(t *testing.T) {
	unknown := cost.Fallback("local-8b")
	unknown.Cache = cost.OpenAICacheModel()
	truncated := func(limit int) (kinds []string) {
		r := newRig(t, rigOpts{noCompact: true, steps: 40, model: &unknown, mock: mock.Config{ContextLimit: limit}}, scriptedWork(8, func(*mock.Call) string { return "" }))
		if _, err := r.agent.Run(context.Background(), "build everything"); err != nil {
			t.Fatal(err)
		}
		for _, e := range r.log.OfType(events.TypeCacheAnomaly) {
			var p struct {
				Kind   string
				Window int
			}
			_ = json.Unmarshal(e.Data, &p)
			if p.Kind == "truncated_prompt" {
				kinds = append(kinds, fmt.Sprint(p.Window))
			}
		}
		return kinds
	}
	if got := truncated(3500); len(got) != 1 || got[0] != "3584" {
		t.Errorf("a server with a 3500-token window: %v (want one report of a 3584-token window)", got)
	}
	if got := truncated(0); len(got) != 0 {
		t.Errorf("a server that reads the whole prompt: %v", got)
	}
}
