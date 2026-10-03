package anthropic

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/provider"
)

// wireUsage is the Messages API usage object. Anthropic reports the uncached
// remainder as input_tokens; cache reads and writes are separate counters, so
// the prompt total is the sum of all three.
type wireUsage struct {
	InputTokens              tokens `json:"input_tokens"`
	CacheCreationInputTokens tokens `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     tokens `json:"cache_read_input_tokens"`
	CacheCreation            *struct {
		Ephemeral5m tokens `json:"ephemeral_5m_input_tokens"`
		Ephemeral1h tokens `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
	OutputTokens        tokens `json:"output_tokens"`
	OutputTokensDetails *struct {
		ThinkingTokens tokens `json:"thinking_tokens"`
	} `json:"output_tokens_details"`
}

// tokens is a token counter as the wire reports it. It decodes any JSON number (or
// a numeric string) and never fails: a report whose counter is negative,
// fractional, astronomical or not a number keeps its other members, because a
// report that fails to decode is a report dropped, and a dropped usage report reads
// as "free". normalize limits the value to 0..provider.MaxUsageTokens.
type tokens int

// UnmarshalJSON uses the shared tolerant token-count parser and never returns a decoding error.
func (t *tokens) UnmarshalJSON(b []byte) error {
	*t = tokens(provider.ParseTokenCount(b))
	return nil
}

// merge folds a later (cumulative) usage report into u. Streaming reports usage
// twice: message_start carries the input side, message_delta carries cumulative
// totals. Counters only grow, so taking the larger value is right for a proper
// report and also survives gateways whose message_delta repeats the input
// fields as zero.
func (u *wireUsage) merge(o wireUsage) {
	u.InputTokens = max(u.InputTokens, o.InputTokens)
	u.CacheCreationInputTokens = max(u.CacheCreationInputTokens, o.CacheCreationInputTokens)
	u.CacheReadInputTokens = max(u.CacheReadInputTokens, o.CacheReadInputTokens)
	u.OutputTokens = max(u.OutputTokens, o.OutputTokens)
	if o.OutputTokensDetails != nil {
		u.OutputTokensDetails = o.OutputTokensDetails
	}
	if o.CacheCreation != nil {
		if u.CacheCreation == nil || o.CacheCreation.Ephemeral5m+o.CacheCreation.Ephemeral1h >= u.CacheCreation.Ephemeral5m+u.CacheCreation.Ephemeral1h {
			u.CacheCreation = o.CacheCreation
		}
	}
}

// normalize converts to core.Usage: uncached input, cache read, cache write per
// TTL, output. Every counter is clamped to 0..provider.MaxUsageTokens (a negative
// or absurd one, from a buggy or hostile gateway, must not reach the agent's or the
// swarm's spend), and the reasoning share cannot exceed the output.
func (u wireUsage) normalize() core.Usage {
	pos := func(n tokens) int { return provider.ClampTokens(int(n)) }
	total := pos(u.CacheCreationInputTokens)
	w5, w1 := total, 0 // no TTL split reported: the default lifetime is 5m
	if u.CacheCreation != nil {
		w5, w1 = pos(u.CacheCreation.Ephemeral5m), pos(u.CacheCreation.Ephemeral1h)
		if rest := total - w5 - w1; rest > 0 {
			w5 += rest // an unexplained remainder is billed at the default lifetime
		}
	}
	out := core.Usage{
		InputTokens:        pos(u.InputTokens),
		CacheReadTokens:    pos(u.CacheReadInputTokens),
		CacheWrite5mTokens: w5,
		CacheWrite1hTokens: w1,
		OutputTokens:       pos(u.OutputTokens),
	}
	if u.OutputTokensDetails != nil {
		out.ReasoningTokens = min(pos(u.OutputTokensDetails.ThinkingTokens), out.OutputTokens)
	}
	return out
}

// checkResultLimits applies the response limits to a message that arrived whole (a
// non-streaming body): the streaming path counts as it reads, this counts what was
// parsed.
func checkResultLimits(r *result, lim provider.StreamLimits) error {
	lim = lim.Normalized()
	if len(r.blocks) > lim.MaxBlocks {
		return provider.LimitExceededCount("content blocks", lim.MaxBlocks)
	}
	text, think, tools := 0, 0, 0
	for _, b := range r.blocks {
		switch b.Kind {
		case core.BlockText:
			text += len(b.Text)
		case core.BlockThinking, core.BlockRedactedThinking:
			think += len(b.Text) + len(b.Wire)
		case core.BlockToolUse:
			if tools++; tools > lim.MaxToolCalls {
				return provider.LimitExceededCount("tool calls", lim.MaxToolCalls)
			}
			if len(b.Input) > lim.MaxToolArgBytes {
				return provider.LimitExceeded("tool call arguments", int64(lim.MaxToolArgBytes))
			}
		}
	}
	switch {
	case text > lim.MaxTextBytes:
		return provider.LimitExceeded("answer text", int64(lim.MaxTextBytes))
	case think > lim.MaxTextBytes:
		return provider.LimitExceeded("reasoning text", int64(lim.MaxTextBytes))
	}
	return nil
}

// wireTransformation is one input_transformations entry: something the API did
// to the request that the caller did not ask for.
type wireTransformation struct {
	Type   string `json:"type"`
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// extras are the response members that describe what the API did with the
// request rather than what the model said.
type extras struct {
	InputTransformations []wireTransformation `json:"input_transformations"`
	ContextManagement    *struct {
		AppliedEdits []json.RawMessage `json:"applied_edits"`
	} `json:"context_management"`
	Diagnostics *struct {
		CacheMissReason *struct {
			Type                   string `json:"type"`
			CacheMissedInputTokens int    `json:"cache_missed_input_tokens"`
		} `json:"cache_miss_reason"`
	} `json:"diagnostics"`
}

// transformations lists what the API did to the request. Thinking blocks it
// dropped are the important entry: they cost reasoning and change the cached
// prefix from that point on. Entries are de-duplicated because a stream repeats
// them on the final message_delta after a mid-stream fallback.
func (e extras) transformations() []provider.Transformation {
	var out []provider.Transformation
	seen := map[provider.Transformation]bool{}
	add := func(t provider.Transformation) {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	for _, t := range e.InputTransformations {
		add(provider.Transformation{Type: t.Type, Path: t.Path, Reason: t.Reason})
	}
	if e.ContextManagement != nil {
		for _, raw := range e.ContextManagement.AppliedEdits {
			var head struct {
				Type string `json:"type"`
			}
			_ = json.Unmarshal(raw, &head)
			var compact bytes.Buffer
			if json.Compact(&compact, raw) != nil {
				compact.Reset()
			}
			add(provider.Transformation{Type: "context_edit", Path: head.Type, Reason: compact.String()})
		}
	}
	if e.Diagnostics != nil && e.Diagnostics.CacheMissReason != nil {
		d := e.Diagnostics.CacheMissReason
		add(provider.Transformation{Type: "cache_miss", Reason: fmt.Sprintf("%s (%d input tokens)", d.Type, d.CacheMissedInputTokens)})
	}
	return out
}

// result is a finished message, however it arrived.
type result struct {
	id, model  string
	blocks     []core.Block
	stop       core.StopReason
	stopDetail string
	usage      wireUsage
	rawUsage   json.RawMessage
	transforms []provider.Transformation
}

// stopReason maps the API's stop_reason onto core's.
func stopReason(reason, sequence string, details json.RawMessage, blocks []core.Block) (core.StopReason, string) {
	switch reason {
	case "end_turn":
		return core.StopEnd, ""
	case "stop_sequence":
		return core.StopEnd, sequence
	case "tool_use":
		return core.StopToolUse, ""
	case "max_tokens":
		return core.StopMaxTokens, ""
	case "pause_turn":
		return core.StopPause, ""
	case "refusal":
		return core.StopRefusal, refusalDetail(details)
	case "model_context_window_exceeded", "compaction":
		// Output was cut by a limit that a larger max_tokens does not fix; the
		// reason stays visible in StopDetail rather than pretending to be either
		// a normal end or a max_tokens stop.
		return core.StopOther, reason
	case "":
		for _, b := range blocks {
			if b.Kind == core.BlockToolUse {
				return core.StopToolUse, ""
			}
		}
		return core.StopEnd, ""
	}
	return core.StopOther, reason
}

// refusalDetail renders stop_details compactly: "cyber: explanation". Both
// members can be null on a refusal, so it may be empty.
func refusalDetail(raw json.RawMessage) string {
	var d struct {
		Category    *string `json:"category"`
		Explanation *string `json:"explanation"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &d) != nil {
		return ""
	}
	var parts []string
	if d.Category != nil && *d.Category != "" {
		parts = append(parts, *d.Category)
	}
	if d.Explanation != nil && *d.Explanation != "" {
		parts = append(parts, *d.Explanation)
	}
	return strings.Join(parts, ": ")
}

// blockFromRaw converts one complete content block, as the API sent it, into a
// core.Block. Anything that may have to be sent back (thinking, redacted
// thinking, tool_use, and block types this adapter does not know) keeps the raw
// JSON in Wire, because replaying a rebuilt block is how signatures break.
func blockFromRaw(raw json.RawMessage) (core.Block, bool) {
	var head struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &head) != nil || head.Type == "" {
		return core.Block{}, false
	}
	wire := append(json.RawMessage(nil), raw...)
	switch head.Type {
	case "text":
		var t struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(raw, &t)
		if t.Text == "" {
			return core.Block{}, false // an empty text block carries nothing and cannot be replayed
		}
		return core.Text(t.Text), true // citations are ignored: the text is what matters
	case "thinking":
		var t struct {
			Thinking string `json:"thinking"`
		}
		_ = json.Unmarshal(raw, &t)
		// Kept even when the text is empty: on models that hide their reasoning
		// the whole reasoning lives in the signature, and dropping "empty"
		// blocks deletes it.
		return core.Block{Kind: core.BlockThinking, Text: t.Thinking, Wire: wire, WireFormat: Dialect}, true
	case "redacted_thinking":
		return core.Block{Kind: core.BlockRedactedThinking, Wire: wire, WireFormat: Dialect}, true
	case "tool_use":
		var t struct {
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		}
		_ = json.Unmarshal(raw, &t)
		blk := core.Block{Kind: core.BlockToolUse, ToolID: t.ID, ToolName: t.Name, Wire: wire, WireFormat: Dialect}
		in := bytes.TrimSpace(t.Input)
		switch {
		case len(in) == 0:
			blk.Input = json.RawMessage("{}")
		case isJSONObject(in):
			blk.Input = append(json.RawMessage(nil), in...)
		default:
			blk.Input = quoted(string(in))
			blk.Invalid = "tool input is not a JSON object"
		}
		return blk, true
	case "compaction":
		return core.Block{Kind: core.BlockCompaction, Wire: wire, WireFormat: Dialect}, true
	}
	return core.Block{Kind: core.BlockKind(head.Type), Wire: wire, WireFormat: Dialect}, true
}

// quoted returns an independent JSON string encoding using the Anthropic wire writer.
func quoted(s string) json.RawMessage {
	var w jw
	w.str(s)
	return append(json.RawMessage(nil), w.bytes()...)
}

// parseMessage decodes a non-streaming response body.
func parseMessage(body []byte) (*result, error) {
	var m struct {
		Type        string            `json:"type"`
		ID          string            `json:"id"`
		Model       string            `json:"model"`
		Content     []json.RawMessage `json:"content"`
		StopReason  string            `json:"stop_reason"`
		StopSeq     *string           `json:"stop_sequence"`
		StopDetails json.RawMessage   `json:"stop_details"`
		Usage       json.RawMessage   `json:"usage"`
		extras
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}
	r := &result{id: m.ID, model: m.Model, transforms: m.extras.transformations()}
	for _, raw := range m.Content {
		if blk, ok := blockFromRaw(raw); ok {
			r.blocks = append(r.blocks, blk)
		}
	}
	if len(m.Usage) > 0 && string(m.Usage) != "null" {
		_ = json.Unmarshal(m.Usage, &r.usage)
		r.rawUsage = append(json.RawMessage(nil), m.Usage...)
	}
	seq := ""
	if m.StopSeq != nil {
		seq = *m.StopSeq
	}
	r.stop, r.stopDetail = stopReason(m.StopReason, seq, m.StopDetails, r.blocks)
	return r, nil
}

// mergeRawUsage overlays later usage members onto earlier ones, member by
// member, keeping every value exactly as the API sent it. Keys are sorted so the
// audit record is deterministic.
func mergeRawUsage(dst map[string]json.RawMessage, raw json.RawMessage) {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return
	}
	for k, v := range m {
		dst[k] = append(json.RawMessage(nil), v...)
	}
}

// encodeRawUsage writes raw usage fields in sorted key order and returns independent bytes, or nil
// for no fields.
func encodeRawUsage(m map[string]json.RawMessage) json.RawMessage {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var w jw
	w.putc('{')
	for i, k := range keys {
		w.member(i == 0, k)
		w.raw(m[k])
	}
	w.putc('}')
	return append(json.RawMessage(nil), w.bytes()...)
}
