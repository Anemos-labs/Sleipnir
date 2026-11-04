package tooleval

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/core"
)

var testTools = []core.ToolSpec{
	{Name: "spawn", Description: "start a worker", InputSchema: json.RawMessage(`{"type":"object","properties":{"task":{"type":"string"},"solo":{"type":"boolean"},"files":{"type":"array","items":{"type":"string"}}},"required":["task"]}`)},
	{Name: "wait", Description: "sleep", InputSchema: json.RawMessage(`{"type":"object","properties":{"timeout_sec":{"type":"integer"}}}`)},
	{Name: "task", Description: "the board", InputSchema: json.RawMessage(`{"type":"object","properties":{"action":{"type":"string","enum":["create","accept"]},"id":{"type":"string"}},"required":["action"]}`)},
}

func call(tool, args string) core.Block { return core.ToolUse("c", tool, json.RawMessage(args)) }

func TestCheck(t *testing.T) {
	spawnT1Solo := Rule{Tool: "spawn", Args: map[string]any{"task": "^T1$", "solo": true}, Why: "small work"}
	tests := []struct {
		name  string
		calls []core.Block
		e     Expect
		pass  bool
		why   string
	}{
		{"all matched", []core.Block{call("spawn", `{"task":"T1","solo":true}`)}, Expect{All: []Rule{spawnT1Solo}}, true, ""},
		{"all missed", []core.Block{call("spawn", `{"task":"T1"}`)}, Expect{All: []Rule{spawnT1Solo}}, false, `no call spawn(solo=true task="^T1$"): small work (called spawn {"task":"T1"})`},
		{"text only", nil, Expect{Any: []Rule{{Tool: "wait"}}}, false, "none of wait (no call: answered in text)"},
		{"any second", []core.Block{call("wait", `{}`)}, Expect{Any: []Rule{{Tool: "spawn"}, {Tool: "wait"}}}, true, ""},
		{"none hit", []core.Block{call("spawn", `{"task":"T23"}`)}, Expect{None: []Rule{{Tool: "spawn", Args: map[string]any{"task": "^T23$"}, Why: "queued"}}}, false, `called spawn {"task":"T23"}: queued`},
		{"none missed", []core.Block{call("spawn", `{"task":"T24"}`)}, Expect{None: []Rule{{Tool: "spawn", Args: map[string]any{"task": "^T23$"}}}}, true, ""},
		{"min calls", nil, Expect{MinCalls: 1}, false, "0 calls, fewer than 1"},
		{"max calls", []core.Block{call("wait", `{}`), call("wait", `{}`)}, Expect{MaxCalls: 1}, false, "2 calls, more than 1"},
		{"unknown tool first", []core.Block{call("spawn_worker", `{}`)}, Expect{None: []Rule{{Tool: "wait"}}}, false, `called "spawn_worker", which is not a tool`},
		{"missing required", []core.Block{call("spawn", `{"solo":true}`)}, Expect{MinCalls: 1}, false, `spawn: no "task" argument`},
		{"wrong type", []core.Block{call("spawn", `{"task":"T1","solo":"yes"}`)}, Expect{MinCalls: 1}, false, "spawn: solo is not a boolean"},
		{"not an integer", []core.Block{call("wait", `{"timeout_sec":1.5}`)}, Expect{MinCalls: 1}, false, "wait: timeout_sec is not an integer"},
		{"enum", []core.Block{call("task", `{"action":"approve"}`)}, Expect{MinCalls: 1}, false, `task: action is "approve", not one of [create accept]`},
		{"array items", []core.Block{call("spawn", `{"task":"T1","files":["a",2]}`)}, Expect{MinCalls: 1}, false, "spawn: files[1] is not a string"},
		{"empty input is an object", []core.Block{core.ToolUse("c", "wait", nil)}, Expect{MinCalls: 1}, true, ""},
		{"broken JSON", []core.Block{{Kind: core.BlockToolUse, ToolName: "wait", Invalid: "unexpected end"}}, Expect{MinCalls: 1}, false, "wait: the arguments are not JSON (unexpected end)"},
	}
	for _, tc := range tests {
		pass, why := Check(tc.calls, testTools, tc.e)
		if pass != tc.pass || why != tc.why {
			t.Errorf("%s: got %v %q, want %v %q", tc.name, pass, why, tc.pass, tc.why)
		}
	}
}

func TestArgMatches(t *testing.T) {
	tests := []struct {
		want, got any
		ok        bool
	}{
		{"^T1$", "T1", true},
		{"^T1$", "T12", false},
		{"^$", nil, true},         // a missing argument matches only an empty pattern
		{"review", nil, false},    // ... not another
		{"^3$", float64(3), true}, // a number is matched as JSON
		{true, true, true},
		{true, false, false},
		{float64(2), float64(2), true},
		{[]any{"^a$"}, []any{"b", "a"}, true}, // an array holds a match for each element
		{[]any{"^a$", "^c$"}, []any{"b", "a"}, false},
		{[]any{"^a$"}, "a", false},
		{"(", "x", false}, // a pattern that does not compile matches nothing
	}
	for _, tc := range tests {
		if got := argMatches(tc.want, tc.got, "k"); got != tc.ok {
			t.Errorf("argMatches(%v, %v) = %v", tc.want, tc.got, got)
		}
	}
}

func TestDidClipsALongList(t *testing.T) {
	var calls []core.Block
	for i := 0; i < 6; i++ {
		calls = append(calls, call("wait", `{}`))
	}
	if got := did(calls); !strings.Contains(got, "and 2 more") {
		t.Fatalf("did: %s", got)
	}
	long := call("spawn", `{"task":"`+strings.Repeat("x", 300)+`"}`)
	if got := shown(long); len(got) > 200 || !strings.HasSuffix(got, "…") {
		t.Fatalf("shown: %s", got)
	}
}
