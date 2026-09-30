package agent_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/agent"
	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/kv"
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

// compactHooks is a fakeHooks that also listens to automatic compactions.
type compactHooks struct {
	fakeHooks
	mu2   sync.Mutex
	calls []string
	panic bool // BeforeCompact panics: the compaction must still happen
}

func (c *compactHooks) BeforeCompact(_ context.Context, id, role, reason string) {
	c.mu2.Lock()
	c.calls = append(c.calls, "before:"+id+":"+role+":"+reason)
	c.mu2.Unlock()
	if c.panic {
		panic("a hook blew up")
	}
}

func (c *compactHooks) AfterCompact(_ context.Context, id, role, reason string) {
	c.mu2.Lock()
	c.calls = append(c.calls, "after:"+id+":"+role+":"+reason)
	c.mu2.Unlock()
}

func (c *compactHooks) seen() []string {
	c.mu2.Lock()
	defer c.mu2.Unlock()
	return append([]string(nil), c.calls...)
}

// An automatic compaction is announced to the hooks before it is applied and again
// once it has been, in that order, with the agent's id and role; the compaction is
// applied whatever a hook does, panics included (a hook cannot veto it: a refused
// compaction is attempted again while the prompt keeps growing).
func TestAutomaticCompactionRunsTheCompactionHooks(t *testing.T) {
	for _, panicking := range []bool{false, true} {
		t.Run(map[bool]string{false: "hooks", true: "a panicking hook"}[panicking], func(t *testing.T) {
			pl := kv.DefaultPlanner()
			pl.SoftThreadTokens = 6500
			pl.MinThreadTokens = 2000
			hk := &compactHooks{panic: panicking}
			r := newRig(t, rigOpts{planner: pl, hooks: hk, mock: mock.Config{Engine: mock.EngineConfig{BlockTokens: 16, MinCacheTokens: 64}}},
				scriptedWork(22, func(c *mock.Call) string {
					n := 0
					for _, m := range c.Messages {
						if m.Role == "assistant" {
							n++
						}
					}
					return compactorPatch(max(2*n-6, 3))(c)
				}))
			if _, err := r.agent.Run(context.Background(), "please build the whole thing"); err != nil {
				t.Fatal(err)
			}
			time.Sleep(50 * time.Millisecond)
			if len(r.log.OfType(events.TypeCompactCommit)) == 0 {
				if _, err := r.agent.Run(context.Background(), "and once more"); err != nil {
					t.Fatal(err)
				}
			}
			commits := len(r.log.OfType(events.TypeCompactCommit))
			if commits == 0 {
				t.Fatal("no compaction was committed: the hooks were vetoed or never reached")
			}
			calls := hk.seen()
			var befores, afters int
			for i, c := range calls {
				switch {
				case strings.HasPrefix(c, "before:be-1:backend:"):
					befores++
				case strings.HasPrefix(c, "after:be-1:backend:"):
					afters++
					if i == 0 || !strings.HasPrefix(calls[i-1], "before:") {
						t.Errorf("AfterCompact without its BeforeCompact just before it: %v", calls)
					}
				default:
					t.Errorf("unexpected call %q: the rig's agent is be-1, a backend", c)
				}
			}
			if befores < commits || afters != befores {
				t.Errorf("%d commits, %d BeforeCompact, %d AfterCompact: %v", commits, befores, afters, calls)
			}
			if panicking && len(r.log.OfType("agent.panic")) == 0 {
				t.Error("a panicking hook must be recorded, not swallowed")
			}
		})
	}
}

// A Hooks that does not implement CompactionHooks changes nothing.
func TestCompactionWithoutCompactionHooksIsUnchanged(t *testing.T) {
	pl := kv.DefaultPlanner()
	pl.SoftThreadTokens = 6500
	pl.MinThreadTokens = 2000
	r := newRig(t, rigOpts{planner: pl, hooks: &fakeHooks{}, mock: mock.Config{Engine: mock.EngineConfig{BlockTokens: 16, MinCacheTokens: 64}}},
		scriptedWork(22, func(c *mock.Call) string {
			n := 0
			for _, m := range c.Messages {
				if m.Role == "assistant" {
					n++
				}
			}
			return compactorPatch(max(2*n-6, 3))(c)
		}))
	if _, err := r.agent.Run(context.Background(), "please build the whole thing"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if len(r.log.OfType(events.TypeCompactCommit)) == 0 {
		if _, err := r.agent.Run(context.Background(), "and once more"); err != nil {
			t.Fatal(err)
		}
	}
	if len(r.log.OfType(events.TypeCompactCommit)) == 0 {
		t.Fatal("no compaction was committed")
	}
}
