package mock

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/core"
)

// This file turns a validated request into what the cache and the thinking
// binding see: the rendered block list (with cleared turn-scoped messages and
// dropped thinking blocks removed) and the conversation record a thinking
// signature is bound to.

// tok is the mock tokenizer: four bytes per token, at least one.
func tok(n int) int {
	if n <= 0 {
		return 1
	}
	return (n + 3) / 4
}

// hashParts hashes NUL-terminated parts and returns the first sixteen SHA-256 bytes as
// hexadecimal.
func hashParts(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// canon returns canonical JSON when possible and preserves the original bytes as text on failure.
func canon(raw []byte) string {
	if c, err := core.Canonical(raw); err == nil {
		return string(c)
	}
	return string(raw)
}

// blockText is the text a block contributes to a Msg view.
func (b aBlock) plain() string {
	switch b.typ {
	case "text":
		return b.text
	case "tool_result":
		var sb strings.Builder
		for _, c := range b.result {
			sb.WriteString(c.plain())
		}
		return sb.String()
	}
	return ""
}

// active reports whether a system message renders at index i: a turn-scoped
// message shows until the next user message and afterwards renders nothing while
// staying in the array.
func (q *aReq) active(i int) bool {
	m := q.msgs[i]
	if m.role != "system" || m.clearAt == "" {
		return true
	}
	for _, n := range q.msgs[i+1:] {
		if n.role == "user" {
			return false
		}
	}
	return true
}

// dropKey addresses a block by message and block index.
type dropKey [2]int

// explicitRequest renders the request for the cache. dropped lists thinking
// blocks the API removed from the prompt (a drop_block or a model mismatch);
// they cost no tokens and change the prefix from their position on.
func (a *AnthropicServer) explicitRequest(q *aReq, dropped map[dropKey]bool) (ExplicitRequest, *aErr) {
	ttl5, ttl1 := a.cache.cfg.TTL5m, a.cache.cfg.TTL1h
	req := ExplicitRequest{
		Model: q.model,
		Params: ExplicitParams{
			ToolChoice: q.toolChoice, Thinking: q.thinking, Effort: q.effort, SystemToggles: q.speed,
		},
	}
	mark := func(b *ExplicitBlock, cc *aCC) {
		if cc != nil {
			b.Marker, b.MarkerTTL = true, cc.duration(ttl5, ttl1)
		}
	}
	for i, t := range q.tools {
		b := ExplicitBlock{
			Tier: TierTools, Kind: "tool", Label: fmt.Sprintf("tools[%d]", i),
			Hash:   hashParts("tool", t.name, t.desc, fmt.Sprint(t.strict), string(t.schema)),
			Tokens: tok(len(t.name) + len(t.desc) + len(t.schema)),
		}
		mark(&b, t.cc)
		req.Blocks = append(req.Blocks, b)
	}
	for i, s := range q.system {
		b := ExplicitBlock{Tier: TierSystem, Kind: "text", Label: fmt.Sprintf("system[%d]", i), Hash: hashParts("sys", s.text), Tokens: tok(len(s.text))}
		mark(&b, s.cc)
		req.Blocks = append(req.Blocks, b)
	}
	lastEligible := -1
	for i, m := range q.msgs {
		if !q.active(i) {
			continue
		}
		for j, b := range m.blocks {
			if dropped[dropKey{i, j}] {
				continue
			}
			xb := ExplicitBlock{Tier: TierMessages, Kind: b.typ, Label: fmt.Sprintf("messages[%d].content[%d]", i, j)}
			switch b.typ {
			case "text":
				xb.Hash, xb.Tokens = hashParts(m.role, "text", b.text), tok(len(b.text))
			case "image":
				xb.Hash, xb.Tokens = hashParts(m.role, "image", canon(b.raw)), a.imageTokens()
			case "tool_use":
				xb.Hash, xb.Tokens = hashParts(m.role, "tool_use", b.id, b.name, canon(b.input)), tok(len(b.name)+len(b.input))
			case "tool_result":
				n := 0
				parts := []string{m.role, "tool_result", b.toolUseID, fmt.Sprint(b.isError)}
				for _, c := range b.result {
					parts = append(parts, c.typ, c.text, canon(c.raw))
					n += len(c.text)
				}
				xb.Hash, xb.Tokens = hashParts(parts...), tok(n)+b.images*a.imageTokens()
				xb.HasImage = b.images > 0
			case "thinking":
				xb.Hash = hashParts(m.role, "thinking", b.thinking, b.signature)
				xb.Tokens = tok(len(b.thinking))
				if b.thinking == "" {
					xb.Tokens = 16 // the reasoning lives in the signature
				}
			case "redacted_thinking":
				xb.Hash, xb.Tokens = hashParts(m.role, "redacted", b.signature), tok(len(b.signature)/2)
			default:
				xb.Hash, xb.Tokens = hashParts(m.role, b.typ, canon(b.raw)), tok(len(b.raw))
			}
			mark(&xb, b.cc)
			req.Blocks = append(req.Blocks, xb)
			if m.role != "system" && !b.isThinking() && (b.typ != "tool_result" || len(b.result) > 0) {
				lastEligible = len(req.Blocks) - 1
			}
		}
	}
	// Top-level automatic caching: one breakpoint on the last eligible block. It
	// uses a slot, and an explicit marker on that block with a different TTL is a
	// 400 (with the same TTL the automatic one is a no-op).
	if q.topCC != nil && lastEligible >= 0 {
		b := &req.Blocks[lastEligible]
		d := q.topCC.duration(ttl5, ttl1)
		switch {
		case b.Marker && b.MarkerTTL != d:
			return req, badReq("cache_control: the top-level cache_control ttl differs from the explicit marker on the last block")
		case !b.Marker:
			b.Marker, b.MarkerTTL = true, d
		}
	}
	return req, nil
}

// imageTokens uses the positive configured synthetic image token count or defaults to 1000.
func (a *AnthropicServer) imageTokens() int {
	if a.acfg.ImageTokens > 0 {
		return a.acfg.ImageTokens
	}
	return 1000
}

// ---------------------------------------------------------------------------
// preserved thinking

// The signature of a thinking block records the model and the conversation
// record before it (tools, system, every earlier message, and earlier blocks of
// its own message), plus the thinking text. Earlier thinking blocks are not part
// of the record: removing them from the front of the history is allowed, which
// is also why the mock does not chain signatures (a documented simplification:
// removing a block from the middle is not detected).

type recorder struct {
	base string // tools + system
	rec  string // record before the current message
}

// canonBlock canonicalizes a mock cache record after removing cache-control markers so marker
// movement does not change the key.
func canonBlock(b aBlock) string {
	// cache_control never enters the record: markers can move freely.
	return canon(stripCC(b.raw))
}

// stripCC removes top-level cache_control from parseable JSON and returns stable encoding,
// preserving original input on failure.
func stripCC(raw []byte) []byte {
	m, err := parseObject(raw)
	if err != nil {
		return raw
	}
	delete(m, "cache_control")
	out, err := core.MarshalStable(m)
	if err != nil {
		return raw
	}
	return out
}

// canonMsg hashes role, thinking-clear boundary, and canonical nonthinking blocks for mock
// conversation binding.
func canonMsg(m aMsg) string {
	var parts []string
	parts = append(parts, m.role, m.clearAt)
	for _, b := range m.blocks {
		if b.isThinking() {
			continue
		}
		parts = append(parts, canonBlock(b))
	}
	return hashParts(parts...)
}

// baseRecord hashes tools as an order-independent set and system blocks in order for mock
// signature binding.
func (q *aReq) baseRecord() string {
	var tools []string
	for _, t := range q.tools {
		// Tools are bound as a name-keyed set: reordering is not an edit.
		tools = append(tools, hashParts(t.name, t.desc, fmt.Sprint(t.strict), canon(t.schema)))
	}
	sort.Strings(tools)
	var sys []string
	for _, s := range q.system {
		sys = append(sys, canonBlock(s))
	}
	return hashParts(strings.Join(tools, ","), strings.Join(sys, ","))
}

// recordBefore returns the record a thinking block at (msg i, block j) is bound
// to.
func (q *aReq) recordsBefore() func(i, j int) string {
	base := q.baseRecord()
	recs := make([]string, len(q.msgs)+1)
	recs[0] = base
	for i, m := range q.msgs {
		recs[i+1] = hashParts(recs[i], canonMsg(m))
	}
	return func(i, j int) string {
		parts := []string{recs[i]}
		if i < len(q.msgs) {
			for _, b := range q.msgs[i].blocks[:j] {
				if !b.isThinking() {
					parts = append(parts, canonBlock(b))
				}
			}
		}
		return hashParts(parts...)
	}
}

// signature layout: sig1.<model tag>.<record>.<mac>. The mac covers the thinking
// text, so an edited block is distinguishable from a block from another
// conversation.
func (a *AnthropicServer) sign(model, record, text string) string {
	tag := modelTag(model)
	return fmt.Sprintf("sig1.%s.%s.%s", tag, record[:24], a.mac(tag, record[:24], text))
}

// mac derives a keyed signature over the tag, record, and text and keeps its first 16 characters.
func (a *AnthropicServer) mac(tag, record, text string) string {
	return hashParts(a.acfg.SignatureKey, tag, record, text)[:16]
}

// modelTag returns an eight-character hash tag shared by case variants of a model name.
func modelTag(model string) string {
	return hashParts("model", strings.ToLower(model))[:8]
}

type sigCheck int

const (
	sigOK sigCheck = iota
	sigTampered
	sigOtherModel
	sigOtherConversation
)

// verify checks mock signature integrity, model binding, and conversation binding; record must
// contain at least 24 bytes.
func (a *AnthropicServer) verify(sig, model, record, text string) sigCheck {
	parts := strings.Split(sig, ".")
	if len(parts) != 4 || parts[0] != "sig1" || parts[3] != a.mac(parts[1], parts[2], text) {
		return sigTampered
	}
	if parts[1] != modelTag(model) {
		return sigOtherModel
	}
	if parts[2] != record[:24] {
		return sigOtherConversation
	}
	return sigOK
}

// xform is one input_transformations entry.
type xform struct {
	Type   string `json:"type"`
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type bindingOutcome struct {
	err     *aErr
	dropped map[dropKey]bool
	xforms  []xform
}

// checkBinding runs the preserved-thinking checks on every replayed thinking
// block: the model check first (a block minted by another model is dropped
// silently), then the prefix check (the conversation before the block must be
// what it was when the block was produced).
//
// Whether a prefix mismatch is an error, a drop or merely recorded follows the
// docs: on an enforced request the default is a 400; thinking.block_binding
// selects "error" or "drop_block", and setting it opts any request in.
func (a *AnthropicServer) checkBinding(q *aReq) bindingOutcome {
	out := bindingOutcome{dropped: map[dropKey]bool{}}
	runs := q.rules.binding || a.acfg.EnforceThinkingBinding
	enforced := a.acfg.EnforceThinkingBinding || (q.rules.binding && q.bindingSet)
	header := q.betas[betaBinding]
	before := q.recordsBefore()
	dropRest := false
	for i, m := range q.msgs {
		for j, b := range m.blocks {
			if !b.isThinking() {
				continue
			}
			path := fmt.Sprintf("messages.%d.content.%d", i, j)
			text := b.thinking
			switch a.verify(b.signature, q.model, before(i, j), text) {
			case sigOK:
				if dropRest {
					out.dropped[dropKey{i, j}] = true
					out.xforms = append(out.xforms, xform{"thinking_dropped", path, "prefix_binding_mismatch"})
				}
			case sigTampered:
				if !runs {
					continue
				}
				if b.typ == "thinking" && strings.HasPrefix(b.signature, "sig1.") {
					out.err = badReq("`thinking` or `redacted_thinking` blocks in the latest assistant message cannot be modified. These blocks must remain as they were in the original response.")
				} else {
					out.err = badReq("%s: Invalid `signature` in `thinking` block.", path)
				}
				return out
			case sigOtherModel:
				out.dropped[dropKey{i, j}] = true
				out.xforms = append(out.xforms, xform{"thinking_dropped", path, "model_binding_mismatch"})
			case sigOtherConversation:
				if !runs {
					continue
				}
				switch {
				case !enforced:
					out.xforms = append(out.xforms, xform{"thinking_mismatch_allowed", path, "prefix_binding_mismatch"})
				case q.bindingMode == "drop_block":
					dropRest = true
					out.dropped[dropKey{i, j}] = true
					out.xforms = append(out.xforms, xform{"thinking_dropped", path, "prefix_binding_mismatch"})
				default:
					msg := fmt.Sprintf("%s: Invalid `signature` in `thinking` block. The block is bound to a different conversation. Remove the block, or set `thinking.block_binding.prefix_mismatch_behavior` to \"drop_block\".", path)
					if !header {
						msg += " That setting requires the `thinking-binding-controls-2026-08-01` value in the `anthropic-beta` header."
					}
					out.err = &aErr{status: 400, typ: "invalid_request_error", msg: msg}
					return out
				}
			}
		}
	}
	return out
}

// parseObject decodes the first JSON object while preserving numeric spelling with json.Number; it
// does not check for trailing values.
func parseObject(raw []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	return m, nil
}

// textRaw is the block form of string content: the API treats "hi" and
// [{"type":"text","text":"hi"}] as the same message, so the record must too.
func textRaw(s string) json.RawMessage {
	b, _ := json.Marshal(struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}{"text", s})
	return b
}
