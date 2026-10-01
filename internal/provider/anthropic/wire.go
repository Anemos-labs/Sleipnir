// Package anthropic adapts core prompts to the Anthropic Messages API
// (POST /v1/messages), and to Anthropic-compatible gateways that forward
// cache_control and thinking blocks untouched.
//
// Its job is mostly negative. kv has already decided every byte of the prompt
// and where the cache breakpoints go; the adapter must put those bytes on the
// wire without reordering, re-serialising or "tidying" anything, because on this
// API two things are byte-sensitive:
//
//   - the prompt cache is a prefix match over tools, system and messages, so an
//     adapter that re-marshals a tool schema or turns block-form system into a
//     string silently turns every cache read into a full-price write;
//   - on preserved-thinking models (Fable 5.1, Opus 5.5, Sonnet 5.5) a thinking
//     block is valid only while everything before it is unchanged, so replayed
//     assistant content must be the exact JSON the API sent (Block.Wire), and a
//     per-turn hot block must be an appended, never-deleted, turn-scoped system
//     message rather than text patched into an earlier turn.
package anthropic

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/core"
)

// Dialect is the wire-format name stamped on replayable blocks (Block.WireFormat).
const Dialect = "anthropic"

// anthropic-beta values that a request body can require. The adapter adds them
// itself: a gateway that strips the header but passes the body field produces a
// hard 400, so field and header must always travel together.
const (
	BetaTurnScopedSystem   = "mid-conversation-system-clear-at-2026-08-21"
	BetaThinkingBinding    = "thinking-binding-controls-2026-08-01"
	BetaThinkingDisplayUpd = "thinking-display-updates-2026-08-18"
)

// Warning reports something Build changed or could not honour. Warnings never
// stop a request: caching hints are optimisations, and an agent must not die
// because a marker could not be placed exactly where the planner wanted it.
type Warning struct {
	// Code is stable and machine-readable ("marker_moved", "marker_dropped", ...).
	Code    string
	Message string
}

func (w Warning) String() string { return w.Code + ": " + w.Message }

// Options tunes rendering. The zero value is the full-featured first-party API:
// every "No..." flag names something a particular endpoint lacks, so a caller
// that says nothing gets the most capable (and most cache-friendly) rendering.
type Options struct {
	// NoTurnScopedSystem: the endpoint has no mid-conversation role:system (and
	// so no clear_at). Such messages are folded into a user text block, the
	// documented append-only fallback.
	NoTurnScopedSystem bool
	// NoThinkingReplay drops thinking blocks instead of replaying them.
	NoThinkingReplay bool
	// NoZeroMaxTokens: the endpoint rejects max_tokens:0, so a warm-up uses one
	// output token instead.
	NoZeroMaxTokens bool

	// Warm renders a cache pre-warm: max_tokens 0 (or 1), never streamed.
	Warm bool
	// BindingMode is thinking.block_binding.prefix_mismatch_behavior:
	// "drop_block" or "error"; empty leaves the endpoint default.
	BindingMode string

	// DefaultMaxTokens is used when Params.MaxTokens is zero (the API requires a
	// value). Default 8192: output-token rate limits are reserved from max_tokens.
	DefaultMaxTokens int
	// ThinkingDisplay is thinking.display ("summarized", "omitted", "updates");
	// empty leaves the endpoint default.
	ThinkingDisplay string
	// ThinkingBudget is budget_tokens for families without adaptive thinking.
	ThinkingBudget int
	// UserID is metadata.user_id (abuse detection only; not a cache key).
	UserID string
	// MaxBreakpoints caps cache_control markers per request. Default 4.
	MaxBreakpoints int
	// Models resolves per-model wire acceptance. Default DefaultModelInfo.
	Models ModelResolver
	// Media resolves a Block.MediaRef that is not already a data URL, http(s)
	// URL or file id (kv stores images in a blob store by hash).
	Media func(ref, mediaType string) ([]byte, error)
	// ExtraBody is merged into the top level, keys sorted. It may not override a
	// member the adapter owns.
	ExtraBody map[string]any
}

func (o *Options) defaults() {
	if o.DefaultMaxTokens <= 0 {
		o.DefaultMaxTokens = 8192
	}
	if o.MaxBreakpoints <= 0 {
		o.MaxBreakpoints = 4
	}
	if o.Models == nil {
		o.Models = DefaultModelInfo
	}
}

// Built is a rendered request.
type Built struct {
	Body []byte
	// Warnings lists deviations from the prompt as given.
	Warnings []Warning
	// Betas lists the anthropic-beta values the body requires.
	Betas []string
}

// Build renders a prompt as a Messages API body. It is a pure function of its
// arguments and never mutates them: the same prompt always yields the same
// bytes, which is what makes the prefix cache and golden tests possible.
func Build(p *core.Prompt, o Options, stream bool) ([]byte, error) {
	b, err := BuildReport(p, o, stream)
	if err != nil {
		return nil, err
	}
	return b.Body, nil
}

// BuildReport is Build plus the side channel: warnings and required betas.
func BuildReport(p *core.Prompt, o Options, stream bool) (*Built, error) {
	o.defaults()
	if p == nil {
		return nil, errors.New("anthropic: nil prompt")
	}
	if strings.TrimSpace(p.Model) == "" {
		return nil, errors.New("anthropic: prompt has no model")
	}
	if len(p.Messages) == 0 {
		return nil, errors.New("anthropic: prompt has no messages")
	}
	b := &builder{o: o, p: p, info: o.Models(p.Model), refs: map[core.BlockRef]int{}, drops: map[core.BlockRef]string{}}
	return b.run(stream)
}

// rblock is one rendered block. The JSON is kept without its closing brace so a
// cache_control member can be spliced in without touching the bytes before it
// (blocks replayed from Block.Wire must stay byte-identical).
type rblock struct {
	prefix   []byte
	members  bool   // prefix already holds at least one member
	eligible bool   // may carry cache_control
	why      string // when not eligible: what it is
	label    string // for warnings
	cc       string // rendered cache_control, "" when unmarked
}

// rmsg is one rendered message: block indices into builder.blocks.
type rmsg struct {
	role    string
	clearAt string
	blocks  []int
}

type builder struct {
	o    Options
	p    *core.Prompt
	info ModelInfo

	warns []Warning
	betas []string

	// blocks is in wire order: tools, system, then message blocks.
	blocks []rblock
	tools  []int
	system []int
	msgs   []rmsg

	// refs maps a prompt block to its rendered block. For a block that was not
	// sent (empty text, unreplayable thinking) the entry is the nearest earlier
	// rendered block and drops records why, so a marker on it can move back.
	refs  map[core.BlockRef]int
	drops map[core.BlockRef]string

	ephemeralWarned bool
	// thinkingReplayed: a thinking block has been rendered into an earlier message.
	thinkingReplayed bool
}

func (b *builder) warn(code, format string, args ...any) {
	b.warns = append(b.warns, Warning{Code: code, Message: fmt.Sprintf(format, args...)})
}

func (b *builder) need(beta string) {
	for _, x := range b.betas {
		if x == beta {
			return
		}
	}
	b.betas = append(b.betas, beta)
}

func (b *builder) add(rb rblock) int {
	b.blocks = append(b.blocks, rb)
	return len(b.blocks) - 1
}

func (b *builder) run(stream bool) (*Built, error) {
	if err := b.renderTools(); err != nil {
		return nil, err
	}
	if err := b.renderSystem(); err != nil {
		return nil, err
	}
	if err := b.renderMessages(); err != nil {
		return nil, err
	}
	if len(b.msgs) == 0 {
		return nil, errors.New("anthropic: every message rendered empty")
	}
	b.placeBreakpoints()
	body, err := b.assemble(stream)
	if err != nil {
		return nil, err
	}
	return &Built{Body: body, Warnings: b.warns, Betas: b.betas}, nil
}

// ---------------------------------------------------------------------------
// tools and system

func (b *builder) renderTools() error {
	for i, t := range b.p.Tools {
		if t.Name == "" {
			return fmt.Errorf("anthropic: tool %d has no name", i)
		}
		schema := []byte(t.InputSchema)
		if len(bytes.TrimSpace(schema)) == 0 {
			b.warn("empty_schema", "tool %q has no input_schema; sent {\"type\":\"object\"}", t.Name)
			schema = []byte(`{"type":"object"}`)
		}
		if !isJSONObject(schema) {
			return fmt.Errorf("anthropic: tool %q input_schema is not a JSON object", t.Name)
		}
		var w jw
		w.putc('{')
		w.member(true, "name")
		w.str(t.Name)
		if t.Description != "" {
			w.member(false, "description")
			w.str(t.Description)
		}
		w.member(false, "input_schema")
		w.raw(schema) // verbatim: canonical bytes from kv, whatever they are
		if t.Strict {
			w.member(false, "strict")
			w.lit("true")
		}
		idx := b.add(rblock{prefix: w.bytes(), members: true, eligible: true, label: fmt.Sprintf("tools[%d]", i)})
		b.tools = append(b.tools, idx)
		b.refs[core.BlockRef{Sys: true, Msg: -1, Blk: i}] = idx
	}
	return nil
}

func (b *builder) renderSystem() error {
	for i, blk := range b.p.System {
		ref := core.BlockRef{Sys: true, Msg: 0, Blk: i}
		if blk.Kind != core.BlockText {
			return fmt.Errorf("anthropic: system block %d is %q; only text blocks are allowed", i, blk.Kind)
		}
		if blank(blk.Text) {
			b.drop(ref, "empty system block")
			b.warn("empty_block_dropped", "system[%d] is empty and was not sent", i)
			continue
		}
		idx := b.add(textBlock(blk.Text, fmt.Sprintf("system[%d]", i)))
		b.system = append(b.system, idx)
		b.refs[ref] = idx
	}
	return nil
}

// ---------------------------------------------------------------------------
// messages

func (b *builder) renderMessages() error {
	for mi, m := range b.p.Messages {
		var err error
		switch m.Role {
		case core.RoleUser:
			err = b.userMessage(mi, m)
		case core.RoleAssistant:
			err = b.assistantMessage(mi, m)
		case core.RoleSystem:
			err = b.systemMessage(mi, m)
		default:
			err = fmt.Errorf("anthropic: message %d has unsupported role %q", mi, m.Role)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (b *builder) drop(ref core.BlockRef, why string) {
	b.refs[ref] = len(b.blocks) - 1
	b.drops[ref] = why
}

// pushMsg records a rendered message. An empty one is not sent (the API rejects
// empty content), which may leave two same-role messages adjacent; the API
// merges those itself.
func (b *builder) pushMsg(mi int, role string, blocks []int, refs []core.BlockRef) {
	if len(blocks) == 0 {
		b.warn("empty_message_dropped", "messages[%d] rendered no blocks and was not sent", mi)
		return
	}
	b.msgs = append(b.msgs, rmsg{role: role, blocks: blocks})
	for i, r := range refs {
		b.refs[r] = blocks[i]
	}
}

func (b *builder) userMessage(mi int, m core.Message) error {
	var idxs []int
	var refs []core.BlockRef
	seenOther := false
	for bi, blk := range m.Blocks {
		ref := core.BlockRef{Msg: mi, Blk: bi}
		label := fmt.Sprintf("messages[%d].blocks[%d]", mi, bi)
		var rb rblock
		switch blk.Kind {
		case core.BlockText:
			if blank(blk.Text) {
				b.drop(ref, "empty text block")
				b.warn("empty_block_dropped", "%s is empty and was not sent", label)
				continue
			}
			b.noteEphemeral(blk, mi)
			rb = textBlock(blk.Text, label)
			seenOther = true
		case core.BlockImage:
			j, err := b.imageJSON(blk)
			if err != nil {
				return fmt.Errorf("anthropic: %s: %w", label, err)
			}
			rb = rblock{prefix: j[:len(j)-1], members: true, eligible: true, label: label}
			seenOther = true
		case core.BlockToolResult:
			var err error
			rb, err = b.toolResult(blk, label)
			if err != nil {
				return err
			}
			if seenOther {
				b.warn("tool_result_order", "%s follows non-tool_result content; the API requires tool_result blocks first", label)
			}
		default:
			return fmt.Errorf("anthropic: %s: %q blocks cannot appear in a user message", label, blk.Kind)
		}
		idxs = append(idxs, b.add(rb))
		refs = append(refs, ref)
	}
	b.pushMsg(mi, "user", idxs, refs)
	return nil
}

// noteEphemeral flags the inline hot tail where it does harm. Rendered as given,
// it is a per-request edit of a message an assistant turn was already produced
// against, so the thinking blocks replayed above it are bound to a record that no
// longer exists: a 400 on a model that enforces preserved thinking (or a dropped
// block with drop_block). The fix belongs in kv (a persisted or turn-scoped
// delivery, see kv.ResolveHot); the adapter cannot make it, and must not guess.
//
// On models that do not bind thinking the inline tail is the cheapest delivery
// there is (it sits after the last marker), so it is not worth a warning per
// request, and neither is the first request, which has no thinking to void.
func (b *builder) noteEphemeral(blk core.Block, mi int) {
	if !blk.Ephemeral || b.ephemeralWarned || !b.thinkingReplayed {
		return
	}
	if b.info.Known && !b.info.PreservedThinking && b.o.BindingMode == "" {
		return
	}
	b.ephemeralWarned = true
	b.warn("ephemeral_inline", "messages[%d] carries an ephemeral block inline while earlier thinking blocks are replayed; the next request removes it, which voids their binding to the conversation (use a persisted or turn-scoped hot block)", mi)
}

func (b *builder) assistantMessage(mi int, m core.Message) error {
	var idxs []int
	var refs []core.BlockRef
	for bi, blk := range m.Blocks {
		ref := core.BlockRef{Msg: mi, Blk: bi}
		label := fmt.Sprintf("messages[%d].blocks[%d]", mi, bi)
		var rb rblock
		switch blk.Kind {
		case core.BlockText:
			if blank(blk.Text) {
				b.drop(ref, "empty text block")
				b.warn("empty_block_dropped", "%s is empty and was not sent", label)
				continue
			}
			rb = textBlock(blk.Text, label)
		case core.BlockToolUse:
			var err error
			rb, err = b.toolUse(blk, label)
			if err != nil {
				return err
			}
		case core.BlockThinking, core.BlockRedactedThinking:
			// Replayed verbatim or not at all. Rebuilding a thinking block from
			// its text would produce a block whose signature the API rejects, and
			// a rejected block fails the whole request; kv applies the same rule
			// so that its planner never counts what is not sent.
			prefix, members, ok := wireObject(blk.Wire)
			if b.o.NoThinkingReplay || blk.WireFormat != Dialect || !ok {
				b.drop(ref, "thinking block not replayable")
				b.warn("thinking_dropped", "%s (%s) has no replayable %s wire form and was not sent", label, blk.Kind, Dialect)
				continue
			}
			rb = rblock{prefix: prefix, members: members, why: "thinking blocks cannot carry cache_control", label: label}
			b.thinkingReplayed = true
		case core.BlockToolResult, core.BlockImage:
			return fmt.Errorf("anthropic: %s: %q blocks cannot appear in an assistant message", label, blk.Kind)
		default:
			// Compaction, fallback and server-tool blocks are echoed exactly as
			// received; only the API knows how to read them back.
			prefix, members, ok := wireObject(blk.Wire)
			if blk.WireFormat != Dialect || !ok {
				b.drop(ref, "unreplayable block")
				b.warn("block_dropped", "%s (%s) has no replayable %s wire form and was not sent", label, blk.Kind, Dialect)
				continue
			}
			rb = rblock{prefix: prefix, members: members, eligible: blk.Kind == core.BlockCompaction, why: string(blk.Kind) + " blocks cannot carry cache_control", label: label}
		}
		idxs = append(idxs, b.add(rb))
		refs = append(refs, ref)
	}
	b.pushMsg(mi, "assistant", idxs, refs)
	return nil
}

// systemMessage renders a mid-conversation operator message. Native rendering
// is {"role":"system","content":[...],"clear_at":"next_user_message"}: the
// message stays in the array byte-identical forever, renders only until the next
// user message, and so costs the cache and the thinking bindings nothing.
//
// Whether a message is native depends only on what precedes it (the API wants a
// system message right after a user message), never on what follows: a message
// that rendered one way on request N must render the same way on request N+1, or
// the "append-only" property this whole mechanism exists for is lost.
func (b *builder) systemMessage(mi int, m core.Message) error {
	clearAt := m.ClearAt
	switch clearAt {
	case "", "never":
		clearAt = ""
	case "next_user_message":
	default:
		return fmt.Errorf("anthropic: messages[%d]: unsupported clear_at %q", mi, m.ClearAt)
	}

	type piece struct {
		ref core.BlockRef
		rb  rblock
	}
	var pieces []piece
	for bi, blk := range m.Blocks {
		ref := core.BlockRef{Msg: mi, Blk: bi}
		label := fmt.Sprintf("messages[%d].blocks[%d]", mi, bi)
		if blk.Kind != core.BlockText {
			b.drop(ref, "non-text block in a system message")
			b.warn("block_dropped", "%s is a %s block; system messages are text-only", label, blk.Kind)
			continue
		}
		if blank(blk.Text) {
			b.drop(ref, "empty text block")
			b.warn("empty_block_dropped", "%s is empty and was not sent", label)
			continue
		}
		pieces = append(pieces, piece{ref, textBlock(blk.Text, label)})
	}
	if len(pieces) == 0 {
		b.warn("empty_message_dropped", "messages[%d] (system) rendered no blocks and was not sent", mi)
		return nil
	}

	var prev *rmsg
	if n := len(b.msgs); n > 0 {
		prev = &b.msgs[n-1]
	}
	// The model gate matters as much as the position: the API answers a 400 to a
	// system message on a family without the feature, and a request that only
	// wanted to deliver a reminder must not die of it.
	native := !b.o.NoTurnScopedSystem && b.info.midSystem() && prev != nil && prev.role == "user"

	if native {
		var idxs []int
		for _, pc := range pieces {
			pc.rb.eligible = false
			pc.rb.why = "system messages take no cache_control"
			idx := b.add(pc.rb)
			idxs = append(idxs, idx)
			b.refs[pc.ref] = idx
		}
		b.msgs = append(b.msgs, rmsg{role: "system", clearAt: clearAt, blocks: idxs})
		if clearAt != "" {
			b.need(BetaTurnScopedSystem)
		}
		return nil
	}

	// Folded: the text becomes ordinary trailing content of the preceding user
	// message (a new user message when there is none). This is the documented
	// fallback and it is append-only too: on the next request the same text sits
	// in the same place. The cost is that it never clears.
	switch {
	case b.o.NoTurnScopedSystem:
		// The caller declared the endpoint without the feature: folding is the
		// expected rendering, not news.
	case !b.info.midSystem():
		b.warn("system_folded", "model %q does not accept mid-conversation system messages; messages[%d] was folded into user text", b.p.Model, mi)
	default:
		b.warn("system_folded", "messages[%d] (system) does not follow a user message and was folded into user text", mi)
	}
	if prev != nil && prev.role == "user" {
		for _, pc := range pieces {
			idx := b.add(pc.rb)
			prev.blocks = append(prev.blocks, idx)
			b.refs[pc.ref] = idx
		}
		return nil
	}
	var idxs []int
	for _, pc := range pieces {
		idx := b.add(pc.rb)
		idxs = append(idxs, idx)
		b.refs[pc.ref] = idx
	}
	b.msgs = append(b.msgs, rmsg{role: "user", blocks: idxs})
	return nil
}

// ---------------------------------------------------------------------------
// blocks

func textBlock(text, label string) rblock {
	var w jw
	w.lit(`{"type":"text","text":`)
	w.str(text)
	return rblock{prefix: w.bytes(), members: true, eligible: true, label: label}
}

func (b *builder) toolUse(blk core.Block, label string) (rblock, error) {
	if prefix, members, ok := wireObject(blk.Wire); ok && blk.WireFormat == Dialect {
		return rblock{prefix: prefix, members: members, eligible: true, label: label}, nil
	}
	if blk.ToolID == "" || blk.ToolName == "" {
		return rblock{}, fmt.Errorf("anthropic: %s: tool_use needs an id and a name", label)
	}
	input := []byte(blk.Input)
	switch {
	case len(bytes.TrimSpace(input)) == 0:
		input = []byte("{}")
	case !isJSONObject(input):
		b.warn("tool_input_not_object", "%s: tool_use input is not a JSON object; sent {}", label)
		input = []byte("{}")
	}
	var w jw
	w.lit(`{"type":"tool_use","id":`)
	w.str(blk.ToolID)
	w.lit(`,"name":`)
	w.str(blk.ToolName)
	w.lit(`,"input":`)
	w.raw(input)
	return rblock{prefix: w.bytes(), members: true, eligible: true, label: label}, nil
}

func (b *builder) toolResult(blk core.Block, label string) (rblock, error) {
	if blk.ToolID == "" {
		return rblock{}, fmt.Errorf("anthropic: %s: tool_result needs the id of the tool_use it answers", label)
	}
	var content []byte
	n := 0
	for ci, c := range blk.Result {
		clabel := fmt.Sprintf("%s.result[%d]", label, ci)
		switch c.Kind {
		case core.BlockText:
			if blank(c.Text) {
				b.warn("empty_block_dropped", "%s is empty and was not sent", clabel)
				continue
			}
			var w jw
			w.lit(`{"type":"text","text":`)
			w.str(c.Text)
			w.putc('}')
			content = appendElem(content, w.bytes(), n)
		case core.BlockImage:
			j, err := b.imageJSON(c)
			if err != nil {
				return rblock{}, fmt.Errorf("anthropic: %s: %w", clabel, err)
			}
			content = appendElem(content, j, n)
		default:
			b.warn("block_dropped", "%s is a %s block; tool results carry text and images", clabel, c.Kind)
			continue
		}
		n++
	}
	var w jw
	w.lit(`{"type":"tool_result","tool_use_id":`)
	w.str(blk.ToolID)
	if n > 0 {
		w.lit(`,"content":[`)
		w.raw(content)
		w.putc(']')
	}
	if blk.IsError {
		w.lit(`,"is_error":true`)
	}
	rb := rblock{prefix: w.bytes(), members: true, eligible: n > 0, label: label}
	if n == 0 {
		rb.why = "empty tool_result"
	}
	return rb, nil
}

func appendElem(dst, elem []byte, n int) []byte {
	if n > 0 {
		dst = append(dst, ',')
	}
	return append(dst, elem...)
}

// imageJSON renders a complete image block. kv keeps image bytes in a blob store
// and the block only holds the hash, so anything that is not already a data URL,
// http(s) URL or file id needs Options.Media to turn it back into bytes.
func (b *builder) imageJSON(blk core.Block) ([]byte, error) {
	ref := blk.MediaRef
	if ref == "" {
		return nil, errors.New("image block has no media reference")
	}
	var w jw
	w.lit(`{"type":"image","source":`)
	switch {
	case strings.HasPrefix(ref, "data:"):
		mt, data, err := parseDataURL(ref)
		if err != nil {
			return nil, err
		}
		if mt == "" {
			mt = blk.MediaType
		}
		if mt == "" {
			return nil, errors.New("data URL image has no media type")
		}
		w.lit(`{"type":"base64","media_type":`)
		w.str(mt)
		w.lit(`,"data":`)
		w.str(data)
		w.putc('}')
	case strings.HasPrefix(ref, "https://"), strings.HasPrefix(ref, "http://"):
		w.lit(`{"type":"url","url":`)
		w.str(ref)
		w.putc('}')
	case strings.HasPrefix(ref, "file_"):
		w.lit(`{"type":"file","file_id":`)
		w.str(ref)
		w.putc('}')
	default:
		if b.o.Media == nil {
			return nil, fmt.Errorf("image %q is a blob reference and Options.Media is not set", trunc(ref, 24))
		}
		data, err := b.o.Media(ref, blk.MediaType)
		if err != nil {
			return nil, fmt.Errorf("resolving image %q: %w", trunc(ref, 24), err)
		}
		mt := blk.MediaType
		if mt == "" {
			mt = http.DetectContentType(data[:min(len(data), 512)])
		}
		if !strings.HasPrefix(mt, "image/") {
			return nil, fmt.Errorf("image %q has media type %q", trunc(ref, 24), mt)
		}
		w.lit(`{"type":"base64","media_type":`)
		w.str(mt)
		w.lit(`,"data":`)
		w.str(base64.StdEncoding.EncodeToString(data))
		w.putc('}')
	}
	w.putc('}')
	return w.bytes(), nil
}

func parseDataURL(u string) (mediaType, data string, err error) {
	rest := strings.TrimPrefix(u, "data:")
	head, payload, ok := strings.Cut(rest, ",")
	if !ok {
		return "", "", errors.New("malformed data URL")
	}
	mt, params, _ := strings.Cut(head, ";")
	if !strings.Contains(";"+params+";", ";base64;") {
		return "", "", errors.New("data URL is not base64 encoded")
	}
	return mt, payload, nil
}

// ---------------------------------------------------------------------------
// cache breakpoints

type mark struct {
	idx   int
	long  bool
	label string
}

// placeBreakpoints puts cache_control on the blocks the planner addressed.
//
// A marker asks for "cache the prefix ending here". If the addressed block
// cannot carry one (a thinking block, an empty tool_result, a turn-scoped system
// message, a block that was not sent) the nearest earlier block that can is used
// instead: the prefix is the same content up to that block, and everything
// between it and the requested position is content the model does not read as
// prefix anyway. The alternative, dropping the marker, would silently lose the
// rolling entry every tool loop depends on.
func (b *builder) placeBreakpoints() {
	var marks []mark
	byIdx := map[int]int{}
	for _, bp := range b.p.Breakpoints {
		idx, ok := b.resolve(bp)
		if !ok {
			continue
		}
		long := bp.TTL >= time.Hour
		if at, dup := byIdx[idx]; dup {
			marks[at].long = marks[at].long || long
			continue
		}
		byIdx[idx] = len(marks)
		marks = append(marks, mark{idx: idx, long: long, label: bp.Label})
	}
	sort.SliceStable(marks, func(i, j int) bool { return marks[i].idx < marks[j].idx })

	if over := len(marks) - b.o.MaxBreakpoints; over > 0 {
		// The latest markers cover the most prompt and include the rolling one;
		// the API would answer a fifth marker with a 400.
		for _, m := range marks[:over] {
			b.warn("marker_capped", "more than %d breakpoints; dropped the one on %s", b.o.MaxBreakpoints, b.blocks[m.idx].label)
		}
		marks = marks[over:]
	}
	// A 1h entry must precede every 5m entry in a request. Raise earlier markers
	// rather than lower a later one: a longer lifetime never costs a hit, only a
	// bigger one-off write, while a shortened one costs a cold rewrite after
	// every idle gap.
	for i := len(marks) - 2; i >= 0; i-- {
		if marks[i+1].long && !marks[i].long {
			marks[i].long = true
			b.warn("ttl_promoted", "breakpoint on %s raised to 1h: a 1h breakpoint follows it and the API requires longer TTLs first", b.blocks[marks[i].idx].label)
		}
	}
	for _, m := range marks {
		if m.long {
			b.blocks[m.idx].cc = `{"type":"ephemeral","ttl":"1h"}`
		} else {
			b.blocks[m.idx].cc = `{"type":"ephemeral"}`
		}
	}
}

func (b *builder) resolve(bp core.Breakpoint) (int, bool) {
	at := bp.After
	name := bp.Label
	if name == "" {
		name = "breakpoint"
	}
	if at.Sys && at.Msg < 0 {
		// The tools segment is one cache tier; a marker inside it would cache a
		// prefix no other request shares, so it always goes on the last tool.
		if len(b.tools) == 0 {
			b.warn("marker_unresolved", "%s addresses the tools segment but the request has no tools", name)
			return 0, false
		}
		return b.tools[len(b.tools)-1], true
	}
	idx, ok := b.refs[at]
	if !ok {
		b.warn("marker_unresolved", "%s addresses a block that does not exist (sys=%v msg=%d blk=%d)", name, at.Sys, at.Msg, at.Blk)
		return 0, false
	}
	why, dropped := b.drops[at]
	if !dropped && idx >= 0 && b.blocks[idx].eligible {
		return idx, true
	}
	from := idx
	if !dropped {
		why = b.blocks[idx].why
		from = idx - 1
	}
	for j := from; j >= 0; j-- {
		if b.blocks[j].eligible {
			b.warn("marker_moved", "%s cannot sit on its block (%s); moved to %s", name, why, b.blocks[j].label)
			return j, true
		}
	}
	b.warn("marker_dropped", "%s cannot sit on its block (%s) and no earlier block can carry cache_control", name, why)
	return 0, false
}

// ---------------------------------------------------------------------------
// assembly

// reservedKeys are the top-level members the adapter owns.
var reservedKeys = map[string]bool{
	"model": true, "max_tokens": true, "stream": true, "thinking": true, "output_config": true,
	"temperature": true, "stop_sequences": true, "metadata": true, "tool_choice": true,
	"tools": true, "system": true, "messages": true,
}

func (b *builder) assemble(stream bool) ([]byte, error) {
	p, o := b.p, b.o
	if o.Warm {
		stream = false // max_tokens:0 is rejected with stream:true
	}
	maxTokens := p.Params.MaxTokens
	switch {
	case o.Warm && o.NoZeroMaxTokens:
		maxTokens = 1
	case o.Warm:
		maxTokens = 0
	case maxTokens <= 0:
		maxTokens = o.DefaultMaxTokens
	}

	var w jw
	w.putc('{')
	w.member(true, "model")
	w.str(p.Model)
	w.member(false, "max_tokens")
	w.num(maxTokens)
	if stream {
		w.lit(`,"stream":true`)
	}

	thinking, err := b.thinkingJSON(maxTokens)
	if err != nil {
		return nil, err
	}
	if thinking != nil {
		w.member(false, "thinking")
		w.raw(thinking)
	}

	if e := strings.TrimSpace(p.Params.Effort); e != "" {
		got, clamped, ok := b.info.clampEffort(e)
		switch {
		case !ok:
			b.warn("effort_dropped", "model %q has no effort control; effort %q not sent", p.Model, e)
		default:
			if clamped {
				b.warn("effort_clamped", "model %q does not accept effort %q; sent %q", p.Model, e, got)
			}
			w.lit(`,"output_config":{"effort":`)
			w.str(got)
			w.putc('}')
		}
	}

	if t := p.Params.Temperature; t != nil {
		switch {
		case math.IsNaN(*t) || math.IsInf(*t, 0):
			return nil, errors.New("anthropic: temperature is not a finite number")
		case b.info.NoSampling:
			// Removed from the family: the API answers a 400. The caller's knob is
			// a preference, the run is not worth losing over it.
			b.warn("temperature_dropped", "model %q rejects sampling parameters; temperature not sent", p.Model)
		default:
			w.member(false, "temperature")
			w.raw([]byte(strconv.FormatFloat(*t, 'g', -1, 64)))
		}
	}

	if len(p.Params.Stop) > 0 {
		var seqs []string
		for _, s := range p.Params.Stop {
			if s == "" {
				b.warn("stop_sequence_dropped", "empty stop sequence not sent")
				continue
			}
			seqs = append(seqs, s)
		}
		if len(seqs) > 0 {
			w.member(false, "stop_sequences")
			w.putc('[')
			for i, s := range seqs {
				if i > 0 {
					w.putc(',')
				}
				w.str(s)
			}
			w.putc(']')
		}
	}

	if o.UserID != "" {
		w.lit(`,"metadata":{"user_id":`)
		w.str(o.UserID)
		w.putc('}')
	}

	// tool_choice is passed through exactly as the caller set it and never
	// invented here. It is a cache-key input for the messages tier: flipping it
	// between requests (a compactor fork that asks for "none", say) makes every
	// cached message a full-price rewrite while tools and system stay warm.
	switch p.Params.ToolChoice {
	case "":
	case "auto", "none":
		if len(b.tools) == 0 {
			b.warn("tool_choice_dropped", "tool_choice %q with no tools is a 400; not sent", p.Params.ToolChoice)
			break
		}
		w.lit(`,"tool_choice":{"type":`)
		w.str(p.Params.ToolChoice)
		w.putc('}')
	default:
		return nil, fmt.Errorf("anthropic: unsupported tool_choice %q (forced tool use is rejected by current models)", p.Params.ToolChoice)
	}

	if len(b.tools) > 0 {
		w.member(false, "tools")
		w.putc('[')
		for i, idx := range b.tools {
			if i > 0 {
				w.putc(',')
			}
			b.writeBlock(&w, idx)
		}
		w.putc(']')
	}
	if len(b.system) > 0 {
		w.member(false, "system") // always an array of blocks, never a string
		w.putc('[')
		for i, idx := range b.system {
			if i > 0 {
				w.putc(',')
			}
			b.writeBlock(&w, idx)
		}
		w.putc(']')
	}
	w.member(false, "messages")
	w.putc('[')
	for i, m := range b.msgs {
		if i > 0 {
			w.putc(',')
		}
		w.lit(`{"role":`)
		w.str(m.role)
		w.lit(`,"content":[`)
		for j, idx := range m.blocks {
			if j > 0 {
				w.putc(',')
			}
			b.writeBlock(&w, idx)
		}
		w.putc(']')
		if m.clearAt != "" {
			w.lit(`,"clear_at":`)
			w.str(m.clearAt)
		}
		w.putc('}')
	}
	w.putc(']')

	if len(o.ExtraBody) > 0 {
		keys := make([]string, 0, len(o.ExtraBody))
		for k := range o.ExtraBody {
			if reservedKeys[k] {
				return nil, fmt.Errorf("anthropic: ExtraBody key %q collides with a member the adapter owns", k)
			}
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v, err := core.MarshalStable(o.ExtraBody[k])
			if err != nil {
				return nil, fmt.Errorf("anthropic: ExtraBody[%q]: %w", k, err)
			}
			w.member(false, k)
			w.raw(v)
		}
	}
	w.putc('}')
	return w.bytes(), nil
}

func (b *builder) writeBlock(w *jw, idx int) {
	rb := &b.blocks[idx]
	w.raw(rb.prefix)
	if rb.cc != "" {
		if rb.members {
			w.putc(',')
		}
		w.lit(`"cache_control":`)
		w.lit(rb.cc)
	}
	w.putc('}')
}

// thinkingJSON renders the thinking member (nil when it is omitted).
func (b *builder) thinkingJSON(maxTokens int) ([]byte, error) {
	o, p := b.o, b.p
	mode := p.Params.Thinking
	switch mode {
	case "", "off", "adaptive":
	default:
		return nil, fmt.Errorf("anthropic: unsupported thinking mode %q", mode)
	}
	switch o.BindingMode {
	case "", "drop_block", "error":
	default:
		return nil, fmt.Errorf("anthropic: unsupported binding mode %q (want drop_block or error)", o.BindingMode)
	}

	typ, budget := "", 0
	if mode == "adaptive" {
		switch {
		case !b.info.Known || b.info.Thinking == ThinkAdaptive:
			typ = "adaptive"
		case b.info.Thinking == ThinkBudget:
			budget = o.ThinkingBudget
			if budget <= 0 {
				budget = 8192
			}
			if budget >= maxTokens {
				budget = maxTokens - 1
			}
			switch {
			case o.Warm:
				// max_tokens:0 rejects thinking.type "enabled", and one token cannot
				// hold a budget. Send the warm-up without it.
				b.warn("prewarm_thinking_dropped", "model %q needs a thinking budget, which a pre-warm cannot carry", p.Model)
			case budget < 1024:
				b.warn("thinking_unsupported", "max_tokens %d leaves no room for a thinking budget on %q", maxTokens, p.Model)
			default:
				typ = "enabled"
			}
		default:
			b.warn("thinking_unsupported", "model %q has no thinking control; thinking not sent", p.Model)
		}
	}
	if typ == "" && o.BindingMode != "" {
		if b.info.AlwaysThinks {
			// Omitting thinking runs adaptive on these models, so an explicit
			// adaptive object changes nothing but carries the binding control.
			typ = "adaptive"
		} else {
			b.warn("binding_ignored", "thinking.block_binding needs thinking on; model %q has it off", p.Model)
		}
	}
	if typ == "" {
		return nil, nil
	}

	var w jw
	w.lit(`{"type":`)
	w.str(typ)
	if typ == "enabled" {
		w.lit(`,"budget_tokens":`)
		w.num(budget)
	}
	if d := o.ThinkingDisplay; d != "" {
		w.lit(`,"display":`)
		w.str(d)
	}
	if o.BindingMode != "" {
		w.lit(`,"block_binding":{"prefix_mismatch_behavior":`)
		w.str(o.BindingMode)
		w.putc('}')
	}
	w.putc('}')
	// Fixed order, so the header is stable for identical requests.
	if o.BindingMode != "" {
		b.need(BetaThinkingBinding)
	}
	if o.ThinkingDisplay == "updates" {
		b.need(BetaThinkingDisplayUpd)
	}
	return w.bytes(), nil
}

// ---------------------------------------------------------------------------
// helpers

// wireObject validates a replayable block: valid JSON, an object. It returns the
// object without its closing brace so a marker can be spliced in.
func wireObject(raw []byte) (prefix []byte, members, ok bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) < 2 || raw[0] != '{' || raw[len(raw)-1] != '}' || !json.Valid(raw) {
		return nil, false, false
	}
	inner := bytes.TrimSpace(raw[1 : len(raw)-1])
	return raw[:len(raw)-1], len(inner) > 0, true
}

func isJSONObject(raw []byte) bool {
	t := bytes.TrimSpace(raw)
	return len(t) > 0 && t[0] == '{' && json.Valid(t)
}

// blank reports text the API refuses ("text content blocks must contain
// non-whitespace text").
func blank(s string) bool { return strings.TrimSpace(s) == "" }

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
