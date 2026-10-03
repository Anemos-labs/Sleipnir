package export

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/provider/openaichat"
	"github.com/anemos-labs/sleipnir/internal/rl"
)

// promptFor returns the step's exact prompt. A prompt embedded in the step is
// verified against the recorded wire hash (it came from a file, not from a
// verified replay); one from a resolver is trusted, because the usual resolver
// (traj.Run) has already verified it.
func (x *exporter) promptFor(we *workEpisode, st *rl.Step) (*core.Prompt, error) {
	if st.Inline != nil {
		if st.Prompt.WireHash != "" {
			got, err := WireHashOf(st.Inline)
			if err != nil {
				return nil, err
			}
			if got != st.Prompt.WireHash {
				return nil, fmt.Errorf("inline prompt of %s hashes to %s, recorded %s", st.ID, got.Short(), st.Prompt.WireHash.Short())
			}
		}
		return st.Inline, nil
	}
	if we.src.Prompts == nil {
		return nil, fmt.Errorf("step %s has no inline prompt and the source has no prompt resolver", st.ID)
	}
	p, err := we.src.Prompts.Prompt(we.src.Episode, st)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, fmt.Errorf("resolver returned no prompt for %s", st.ID)
	}
	return p, nil
}

// WireHashOf computes the wire hash of a prompt: the identity of what the model
// sees, exactly as core.BuildManifest records it (model, tools, system blocks and
// messages; breakpoints, cache key and parameters excluded).
func WireHashOf(p *core.Prompt) (core.Hash, error) {
	var tools core.Hash
	if len(p.Tools) > 0 {
		b, err := core.MarshalStable(p.Tools)
		if err != nil {
			return "", err
		}
		tools = core.HashBytes(b)
	}
	var system []core.Hash
	for _, blk := range p.System {
		b, err := core.MarshalStable(blk)
		if err != nil {
			return "", err
		}
		system = append(system, core.HashBytes(b))
	}
	msgs := make([]core.Hash, len(p.Messages))
	for i, m := range p.Messages {
		b, err := messageBytes(m)
		if err != nil {
			return "", err
		}
		msgs[i] = core.HashBytes(b)
	}
	return core.WireHash(p.Model, tools, system, msgs), nil
}

// wireMsg is the stored shape of a message (see core.BuildManifest): the
// model-visible part of a core.Message.
type wireMsg struct {
	Role   core.Role    `json:"role"`
	Blocks []core.Block `json:"blocks"`
}

// messageBytes stably encodes only a message's role and blocks, excluding other message metadata.
func messageBytes(m core.Message) ([]byte, error) {
	return core.MarshalStable(wireMsg{Role: m.Role, Blocks: m.Blocks})
}

// ---- redaction of core values ----------------------------------------------

// redactText redacts a string and reports whether it changed.
func (x *exporter) redactText(s string) (string, bool) {
	if x.red == nil || s == "" {
		return s, false
	}
	return x.red.Changed(s)
}

// redactRaw redacts nonempty JSON when a redactor is configured and reports whether output bytes
// changed.
func (x *exporter) redactRaw(raw json.RawMessage) (json.RawMessage, bool) {
	if x.red == nil || len(raw) == 0 {
		return raw, false
	}
	out := x.red.JSON(raw)
	return out, string(out) != string(raw)
}

// redactBlock returns a redacted copy of b. Every string the model could see or
// a trainer could read is covered: text, thinking text, tool arguments (and their
// provider-native wire copy), tool results, the invalid-arguments note.
func (x *exporter) redactBlock(b core.Block) (core.Block, bool) {
	if x.red == nil {
		return b, false
	}
	changed := false
	var c bool
	if b.Text, c = x.redactText(b.Text); c {
		changed = true
	}
	if b.Invalid, c = x.redactText(b.Invalid); c {
		changed = true
	}
	if b.Input, c = x.redactRaw(b.Input); c {
		changed = true
	}
	if b.Wire, c = x.redactRaw(b.Wire); c {
		changed = true
	}
	if len(b.Result) > 0 {
		res := make([]core.Block, len(b.Result))
		for i, r := range b.Result {
			var rc bool
			res[i], rc = x.redactBlock(r)
			changed = changed || rc
		}
		b.Result = res
	}
	return b, changed
}

// redactBlocks applies configured redaction to a new block slice and reports any change; without a
// redactor it returns the original slice.
func (x *exporter) redactBlocks(bs []core.Block) ([]core.Block, bool) {
	if x.red == nil {
		return bs, false
	}
	out := make([]core.Block, len(bs))
	changed := false
	for i, b := range bs {
		var c bool
		out[i], c = x.redactBlock(b)
		changed = changed || c
	}
	return out, changed
}

// redactPrompt returns a redacted copy of p (p itself is never modified) and
// whether anything changed. With no Redactor it returns p.
func (x *exporter) redactPrompt(p *core.Prompt) (*core.Prompt, bool) {
	if x.red == nil {
		return p, false
	}
	out := *p
	changed := false
	var c bool
	if out.System, c = x.redactBlocks(p.System); c {
		changed = true
	}
	out.Tools = make([]core.ToolSpec, len(p.Tools))
	for i, t := range p.Tools {
		t.Description, c = x.redactText(t.Description)
		changed = changed || c
		t.InputSchema, c = x.redactRaw(t.InputSchema)
		changed = changed || c
		out.Tools[i] = t
	}
	out.Messages = make([]core.Message, len(p.Messages))
	for i, m := range p.Messages {
		m.Blocks, c = x.redactBlocks(m.Blocks)
		changed = changed || c
		out.Messages[i] = m
	}
	return &out, changed
}

// redactTurn returns a redacted copy of t and whether anything changed.
func (x *exporter) redactTurn(t core.Turn) (core.Turn, bool) {
	if x.red == nil {
		return t, false
	}
	var c bool
	t.Blocks, c = x.redactBlocks(t.Blocks)
	return t, c
}

// ---- wire rendering ---------------------------------------------------------

// wirePrompt is a prompt rendered for a chat endpoint: exactly the messages and
// tools openaichat.Build sends.
type wirePrompt struct {
	Messages []json.RawMessage
	Tools    json.RawMessage // nil when the prompt has no tools
}

// renderPrompt renders p with openaichat.Build, so the training prompt equals
// what the endpoint received. objectArgs converts tool-call arguments from the
// wire's JSON strings to JSON objects (the Hugging Face convention).
func (x *exporter) renderPrompt(p *core.Prompt, objectArgs bool) (wirePrompt, error) {
	body, err := openaichat.Build(p, x.o.Wire, false)
	if err != nil {
		return wirePrompt{}, fmt.Errorf("render prompt: %w", err)
	}
	var req struct {
		Messages []json.RawMessage `json:"messages"`
		Tools    json.RawMessage   `json:"tools"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return wirePrompt{}, fmt.Errorf("parse rendered prompt: %w", err)
	}
	if objectArgs {
		for i, m := range req.Messages {
			if req.Messages[i], err = argsToObjects(m); err != nil {
				return wirePrompt{}, err
			}
		}
	}
	return wirePrompt{Messages: req.Messages, Tools: req.Tools}, nil
}

// argsToObjects rewrites function.arguments of every tool call of a wire message
// from a JSON string to the JSON value it holds. Arguments that do not parse are
// left as the string the model produced: nothing is repaired.
func argsToObjects(msg json.RawMessage) (json.RawMessage, error) {
	if !strings.Contains(string(msg), `"tool_calls"`) {
		return msg, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(msg, &m); err != nil {
		return nil, fmt.Errorf("parse wire message: %w", err)
	}
	raw, ok := m["tool_calls"]
	if !ok {
		return msg, nil
	}
	var calls []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &calls); err != nil {
		return msg, nil // not the shape we know: leave the message as sent
	}
	for _, c := range calls {
		var fn map[string]json.RawMessage
		if json.Unmarshal(c["function"], &fn) != nil {
			continue
		}
		var args string
		if json.Unmarshal(fn["arguments"], &args) != nil {
			continue
		}
		if json.Valid([]byte(args)) && strings.TrimSpace(args) != "" {
			fn["arguments"] = json.RawMessage(args)
			if b, err := marshalStable(fn); err == nil {
				c["function"] = b
			}
		}
	}
	b, err := marshalStable(calls)
	if err != nil {
		return nil, err
	}
	m["tool_calls"] = b
	return marshalStable(m)
}

// renderCompletion renders one completion turn as the assistant wire message the
// next prompt would contain, then applies the reasoning option.
func (x *exporter) renderCompletion(t core.Turn, model string, objectArgs bool) (json.RawMessage, error) {
	return x.renderCompletionAs(t, model, objectArgs, x.o.Reasoning)
}

// renderCompletionAs is renderCompletion with an explicit reasoning mode; "keep"
// yields the message exactly as the wire replays it.
func (x *exporter) renderCompletionAs(t core.Turn, model string, objectArgs bool, reasoning string) (json.RawMessage, error) {
	p := &core.Prompt{Model: model, Messages: []core.Message{{Role: core.RoleAssistant, Blocks: t.Blocks}}}
	wp, err := x.renderPrompt(p, objectArgs)
	if err != nil {
		return nil, err
	}
	if len(wp.Messages) != 1 {
		return nil, fmt.Errorf("completion rendered to %d messages", len(wp.Messages))
	}
	if reasoning == "keep" {
		return wp.Messages[0], nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(wp.Messages[0], &m); err != nil {
		return nil, err
	}
	delete(m, "reasoning_details")
	if reasoning == "field" {
		var sb strings.Builder
		for _, b := range t.Blocks {
			if b.Kind == core.BlockThinking && b.Text != "" {
				if sb.Len() > 0 {
					sb.WriteString("\n")
				}
				sb.WriteString(b.Text)
			}
		}
		if sb.Len() > 0 {
			rb, _ := marshalStable(sb.String())
			m["reasoning_content"] = rb
		}
	}
	return marshalStable(m)
}
