package core

import (
	"encoding/json"
	"fmt"
	"strings"
)

// TokenTrace is what a self-hosted endpoint returned for one call: the token
// ids of the prompt it saw and of the completion it sampled, with the sampling
// logprobs. It is only recorded when the endpoint provides it; nothing here is
// ever reconstructed by re-tokenizing.
type TokenTrace struct {
	Tokenizer     string    `json:"tokenizer,omitempty"`
	ModelVersion  string    `json:"model_version,omitempty"`
	PromptIDs     []int32   `json:"prompt_ids,omitempty"`
	CompletionIDs []int32   `json:"completion_ids,omitempty"`
	Logprobs      []float32 `json:"logprobs,omitempty"` // one per completion id
}

// Consistent reports whether the trace is internally coherent: logprobs, when
// present, line up one to one with the completion ids.
func (t *TokenTrace) Consistent() bool {
	return t != nil && len(t.CompletionIDs) > 0 && (len(t.Logprobs) == 0 || len(t.Logprobs) == len(t.CompletionIDs))
}

// Manifest stores one request's prompt as content hashes, so a swarm whose
// agents share a large prefix logs that prefix once and each request costs a few
// dozen bytes. Messages are delta-encoded against the same agent's previous
// request: prompts are append-only between declared rebases, so a request is
// "the previous messages, minus a changed tail, plus a few new ones".
type Manifest struct {
	Model  string `json:"model"`
	Tools  Hash   `json:"tools,omitempty"`  // blob: the tools array, canonical JSON
	System []Hash `json:"system,omitempty"` // one blob per system block
	Base   string `json:"base,omitempty"`   // request id whose message list this extends
	Keep   int    `json:"keep,omitempty"`   // leading messages taken from Base
	Add    []Hash `json:"add,omitempty"`    // message blobs following them
	// Wire is the hash of the complete model-visible prompt (see WireHash).
	Wire Hash `json:"wire"`
}

// ManifestState is what the next request's delta is computed against.
type ManifestState struct {
	Req   string
	Msgs  []Hash
	Tools Hash
}

// BlobPutter stores bytes and returns their content hash.
type BlobPutter func(b []byte) (Hash, error)

// BlobGetter fetches stored bytes.
type BlobGetter func(h Hash) ([]byte, error)

// wireMsg is the model-visible part of a message. The turn id is bookkeeping and
// deliberately excluded so identical content shares one blob across agents.
type wireMsg struct {
	Role   Role    `json:"role"`
	Blocks []Block `json:"blocks"`
}

func messageJSON(m Message) ([]byte, error) {
	return MarshalStable(wireMsg{Role: m.Role, Blocks: m.Blocks})
}

// WireHash is the identity of what the model sees: model, tools, system and
// messages. Breakpoints, cache keys and sampling parameters are request
// mechanics and are excluded. It is derived from the part hashes, so it can be
// recomputed from a manifest without reading any blob.
func WireHash(model string, tools Hash, system, msgs []Hash) Hash {
	var b strings.Builder
	b.WriteString(model)
	b.WriteByte('\n')
	b.WriteString(string(tools))
	b.WriteByte('\n')
	for _, h := range system {
		b.WriteString(string(h))
		b.WriteByte(',')
	}
	b.WriteByte('\n')
	for _, h := range msgs {
		b.WriteString(string(h))
		b.WriteByte(',')
	}
	return HashString(b.String())
}

// BuildManifest hashes and stores the parts of p that are new and returns the
// manifest plus the state to delta the next request against. Only messages after
// the longest common prefix with prev are stored.
func BuildManifest(p *Prompt, prev ManifestState, req string, put BlobPutter) (Manifest, ManifestState, error) {
	m := Manifest{Model: p.Model}
	st := ManifestState{Req: req}

	if len(p.Tools) > 0 {
		tb, err := MarshalStable(p.Tools)
		if err != nil {
			return m, st, err
		}
		th := HashBytes(tb)
		if th != prev.Tools {
			if _, err := put(tb); err != nil {
				return m, st, err
			}
		}
		m.Tools, st.Tools = th, th
	}
	for _, b := range p.System {
		sb, err := MarshalStable(b)
		if err != nil {
			return m, st, err
		}
		h, err := put(sb)
		if err != nil {
			return m, st, err
		}
		m.System = append(m.System, h)
	}

	hashes := make([]Hash, len(p.Messages))
	blobs := make([][]byte, len(p.Messages))
	for i, msg := range p.Messages {
		b, err := messageJSON(msg)
		if err != nil {
			return m, st, err
		}
		blobs[i], hashes[i] = b, HashBytes(b)
	}
	keep := 0
	for keep < len(hashes) && keep < len(prev.Msgs) && hashes[keep] == prev.Msgs[keep] {
		keep++
	}
	if prev.Req != "" && keep > 0 {
		m.Base, m.Keep = prev.Req, keep
	} else {
		keep = 0
	}
	for i := keep; i < len(hashes); i++ {
		if _, err := put(blobs[i]); err != nil {
			return m, st, err
		}
		m.Add = append(m.Add, hashes[i])
	}
	st.Msgs = hashes
	m.Wire = WireHash(p.Model, m.Tools, m.System, hashes)
	return m, st, nil
}

// Messages resolves the manifest's full message hash list given the message
// list of its base request.
func (m Manifest) Messages(base []Hash) ([]Hash, error) {
	if m.Keep > len(base) {
		return nil, fmt.Errorf("manifest keeps %d messages of a base that has %d", m.Keep, len(base))
	}
	out := make([]Hash, 0, m.Keep+len(m.Add))
	out = append(out, base[:m.Keep]...)
	out = append(out, m.Add...)
	return out, nil
}

// Expand rebuilds the model-visible prompt (model, tools, system, messages) from
// a manifest, its base's message list and a blob getter, and verifies every
// part against its hash and the whole against the wire hash. The returned
// prompt has no breakpoints, cache key or parameters.
func (m Manifest) Expand(base []Hash, get BlobGetter) (*Prompt, []Hash, error) {
	msgs, err := m.Messages(base)
	if err != nil {
		return nil, nil, err
	}
	if got := WireHash(m.Model, m.Tools, m.System, msgs); got != m.Wire {
		return nil, nil, fmt.Errorf("wire hash mismatch: manifest says %s, parts give %s", m.Wire.Short(), got.Short())
	}
	p := &Prompt{Model: m.Model}
	fetch := func(h Hash) ([]byte, error) {
		b, err := get(h)
		if err != nil {
			return nil, fmt.Errorf("blob %s: %w", h.Short(), err)
		}
		if HashBytes(b) != h {
			return nil, fmt.Errorf("blob %s is corrupt", h.Short())
		}
		return b, nil
	}
	if m.Tools != "" {
		b, err := fetch(m.Tools)
		if err != nil {
			return nil, nil, err
		}
		if err := json.Unmarshal(b, &p.Tools); err != nil {
			return nil, nil, err
		}
	}
	for _, h := range m.System {
		b, err := fetch(h)
		if err != nil {
			return nil, nil, err
		}
		var blk Block
		if err := json.Unmarshal(b, &blk); err != nil {
			return nil, nil, err
		}
		p.System = append(p.System, blk)
	}
	for _, h := range msgs {
		b, err := fetch(h)
		if err != nil {
			return nil, nil, err
		}
		var w wireMsg
		if err := json.Unmarshal(b, &w); err != nil {
			return nil, nil, err
		}
		p.Messages = append(p.Messages, Message{Role: w.Role, Blocks: w.Blocks})
	}
	return p, msgs, nil
}
