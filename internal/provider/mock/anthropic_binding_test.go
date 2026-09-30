package mock

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

// convo drives an agent-like tool loop against the mock, keeping the message
// array the way a harness would.
type convo struct {
	t       *testing.T
	ts      *httptest.Server
	model   string
	fields  obj
	msgs    []obj
	headers []string
	round   int
}

func newConvo(t *testing.T, cfg AnthropicConfig, model string, headers ...string) (*convo, *AnthropicServer) {
	t.Helper()
	srv, ts := newA(t, cfg, func(c *Call) Reply {
		return Reply{Reasoning: fmt.Sprintf("reasoning for turn %d", len(c.Messages)), ToolCalls: []ToolCall{{ID: fmt.Sprintf("toolu_%d", len(c.Messages)), Name: "bash", Args: `{"command":"ls"}`}}}
	})
	tools := []obj{{"name": "bash", "description": "run", "input_schema": obj{"type": "object"}}}
	return &convo{t: t, ts: ts, model: model, headers: headers, fields: obj{
		"model": model, "tools": tools, "system": []obj{text("You are an agent.")}, "thinking": obj{"type": "adaptive"},
	}, msgs: []obj{userMsg(text("start"))}}, srv
}

func (c *convo) body(extra obj) string {
	f := obj{}
	for k, v := range c.fields {
		f[k] = v
	}
	for k, v := range extra {
		f[k] = v
	}
	return req(f, c.msgs...)
}

// step sends the current messages and, on success, appends the assistant turn
// and a tool_result (plus a turn-scoped hot message when hot is set).
func (c *convo) step(hot string) reply {
	c.t.Helper()
	r := post(c.t, c.ts, c.body(nil), c.headers...)
	if r.status != 200 {
		return r
	}
	c.round++
	m := r.json(c.t)
	content := m["content"].([]any)
	c.msgs = append(c.msgs, obj{"role": "assistant", "content": content})
	for _, b := range content {
		bm := b.(map[string]any)
		if bm["type"] == "tool_use" {
			c.msgs = append(c.msgs, userMsg(obj{"type": "tool_result", "tool_use_id": bm["id"], "content": fmt.Sprint("output ", c.round)}))
			if hot != "" {
				c.msgs = append(c.msgs, obj{"role": "system", "clear_at": "next_user_message", "content": []obj{text(fmt.Sprint(hot, " ", c.round))}})
			}
		}
	}
	return r
}

const bindingErr = "messages.1.content.0: Invalid `signature` in `thinking` block. The block is bound to a different conversation. Remove the block, or set `thinking.block_binding.prefix_mismatch_behavior` to \"drop_block\"."

var enforce = AnthropicConfig{EnforceThinkingBinding: true}

func TestBindingAppendOnlyLoopPasses(t *testing.T) {
	c, srv := newConvo(t, enforce, "claude-opus-5-5", "Anthropic-Beta", betaClearAt)
	for i := 0; i < 8; i++ {
		if r := c.step("board"); r.status != 200 {
			t.Fatalf("round %d: %d %s", i+1, r.status, r.body)
		}
	}
	if st := srv.AnthropicStats(); len(st) != 8 {
		t.Fatalf("stats %d", len(st))
	}
}

func TestBindingRewritingHistoryIsA400(t *testing.T) {
	t.Run("the last user message is rewritten every round (inline hot tail)", func(t *testing.T) {
		c, _ := newConvo(t, enforce, "claude-opus-5-5")
		// Round 1 carries the hot block inline in the last user message ...
		c.msgs = []obj{userMsg(text("start"), text("board: round 1"))}
		if r := c.step(""); r.status != 200 {
			t.Fatalf("round 1: %d %s", r.status, r.body)
		}
		// ... and round 2 rebuilds it: the old copy is gone, the new one is appended.
		c.msgs[0] = userMsg(text("start"))
		c.msgs[len(c.msgs)-1]["content"] = append(c.msgs[len(c.msgs)-1]["content"].([]obj), text("board: round 2"))
		r := c.step("")
		if r.status != 400 || r.errType(t) != "invalid_request_error" {
			t.Fatalf("round 2 must be rejected: %d %s", r.status, r.body)
		}
		want := bindingErr + " That setting requires the `thinking-binding-controls-2026-08-01` value in the `anthropic-beta` header."
		if r.errMsg(t) != want {
			t.Fatalf("message:\n got %q\nwant %q", r.errMsg(t), want)
		}
	})
	t.Run("with the beta header the message stops after the drop_block hint", func(t *testing.T) {
		c, _ := newConvo(t, enforce, "claude-opus-5-5", "Anthropic-Beta", betaBinding)
		c.step("")
		c.msgs[0] = userMsg(text("start, edited"))
		r := c.step("")
		if r.status != 400 || r.errMsg(t) != bindingErr {
			t.Fatalf("%d %q", r.status, r.errMsg(t))
		}
	})
	t.Run("editing an earlier tool_result", func(t *testing.T) {
		c, _ := newConvo(t, enforce, "claude-opus-5-5")
		c.step("")
		c.step("")
		c.msgs[2]["content"].([]obj)[0]["content"] = "rewritten output"
		if r := c.step(""); r.status != 400 || !strings.Contains(r.errMsg(t), "bound to a different conversation") {
			t.Fatalf("%d %s", r.status, r.body)
		}
	})
	t.Run("editing the system prompt or a tool", func(t *testing.T) {
		for name, mut := range map[string]obj{
			"system": {"system": []obj{text("You are a different agent.")}},
			"tools":  {"tools": []obj{{"name": "bash", "description": "run commands", "input_schema": obj{"type": "object"}}}},
		} {
			c, _ := newConvo(t, enforce, "claude-opus-5-5")
			c.step("")
			c.fields = mergeObj(c.fields, mut)
			if r := c.step(""); r.status != 400 || !strings.Contains(r.errMsg(t), "bound to a different conversation") {
				t.Errorf("%s: %d %s", name, r.status, r.body)
			}
		}
	})
	t.Run("deleting an earlier cleared turn-scoped message", func(t *testing.T) {
		c, _ := newConvo(t, enforce, "claude-opus-5-5", "Anthropic-Beta", betaClearAt)
		c.step("board")
		c.step("board")
		// msgs: user, asst, result, sys(1), asst, result, sys(2)
		c.msgs = append(c.msgs[:3], c.msgs[4:]...)
		if r := c.step("board"); r.status != 400 {
			t.Fatalf("a cleared message still counts as history: %d %s", r.status, r.body)
		}
	})
}

func mergeObj(a, b obj) obj {
	out := obj{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func TestBindingThingsThatAreNotEdits(t *testing.T) {
	c, _ := newConvo(t, enforce, "claude-opus-5-5")
	if r := c.step(""); r.status != 200 {
		t.Fatal(r.body)
	}
	steps := []struct {
		name string
		mut  func()
	}{
		{"cache_control markers move", func() {
			c.msgs[0]["content"] = []obj{withCC(text("start"), "1h")}
		}},
		{"max_tokens and effort change", func() {
			c.fields["max_tokens"] = 999
			c.fields["output_config"] = obj{"effort": "low"}
		}},
		{"tool_choice changes", func() { c.fields["tool_choice"] = obj{"type": "none"} }},
		{"metadata changes", func() { c.fields["metadata"] = obj{"user_id": "someone"} }},
		{"string content instead of a text block", func() { c.msgs[0]["content"] = "start" }},
		{"the tools are reordered", func() {
			c.fields["tools"] = []obj{{"name": "bash", "description": "run", "input_schema": obj{"type": "object"}}}
		}},
	}
	for _, s := range steps {
		s.mut()
		if r := c.step(""); r.status != 200 {
			t.Fatalf("%s must not invalidate thinking: %d %s", s.name, r.status, r.body)
		}
	}
}

func TestBindingDropBlockMode(t *testing.T) {
	drop := obj{"thinking": obj{"type": "adaptive", "block_binding": obj{"prefix_mismatch_behavior": "drop_block"}}}
	c, srv := newConvo(t, enforce, "claude-opus-5-5", "Anthropic-Beta", betaBinding)
	c.step("")
	c.step("")
	c.msgs[0] = userMsg(text("start, edited")) // both thinking blocks are now stale
	c.fields = mergeObj(c.fields, drop)
	r := c.step("")
	if r.status != 200 {
		t.Fatalf("drop_block must degrade instead of failing: %d %s", r.status, r.body)
	}
	xs := r.json(t)["input_transformations"].([]any)
	if len(xs) != 2 {
		t.Fatalf("both stale blocks are reported: %v", xs)
	}
	first := xs[0].(map[string]any)
	if first["type"] != "thinking_dropped" || first["reason"] != "prefix_binding_mismatch" || first["path"] != "messages.1.content.0" {
		t.Errorf("entry = %v", first)
	}
	st := srv.AnthropicStats()
	if got := st[len(st)-1].Transformations; len(got) != 2 || got[0] != "thinking_dropped:prefix_binding_mismatch" {
		t.Errorf("stat transformations %v", got)
	}
	// Dropped blocks are not billed: the prompt is smaller than the same history
	// with the blocks in place would be, and the cached prefix changes from the
	// first dropped block on.
	if last, prev := st[len(st)-1], st[len(st)-2]; last.PromptTokens >= prev.PromptTokens+tok(len("output 2"))+20 {
		t.Errorf("dropped thinking should not count as input: %d vs %d", last.PromptTokens, prev.PromptTokens)
	}
	// A drop is per request: the next request (with the blocks still in the
	// history) has them dropped again, and the next thinking block minted
	// afterwards is valid.
	if r := c.step(""); r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	}
}

func TestBindingHeaderControlsWhatIsReported(t *testing.T) {
	c, _ := newConvo(t, enforce, "claude-opus-5-5")
	c.fields["thinking"] = obj{"type": "adaptive"}
	r := c.step("")
	if _, ok := r.json(t)["input_transformations"]; ok {
		t.Fatalf("without the beta header the field is absent: %s", r.body)
	}
	c2, _ := newConvo(t, enforce, "claude-opus-5-5", "Anthropic-Beta", betaBinding)
	r = c2.step("")
	xs, ok := r.json(t)["input_transformations"].([]any)
	if !ok || len(xs) != 0 {
		t.Fatalf("with the header every response carries the array, empty when nothing happened: %s", r.body)
	}
	evs := readSSE(t, post(t, c2.ts, c2.body(obj{"stream": true}), "Anthropic-Beta", betaBinding).body)
	if x, ok := evs[0].data["message"].(map[string]any)["input_transformations"].([]any); !ok || len(x) != 0 {
		t.Errorf("message_start carries the array: %v", evs[0].data)
	}
	last := evs[len(evs)-2].data
	if _, ok := last["input_transformations"]; !ok {
		t.Errorf("message_delta repeats it: %v", last)
	}
}

func TestBindingEnforcementScope(t *testing.T) {
	stale := func(c *convo) { c.step(""); c.msgs[0] = userMsg(text("edited")) }

	t.Run("an account that is not enforced only records, and only with the beta header", func(t *testing.T) {
		c, _ := newConvo(t, AnthropicConfig{}, "claude-opus-5-5", "Anthropic-Beta", betaBinding)
		stale(c)
		r := c.step("")
		if r.status != 200 {
			t.Fatalf("%d %s", r.status, r.body)
		}
		x := r.json(t)["input_transformations"].([]any)
		if len(x) != 1 || x[0].(map[string]any)["type"] != "thinking_mismatch_allowed" {
			t.Errorf("transformations = %v", x)
		}
	})
	t.Run("setting the field opts a request in, even to error", func(t *testing.T) {
		c, _ := newConvo(t, AnthropicConfig{}, "claude-opus-5-5", "Anthropic-Beta", betaBinding)
		stale(c)
		c.fields["thinking"] = obj{"type": "adaptive", "block_binding": obj{"prefix_mismatch_behavior": "error"}}
		if r := c.step(""); r.status != 400 || r.errMsg(t) != bindingErr {
			t.Fatalf("%d %q", r.status, r.errMsg(t))
		}
	})
	t.Run("models without the check accept stale blocks", func(t *testing.T) {
		c, _ := newConvo(t, AnthropicConfig{}, "mock-1")
		stale(c)
		if r := c.step(""); r.status != 200 {
			t.Fatalf("%d %s", r.status, r.body)
		}
		c2, _ := newConvo(t, AnthropicConfig{}, "claude-mythos-5-1")
		stale(c2)
		if r := c2.step(""); r.status != 200 {
			t.Fatalf("Mythos 5.1 does not run the check: %d %s", r.status, r.body)
		}
	})
}

func TestBindingModelSwitchDropsBlocksSilently(t *testing.T) {
	c, srv := newConvo(t, enforce, "claude-opus-5-5")
	c.step("")
	c.fields["model"] = "claude-fable-5-1"
	r := c.step("")
	if r.status != 200 {
		t.Fatalf("a model switch is never a failure: %d %s", r.status, r.body)
	}
	if _, ok := r.json(t)["input_transformations"]; ok {
		t.Errorf("silent without the beta header: %s", r.body)
	}
	c.headers = []string{"Anthropic-Beta", betaBinding}
	c.fields["model"] = "claude-opus-5-5" // and back
	c.msgs = c.msgs[:1]
	c.msgs[0] = userMsg(text("start"))
	c.step("")
	c.fields["model"] = "claude-fable-5-1"
	r = c.step("")
	xs := r.json(t)["input_transformations"].([]any)
	if len(xs) != 1 || xs[0].(map[string]any)["reason"] != "model_binding_mismatch" {
		t.Fatalf("transformations = %v", xs)
	}
	_ = srv
}

func TestBindingTamperedAndModifiedSignatures(t *testing.T) {
	c, _ := newConvo(t, enforce, "claude-opus-5-5")
	c.step("")
	th := c.msgs[1]["content"].([]any)[0].(map[string]any)

	orig := th["signature"]
	th["signature"] = "not-a-signature"
	if r := c.step(""); r.status != 400 || r.errMsg(t) != "messages.1.content.0: Invalid `signature` in `thinking` block." {
		t.Fatalf("tampered: %d %q", r.status, r.errMsg(t))
	}
	th["signature"] = orig
	th["thinking"] = "the client edited the reasoning text"
	if r := c.step(""); r.status != 400 || !strings.Contains(r.errMsg(t), "cannot be modified") {
		t.Fatalf("modified text: %d %q", r.status, r.errMsg(t))
	}
}

func TestBindingRemovingLeadingThinkingIsAllowed(t *testing.T) {
	c, _ := newConvo(t, enforce, "claude-opus-5-5")
	c.step("")
	c.step("")
	c.step("")
	// Strip the thinking block of the first assistant turn (the oldest): the
	// remaining blocks keep verifying.
	first := c.msgs[1]["content"].([]any)
	c.msgs[1]["content"] = first[1:]
	if r := c.step(""); r.status != 200 {
		t.Fatalf("removing from the front is allowed: %d %s", r.status, r.body)
	}
	// Removing every thinking block is allowed too.
	for _, m := range c.msgs {
		if m["role"] != "assistant" {
			continue
		}
		var keep []any
		for _, b := range m["content"].([]any) {
			if b.(map[string]any)["type"] != "thinking" {
				keep = append(keep, b)
			}
		}
		m["content"] = keep
	}
	if r := c.step(""); r.status != 200 {
		t.Fatalf("stripping all thinking is allowed: %d %s", r.status, r.body)
	}
}

func TestBindingSurvivesAJSONRoundTrip(t *testing.T) {
	// The client may re-encode blocks (key order, whitespace); the record is over
	// the canonical form, so a re-serialisation alone is not an edit.
	c, _ := newConvo(t, enforce, "claude-opus-5-5")
	c.step("")
	raw, _ := json.Marshal(c.msgs)
	var back []obj
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	c.msgs = back
	if r := c.step(""); r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	}
}
