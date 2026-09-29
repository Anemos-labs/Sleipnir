package openaichat

import (
	"encoding/json"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/provider"
)

// chunk is one streamed frame. Non-streaming replies share the field names
// under choices[].message, handled by the same accumulator.
type chunk struct {
	ID       string `json:"id"`
	Model    string `json:"model"`
	Provider string `json:"provider"`
	Choices  []struct {
		Index        int     `json:"index"`
		Delta        *delta  `json:"delta"`
		Message      *delta  `json:"message"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *usage    `json:"usage"`
	Error *apiError `json:"error"`
}

type delta struct {
	Role             string          `json:"role"`
	Content          *string         `json:"content"`
	Reasoning        string          `json:"reasoning"`
	ReasoningContent string          `json:"reasoning_content"`
	ReasoningDetails []reasoningItem `json:"reasoning_details"`
	ToolCalls        []toolDelta     `json:"tool_calls"`
}

type reasoningItem struct {
	Type      string  `json:"type"`
	Text      *string `json:"text,omitempty"`
	Summary   *string `json:"summary,omitempty"`
	Data      *string `json:"data,omitempty"`
	Signature *string `json:"signature,omitempty"`
	Format    *string `json:"format,omitempty"`
	Index     *int    `json:"index,omitempty"`
}

type toolDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type usage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	TotalTokens         int `json:"total_tokens"`
	PromptTokensDetails *struct {
		CachedTokens     int `json:"cached_tokens"`
		CacheWriteTokens int `json:"cache_write_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails *struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
	// DeepSeek-native names, passed through by some gateways.
	PromptCacheHitTokens  *int     `json:"prompt_cache_hit_tokens"`
	PromptCacheMissTokens *int     `json:"prompt_cache_miss_tokens"`
	Cost                  *float64 `json:"cost"`
}

type apiError struct {
	Code     json.RawMessage `json:"code"`
	Message  string          `json:"message"`
	Metadata json.RawMessage `json:"metadata"`
}

// normalize converts the wire usage to core.Usage.
func (u *usage) normalize() core.Usage {
	if u == nil {
		return core.Usage{}
	}
	cached, wrote := 0, 0
	if u.PromptTokensDetails != nil {
		cached = u.PromptTokensDetails.CachedTokens
		wrote = u.PromptTokensDetails.CacheWriteTokens
	} else if u.PromptCacheHitTokens != nil {
		cached = *u.PromptCacheHitTokens
	}
	in := u.PromptTokens - cached - wrote
	if in < 0 {
		in = 0
	}
	out := core.Usage{
		InputTokens:        in,
		CacheReadTokens:    cached,
		CacheWrite5mTokens: wrote,
		OutputTokens:       u.CompletionTokens,
	}
	if u.CompletionTokensDetails != nil {
		out.ReasoningTokens = u.CompletionTokensDetails.ReasoningTokens
	}
	return out
}

// accumulator folds streamed deltas into a finished assistant turn.
type accumulator struct {
	id, model, provider string
	text                strings.Builder
	reasoning           strings.Builder
	details             map[int]*reasoningItem
	detailOrder         []int
	calls               map[int]*callAcc
	finish              string
	usage               *usage
	rawUsage            json.RawMessage
	started             bool
	textOpen            bool
}

type callAcc struct {
	id, name string
	args     strings.Builder
}

func newAccumulator() *accumulator {
	return &accumulator{details: map[int]*reasoningItem{}, calls: map[int]*callAcc{}}
}

// feed applies one chunk and emits streaming events.
func (a *accumulator) feed(c *chunk, start time.Time, on func(provider.Event)) {
	if c.ID != "" {
		a.id = c.ID
	}
	if c.Model != "" {
		a.model = c.Model
	}
	if c.Provider != "" {
		a.provider = c.Provider
	}
	if c.Usage != nil {
		a.usage = c.Usage
	}
	for _, ch := range c.Choices {
		d := ch.Delta
		if d == nil {
			d = ch.Message
		}
		if d != nil {
			if !a.started {
				a.started = true
				on(provider.Event{Kind: provider.EvStart, RequestID: a.id, Elapsed: time.Since(start)})
			}
			a.applyDelta(d, on)
		}
		if ch.FinishReason != nil && *ch.FinishReason != "" {
			a.finish = *ch.FinishReason
		}
	}
}

func (a *accumulator) applyDelta(d *delta, on func(provider.Event)) {
	if d.Content != nil && *d.Content != "" {
		a.text.WriteString(*d.Content)
		on(provider.Event{Kind: provider.EvText, Text: *d.Content})
	}
	r := d.Reasoning
	if r == "" {
		r = d.ReasoningContent
	}
	if r != "" {
		a.reasoning.WriteString(r)
		on(provider.Event{Kind: provider.EvThinking, Text: r})
	}
	for i := range d.ReasoningDetails {
		it := d.ReasoningDetails[i]
		idx := 0
		if it.Index != nil {
			idx = *it.Index
		}
		cur, ok := a.details[idx]
		if !ok {
			cp := it
			cur = &cp
			a.details[idx] = cur
			a.detailOrder = append(a.detailOrder, idx)
			continue
		}
		if it.Text != nil {
			s := *it.Text
			if cur.Text != nil {
				s = *cur.Text + s
			}
			cur.Text = &s
		}
		if it.Summary != nil {
			s := *it.Summary
			if cur.Summary != nil {
				s = *cur.Summary + s
			}
			cur.Summary = &s
		}
		if it.Data != nil {
			s := *it.Data
			if cur.Data != nil {
				s = *cur.Data + s
			}
			cur.Data = &s
		}
		if it.Signature != nil {
			cur.Signature = it.Signature
		}
		if it.Format != nil {
			cur.Format = it.Format
		}
	}
	for _, tc := range d.ToolCalls {
		c, ok := a.calls[tc.Index]
		if !ok {
			c = &callAcc{}
			a.calls[tc.Index] = c
		}
		if tc.ID != "" {
			c.id = tc.ID
		}
		if tc.Function.Name != "" {
			c.name = tc.Function.Name
			on(provider.Event{Kind: provider.EvToolStart, Index: tc.Index, ToolID: c.id, ToolName: c.name})
		}
		if tc.Function.Arguments != "" {
			c.args.WriteString(tc.Function.Arguments)
			on(provider.Event{Kind: provider.EvToolDelta, Index: tc.Index, Text: tc.Function.Arguments})
		}
	}
}

// finish builds the assistant turn.
func (a *accumulator) build(fallbackID func(name, args string, n int) string) (core.Turn, core.StopReason) {
	var blocks []core.Block

	if len(a.detailOrder) > 0 {
		sort.Ints(a.detailOrder)
		items := make([]*reasoningItem, 0, len(a.detailOrder))
		for _, i := range a.detailOrder {
			items = append(items, a.details[i])
		}
		// Sent back unchanged, as the endpoint documents: same entries, same
		// order, same fields.
		if raw, err := core.MarshalStable(items); err == nil {
			blocks = append(blocks, core.Block{
				Kind: core.BlockThinking, Text: a.reasoning.String(),
				Wire: raw, WireFormat: Dialect,
			})
		}
	} else if a.reasoning.Len() > 0 {
		// Plain reasoning text: kept for display and logs, never replayed.
		blocks = append(blocks, core.Block{Kind: core.BlockThinking, Text: a.reasoning.String()})
	}
	if a.text.Len() > 0 {
		blocks = append(blocks, core.Text(a.text.String()))
	}
	idxs := make([]int, 0, len(a.calls))
	for i := range a.calls {
		idxs = append(idxs, i)
	}
	sort.Ints(idxs)
	for n, i := range idxs {
		c := a.calls[i]
		args := c.args.String()
		id := c.id
		if id == "" {
			id = fallbackID(c.name, args, n)
		}
		b := core.Block{Kind: core.BlockToolUse, ToolID: id, ToolName: c.name}
		if strings.TrimSpace(args) == "" {
			args = "{}"
		}
		if json.Valid([]byte(args)) {
			b.Input = json.RawMessage(args)
		} else {
			// Truncated or malformed arguments: keep the text so the harness
			// can tell the model exactly what it produced.
			q, _ := core.MarshalStable(args)
			b.Input = q
			b.Invalid = "arguments are not valid JSON"
		}
		if wire, err := core.MarshalStable(map[string]any{
			"id": id, "type": "function",
			"function": map[string]any{"name": c.name, "arguments": args},
		}); err == nil {
			b.Wire, b.WireFormat = wire, Dialect
		}
		blocks = append(blocks, b)
	}

	stop := core.StopEnd
	switch a.finish {
	case "tool_calls", "function_call":
		stop = core.StopToolUse
	case "length":
		stop = core.StopMaxTokens
	case "content_filter":
		stop = core.StopRefusal
	case "stop", "":
		stop = core.StopEnd
	default:
		stop = core.StopOther
	}
	if len(idxs) > 0 && stop == core.StopEnd {
		stop = core.StopToolUse
	}
	return core.Turn{Role: core.RoleAssistant, Blocks: blocks, Origin: core.OriginModel, Model: a.model}, stop
}

// readStream consumes an SSE body.
func readStream(body io.Reader, start time.Time, on func(provider.Event)) (*accumulator, error) {
	acc := newAccumulator()
	r := provider.NewSSEReader(body)
	for {
		ev, err := r.Next()
		if err == io.EOF {
			return acc, nil
		}
		if err != nil {
			return acc, &provider.Error{Kind: provider.ErrNetwork, Message: err.Error(), Err: err}
		}
		data := strings.TrimSpace(string(ev.Data))
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			return acc, nil
		}
		var c chunk
		if err := json.Unmarshal([]byte(data), &c); err != nil {
			continue // tolerate non-JSON keep-alive payloads
		}
		if c.Error != nil {
			return acc, mapInBandError(c.Error)
		}
		if c.Usage != nil {
			if raw, err := json.Marshal(c.Usage); err == nil {
				acc.rawUsage = raw
			}
		}
		acc.feed(&c, start, on)
	}
}
