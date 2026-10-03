// Package openairesp adapts core prompts to OpenAI's Responses API (POST /v1/responses), the dialect "openai-responses": what OpenAI's
// own newer models are served on, and what "Sign in with ChatGPT" (a ChatGPT plan instead of an API key) allows.
//
// Every request is stateless (store false, the whole history in input): the harness keeps the conversation, and the prompt cache is
// OpenAI's automatic prefix cache, which the adapter helps by sending the cache key and by rendering the history in exactly the order kv
// decided. A reasoning model's reasoning comes back as encrypted items; they are kept in Block.Wire and sent back verbatim, with the
// items that followed them, because an endpoint that is not storing the conversation cannot look them up.
package openairesp

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/core"
)

// Dialect is the wire-format name stamped on replayable blocks.
const Dialect = "openai-responses"

// PlanNamespace is the namespace the tools go in on a plan token: the preview of "Sign in with ChatGPT" takes function tools only
// inside a namespace (or as an additional_tools item), and one namespace keeps the tool list one stable block of the prefix.
const PlanNamespace = "sleipnir"

// Options tunes rendering for an endpoint.
type Options struct {
	// Plan renders for a ChatGPT plan token: no output limit or sampling parameter (the preview refuses them), the tools in a namespace.
	Plan bool
	// ReasoningSummary asks for the readable summary of the reasoning ("auto", "concise", "detailed"); empty asks for none. Some
	// models need the organisation to be verified before they give one, so it is off unless a person turns it on.
	ReasoningSummary string
	// ExtraBody is merged into the top level of every request.
	ExtraBody map[string]any
}

// request fixes field order so bodies are reproducible for golden tests and payload diffing. Order carries no meaning for the server.
type request struct {
	Model           string            `json:"model"`
	Instructions    string            `json:"instructions,omitempty"`
	Input           []json.RawMessage `json:"input"`
	Tools           []any             `json:"tools,omitempty"`
	ToolChoice      string            `json:"tool_choice,omitempty"`
	Reasoning       *reasoning        `json:"reasoning,omitempty"`
	Include         []string          `json:"include,omitempty"`
	Store           bool              `json:"store"`
	Stream          bool              `json:"stream"`
	MaxOutputTokens int               `json:"max_output_tokens,omitempty"`
	Temperature     *float64          `json:"temperature,omitempty"`
	PromptCacheKey  string            `json:"prompt_cache_key,omitempty"`
}

type reasoning struct {
	Effort  string `json:"effort,omitempty"`
	Summary string `json:"summary,omitempty"`
}

type function struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
	Strict      bool            `json:"strict"`
}

type namespace struct {
	Type        string     `json:"type"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Tools       []function `json:"tools"`
}

// Build renders a prompt into a Responses request body. A warm-up (max true) asks for as little output as the endpoint allows.
func Build(p *core.Prompt, o Options, warm bool) ([]byte, error) {
	req := request{Model: p.Model, Stream: true, Include: []string{"reasoning.encrypted_content"}, PromptCacheKey: p.CacheKey}
	switch p.Params.ToolChoice {
	case "none", "auto":
		req.ToolChoice = p.Params.ToolChoice
	}
	if e := effort(p.Params.Effort); e != "" || o.ReasoningSummary != "" {
		req.Reasoning = &reasoning{Effort: e, Summary: o.ReasoningSummary}
	}
	if !o.Plan { // the plan's preview refuses both
		req.MaxOutputTokens = p.Params.MaxTokens
		req.Temperature = p.Params.Temperature
		if warm {
			req.MaxOutputTokens = 16 // the smallest the API takes
		}
	}
	if len(p.System) > 0 {
		var sb strings.Builder
		for i, b := range p.System {
			if i > 0 {
				sb.WriteString("\n\n")
			}
			sb.WriteString(b.Text)
		}
		req.Instructions = sb.String()
	}
	if len(p.Tools) > 0 {
		fns := make([]function, len(p.Tools))
		for i, t := range p.Tools {
			fns[i] = function{Type: "function", Name: t.Name, Description: t.Description, Parameters: t.InputSchema, Strict: t.Strict}
		}
		if o.Plan {
			req.Tools = []any{namespace{Type: "namespace", Name: PlanNamespace, Description: "The tools of the harness.", Tools: fns}}
		} else {
			for _, f := range fns {
				req.Tools = append(req.Tools, f)
			}
		}
	}
	for _, m := range p.Messages {
		items, err := renderMessage(m, o)
		if err != nil {
			return nil, err
		}
		req.Input = append(req.Input, items...)
	}
	if req.Input == nil {
		req.Input = []json.RawMessage{}
	}
	body, err := core.MarshalStable(req)
	if err != nil {
		return nil, err
	}
	if len(o.ExtraBody) == 0 {
		return body, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}
	for k, v := range o.ExtraBody {
		b, err := core.MarshalStable(v)
		if err != nil {
			return nil, err
		}
		m[k] = b
	}
	return core.MarshalStable(m)
}

// effort passes recognized effort names; model-specific ranges are selected by
// the caller and refined from endpoint validation when necessary.
func effort(e string) string {
	switch e {
	case "none", "minimal", "low", "medium", "high", "xhigh", "max":
		return e
	}
	return ""
}

// renderMessage maps system messages to developer wire items, dispatches supported roles, and
// rejects other roles.
func renderMessage(m core.Message, o Options) ([]json.RawMessage, error) {
	switch m.Role {
	case core.RoleAssistant:
		return renderAssistant(m, o)
	case core.RoleUser:
		return renderUser(m, "user")
	case core.RoleSystem:
		return renderUser(m, "developer") // the endpoint takes instructions and developer messages, not system ones, in the input
	}
	return nil, fmt.Errorf("openairesp: unsupported role %q", m.Role)
}

// item marshals one input item.
func item(v any) (json.RawMessage, error) { return core.MarshalStable(v) }

// messageItem encodes a wire message containing one text-bearing content part with the supplied
// role and type.
func messageItem(role, partType, text string) (json.RawMessage, error) {
	return item(map[string]any{"type": "message", "role": role, "content": []map[string]any{{"type": partType, "text": text}}})
}

// renderAssistant replays a model turn. The reasoning items it has are sent back verbatim, and then the items that came after them as
// they came (ids included: a stateless endpoint checks that a reasoning item is followed by the item it was produced with). With no
// reasoning in the turn there is nothing to be paired with, so the ids are left out and the items are built from what the harness holds.
func renderAssistant(m core.Message, o Options) ([]json.RawMessage, error) {
	paired := false
	for _, b := range m.Blocks {
		if b.Kind == core.BlockThinking && replayable(b) {
			paired = true
		}
	}
	var out []json.RawMessage
	for _, b := range m.Blocks {
		switch b.Kind {
		case core.BlockThinking, core.BlockRedactedThinking:
			if replayable(b) {
				out = append(out, b.Wire)
			}
		case core.BlockText:
			if b.Text == "" {
				continue
			}
			if paired && replayable(b) {
				out = append(out, b.Wire)
				continue
			}
			it, err := messageItem("assistant", "output_text", b.Text)
			if err != nil {
				return nil, err
			}
			out = append(out, it)
		case core.BlockToolUse:
			if paired && replayable(b) && b.Invalid == "" {
				out = append(out, b.Wire)
				continue
			}
			args := string(b.Input)
			if args == "" || b.Invalid != "" {
				// Arguments the model cut off or garbled are not JSON, and an endpoint that checks the history refuses the whole
				// request for them. The tool result already tells the model what it sent.
				args = "{}"
			}
			call := map[string]any{"type": "function_call", "call_id": b.ToolID, "name": b.ToolName, "arguments": args}
			if o.Plan {
				call["namespace"] = PlanNamespace
			}
			it, err := item(call)
			if err != nil {
				return nil, err
			}
			out = append(out, it)
		}
	}
	return out, nil
}

// replayable reports whether a block retains nonempty wire data in this provider's dialect.
func replayable(b core.Block) bool { return b.WireFormat == Dialect && len(b.Wire) > 0 }

// renderUser emits the tool results first (a result goes right after the call it answers), then the rest of the turn as one message.
// The order is fixed, so the same turn always renders to the same bytes.
func renderUser(m core.Message, role string) ([]json.RawMessage, error) {
	var out []json.RawMessage
	var parts []map[string]any
	for _, b := range m.Blocks {
		switch b.Kind {
		case core.BlockToolResult:
			it, err := item(map[string]any{"type": "function_call_output", "call_id": b.ToolID, "output": toolResultText(b)})
			if err != nil {
				return nil, err
			}
			out = append(out, it)
		case core.BlockText:
			parts = append(parts, map[string]any{"type": "input_text", "text": b.Text})
		case core.BlockImage:
			parts = append(parts, map[string]any{"type": "input_image", "image_url": b.MediaRef})
		}
	}
	if len(parts) > 0 {
		it, err := item(map[string]any{"type": "message", "role": role, "content": parts})
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, nil
}

// toolResultText concatenates text children and prefixes error results unless they already begin
// with Error.
func toolResultText(b core.Block) string {
	var sb strings.Builder
	for _, c := range b.Result {
		if c.Kind == core.BlockText {
			sb.WriteString(c.Text)
		}
	}
	s := sb.String()
	if b.IsError && !strings.HasPrefix(s, "Error") {
		return "Error: " + s
	}
	return s
}
