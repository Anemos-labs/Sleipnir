package openairesp_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/provider/openairesp"
)

func schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`)
}

func basePrompt() *core.Prompt {
	return &core.Prompt{
		Model:    "gpt-test",
		CacheKey: "sess-1",
		Params:   core.Params{MaxTokens: 512, Effort: "high"},
		System:   []core.Block{core.Text("You are a coding agent."), core.Text("Be brief.")},
		Tools:    []core.ToolSpec{{Name: "read", Description: "read a file", InputSchema: schema()}},
		Messages: []core.Message{{Role: core.RoleUser, Blocks: []core.Block{core.Text("look at main.go")}}},
	}
}

func body(t *testing.T, p *core.Prompt, o openairesp.Options, warm bool) map[string]any {
	t.Helper()
	b, err := openairesp.Build(p, o, warm)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	return m
}

func TestBuildIsStatelessAndStreamsAndAsksForEncryptedReasoning(t *testing.T) {
	m := body(t, basePrompt(), openairesp.Options{}, false)
	if m["store"] != false || m["stream"] != true {
		t.Errorf("store %v, stream %v: the harness keeps the conversation, and the stream is the only form read", m["store"], m["stream"])
	}
	if inc, _ := m["include"].([]any); len(inc) != 1 || inc[0] != "reasoning.encrypted_content" {
		t.Errorf("include = %v: without it a reasoning item cannot be sent back", m["include"])
	}
	if m["instructions"] != "You are a coding agent.\n\nBe brief." {
		t.Errorf("instructions = %q", m["instructions"])
	}
	if m["prompt_cache_key"] != "sess-1" || m["max_output_tokens"] != float64(512) {
		t.Errorf("cache key %v, max_output_tokens %v", m["prompt_cache_key"], m["max_output_tokens"])
	}
	if r, _ := m["reasoning"].(map[string]any); r["effort"] != "high" || r["summary"] != nil {
		t.Errorf("reasoning = %v: the effort, and no summary unless asked for", m["reasoning"])
	}
	tools, _ := m["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["type"] != "function" || tools[0].(map[string]any)["strict"] != false {
		t.Errorf("tools = %v: plain function tools, strict spelled out (the API's default for them is strict)", m["tools"])
	}
	input, _ := m["input"].([]any)
	if len(input) != 1 {
		t.Fatalf("input = %v", m["input"])
	}
	first := input[0].(map[string]any)
	if first["role"] != "user" || first["content"].([]any)[0].(map[string]any)["type"] != "input_text" {
		t.Errorf("the user turn = %v", first)
	}
}

// The plan's preview refuses a limit on the output and the sampling parameters, takes function tools only inside a namespace, and every
// call the harness puts back is in that namespace too.
func TestBuildForAPlanTokenFollowsThePreviewRules(t *testing.T) {
	p := basePrompt()
	temp := 0.2
	p.Params.Temperature = &temp
	p.Messages = append(p.Messages,
		core.Message{Role: core.RoleAssistant, Blocks: []core.Block{core.ToolUse("call_1", "read", json.RawMessage(`{"path":"main.go"}`))}},
		core.Message{Role: core.RoleUser, Blocks: []core.Block{core.ToolResult("call_1", false, core.Text("package main"))}})
	m := body(t, p, openairesp.Options{Plan: true}, false)
	for _, k := range []string{"max_output_tokens", "temperature"} {
		if _, ok := m[k]; ok {
			t.Errorf("%s is sent on a plan token, and the preview refuses it", k)
		}
	}
	tools, _ := m["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %v", m["tools"])
	}
	ns := tools[0].(map[string]any)
	if ns["type"] != "namespace" || ns["name"] != openairesp.PlanNamespace || len(ns["tools"].([]any)) != 1 {
		t.Errorf("the tools are one namespace: %v", ns)
	}
	var call map[string]any
	for _, it := range m["input"].([]any) {
		if it.(map[string]any)["type"] == "function_call" {
			call = it.(map[string]any)
		}
	}
	if call == nil || call["namespace"] != openairesp.PlanNamespace || call["call_id"] != "call_1" || call["arguments"] != `{"path":"main.go"}` {
		t.Errorf("a call the harness replays names its namespace: %v", call)
	}
	// and a key's request carries none of it
	if _, ok := body(t, p, openairesp.Options{}, false)["input"].([]any)[1].(map[string]any)["namespace"]; ok {
		t.Error("a call in a request with an API key names no namespace")
	}
}

func TestBuildWarmUpAsksForAsLittleAsTheAPITakes(t *testing.T) {
	if m := body(t, basePrompt(), openairesp.Options{}, true); m["max_output_tokens"] != float64(16) {
		t.Errorf("a warm-up asks for %v tokens", m["max_output_tokens"])
	}
}

func TestEffortIsMappedAndUnknownOnesAreLeftToTheModel(t *testing.T) {
	for in, want := range map[string]any{"none": "none", "low": "low", "xhigh": "xhigh", "max": "max", "": nil, "ultra": nil} {
		p := basePrompt()
		p.Params.Effort = in
		r, _ := body(t, p, openairesp.Options{}, false)["reasoning"].(map[string]any)
		if got := r["effort"]; got != want {
			t.Errorf("effort %q went out as %v, want %v", in, got, want)
		}
	}
	p := basePrompt()
	p.Params.Effort = ""
	if r, _ := body(t, p, openairesp.Options{ReasoningSummary: "auto"}, false)["reasoning"].(map[string]any); r["summary"] != "auto" {
		t.Errorf("a summary asked for is sent: %v", r)
	}
}

// A reasoning item goes back verbatim, and so do the items it was produced with (ids and all): an endpoint that does not store the
// conversation checks that they belong together. A turn without it is rebuilt without ids, so nothing is left pointing at an item that
// is not there.
func TestReplayKeepsReasoningWithItsItemsAndDropsIdsWithoutIt(t *testing.T) {
	reasoning := json.RawMessage(`{"type":"reasoning","id":"rs_1","encrypted_content":"abc","summary":[]}`)
	msg := json.RawMessage(`{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"checking"}]}`)
	call := json.RawMessage(`{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read","arguments":"{\"path\":\"a.go\"}"}`)
	wire := func(b core.Block, w json.RawMessage) core.Block {
		b.Wire, b.WireFormat = w, openairesp.Dialect
		return b
	}
	turn := func(withReasoning bool) core.Message {
		var blocks []core.Block
		if withReasoning {
			blocks = append(blocks, wire(core.Block{Kind: core.BlockThinking}, reasoning))
		}
		blocks = append(blocks, wire(core.Text("checking"), msg), wire(core.ToolUse("call_1", "read", json.RawMessage(`{"path":"a.go"}`)), call))
		return core.Message{Role: core.RoleAssistant, Blocks: blocks}
	}
	items := func(withReasoning bool) string {
		p := basePrompt()
		p.Messages = append(p.Messages, turn(withReasoning))
		b, _ := json.Marshal(body(t, p, openairesp.Options{}, false)["input"])
		return string(b)
	}
	with := items(true)
	for _, want := range []string{`"id":"rs_1"`, `"encrypted_content":"abc"`, `"id":"msg_1"`, `"id":"fc_1"`} {
		if !strings.Contains(with, want) {
			t.Errorf("with its reasoning the turn goes back as it came; %s is missing:\n%s", want, with)
		}
	}
	without := items(false)
	for _, no := range []string{`"id":"msg_1"`, `"id":"fc_1"`, "rs_1"} {
		if strings.Contains(without, no) {
			t.Errorf("with no reasoning in the turn an id would point at nothing (%s):\n%s", no, without)
		}
	}
	if !strings.Contains(without, `"call_id":"call_1"`) || !strings.Contains(without, `"output_text"`) {
		t.Errorf("the turn is rebuilt from what the harness holds:\n%s", without)
	}
}

func TestToolResultsComeFirstAndErrorsSaySo(t *testing.T) {
	p := basePrompt()
	p.Messages = append(p.Messages,
		core.Message{Role: core.RoleAssistant, Blocks: []core.Block{core.ToolUse("c1", "read", json.RawMessage(`{}`))}},
		core.Message{Role: core.RoleUser, Blocks: []core.Block{core.Text("and then?"), core.ToolResult("c1", true, core.Text("no such file"))}},
		core.Message{Role: core.RoleSystem, Blocks: []core.Block{core.Text("note from the harness")}})
	var types, roles []string
	for _, it := range body(t, p, openairesp.Options{}, false)["input"].([]any) {
		m := it.(map[string]any)
		types = append(types, m["type"].(string))
		if r, ok := m["role"].(string); ok {
			roles = append(roles, r)
		}
		if m["type"] == "function_call_output" && m["output"] != "Error: no such file" {
			t.Errorf("an error result = %v", m["output"])
		}
	}
	if got := strings.Join(types, " "); got != "message function_call function_call_output message message" {
		t.Errorf("order of the items: %s", got)
	}
	if got := strings.Join(roles, " "); got != "user user developer" {
		t.Errorf("roles %s: a system message in the input goes as a developer one, the endpoint takes instructions and developer messages", got)
	}
}

func TestMalformedArgumentsAreNotReplayedAsInvalidJSON(t *testing.T) {
	p := basePrompt()
	bad := core.ToolUse("c1", "read", json.RawMessage(`"{\"path\":"`))
	bad.Invalid = "arguments are not valid JSON"
	p.Messages = append(p.Messages, core.Message{Role: core.RoleAssistant, Blocks: []core.Block{bad}})
	b, _ := json.Marshal(body(t, p, openairesp.Options{}, false)["input"])
	if !strings.Contains(string(b), `"arguments":"{}"`) {
		t.Errorf("an endpoint that checks the history refuses a call whose arguments are not JSON: %s", b)
	}
}
