package openairesp

import (
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/provider"
)

// tokens is a token counter as the wire reports it: any JSON number (or a numeric string), never a failure, because a frame that fails
// to decode is a frame dropped, and a dropped usage report reads as "free". It is limited again in usage.normalize.
type tokens int

// UnmarshalJSON uses the shared tolerant token-count parser and never returns a decoding error.
func (t *tokens) UnmarshalJSON(b []byte) error {
	*t = tokens(provider.ParseTokenCount(b))
	return nil
}

type usage struct {
	InputTokens        tokens `json:"input_tokens"`
	OutputTokens       tokens `json:"output_tokens"`
	TotalTokens        tokens `json:"total_tokens"`
	InputTokensDetails *struct {
		CachedTokens     tokens `json:"cached_tokens"`
		CacheWriteTokens tokens `json:"cache_write_tokens"`
	} `json:"input_tokens_details"`
	OutputTokensDetails *struct {
		ReasoningTokens tokens `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

// UnmarshalJSON decodes leniently: whatever the wire holds, the members that made sense are kept.
func (u *usage) UnmarshalJSON(b []byte) error {
	type plain usage // no methods: no recursion
	_ = json.Unmarshal(b, (*plain)(u))
	return nil
}

// normalize converts to the harness's accounting: Responses reports the whole prompt and the cached part of it.
func (u *usage) normalize() core.Usage {
	if u == nil {
		return core.Usage{}
	}
	prompt := provider.ClampTokens(int(u.InputTokens))
	cached := 0
	written := 0
	if u.InputTokensDetails != nil {
		cached = min(provider.ClampTokens(int(u.InputTokensDetails.CachedTokens)), prompt)
		written = min(provider.ClampTokens(int(u.InputTokensDetails.CacheWriteTokens)), prompt-cached)
	}
	// Cache writes are a separate input category, not an additive charge.
	// The legacy 5m bucket holds the adapter's base write rate, independent of TTL.
	out := core.Usage{InputTokens: prompt - cached - written, CacheReadTokens: cached, CacheWrite5mTokens: written, OutputTokens: provider.ClampTokens(int(u.OutputTokens))}
	if u.OutputTokensDetails != nil {
		out.ReasoningTokens = min(provider.ClampTokens(int(u.OutputTokensDetails.ReasoningTokens)), out.OutputTokens)
	}
	return out
}

// response is the Responses object, as the completed event (or a body that was not streamed) carries it.
type response struct {
	ID                string            `json:"id"`
	Model             string            `json:"model"`
	Status            string            `json:"status"`
	Output            []json.RawMessage `json:"output"`
	Usage             *usage            `json:"usage"`
	Error             *apiError         `json:"error"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
}

// apiError is an error as the API spells it, inside a response and in an event of its own.
type apiError struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// event is one streamed frame. The type is in the data as well as in the SSE event line, and is what is switched on.
type event struct {
	Type        string          `json:"type"`
	OutputIndex int             `json:"output_index"`
	Delta       string          `json:"delta"`
	Item        json.RawMessage `json:"item"`
	Response    *response       `json:"response"`
	// An error event carries the error members at its top level, or one level down.
	Code    string    `json:"code"`
	Message string    `json:"message"`
	Error   *apiError `json:"error"`
}

// item is the part of an output item that the harness reads; the rest is kept as it came.
type outputItem struct {
	Type             string `json:"type"`
	EncryptedContent string `json:"encrypted_content"`
	Summary          []struct {
		Text string `json:"text"`
	} `json:"summary"`
	Content []struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Refusal string `json:"refusal"`
	} `json:"content"`
	Name      string `json:"name"`
	CallID    string `json:"call_id"`
	Arguments string `json:"arguments"`
}

// accumulator folds the frames of a response into a finished assistant turn. It enforces the response limits as it goes, so a runaway or
// hostile reply stops at the limit instead of at the end of memory.
type accumulator struct {
	id, model string
	items     map[int]json.RawMessage // the finished output items, by their place in the output
	calls     int                     // function calls begun
	textBytes int
	argBytes  map[int]int
	thinkSize int

	started bool
	ttfb    time.Duration
	done    bool // the endpoint said the response is over: completed, incomplete, or failed

	usage      *usage
	rawUsage   json.RawMessage
	incomplete string
	refused    bool

	lim provider.StreamLimits
}

// newAccumulator allocates response item and argument-size tracking with normalized stream limits.
func newAccumulator(lim provider.StreamLimits) *accumulator {
	return &accumulator{items: map[int]json.RawMessage{}, argBytes: map[int]int{}, lim: lim.Normalized()}
}

// idText bounds and cleans an identifier the server chose: they are logged and shown, never interpreted.
func idText(s string) string { return provider.SanitizeText(s, 256) }

// start announces the first frame once: the swarm's warm gate waits for it.
func (a *accumulator) start(begin time.Time, on func(provider.Event)) {
	if a.started {
		return
	}
	a.started = true
	a.ttfb = time.Since(begin)
	on(provider.Event{Kind: provider.EvStart, RequestID: a.id, Elapsed: a.ttfb})
}

// feed applies one frame and emits streaming events. It returns a provider error for a failure the endpoint reports, and when the
// response outgrows a limit.
func (a *accumulator) feed(e *event, begin time.Time, on func(provider.Event)) error {
	// Queue/acceptance events arrive before prefill has finished. Releasing the
	// warm gate here sends followers onto a cache the primer has not written yet.
	switch e.Type {
	case "response.output_text.delta", "response.refusal.delta", "response.reasoning_summary_text.delta", "response.reasoning_text.delta",
		"response.function_call_arguments.delta", "response.output_item.done", "response.completed", "response.incomplete":
		a.start(begin, on)
	}
	switch e.Type {
	case "response.created", "response.in_progress", "response.queued":
		a.note(e.Response)
	case "response.output_item.added":
		var it outputItem
		if json.Unmarshal(e.Item, &it) == nil && it.Type == "function_call" {
			a.calls++
			if a.calls > a.lim.MaxToolCalls {
				return provider.LimitExceededCount("tool calls", a.lim.MaxToolCalls)
			}
			on(provider.Event{Kind: provider.EvToolStart, Index: e.OutputIndex, ToolID: idText(it.CallID), ToolName: idText(it.Name)})
		}
	case "response.output_text.delta", "response.refusal.delta":
		if a.textBytes+len(e.Delta) > a.lim.MaxTextBytes {
			return provider.LimitExceeded("answer text", int64(a.lim.MaxTextBytes))
		}
		a.textBytes += len(e.Delta)
		on(provider.Event{Kind: provider.EvText, Text: e.Delta})
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		if a.thinkSize+len(e.Delta) > a.lim.MaxTextBytes {
			return provider.LimitExceeded("reasoning text", int64(a.lim.MaxTextBytes))
		}
		a.thinkSize += len(e.Delta)
		on(provider.Event{Kind: provider.EvThinking, Text: e.Delta})
	case "response.function_call_arguments.delta":
		if a.argBytes[e.OutputIndex]+len(e.Delta) > a.lim.MaxToolArgBytes {
			return provider.LimitExceeded("tool call arguments", int64(a.lim.MaxToolArgBytes))
		}
		a.argBytes[e.OutputIndex] += len(e.Delta)
		on(provider.Event{Kind: provider.EvToolDelta, Index: e.OutputIndex, Text: e.Delta})
	case "response.output_item.done":
		if len(e.Item) == 0 {
			break
		}
		if _, ok := a.items[e.OutputIndex]; !ok && len(a.items) >= a.lim.MaxBlocks {
			return provider.LimitExceededCount("output items", a.lim.MaxBlocks)
		}
		a.items[e.OutputIndex] = e.Item
	case "response.completed", "response.incomplete":
		a.done = true
		a.note(e.Response)
		if e.Response != nil {
			if e.Response.Usage != nil {
				a.usage = e.Response.Usage
			}
			if d := e.Response.IncompleteDetails; d != nil {
				a.incomplete = d.Reason
			}
			// A body that carries the items only here (an endpoint that does not send them one by one) is read from here.
			if len(a.items) == 0 {
				for i, raw := range e.Response.Output {
					if i >= a.lim.MaxBlocks {
						return provider.LimitExceededCount("output items", a.lim.MaxBlocks)
					}
					a.items[i] = raw
				}
			}
		}
	case "response.failed":
		a.done = true
		if e.Response != nil && e.Response.Error != nil {
			return mapAPIError(e.Response.Error)
		}
		return &provider.Error{Kind: provider.ErrServer, Message: "the response failed, and the endpoint did not say why"}
	case "error":
		er := e.Error
		if er == nil {
			er = &apiError{Code: e.Code, Message: e.Message}
		}
		return mapAPIError(er)
	}
	return nil
}

// note updates response identity and model from nonempty sanitized metadata, ignoring nil
// responses.
func (a *accumulator) note(r *response) {
	if r == nil {
		return
	}
	if r.ID != "" {
		a.id = idText(r.ID)
	}
	if r.Model != "" {
		a.model = idText(r.Model)
	}
}

// build makes the assistant turn from the output items, in the order the model produced them.
func (a *accumulator) build() (core.Turn, core.StopReason) {
	idxs := make([]int, 0, len(a.items))
	for i := range a.items {
		idxs = append(idxs, i)
	}
	sort.Ints(idxs)
	var blocks []core.Block
	calls := 0
	for _, i := range idxs {
		raw := a.items[i]
		var it outputItem
		if json.Unmarshal(raw, &it) != nil {
			continue
		}
		wire := append(json.RawMessage(nil), raw...)
		switch it.Type {
		case "reasoning":
			var sb strings.Builder
			for j, s := range it.Summary {
				if j > 0 {
					sb.WriteString("\n\n")
				}
				sb.WriteString(s.Text)
			}
			b := core.Block{Kind: core.BlockThinking, Text: sb.String()}
			if it.EncryptedContent != "" { // only this can go back: the endpoint is not keeping the conversation
				b.Wire, b.WireFormat = wire, Dialect
			}
			if b.Text != "" || len(b.Wire) > 0 {
				blocks = append(blocks, b)
			}
		case "message":
			var sb strings.Builder
			for _, c := range it.Content {
				switch c.Type {
				case "output_text":
					sb.WriteString(c.Text)
				case "refusal":
					sb.WriteString(c.Refusal)
					a.refused = true
				}
			}
			if sb.Len() > 0 {
				blocks = append(blocks, core.Block{Kind: core.BlockText, Text: sb.String(), Wire: wire, WireFormat: Dialect})
			}
		case "function_call":
			args := it.Arguments
			if strings.TrimSpace(args) == "" {
				args = "{}"
			}
			id := idText(it.CallID)
			if id == "" {
				id = fallbackToolID(it.Name, args, calls)
			}
			b := core.Block{Kind: core.BlockToolUse, ToolID: id, ToolName: it.Name, Wire: wire, WireFormat: Dialect}
			if json.Valid([]byte(args)) {
				b.Input = json.RawMessage(args)
			} else {
				// Truncated or malformed arguments: keep the text so the harness can tell the model exactly what it produced.
				q, _ := core.MarshalStable(args)
				b.Input = q
				b.Invalid = "arguments are not valid JSON"
			}
			calls++
			blocks = append(blocks, b)
		}
	}
	stop := core.StopEnd
	switch {
	case a.refused || a.incomplete == "content_filter":
		stop = core.StopRefusal
	case a.incomplete == "max_output_tokens":
		stop = core.StopMaxTokens
	case a.incomplete != "":
		stop = core.StopOther
	case calls > 0:
		stop = core.StopToolUse
	}
	return core.Turn{Role: core.RoleAssistant, Blocks: blocks, Origin: core.OriginModel, Model: a.model}, stop
}

// errStreamCut is what a body that ends before the endpoint said the response was over is.
const errStreamCut = "the stream ended before the response was completed: the connection was cut"

// readStream consumes an SSE body. A response that outgrows a limit ends with a provider error (see provider.LimitExceeded); the caller
// cancels the request.
//
// A stream is complete when the endpoint says so: response.completed, response.incomplete, or an error. A body that just ends is a
// connection that was cut, and taking it for a short answer would end the agent's task on half a sentence, or on nothing at all: an
// answer with no tool calls is the end of a run.
func readStream(body io.Reader, begin time.Time, on func(provider.Event), lim provider.StreamLimits) (*accumulator, error) {
	acc := newAccumulator(lim)
	r := provider.NewSSEReaderLimits(body, lim)
	for {
		ev, err := r.Next()
		if err == io.EOF {
			if !acc.done {
				return acc, &provider.Error{Kind: provider.ErrNetwork, Message: errStreamCut}
			}
			return acc, nil
		}
		if err != nil {
			if pe, ok := provider.AsError(err); ok {
				return acc, pe // a limit
			}
			return acc, &provider.Error{Kind: provider.ErrNetwork, Message: provider.SanitizeText(err.Error(), 0), Err: err}
		}
		data := strings.TrimSpace(string(ev.Data))
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			if !acc.done {
				return acc, &provider.Error{Kind: provider.ErrNetwork, Message: errStreamCut}
			}
			return acc, nil
		}
		var e event
		if err := json.Unmarshal([]byte(data), &e); err != nil {
			var bad *json.UnmarshalTypeError
			if !errors.As(err, &bad) {
				if ev.Event == "error" {
					return acc, &provider.Error{Kind: provider.ErrServer, Message: provider.SanitizeText("error event: "+data, 0), Raw: provider.CapRaw([]byte(data))}
				}
				continue // tolerate non-JSON keep-alive payloads
			}
			// Valid JSON with a member of the wrong type: encoding/json has decoded everything else.
		}
		if e.Type == "" {
			e.Type = ev.Event
		}
		if e.Type == "response.completed" || e.Type == "response.incomplete" {
			acc.rawUsage = rawUsageOf([]byte(data))
		}
		if err := acc.feed(&e, begin, on); err != nil {
			return acc, err
		}
		if acc.done {
			return acc, nil
		}
	}
}

// rawUsageOf extracts the usage object of a completed event verbatim, for the audit record; one too large to keep is replaced by a marker.
func rawUsageOf(body []byte) json.RawMessage {
	var r struct {
		Response struct {
			Usage json.RawMessage `json:"usage"`
		} `json:"response"`
		Usage json.RawMessage `json:"usage"`
	}
	if json.Unmarshal(body, &r) != nil {
		return nil
	}
	u := r.Response.Usage
	if len(u) == 0 {
		u = r.Usage
	}
	if len(u) == 0 || string(u) == "null" {
		return nil
	}
	if len(u) > 4096 {
		return json.RawMessage(`{"truncated":true}`)
	}
	return append(json.RawMessage(nil), u...)
}

// mapAPIError classifies an error the API reported inside a response or an event.
func mapAPIError(e *apiError) *provider.Error {
	msg := provider.SanitizeText(e.Message, provider.MaxErrorText)
	if msg == "" {
		msg = "the endpoint sent an error with no message"
	}
	pe := &provider.Error{Message: msg, Raw: provider.CapRaw(mustJSON(e))}
	code := strings.ToLower(e.Code + " " + e.Type)
	switch {
	case strings.Contains(code, "context_length"):
		pe.Kind = provider.ErrContextLength
	case strings.Contains(code, "usage_limit"), strings.Contains(code, "insufficient_quota"), strings.Contains(code, "billing"):
		pe.Kind = provider.ErrPayment
	case strings.Contains(code, "rate_limit"):
		pe.Kind = provider.ErrRateLimit
	case strings.Contains(code, "overloaded"), strings.Contains(code, "server_is_overloaded"), strings.Contains(code, "slow_down"):
		pe.Kind = provider.ErrOverloaded
	case strings.Contains(code, "invalid"), strings.Contains(code, "not_found"), strings.Contains(code, "unsupported"):
		pe.Kind = provider.ErrBadRequest
	case strings.Contains(code, "auth"), strings.Contains(code, "permission"):
		pe.Kind = provider.ErrAuth
	default:
		pe.Kind = provider.ErrServer
	}
	return pe
}

// mustJSON encodes a value known to be JSON-marshalable; encoding failures produce nil bytes.
func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
