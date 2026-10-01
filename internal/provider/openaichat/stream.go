package openaichat

import (
	"bytes"
	"encoding/json"
	"errors"
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
	ID             string        `json:"id"`
	Model          string        `json:"model"`
	Provider       string        `json:"provider"`
	Choices        []chunkChoice `json:"choices"`
	PromptTokenIDs []int32       `json:"prompt_token_ids"`
	Usage          *usage        `json:"usage"`
	// Error is kept raw: endpoints spell it as an object whose message is text, an
	// object whose message is not, and a bare string, and a frame that carries an
	// error must never be lost to its shape (see errorFrame).
	Error json.RawMessage `json:"error"`
}

// chunkChoice is one choice of a frame.
type chunkChoice struct {
	Index        int     `json:"index"`
	Delta        *delta  `json:"delta"`
	Message      *delta  `json:"message"`
	FinishReason *string `json:"finish_reason"`
	// vLLM-style token capture: ids of the tokens in this frame and their
	// sampling logprobs.
	TokenIDs []int32 `json:"token_ids"`
	Logprobs *struct {
		Content []struct {
			Logprob float64 `json:"logprob"`
		} `json:"content"`
	} `json:"logprobs"`
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

// tokens is a token counter as the wire reports it. It decodes any JSON number (or
// a numeric string) and never fails: a frame whose counter is negative, fractional,
// astronomical or not a number at all keeps its other fields, because a frame that
// fails to decode is a frame dropped, and a dropped usage report reads as "free".
// The value is limited to 0..MaxUsageTokens again in usage.normalize.
type tokens int

func (t *tokens) UnmarshalJSON(b []byte) error {
	*t = tokens(provider.ParseTokenCount(b))
	return nil
}

type usage struct {
	PromptTokens        tokens `json:"prompt_tokens"`
	CompletionTokens    tokens `json:"completion_tokens"`
	TotalTokens         tokens `json:"total_tokens"`
	PromptTokensDetails *struct {
		CachedTokens     tokens `json:"cached_tokens"`
		CacheWriteTokens tokens `json:"cache_write_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails *struct {
		ReasoningTokens tokens `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
	// DeepSeek-native names, passed through by some gateways.
	PromptCacheHitTokens  *tokens  `json:"prompt_cache_hit_tokens"`
	PromptCacheMissTokens *tokens  `json:"prompt_cache_miss_tokens"`
	Cost                  *float64 `json:"cost"`
}

// UnmarshalJSON decodes leniently: whatever the wire holds, the frame survives
// and the members that made sense are kept. Cost is only what parseCost accepts.
func (u *usage) UnmarshalJSON(b []byte) error {
	type plain usage // no methods: no recursion
	_ = json.Unmarshal(b, (*plain)(u))
	var c struct {
		Cost json.RawMessage `json:"cost"`
	}
	_ = json.Unmarshal(b, &c)
	u.Cost = provider.ParseCost(c.Cost)
	return nil
}

type apiError struct {
	Code     json.RawMessage `json:"code"`
	Message  string          `json:"message"`
	Metadata json.RawMessage `json:"metadata"`
}

// errorFrame reads the error member of a frame, or returns nil when there is none.
// Endpoints spell it in more ways than the one the API documents:
//
//	{"error":{"code":429,"message":"slow down"}}         the documented object
//	{"error":{"code":500,"message":{"detail":"..."}}}    a message that is not text
//	{"error":"upstream timed out"}                        a bare string
//
// All of them are errors. A member that is null, empty or some other JSON value is
// not one (servers that always send "error":null exist).
func errorFrame(raw json.RawMessage) *apiError {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil
	}
	switch raw[0] {
	case '"':
		var s string
		if json.Unmarshal(raw, &s) != nil || strings.TrimSpace(s) == "" {
			return nil
		}
		return &apiError{Message: s}
	case '{':
		var e struct {
			Code     json.RawMessage `json:"code"`
			Message  json.RawMessage `json:"message"`
			Metadata json.RawMessage `json:"metadata"`
		}
		_ = json.Unmarshal(raw, &e) // a member of the wrong type leaves the others readable
		return &apiError{Code: e.Code, Message: messageText(e.Message), Metadata: e.Metadata}
	}
	return nil
}

// messageText is an error message as text: a string as it is, anything else (an
// object with the details, an array) as its compact JSON.
func messageText(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	switch {
	case len(raw) == 0, string(raw) == "null":
		return ""
	case raw[0] == '"':
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
	}
	var b bytes.Buffer
	if json.Compact(&b, raw) != nil {
		return ""
	}
	return b.String()
}

// normalize converts the wire usage to core.Usage. Every counter is clamped to
// 0..MaxUsageTokens (a negative or absurd one must not reach the agent's or the
// swarm's spend), the reasoning share cannot exceed the output, and a cost that
// cannot be the charge of one request (NaN, infinite, negative, above
// MaxRequestCostUSD) is dropped from u itself, so nothing that reads u.Cost
// afterwards sees it: the request is then priced from its token counts.
func (u *usage) normalize() core.Usage {
	if u == nil {
		return core.Usage{}
	}
	u.Cost = provider.ValidCost(u.Cost)
	clamp := func(t tokens) int { return provider.ClampTokens(int(t)) }
	prompt := clamp(u.PromptTokens)
	cached, wrote := 0, 0
	if u.PromptTokensDetails != nil {
		cached = clamp(u.PromptTokensDetails.CachedTokens)
		wrote = clamp(u.PromptTokensDetails.CacheWriteTokens)
	} else if u.PromptCacheHitTokens != nil {
		cached = clamp(*u.PromptCacheHitTokens)
	}
	in := prompt - cached - wrote
	if in < 0 {
		in = 0
	}
	out := core.Usage{
		InputTokens:        in,
		CacheReadTokens:    cached,
		CacheWrite5mTokens: wrote,
		OutputTokens:       clamp(u.CompletionTokens),
	}
	if u.CompletionTokensDetails != nil {
		out.ReasoningTokens = min(clamp(u.CompletionTokensDetails.ReasoningTokens), out.OutputTokens)
	}
	return out
}

// detailAcc folds the deltas of one reasoning_details item. The text members are
// built incrementally: concatenating strings per delta would be quadratic, and
// the deltas of a hostile stream are as small as it likes.
type detailAcc struct {
	item                         reasoningItem
	text, summary, data          strings.Builder
	hasText, hasSummary, hasData bool
}

func (d *detailAcc) add(it reasoningItem) {
	if it.Text != nil {
		d.text.WriteString(*it.Text)
		d.hasText = true
	}
	if it.Summary != nil {
		d.summary.WriteString(*it.Summary)
		d.hasSummary = true
	}
	if it.Data != nil {
		d.data.WriteString(*it.Data)
		d.hasData = true
	}
}

// result is the item as it goes back to the endpoint: same entries, same order,
// same fields.
func (d *detailAcc) result() *reasoningItem {
	it := d.item
	it.Text, it.Summary, it.Data = nil, nil, nil
	if d.hasText {
		s := d.text.String()
		it.Text = &s
	}
	if d.hasSummary {
		s := d.summary.String()
		it.Summary = &s
	}
	if d.hasData {
		s := d.data.String()
		it.Data = &s
	}
	return &it
}

// accumulator folds streamed deltas into a finished assistant turn. It enforces
// the response limits as it goes, so a runaway or hostile reply stops at the limit
// instead of at the end of memory.
type accumulator struct {
	id, model, provider string
	text                strings.Builder
	reasoning           strings.Builder
	details             map[int]*detailAcc
	detailOrder         []int
	calls               map[int]*callAcc
	finish              string
	usage               *usage
	rawUsage            json.RawMessage
	started             bool
	ttfb                time.Duration // when the first frame with content came, from the start of the request
	textOpen            bool
	leakChecked         bool // splitLeakedThinking has run or does not apply

	promptIDs []int32
	compIDs   []int32
	logprobs  []float32

	lim        provider.StreamLimits
	thinkBytes int // reasoning text plus reasoning_details payloads
}

type callAcc struct {
	id, name string
	args     strings.Builder
}

func newAccumulator() *accumulator { return newAccumulatorLimits(provider.StreamLimits{}) }

func newAccumulatorLimits(lim provider.StreamLimits) *accumulator {
	return &accumulator{details: map[int]*detailAcc{}, calls: map[int]*callAcc{}, lim: lim.Normalized()}
}

// idText bounds and cleans an identifier the server chose (response id, model,
// upstream provider): they are logged and shown, never interpreted.
func idText(s string) string { return provider.SanitizeText(s, 256) }

// feed applies one chunk and emits streaming events. It returns a provider error
// when the response outgrows a limit.
func (a *accumulator) feed(c *chunk, start time.Time, on func(provider.Event)) error {
	if c.ID != "" {
		a.id = idText(c.ID)
	}
	if c.Model != "" {
		a.model = idText(c.Model)
	}
	if c.Provider != "" {
		a.provider = idText(c.Provider)
	}
	if c.Usage != nil {
		a.usage = c.Usage
	}
	if len(c.PromptTokenIDs) > 0 {
		a.promptIDs = c.PromptTokenIDs
	}
	for _, ch := range c.Choices {
		a.compIDs = append(a.compIDs, ch.TokenIDs...)
		if ch.Logprobs != nil {
			for _, lp := range ch.Logprobs.Content {
				a.logprobs = append(a.logprobs, float32(lp.Logprob))
			}
		}
		d := ch.Delta
		if d == nil {
			d = ch.Message
		}
		if d != nil {
			if !a.started {
				a.started = true
				a.ttfb = time.Since(start)
				on(provider.Event{Kind: provider.EvStart, RequestID: a.id, Elapsed: a.ttfb})
			}
			if err := a.applyDelta(d, on); err != nil {
				return err
			}
		}
		if ch.FinishReason != nil && *ch.FinishReason != "" {
			a.finish = *ch.FinishReason
		}
	}
	return nil
}

// splitLeakedThinking handles an endpoint that does not separate reasoning: the model
// writes its thoughts into the answer and closes them with a bare </think> (no opening
// tag). Everything before the first </think> becomes thinking, so the answer, the
// history and the screen hold only what follows it. Only for a response with no
// reasoning field, and only once, and never when the text opened a <think> itself.
func (a *accumulator) splitLeakedThinking(on func(provider.Event)) error {
	if a.leakChecked || a.reasoning.Len() > 0 || len(a.details) > 0 {
		return nil
	}
	s := a.text.String()
	if strings.Contains(s, "<think>") {
		a.leakChecked = true
		return nil
	}
	i := strings.Index(s, "</think>")
	if i < 0 {
		return nil
	}
	a.leakChecked = true
	thought, answer := strings.TrimSpace(s[:i]), strings.TrimLeft(s[i+len("</think>"):], " \t\r\n")
	if err := a.addThinking(len(thought)); err != nil {
		return err
	}
	a.reasoning.WriteString(thought)
	a.text.Reset()
	a.text.WriteString(answer)
	on(provider.Event{Kind: provider.EvReset})
	if answer != "" {
		on(provider.Event{Kind: provider.EvText, Text: answer})
	}
	return nil
}

func (a *accumulator) applyDelta(d *delta, on func(provider.Event)) error {
	if d.Content != nil && *d.Content != "" {
		if a.text.Len()+len(*d.Content) > a.lim.MaxTextBytes {
			return provider.LimitExceeded("answer text", int64(a.lim.MaxTextBytes))
		}
		a.text.WriteString(*d.Content)
		on(provider.Event{Kind: provider.EvText, Text: *d.Content})
		if err := a.splitLeakedThinking(on); err != nil {
			return err
		}
	}
	r := d.Reasoning
	if r == "" {
		r = d.ReasoningContent
	}
	if r != "" {
		if err := a.addThinking(len(r)); err != nil {
			return err
		}
		a.reasoning.WriteString(r)
		on(provider.Event{Kind: provider.EvThinking, Text: r})
	}
	for i := range d.ReasoningDetails {
		it := d.ReasoningDetails[i]
		idx := 0
		if it.Index != nil {
			idx = *it.Index
		}
		size := 0
		for _, s := range []*string{it.Text, it.Summary, it.Data, it.Signature} {
			if s != nil {
				size += len(*s)
			}
		}
		if err := a.addThinking(size); err != nil {
			return err
		}
		cur, ok := a.details[idx]
		if !ok {
			if len(a.details) >= a.lim.MaxBlocks {
				return provider.LimitExceededCount("reasoning items", a.lim.MaxBlocks)
			}
			cur = &detailAcc{item: it}
			cur.item.Text, cur.item.Summary, cur.item.Data = nil, nil, nil
			a.details[idx] = cur
			a.detailOrder = append(a.detailOrder, idx)
			cur.add(it)
			continue
		}
		cur.add(it)
		if it.Signature != nil {
			cur.item.Signature = it.Signature
		}
		if it.Format != nil {
			cur.item.Format = it.Format
		}
	}
	for _, tc := range d.ToolCalls {
		c, ok := a.calls[tc.Index]
		if !ok {
			if len(a.calls) >= a.lim.MaxToolCalls {
				return provider.LimitExceededCount("tool calls", a.lim.MaxToolCalls)
			}
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
			if c.args.Len()+len(tc.Function.Arguments) > a.lim.MaxToolArgBytes {
				return provider.LimitExceeded("tool call arguments", int64(a.lim.MaxToolArgBytes))
			}
			c.args.WriteString(tc.Function.Arguments)
			on(provider.Event{Kind: provider.EvToolDelta, Index: tc.Index, Text: tc.Function.Arguments})
		}
	}
	return nil
}

// addThinking accounts n more bytes of reasoning against the text limit.
func (a *accumulator) addThinking(n int) error {
	if a.thinkBytes+n > a.lim.MaxTextBytes {
		return provider.LimitExceeded("reasoning text", int64(a.lim.MaxTextBytes))
	}
	a.thinkBytes += n
	return nil
}

// trace returns the captured token trace, or nil when the server returned none.
// Logprobs that do not line up with the completion ids are dropped rather than
// guessed at; the ids themselves are still useful.
func (a *accumulator) trace(model string) *core.TokenTrace {
	if len(a.compIDs) == 0 {
		return nil
	}
	t := &core.TokenTrace{ModelVersion: model, Tokenizer: model, PromptIDs: a.promptIDs, CompletionIDs: a.compIDs}
	if len(a.logprobs) == len(a.compIDs) {
		t.Logprobs = a.logprobs
	}
	return t
}

// finish builds the assistant turn.
func (a *accumulator) build(fallbackID func(name, args string, n int) string) (core.Turn, core.StopReason) {
	var blocks []core.Block

	if len(a.detailOrder) > 0 {
		sort.Ints(a.detailOrder)
		items := make([]*reasoningItem, 0, len(a.detailOrder))
		for _, i := range a.detailOrder {
			items = append(items, a.details[i].result())
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

// readStream consumes an SSE body with the default limits.
func readStream(body io.Reader, start time.Time, on func(provider.Event)) (*accumulator, error) {
	return readStreamLimits(body, start, on, provider.StreamLimits{})
}

// errStreamCut is what a body that ends before the endpoint said it was finished is.
// The wording names the two ways an endpoint says so, for whoever reads the log.
const errStreamCut = "the stream ended before [DONE] or a finish reason: the connection was cut"

// readStreamLimits consumes an SSE body. A response that outgrows a limit ends
// with a provider error (see provider.LimitExceeded); the caller cancels the
// request.
//
// A stream is complete when the endpoint says so: [DONE], or the finish reason of a
// choice. A body that just ends is a connection that was cut (a proxy that closed
// it cleanly, a server that died between two frames), and taking it for a short
// answer would end the agent's task on half a sentence, or on nothing at all: an
// answer with no tool calls is the end of a run.
func readStreamLimits(body io.Reader, start time.Time, on func(provider.Event), lim provider.StreamLimits) (*accumulator, error) {
	acc := newAccumulatorLimits(lim)
	r := provider.NewSSEReaderLimits(body, lim)
	for {
		ev, err := r.Next()
		if err == io.EOF {
			if acc.finish == "" {
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
			return acc, nil
		}
		var c chunk
		if err := json.Unmarshal([]byte(data), &c); err != nil {
			var bad *json.UnmarshalTypeError
			if !errors.As(err, &bad) {
				if ev.Event == "error" {
					return acc, eventError(ev.Event, data)
				}
				continue // tolerate non-JSON keep-alive payloads
			}
			// Valid JSON with a member of the wrong type (a content that is a list of parts,
			// a number where a string belongs): encoding/json has decoded everything else, and
			// a frame that is dropped whole takes its text, its tool calls or its error with it.
		}
		if e := errorFrame(c.Error); e != nil {
			return acc, mapInBandError(e)
		}
		if ev.Event == "error" {
			return acc, eventError(ev.Event, data)
		}
		if c.Usage != nil {
			acc.rawUsage = rawUsageOf([]byte(data)) // the audit record is what the server sent
		}
		if err := acc.feed(&c, start, on); err != nil {
			return acc, err
		}
	}
}

// eventError is the error for an SSE event named "error" whose payload carries no
// error member: whatever it says, the endpoint said it was an error.
func eventError(event, data string) *provider.Error {
	return &provider.Error{Kind: provider.ErrServer, Message: provider.SanitizeText(event+" event: "+data, 0), Raw: provider.CapRaw([]byte(data))}
}
