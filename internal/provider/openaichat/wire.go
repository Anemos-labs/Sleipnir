// Package openaichat adapts core prompts to OpenAI-style /chat/completions,
// the dialect spoken by OpenAI itself and by gateways such as Heimdall and
// OpenRouter.
//
// These endpoints cache automatically: there are no breakpoints to place, so
// the adapter's job for caching is negative. It must render the prompt in
// exactly the order kv decided, replay assistant messages byte-for-byte, and
// tell the gateway which conversation a request belongs to so that requests
// sharing a prefix land on the engine that already holds it.
package openaichat

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/core"
)

// Dialect is the wire-format name stamped on replayable blocks.
const Dialect = "openai-chat"

// Options tunes rendering for a specific endpoint family.
type Options struct {
	// SystemRole is "system" (default) or "developer" (OpenAI reasoning models).
	SystemRole string
	// MaxTokensField is "max_tokens" (default) or "max_completion_tokens".
	MaxTokensField string
	// CacheKeyBody sends prompt_cache_key in the body (OpenAI, Heimdall).
	CacheKeyBody bool
	// SessionHeader sends the cache key as X-Session-Id (gateway affinity).
	SessionHeader bool
	// CacheControlParts adds cache_control markers to content parts at
	// breakpoints, for gateways that front Anthropic models.
	CacheControlParts bool
	// ReasoningEffortField is the request member carrying effort, "" to omit.
	ReasoningEffortField string
	// ExtraBody is merged into the top level of every request.
	ExtraBody map[string]any
	// CaptureTokens asks a vLLM/SGLang-style server for the prompt and completion
	// token ids and per-token logprobs (RL training data). Set per request by the
	// client, only when the profile says the endpoint supports it.
	CaptureTokens bool
}

func (o *Options) defaults() {
	if o.SystemRole == "" {
		o.SystemRole = "system"
	}
	if o.MaxTokensField == "" {
		o.MaxTokensField = "max_tokens"
	}
}

// chatRequest fixes field order so bodies are reproducible for golden tests
// and payload diffing. Order carries no meaning for the server.
type chatRequest struct {
	Model            string         `json:"model"`
	Messages         []message      `json:"messages"`
	Tools            []tool         `json:"tools,omitempty"`
	ToolChoice       any            `json:"tool_choice,omitempty"`
	MaxTokens        int            `json:"max_tokens,omitempty"`
	MaxCompletion    int            `json:"max_completion_tokens,omitempty"`
	Stream           bool           `json:"stream"`
	StreamOptions    *streamOptions `json:"stream_options,omitempty"`
	ReasoningEffort  string         `json:"reasoning_effort,omitempty"`
	Temperature      *float64       `json:"temperature,omitempty"`
	Stop             []string       `json:"stop,omitempty"`
	PromptCacheKey   string         `json:"prompt_cache_key,omitempty"`
	ParallelToolCall *bool          `json:"parallel_tool_calls,omitempty"`
	Logprobs         bool           `json:"logprobs,omitempty"`
	ReturnTokenIDs   bool           `json:"return_token_ids,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type tool struct {
	Type     string   `json:"type"`
	Function function `json:"function"`
}

type function struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
	Strict      bool            `json:"strict,omitempty"`
}

type message struct {
	Role             string            `json:"role"`
	Content          any               `json:"content"`
	ToolCalls        []json.RawMessage `json:"tool_calls,omitempty"`
	ToolCallID       string            `json:"tool_call_id,omitempty"`
	ReasoningDetails json.RawMessage   `json:"reasoning_details,omitempty"`
}

type part struct {
	Type         string        `json:"type"`
	Text         string        `json:"text,omitempty"`
	ImageURL     *imageURL     `json:"image_url,omitempty"`
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

type imageURL struct {
	URL string `json:"url"`
}

type cacheControl struct {
	Type string `json:"type"`
	TTL  string `json:"ttl,omitempty"`
}

// Build renders a prompt into a chat request body.
func Build(p *core.Prompt, o Options, stream bool) ([]byte, error) {
	o.defaults()
	req := chatRequest{Model: p.Model, Stream: stream}
	if stream {
		req.StreamOptions = &streamOptions{IncludeUsage: true}
	}
	if p.Params.MaxTokens > 0 {
		if o.MaxTokensField == "max_completion_tokens" {
			req.MaxCompletion = p.Params.MaxTokens
		} else {
			req.MaxTokens = p.Params.MaxTokens
		}
	}
	if o.CacheKeyBody {
		req.PromptCacheKey = p.CacheKey
	}
	if o.CaptureTokens {
		req.Logprobs, req.ReturnTokenIDs = true, true
	}
	if o.ReasoningEffortField != "" && p.Params.Effort != "" {
		req.ReasoningEffort = p.Params.Effort
	}
	req.Temperature = p.Params.Temperature
	req.Stop = p.Params.Stop
	switch p.Params.ToolChoice {
	case "none":
		req.ToolChoice = "none"
	case "auto":
		req.ToolChoice = "auto"
	}
	for _, t := range p.Tools {
		req.Tools = append(req.Tools, tool{Type: "function", Function: function{
			Name: t.Name, Description: t.Description, Parameters: t.InputSchema, Strict: t.Strict,
		}})
	}

	// Breakpoint lookup for gateways that honour cache_control in parts.
	bpAt := map[core.BlockRef]core.Breakpoint{}
	if o.CacheControlParts {
		for _, b := range p.Breakpoints {
			bpAt[b.After] = b
		}
	}

	if len(p.System) > 0 {
		req.Messages = append(req.Messages, renderSystem(p.System, o.SystemRole, bpAt))
	}
	for mi, m := range p.Messages {
		msgs, err := renderMessage(mi, m, bpAt)
		if err != nil {
			return nil, err
		}
		req.Messages = append(req.Messages, msgs...)
	}

	body, err := core.MarshalStable(req)
	if err != nil {
		return nil, err
	}
	if len(o.ExtraBody) == 0 {
		return body, nil
	}
	// Merge extras deterministically (map keys are sorted on encode).
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

// renderSystem renders the system blocks as one message. Without markers it is
// one string (the block texts joined by a blank line); when a block carries a
// cache marker (the constitution's, on gateways fronting Anthropic models) the
// content becomes one part per block, the marker on that block's part, with the
// same bytes overall.
func renderSystem(blocks []core.Block, role string, bpAt map[core.BlockRef]core.Breakpoint) message {
	marked := false
	for i := range blocks {
		if _, ok := bpAt[core.BlockRef{Sys: true, Msg: 0, Blk: i}]; ok {
			marked = true
		}
	}
	if !marked {
		var sb strings.Builder
		for i, b := range blocks {
			if i > 0 {
				sb.WriteString("\n\n")
			}
			sb.WriteString(b.Text)
		}
		return message{Role: role, Content: sb.String()}
	}
	parts := make([]part, 0, len(blocks))
	for i, b := range blocks {
		text := b.Text
		if i > 0 {
			text = "\n\n" + text
		}
		pt := part{Type: "text", Text: text}
		if bp, ok := bpAt[core.BlockRef{Sys: true, Msg: 0, Blk: i}]; ok {
			pt.CacheControl = &cacheControl{Type: "ephemeral", TTL: ttlString(bp)}
		}
		parts = append(parts, pt)
	}
	return message{Role: role, Content: parts}
}

func renderMessage(mi int, m core.Message, bpAt map[core.BlockRef]core.Breakpoint) ([]message, error) {
	switch m.Role {
	case core.RoleAssistant:
		return renderAssistant(mi, m, bpAt)
	case core.RoleUser, core.RoleSystem:
		return renderUser(mi, m, bpAt)
	}
	return nil, fmt.Errorf("openaichat: unsupported role %q", m.Role)
}

func renderAssistant(mi int, m core.Message, bpAt map[core.BlockRef]core.Breakpoint) ([]message, error) {
	out := message{Role: "assistant"}
	var text strings.Builder
	var mark *core.Breakpoint
	for bi, b := range m.Blocks {
		switch b.Kind {
		case core.BlockText:
			text.WriteString(b.Text)
			if bp, ok := bpAt[core.BlockRef{Msg: mi, Blk: bi}]; ok {
				bp := bp
				mark = &bp
			}
		case core.BlockThinking, core.BlockRedactedThinking:
			// Only opaque reasoning_details are replayable, and only verbatim.
			if b.WireFormat == Dialect && len(b.Wire) > 0 {
				out.ReasoningDetails = b.Wire
			}
		case core.BlockToolUse:
			if b.WireFormat == Dialect && len(b.Wire) > 0 && b.Invalid == "" {
				out.ToolCalls = append(out.ToolCalls, b.Wire)
				continue
			}
			args := string(b.Input)
			if args == "" || b.Invalid != "" {
				// Arguments the model cut off or garbled are not JSON, and an endpoint that checks the history refuses the whole
				// request for them (one run ended on that). The tool result already tells the model what it sent.
				args = "{}"
			}
			tc, err := core.MarshalStable(map[string]any{
				"id": b.ToolID, "type": "function",
				"function": map[string]any{"name": b.ToolName, "arguments": args},
			})
			if err != nil {
				return nil, err
			}
			out.ToolCalls = append(out.ToolCalls, tc)
		}
	}
	if text.Len() > 0 && mark != nil {
		// A marker on assistant text needs the parts form; the text is the same.
		out.Content = []part{{Type: "text", Text: text.String(), CacheControl: &cacheControl{Type: "ephemeral", TTL: ttlString(*mark)}}}
	} else if text.Len() > 0 {
		out.Content = text.String()
	} else if len(out.ToolCalls) > 0 {
		out.Content = nil
	} else {
		out.Content = ""
	}
	return []message{out}, nil
}

// renderUser emits tool results as role=tool messages first (chat requires
// them to directly follow the assistant tool_calls), then any remaining
// blocks as one user message. The order is fixed, so the same turn always
// renders to the same bytes.
func renderUser(mi int, m core.Message, bpAt map[core.BlockRef]core.Breakpoint) ([]message, error) {
	var out []message
	var parts []part
	for bi, b := range m.Blocks {
		ref := core.BlockRef{Msg: mi, Blk: bi}
		switch b.Kind {
		case core.BlockToolResult:
			// Chat has no tool_result block: the result is a role=tool message. A
			// cache marker on it (in every tool loop the rolling marker lands on the
			// last tool result) is only expressible on a content part, so a marked
			// result is sent as a one-part array; unmarked results stay plain strings.
			if bp, ok := bpAt[ref]; ok {
				out = append(out, message{Role: "tool", ToolCallID: b.ToolID, Content: []part{{
					Type: "text", Text: toolResultText(b), CacheControl: &cacheControl{Type: "ephemeral", TTL: ttlString(bp)},
				}}})
				continue
			}
			out = append(out, message{Role: "tool", ToolCallID: b.ToolID, Content: toolResultText(b)})
		case core.BlockText:
			pt := part{Type: "text", Text: b.Text}
			if bp, ok := bpAt[ref]; ok {
				pt.CacheControl = &cacheControl{Type: "ephemeral", TTL: ttlString(bp)}
			}
			parts = append(parts, pt)
		case core.BlockImage:
			parts = append(parts, part{Type: "image_url", ImageURL: &imageURL{URL: b.MediaRef}})
		}
	}
	if len(parts) > 0 {
		// A single text part is sent as a plain string: smaller, and the
		// canonical form every engine accepts.
		if len(parts) == 1 && parts[0].Type == "text" && parts[0].CacheControl == nil {
			out = append(out, message{Role: "user", Content: parts[0].Text})
		} else {
			out = append(out, message{Role: "user", Content: parts})
		}
	}
	return out, nil
}

func ttlString(bp core.Breakpoint) string {
	if bp.TTL >= 30*time.Minute {
		return "1h"
	}
	return ""
}

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
