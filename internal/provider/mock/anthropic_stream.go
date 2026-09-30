package mock

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/reee344/sleipnir/internal/core"
)

// respBlock is one content block of the mock's reply.
type respBlock struct {
	typ       string // text | thinking | tool_use | raw
	text      string // text, or the (possibly hidden) thinking text shown to the client
	hidden    string // thinking text withheld by OmitThinkingText
	signature string
	id, name  string
	args      string
	raw       json.RawMessage
}

// aOut is a composed reply.
type aOut struct {
	id, model string
	blocks    []respBlock
	stop      string
	refusal   bool
	usage     core.Usage
	plan      *ExplicitPlan
	xforms    []xform
	header    bool // the binding beta was sent: input_transformations is reported
}

func usageOf(plan *ExplicitPlan, out int) core.Usage {
	return core.Usage{
		InputTokens: plan.Uncached, CacheReadTokens: plan.Read,
		CacheWrite5mTokens: plan.Write5m, CacheWrite1hTokens: plan.Write1h, OutputTokens: out,
	}
}

// stopReason maps the chat mock's Finish values (and the API's own) onto a
// stop_reason.
func stopReason(finish string, tools bool) string {
	switch finish {
	case "":
		if tools {
			return "tool_use"
		}
		return "end_turn"
	case "stop":
		return "end_turn"
	case "tool_calls":
		return "tool_use"
	case "length":
		return "max_tokens"
	}
	return finish // end_turn, tool_use, max_tokens, stop_sequence, refusal, pause_turn, ...
}

func (a *AnthropicServer) compose(q *aReq, reply Reply, plan *ExplicitPlan, bind bindingOutcome, n int) *aOut {
	o := &aOut{id: "msg_mock_" + strconv.Itoa(n), model: q.model, plan: plan, xforms: bind.xforms, header: q.betas[betaBinding]}
	if q.maxTokens == 0 {
		// A pre-warm: the prefix is written, nothing is generated.
		o.stop = "max_tokens"
		o.usage = usageOf(plan, 0)
		return o
	}
	rec := q.recordsBefore()(len(q.msgs), 0)
	out := 0
	if len(reply.ReasoningDetails) > 0 {
		var raws []json.RawMessage
		if json.Unmarshal(reply.ReasoningDetails, &raws) == nil {
			for _, r := range raws {
				o.blocks = append(o.blocks, respBlock{typ: "raw", raw: r})
				out += tok(len(r) / 2)
			}
		}
	}
	if reply.Reasoning != "" {
		shown := reply.Reasoning
		if a.acfg.OmitThinkingText {
			shown = ""
		}
		o.blocks = append(o.blocks, respBlock{typ: "thinking", text: shown, hidden: reply.Reasoning, signature: a.sign(q.model, rec, shown)})
		out += tok(len(reply.Reasoning))
	}
	if reply.Text != "" {
		o.blocks = append(o.blocks, respBlock{typ: "text", text: reply.Text})
		out += tok(len(reply.Text))
	}
	for i, tc := range reply.ToolCalls {
		id := tc.ID
		if id == "" {
			id = fmt.Sprintf("toolu_mock_%d_%d", n, i)
		}
		o.blocks = append(o.blocks, respBlock{typ: "tool_use", id: id, name: tc.Name, args: tc.Args})
		out += tok(len(tc.Name) + len(tc.Args))
	}
	if reply.OutputTokens > 0 {
		out = reply.OutputTokens
	}
	if out == 0 {
		out = 1
	}
	o.stop = stopReason(reply.Finish, len(reply.ToolCalls) > 0)
	o.refusal = o.stop == "refusal"
	o.usage = usageOf(plan, out)
	return o
}

type cacheCreationJSON struct {
	E5m int `json:"ephemeral_5m_input_tokens"`
	E1h int `json:"ephemeral_1h_input_tokens"`
}

// usageJSON is the usage object in the API's field order.
type usageJSON struct {
	InputTokens              int               `json:"input_tokens"`
	CacheCreationInputTokens int               `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int               `json:"cache_read_input_tokens"`
	CacheCreation            cacheCreationJSON `json:"cache_creation"`
	OutputTokens             int               `json:"output_tokens"`
	ServiceTier              string            `json:"service_tier"`
}

func (o *aOut) usageJSON(outputTokens int) usageJSON {
	return usageJSON{
		InputTokens:              o.usage.InputTokens,
		CacheCreationInputTokens: o.usage.CacheWrite5mTokens + o.usage.CacheWrite1hTokens,
		CacheReadInputTokens:     o.usage.CacheReadTokens,
		CacheCreation:            cacheCreationJSON{E5m: o.usage.CacheWrite5mTokens, E1h: o.usage.CacheWrite1hTokens},
		OutputTokens:             outputTokens,
		ServiceTier:              "standard",
	}
}

// blockJSON renders a block as the API returns it whole.
func (b respBlock) blockJSON() any {
	switch b.typ {
	case "text":
		return map[string]any{"type": "text", "text": b.text}
	case "thinking":
		return map[string]any{"type": "thinking", "thinking": b.text, "signature": b.signature}
	case "tool_use":
		var in json.RawMessage = json.RawMessage(b.args)
		if !json.Valid(in) || len(bytes.TrimSpace(in)) == 0 || bytes.TrimSpace(in)[0] != '{' {
			in = json.RawMessage("{}")
		}
		return map[string]any{"type": "tool_use", "id": b.id, "name": b.name, "input": in}
	}
	return b.raw
}

func (o *aOut) stopDetails() any {
	if o.refusal {
		return map[string]any{"type": "refusal", "category": nil, "explanation": nil}
	}
	return nil
}

// message renders the Message object. final=false is the message_start form:
// empty content, no stop reason, one output token so far.
func (o *aOut) message(final bool) map[string]any {
	m := map[string]any{
		"id": o.id, "type": "message", "role": "assistant", "model": o.model,
		"content": []any{}, "stop_reason": nil, "stop_sequence": nil, "stop_details": nil,
	}
	if final {
		content := make([]any, 0, len(o.blocks))
		for _, b := range o.blocks {
			content = append(content, b.blockJSON())
		}
		m["content"] = content
		m["stop_reason"] = o.stop
		m["stop_details"] = o.stopDetails()
		m["usage"] = o.usageJSON(o.usage.OutputTokens)
	} else {
		m["usage"] = o.usageJSON(1)
	}
	if o.header {
		xs := make([]xform, 0, len(o.xforms))
		xs = append(xs, o.xforms...)
		m["input_transformations"] = xs
	}
	return m
}

// writeStream sends the reply as Anthropic's SSE events, in its order:
// message_start, ping, per block content_block_start / deltas / content_block_stop,
// message_delta, message_stop; a mid-stream fault becomes an in-band error event.
func (a *AnthropicServer) writeStream(w http.ResponseWriter, q *aReq, reply Reply, o *aOut) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	fl, _ := w.(http.Flusher)
	send := func(event string, v any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
		if fl != nil {
			fl.Flush()
		}
	}
	pause := func() { sleep(a.Server.cfg.DecodePer) }
	ping := func() {
		fmt.Fprint(w, "event: ping\ndata: {\"type\": \"ping\"}\n\n")
		if fl != nil {
			fl.Flush()
		}
	}

	send("message_start", map[string]any{"type": "message_start", "message": o.message(false)})
	ping()

	fault := reply.Fault != nil && reply.Fault.MidStream
	for i, b := range o.blocks {
		switch b.typ {
		case "text":
			send("content_block_start", map[string]any{"type": "content_block_start", "index": i, "content_block": map[string]any{"type": "text", "text": ""}})
			for k, piece := range pieces(b.text, 24) {
				send("content_block_delta", map[string]any{"type": "content_block_delta", "index": i, "delta": map[string]any{"type": "text_delta", "text": piece}})
				pause()
				if fault && k == 0 {
					a.midStreamError(send, reply.Fault)
					return
				}
			}
		case "thinking":
			send("content_block_start", map[string]any{"type": "content_block_start", "index": i, "content_block": map[string]any{"type": "thinking", "thinking": "", "signature": ""}})
			for _, piece := range pieces(b.text, 24) {
				send("content_block_delta", map[string]any{"type": "content_block_delta", "index": i, "delta": map[string]any{"type": "thinking_delta", "thinking": piece}})
				pause()
			}
			send("content_block_delta", map[string]any{"type": "content_block_delta", "index": i, "delta": map[string]any{"type": "signature_delta", "signature": b.signature}})
		case "tool_use":
			send("content_block_start", map[string]any{"type": "content_block_start", "index": i, "content_block": map[string]any{"type": "tool_use", "id": b.id, "name": b.name, "input": map[string]any{}}})
			// The API opens every tool_use with an empty partial_json delta.
			send("content_block_delta", map[string]any{"type": "content_block_delta", "index": i, "delta": map[string]any{"type": "input_json_delta", "partial_json": ""}})
			for k, piece := range pieces(b.args, 16) {
				send("content_block_delta", map[string]any{"type": "content_block_delta", "index": i, "delta": map[string]any{"type": "input_json_delta", "partial_json": piece}})
				pause()
				if fault && k == 0 {
					a.midStreamError(send, reply.Fault)
					return
				}
			}
		default:
			send("content_block_start", map[string]any{"type": "content_block_start", "index": i, "content_block": json.RawMessage(b.raw)})
		}
		send("content_block_stop", map[string]any{"type": "content_block_stop", "index": i})
		if i == 0 && len(o.blocks) > 1 {
			ping()
		}
	}
	if fault {
		a.midStreamError(send, reply.Fault)
		return
	}

	usage := any(o.usageJSON(o.usage.OutputTokens))
	if a.acfg.MinimalDeltaUsage {
		usage = map[string]any{"output_tokens": o.usage.OutputTokens}
	}
	delta := map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": o.stop, "stop_sequence": nil, "stop_details": o.stopDetails()},
		"usage": usage,
	}
	if o.header {
		delta["input_transformations"] = o.xforms
		if o.xforms == nil {
			delta["input_transformations"] = []xform{}
		}
	}
	send("message_delta", delta)
	send("message_stop", map[string]any{"type": "message_stop"})
}

func (a *AnthropicServer) midStreamError(send func(string, any), f *Fault) {
	msg := f.Message
	if msg == "" {
		msg = "Overloaded"
	}
	send("error", map[string]any{"type": "error", "error": map[string]any{"type": errType(f.Status), "message": msg}})
}
