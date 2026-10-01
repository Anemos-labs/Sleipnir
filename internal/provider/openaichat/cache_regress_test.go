package openaichat

// Regression tests for the prompt-cache economics review (R17). With
// CacheControlParts (the documented way to reach Anthropic models through a
// chat-completions gateway) every marker the planner places must reach the wire.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/kv"
)

func cxRenderPrompt(t *testing.T, lastIsToolResult bool, maxBP int) *core.Prompt {
	t.Helper()
	est := core.NewBytesEstimator().WithRatio(4)
	tools, err := kv.SortTools([]core.ToolSpec{{Name: "bash", Description: "run", InputSchema: json.RawMessage(`{"type":"object"}`)}})
	if err != nil {
		t.Fatal(err)
	}
	s := &kv.Stack{Agent: "a", Role: "r", Model: "anthropic/claude-opus-5-5", Tools: tools,
		Const:  kv.NewLayer("const", kv.KindConst, 1, []kv.Segment{{Text: strings.Repeat("abcd", 3000)}}),
		Shared: kv.NewLayer("shared", kv.KindShared, 1, []kv.Segment{{Key: "p", Text: strings.Repeat("abcd", 2000), Vol: kv.VolEpoch}}),
		RoleL:  kv.NewLayer("role", kv.KindRole, 1, []kv.Segment{{Key: "r", Text: strings.Repeat("abcd", 1800), Vol: kv.VolEpoch}}),
		Notes:  kv.NewLayer("notes", kv.KindNotes, 1, []kv.Segment{{Key: "facts", Text: strings.Repeat("abcd", 1700), Vol: kv.VolSlow}}),
	}
	th := kv.NewThread()
	th.Append(core.Turn{Role: core.RoleUser, Blocks: []core.Block{core.Text("build it")}})
	th.Append(core.Turn{Role: core.RoleAssistant, Blocks: []core.Block{core.Text("ok"), core.ToolUse("c1", "bash", json.RawMessage(`{}`))}})
	if lastIsToolResult {
		th.Append(core.Turn{Role: core.RoleUser, Blocks: []core.Block{core.ToolResult("c1", false, core.Text("done"))}})
	} else {
		th.Append(core.Turn{Role: core.RoleUser, Blocks: []core.Block{core.ToolResult("c1", false, core.Text("done")), core.Text("[mail] fyi")}})
	}
	s.Thread = th.Snapshot()
	caps := kv.Caps{Dialect: Dialect, MaxBreakpoints: maxBP, LookbackBlocks: 20, MinPrefixTokens: 512}
	return kv.Render(s, kv.RenderOpts{Caps: caps, Policy: kv.DefaultPolicy(), Est: est}).Prompt
}

func cxMessages(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	var req struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatal(err)
	}
	return req.Messages
}

func cxMarked(content any) (parts int, marked int) {
	arr, ok := content.([]any)
	if !ok {
		return 0, 0
	}
	for _, p := range arr {
		parts++
		if m, ok := p.(map[string]any); ok && m["cache_control"] != nil {
			marked++
		}
	}
	return
}

// The rolling marker lands on the last persistent block, and in every tool loop
// that block is a tool_result, which chat renders as a role=tool message. It is
// sent as a one-part array carrying the marker; every planned marker reaches the
// request.
func TestCacheEcon_RollingBreakpointOnToolResultIsRendered(t *testing.T) {
	for _, lastIsToolResult := range []bool{true, false} {
		p := cxRenderPrompt(t, lastIsToolResult, 4)
		body, err := Build(p, Options{CacheControlParts: true}, false)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := strings.Count(string(body), `"cache_control"`), len(p.Breakpoints); got != want || want != 4 {
			t.Fatalf("lastIsToolResult=%v: planner asked for %d markers, the request carries %d", lastIsToolResult, want, got)
		}
		if !lastIsToolResult {
			continue
		}
		var tool map[string]any
		for _, m := range cxMessages(t, body) {
			if m["role"] == "tool" {
				tool = m
			}
		}
		parts, marked := cxMarked(tool["content"])
		if tool == nil || parts != 1 || marked != 1 {
			t.Fatalf("the tool message must carry the rolling marker on a one-part content array: %v", tool)
		}
		if txt := tool["content"].([]any)[0].(map[string]any)["text"]; txt != "done" {
			t.Fatalf("the marker must not change the text: %v", txt)
		}
	}
}

// An unmarked tool result stays the plain string it always was, so requests
// without markers are byte-identical to what they were before.
func TestCacheEcon_UnmarkedToolResultsStayPlainStrings(t *testing.T) {
	p := cxRenderPrompt(t, true, 4)
	for _, opts := range []Options{{}, {CacheControlParts: true}} {
		body, err := Build(p, opts, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range cxMessages(t, body) {
			if m["role"] != "tool" {
				continue
			}
			_, isString := m["content"].(string)
			if opts.CacheControlParts {
				if isString {
					t.Fatal("the marked (last) tool result should be a part array")
				}
			} else if !isString {
				t.Fatalf("without CacheControlParts the tool content must stay a string: %v", m["content"])
			}
		}
	}
	// Two results in one message: only the last carries the marker.
	p2 := *p
	p2.Messages = append([]core.Message(nil), p.Messages...)
	last := &p2.Messages[len(p2.Messages)-1]
	last.Blocks = []core.Block{core.ToolResult("c0", false, core.Text("first")), core.ToolResult("c1", false, core.Text("done"))}
	p2.Breakpoints = []core.Breakpoint{{After: core.BlockRef{Msg: len(p2.Messages) - 1, Blk: 1}, Label: "thread"}}
	body, err := Build(&p2, Options{CacheControlParts: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	var tools []map[string]any
	for _, m := range cxMessages(t, body) {
		if m["role"] == "tool" {
			tools = append(tools, m)
		}
	}
	if len(tools) != 2 {
		t.Fatalf("tool messages: %d", len(tools))
	}
	if _, isString := tools[0]["content"].(string); !isString {
		t.Fatalf("the first result carries no marker and stays a string: %v", tools[0]["content"])
	}
	if _, marked := cxMarked(tools[1]["content"]); marked != 1 {
		t.Fatalf("the second result carries the marker: %v", tools[1]["content"])
	}
}

// The constitution's marker (the system block) becomes a part with the marker;
// the text the model sees is the same.
func TestCacheEcon_SystemBlockMarkerIsRendered(t *testing.T) {
	p := cxRenderPrompt(t, true, 5) // a fifth slot goes to the constitution
	var sysMark bool
	for _, b := range p.Breakpoints {
		if b.After.Sys && b.Label == "const" {
			sysMark = true
		}
	}
	if !sysMark {
		t.Fatalf("setup: expected a marker on the system block, got %+v", p.Breakpoints)
	}
	plain, _ := Build(p, Options{}, false)
	marked, err := Build(p, Options{CacheControlParts: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	pm, mm := cxMessages(t, plain), cxMessages(t, marked)
	if pm[0]["role"] != "system" || mm[0]["role"] != "system" {
		t.Fatal("system message first")
	}
	txt, isString := pm[0]["content"].(string)
	if !isString {
		t.Fatal("the unmarked system message stays a string")
	}
	parts, n := cxMarked(mm[0]["content"])
	if parts != 1 || n != 1 {
		t.Fatalf("marked system message: %v", mm[0]["content"])
	}
	if got := mm[0]["content"].([]any)[0].(map[string]any)["text"]; got != txt {
		t.Fatal("the marker must not change the system text")
	}
	if strings.Count(string(marked), `"cache_control"`) != len(p.Breakpoints) {
		t.Fatalf("every marker reaches the wire")
	}
}

// A marker on assistant text (a thread that ends with an assistant turn) needs
// the parts form too.
func TestCacheEcon_AssistantTextMarkerIsRendered(t *testing.T) {
	p := cxRenderPrompt(t, true, 4)
	p2 := *p
	p2.Messages = append([]core.Message(nil), p.Messages...)
	ai := len(p2.Messages) - 2
	p2.Breakpoints = []core.Breakpoint{{After: core.BlockRef{Msg: ai, Blk: 0}, Label: "thread"}}
	body, err := Build(&p2, Options{CacheControlParts: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range cxMessages(t, body) {
		if m["role"] == "assistant" {
			if _, marked := cxMarked(m["content"]); marked == 1 {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("assistant text marker missing: %s", body)
	}
}
