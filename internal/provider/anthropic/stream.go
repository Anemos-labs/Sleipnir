package anthropic

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/provider"
)

// member is one member of a JSON object, value kept raw.
type member struct {
	key string
	val json.RawMessage
}

// orderedMembers splits a JSON object into its members in wire order. Go maps
// would lose the order, and the assembled block must look like the block the
// API would have returned in one piece.
func orderedMembers(raw []byte) ([]member, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, errors.New("not a JSON object")
	}
	var out []member
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := kt.(string)
		if !ok {
			return nil, errors.New("malformed object key")
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		out = append(out, member{key: key, val: v})
	}
	return out, nil
}

// blockAcc folds the events of one content block into the block the API would
// have returned whole.
type blockAcc struct {
	index   int
	typ     string
	id      string
	name    string
	members []member

	text, think, partial            strings.Builder
	sig                             string
	sawText, sawThink, sawSig, sawJ bool
	done                            bool
	block                           core.Block
}

func (a *blockAcc) startString(key string) string {
	for _, m := range a.members {
		if m.key == key {
			var s string
			_ = json.Unmarshal(m.val, &s)
			return s
		}
	}
	return ""
}

// setSignature keeps the signature. The API sends it whole in one signature_delta
// (the official SDKs assign it); a gateway that chunked or repeated it is
// tolerated: a value that extends the current one replaces it, anything else is
// appended.
func (a *blockAcc) setSignature(s string) {
	if strings.HasPrefix(s, a.sig) {
		a.sig = s
	} else {
		a.sig += s
	}
	a.sawSig = true
}

// build assembles the final block JSON. Members keep their order and their raw
// bytes; only the values that deltas produced are replaced. invalid is set when
// a tool input did not assemble into a JSON object (truncated at max_tokens, or
// eager_input_streaming let malformed JSON through): the raw text is returned so
// the harness can report it, while the replayable form carries an empty object,
// because the API would reject anything else on the next request.
func (a *blockAcc) build() (raw []byte, invalidText string, invalid bool) {
	var w jw
	w.putc('{')
	first := true
	wrote := map[string]bool{}
	emit := func(key string, val []byte) {
		w.member(first, key)
		first = false
		w.raw(val)
		wrote[key] = true
	}
	str := func(s string) []byte { var t jw; t.str(s); return t.bytes() }

	input := func() []byte {
		text := strings.TrimSpace(a.partial.String())
		switch {
		case text == "":
			return []byte("{}")
		case isJSONObject([]byte(text)):
			return []byte(text)
		}
		invalidText, invalid = a.partial.String(), true
		return []byte("{}")
	}

	for _, m := range a.members {
		switch {
		case m.key == "text" && a.sawText:
			emit("text", str(a.startString("text")+a.text.String()))
		case m.key == "thinking" && a.sawThink:
			emit("thinking", str(a.startString("thinking")+a.think.String()))
		case m.key == "signature" && a.sawSig:
			emit("signature", str(a.sig))
		case m.key == "input" && a.sawJ:
			emit("input", input())
		default:
			if m.key == "input" && a.typ == "tool_use" && !isJSONObject(m.val) {
				invalidText, invalid = string(m.val), true
				emit("input", []byte("{}"))
				continue
			}
			emit(m.key, m.val)
		}
	}
	if a.sawText && !wrote["text"] {
		emit("text", str(a.text.String()))
	}
	if a.sawThink && !wrote["thinking"] {
		emit("thinking", str(a.think.String()))
	}
	if a.sawSig && !wrote["signature"] {
		emit("signature", str(a.sig))
	}
	if a.sawJ && !wrote["input"] {
		emit("input", input())
	}
	w.putc('}')
	return w.bytes(), invalidText, invalid
}

// finish closes the block. ok is false for a block that carries nothing worth
// keeping (an empty text block).
func (a *blockAcc) finish() (blk core.Block, ok bool) {
	raw, invalidText, invalid := a.build()
	a.done = true
	blk, ok = blockFromRaw(raw)
	if !ok {
		return core.Block{}, false
	}
	if invalid && blk.Kind == core.BlockToolUse {
		blk.Input = quoted(invalidText)
		blk.Invalid = "tool input is not valid JSON (cut off by max_tokens?)"
	}
	a.block = blk
	return blk, true
}

// sseEvent is the union of the event payloads this adapter reads.
type sseEvent struct {
	Type         string          `json:"type"`
	Message      json.RawMessage `json:"message"`
	Index        *int            `json:"index"`
	ContentBlock json.RawMessage `json:"content_block"`
	Delta        json.RawMessage `json:"delta"`
	Usage        json.RawMessage `json:"usage"`
	Error        *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
	extras
}

// readStream consumes a Messages SSE body. It returns the finished message and
// the time of its first event.
//
// The stream is complete only when the API said so (message_stop, or a
// message_delta carrying stop_reason). A body that just ends is a dropped
// connection, and treating it as a short answer would silently truncate a turn.
func readStream(body io.Reader, start time.Time, on func(provider.Event)) (*result, time.Duration, error) {
	s := &streamState{
		on: on, start: start, res: &result{},
		blocks: map[int]*blockAcc{}, raw: map[string]json.RawMessage{},
	}
	r := provider.NewSSEReader(body)
	for {
		ev, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, s.ttfb, &provider.Error{Kind: provider.ErrNetwork, Message: err.Error(), Err: err}
		}
		if len(bytes.TrimSpace(ev.Data)) == 0 {
			continue
		}
		done, perr := s.handle(ev)
		if perr != nil {
			return nil, s.ttfb, perr
		}
		if done {
			return s.finish(), s.ttfb, nil
		}
	}
	if s.complete {
		return s.finish(), s.ttfb, nil
	}
	if !s.started {
		return nil, s.ttfb, &provider.Error{Kind: provider.ErrNetwork, Message: "stream ended before message_start"}
	}
	return nil, s.ttfb, &provider.Error{Kind: provider.ErrNetwork, Message: fmt.Sprintf("stream ended before message_stop after %d content block(s)", len(s.blocks))}
}

type streamState struct {
	on    func(provider.Event)
	start time.Time
	ttfb  time.Duration
	res   *result

	usage wireUsage
	raw   map[string]json.RawMessage
	ext   []extras

	blocks map[int]*blockAcc
	order  []int

	started, complete bool
	stopReason        string
	stopSeq           string
	stopDetails       json.RawMessage
}

// known lists the events whose payload must parse: a malformed one means the
// stream is corrupt. Unknown events are skipped, as the API documents.
var known = map[string]bool{
	"message_start": true, "content_block_start": true, "content_block_delta": true,
	"content_block_stop": true, "message_delta": true, "message_stop": true, "error": true,
}

func (s *streamState) handle(ev provider.SSEEvent) (done bool, err error) {
	var e sseEvent
	if jerr := json.Unmarshal(ev.Data, &e); jerr != nil {
		if known[ev.Event] {
			return false, &provider.Error{Kind: provider.ErrServer, Message: fmt.Sprintf("malformed %s event: %v", ev.Event, jerr), Raw: ev.Data}
		}
		return false, nil // keep-alive noise
	}
	if e.Type == "" {
		e.Type = ev.Event
	}
	switch e.Type {
	case "message_start":
		return false, s.messageStart(e)
	case "content_block_start":
		return false, s.blockStart(e)
	case "content_block_delta":
		return false, s.blockDelta(e)
	case "content_block_stop":
		s.blockStop(e)
	case "message_delta":
		s.messageDelta(e)
		if s.stopReason != "" {
			s.complete = true
		}
	case "message_stop":
		s.complete = true
		return true, nil
	case "error":
		if e.Error == nil {
			return false, &provider.Error{Kind: provider.ErrServer, Message: "error event without an error object", Raw: ev.Data}
		}
		return false, inBandError(e.Error.Type, e.Error.Message, ev.Data)
	}
	// ping and unknown event types fall through: they only prove liveness.
	return false, nil
}

func (s *streamState) messageStart(e sseEvent) error {
	var m struct {
		ID    string          `json:"id"`
		Model string          `json:"model"`
		Usage json.RawMessage `json:"usage"`
		extras
	}
	if len(e.Message) > 0 {
		if err := json.Unmarshal(e.Message, &m); err != nil {
			return &provider.Error{Kind: provider.ErrServer, Message: "malformed message_start: " + err.Error(), Raw: e.Message}
		}
	}
	if !s.started {
		s.started = true
		s.ttfb = time.Since(s.start)
		s.res.id, s.res.model = m.ID, m.Model
		// EvStart marks the API beginning to respond, which is the moment cache
		// entries written by this request become readable: the swarm's warm gate
		// releases the followers on it.
		s.on(provider.Event{Kind: provider.EvStart, RequestID: m.ID, Elapsed: s.ttfb})
	}
	s.ext = append(s.ext, m.extras)
	s.addUsage(m.Usage)
	return nil
}

func (s *streamState) addUsage(raw json.RawMessage) {
	if len(raw) == 0 || string(raw) == "null" {
		return
	}
	var u wireUsage
	if json.Unmarshal(raw, &u) == nil {
		s.usage.merge(u)
		mergeRawUsage(s.raw, raw)
	}
}

func (s *streamState) blockStart(e sseEvent) error {
	if e.Index == nil {
		return nil
	}
	idx := *e.Index
	if _, dup := s.blocks[idx]; dup {
		return nil
	}
	members, err := orderedMembers(e.ContentBlock)
	if err != nil {
		return &provider.Error{Kind: provider.ErrServer, Message: "malformed content_block_start: " + err.Error(), Raw: e.ContentBlock}
	}
	a := &blockAcc{index: idx, members: members}
	for _, m := range members {
		switch m.key {
		case "type":
			_ = json.Unmarshal(m.val, &a.typ)
		case "id":
			_ = json.Unmarshal(m.val, &a.id)
		case "name":
			_ = json.Unmarshal(m.val, &a.name)
		}
	}
	s.blocks[idx] = a
	s.order = append(s.order, idx)
	if a.typ == "tool_use" {
		s.on(provider.Event{Kind: provider.EvToolStart, Index: idx, ToolID: a.id, ToolName: a.name})
	}
	return nil
}

func (s *streamState) blockDelta(e sseEvent) error {
	if e.Index == nil {
		return nil
	}
	a := s.blocks[*e.Index]
	if a == nil || a.done {
		return nil
	}
	var d struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Thinking    string `json:"thinking"`
		Signature   string `json:"signature"`
		PartialJSON string `json:"partial_json"`
	}
	if err := json.Unmarshal(e.Delta, &d); err != nil {
		return &provider.Error{Kind: provider.ErrServer, Message: "malformed content_block_delta: " + err.Error(), Raw: e.Delta}
	}
	switch d.Type {
	case "text_delta":
		a.sawText = true
		a.text.WriteString(d.Text)
		if d.Text != "" {
			s.on(provider.Event{Kind: provider.EvText, Index: a.index, Text: d.Text})
		}
	case "thinking_delta":
		a.sawThink = true
		a.think.WriteString(d.Thinking)
		if d.Thinking != "" {
			s.on(provider.Event{Kind: provider.EvThinking, Index: a.index, Text: d.Thinking})
		}
	case "signature_delta":
		a.setSignature(d.Signature)
	case "input_json_delta":
		a.sawJ = true
		a.partial.WriteString(d.PartialJSON)
		if d.PartialJSON != "" {
			s.on(provider.Event{Kind: provider.EvToolDelta, Index: a.index, Text: d.PartialJSON, ToolID: a.id, ToolName: a.name})
		}
	}
	// citations_delta and any delta type added later are ignored: they never
	// change what has to be sent back.
	return nil
}

func (s *streamState) blockStop(e sseEvent) {
	if e.Index == nil {
		return
	}
	if a := s.blocks[*e.Index]; a != nil && !a.done {
		if blk, ok := a.finish(); ok {
			s.on(provider.Event{Kind: provider.EvBlockDone, Index: a.index, Block: &blk})
		}
	}
}

func (s *streamState) messageDelta(e sseEvent) {
	var d struct {
		StopReason  string          `json:"stop_reason"`
		StopSeq     *string         `json:"stop_sequence"`
		StopDetails json.RawMessage `json:"stop_details"`
		extras
	}
	if len(e.Delta) > 0 {
		_ = json.Unmarshal(e.Delta, &d)
	}
	if d.StopReason != "" {
		s.stopReason = d.StopReason
	}
	if d.StopSeq != nil {
		s.stopSeq = *d.StopSeq
	}
	if len(d.StopDetails) > 0 && string(d.StopDetails) != "null" {
		s.stopDetails = d.StopDetails
	}
	s.ext = append(s.ext, e.extras, d.extras)
	s.addUsage(e.Usage)
}

func (s *streamState) finish() *result {
	sort.Ints(s.order)
	for _, idx := range s.order {
		a := s.blocks[idx]
		if !a.done {
			// The API closes every block before message_delta. One left open means
			// a gateway dropped the event; the content is still complete.
			if blk, ok := a.finish(); ok {
				s.on(provider.Event{Kind: provider.EvBlockDone, Index: a.index, Block: &blk})
			}
		}
		if a.block.Kind != "" {
			s.res.blocks = append(s.res.blocks, a.block)
		}
	}
	s.res.usage = s.usage
	s.res.rawUsage = encodeRawUsage(s.raw)
	var all extras
	for _, x := range s.ext {
		all.InputTransformations = append(all.InputTransformations, x.InputTransformations...)
		if x.ContextManagement != nil {
			if all.ContextManagement == nil {
				all.ContextManagement = x.ContextManagement
			} else {
				all.ContextManagement.AppliedEdits = append(all.ContextManagement.AppliedEdits, x.ContextManagement.AppliedEdits...)
			}
		}
		if x.Diagnostics != nil {
			all.Diagnostics = x.Diagnostics
		}
	}
	s.res.transforms = all.transformations()
	s.res.stop, s.res.stopDetail = stopReason(s.stopReason, s.stopSeq, s.stopDetails, s.res.blocks)
	return s.res
}
