package agent_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/reee344/sleipnir/internal/agent"
	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/provider/mock"
	"github.com/reee344/sleipnir/internal/tools"
)

// fakeHooks is a scripted agent.Hooks.
type fakeHooks struct {
	mu     sync.Mutex
	before func(agent.ToolHookCall) agent.ToolHookOutcome
	after  func(agent.ToolHookCall, *tools.Result) agent.ToolHookOutcome
	stop   func(agentID string, continuing bool) agent.StopOutcome

	stops       []bool
	afterInputs []string
}

func (f *fakeHooks) BeforeTool(_ context.Context, c agent.ToolHookCall) agent.ToolHookOutcome {
	if f.before == nil {
		return agent.ToolHookOutcome{}
	}
	return f.before(c)
}

func (f *fakeHooks) AfterTool(_ context.Context, c agent.ToolHookCall, r *tools.Result) agent.ToolHookOutcome {
	f.mu.Lock()
	f.afterInputs = append(f.afterInputs, c.Tool+":"+string(c.Input))
	f.mu.Unlock()
	if f.after == nil {
		return agent.ToolHookOutcome{}
	}
	return f.after(c, r)
}

func (f *fakeHooks) BeforeStop(_ context.Context, id, _, _ string, continuing bool) agent.StopOutcome {
	f.mu.Lock()
	f.stops = append(f.stops, continuing)
	f.mu.Unlock()
	if f.stop == nil {
		return agent.StopOutcome{}
	}
	return f.stop(id, continuing)
}

// toolResults returns the tool-result texts the model saw in its latest request.
func toolResults(c *mock.Call) []string {
	var out []string
	for _, m := range c.Messages {
		if m.Role == "tool" {
			out = append(out, m.Content)
		}
	}
	return out
}

func TestToolHooksCanVetoRewriteAndAnnotate(t *testing.T) {
	ranBig := false
	tls := []fakeTool{
		{name: "echo", readOnly: true, run: func(in json.RawMessage) *tools.Result { return &tools.Result{Text: "echo:" + string(in)} }},
		{name: "big", run: func(json.RawMessage) *tools.Result { ranBig = true; return &tools.Result{Text: "built"} }},
	}
	hk := &fakeHooks{
		before: func(c agent.ToolHookCall) agent.ToolHookOutcome {
			switch c.Tool {
			case "big":
				return agent.ToolHookOutcome{Veto: true, Reason: "no builds today"}
			case "echo":
				return agent.ToolHookOutcome{UpdatedInput: json.RawMessage(`{"rewritten":true}`), Context: "checked by policy"}
			}
			return agent.ToolHookOutcome{}
		},
		after: func(c agent.ToolHookCall, r *tools.Result) agent.ToolHookOutcome {
			if c.Tool == "echo" {
				return agent.ToolHookOutcome{Reason: "lint: fine"}
			}
			return agent.ToolHookOutcome{}
		},
	}
	var seen []string
	r := newRig(t, rigOpts{noCompact: true, tools: tls, hooks: hk}, func(c *mock.Call) mock.Reply {
		if rs := toolResults(c); len(rs) > 0 {
			seen = rs
			return mock.Reply{Text: "done"}
		}
		return mock.Reply{Text: "go", ToolCalls: []mock.ToolCall{
			{ID: "c1", Name: "big", Args: `{"x":1}`},
			{ID: "c2", Name: "echo", Args: `{"q":"original"}`},
		}}
	})
	if _, err := r.agent.Run(context.Background(), "do it"); err != nil {
		t.Fatal(err)
	}
	if ranBig {
		t.Error("a vetoed tool call must not run")
	}
	all := strings.Join(seen, "\n")
	for _, want := range []string{"A hook blocked this call: no builds today", `echo:{"rewritten":true}`, "[hook] checked by policy", "[hook] lint: fine"} {
		if !strings.Contains(all, want) {
			t.Errorf("the model did not see %q in:\n%s", want, all)
		}
	}
	if strings.Contains(all, "original") {
		t.Error("the tool ran with the input the hook replaced")
	}
	// The vetoed call is a protocol event of its own kind.
	var kinds []string
	for _, e := range r.log.OfType(events.TypeToolResult) {
		var m struct {
			Meta map[string]any `json:"meta"`
		}
		_ = json.Unmarshal(e.Data, &m)
		if k, _ := m.Meta["error_kind"].(string); k != "" {
			kinds = append(kinds, k)
		}
	}
	if len(kinds) != 1 || kinds[0] != agent.ErrKindHook {
		t.Errorf("error kinds = %v, want [hook]", kinds)
	}
}

func TestStopHookSendsTheAgentBackBoundedTimes(t *testing.T) {
	hk := &fakeHooks{stop: func(string, bool) agent.StopOutcome {
		return agent.StopOutcome{Veto: true, Reason: "run the tests first"}
	}}
	var requests int
	var lastUser string
	r := newRig(t, rigOpts{noCompact: true, hooks: hk}, func(c *mock.Call) mock.Reply {
		requests++
		lastUser = c.LastUser()
		return mock.Reply{Text: "I am done"}
	})
	res, err := r.agent.Run(context.Background(), "finish up")
	if err != nil {
		t.Fatal(err)
	}
	// One answer, then three vetoes, each of which sends the agent back for another: a hook that
	// always objects must not keep the agent running forever.
	if requests != 4 || res.Text != "I am done" {
		t.Fatalf("%d requests, result %q", requests, res.Text)
	}
	if !strings.Contains(lastUser, "[stop hook] run the tests first") {
		t.Errorf("the agent was not told why it must keep going: %q", lastUser)
	}
	if got := hk.stops; len(got) != 3 || got[0] || !got[1] || !got[2] {
		t.Errorf("continuing flags = %v, want [false true true]", got)
	}
}

func TestToolErrorKindsAreRecorded(t *testing.T) {
	tls := []fakeTool{
		{name: "stale", run: func(json.RawMessage) *tools.Result {
			return tools.Errorf("a.go changed since you last read it (modified by be-2); read it again before editing")
		}},
		{name: "lease", run: func(json.RawMessage) *tools.Result {
			return tools.Errorf("b.go is being edited by be-2 (last write 3s ago). Work on something else")
		}},
		{name: "scope", run: func(json.RawMessage) *tools.Result {
			return tools.Errorf("c.go is outside the scope of your task T1 (scope: api/**)")
		}},
		{name: "denied", run: func(json.RawMessage) *tools.Result {
			return tools.Errorf("permission denied (bash: rm -rf x): approval required: high risk")
		}},
		{name: "plain", run: func(json.RawMessage) *tools.Result { return tools.Errorf("exit code 1") }},
	}
	r := newRig(t, rigOpts{noCompact: true, tools: tls}, func(c *mock.Call) mock.Reply {
		if len(toolResults(c)) > 0 {
			return mock.Reply{Text: "done"}
		}
		return mock.Reply{Text: "go", ToolCalls: []mock.ToolCall{
			{ID: "1", Name: "stale", Args: `{}`}, {ID: "2", Name: "lease", Args: `{}`}, {ID: "3", Name: "scope", Args: `{}`},
			{ID: "4", Name: "denied", Args: `{}`}, {ID: "5", Name: "plain", Args: `{}`},
			{ID: "6", Name: "nosuchtool", Args: `{}`}, {ID: "7", Name: "plain", Args: `{"cut": `},
		}}
	})
	if _, err := r.agent.Run(context.Background(), "provoke errors"); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, e := range r.log.OfType(events.TypeToolResult) {
		var m struct {
			ID   string         `json:"id"`
			Meta map[string]any `json:"meta"`
		}
		_ = json.Unmarshal(e.Data, &m)
		k, _ := m.Meta["error_kind"].(string)
		got[m.ID] = k
	}
	want := map[string]string{
		"1": agent.ErrKindStale, "2": agent.ErrKindLease, "3": agent.ErrKindScope, "4": agent.ErrKindPermission,
		"5": "", "6": agent.ErrKindUnknownTool, "7": agent.ErrKindInvalidInput,
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("call %s: error_kind = %q, want %q", id, got[id], w)
		}
	}
}
