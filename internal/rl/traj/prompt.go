package traj

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/core"
)

// Mismatch kinds reported by [Run.Verify] and returned (as *Mismatch) by
// [Run.Prompt].
const (
	KindLog        = "log"        // the event log itself is damaged: unparseable or duplicated records
	KindManifest   = "manifest"   // a request has no usable manifest
	KindBase       = "base"       // a manifest's base request is unknown, cyclic or shorter than the manifest keeps
	KindBlob       = "blob"       // a blob the prompt needs is missing, corrupt or not the expected shape
	KindWire       = "wire"       // the parts do not hash to the recorded wire hash
	KindCompletion = "completion" // the response's completion blob is missing or is not an assistant turn
	KindTokens     = "tokens"     // the response's token trace blob is missing or unparseable
	KindResponse   = "response"   // a response that names no logged request
)

// Mismatch is one way the log fails the replay check: what was recorded does not
// reproduce what the model saw. It implements error.
type Mismatch struct {
	Req    string // the request concerned; empty for log-level problems
	Kind   string
	Detail string
}

// Error formats a trajectory mismatch with request identity when available.
func (m Mismatch) Error() string {
	if m.Req == "" {
		return "traj: " + m.Kind + ": " + m.Detail
	}
	return "traj: request " + m.Req + ": " + m.Kind + ": " + m.Detail
}

// wireMsg is the stored shape of a message blob: the model-visible part of a
// core.Message (the turn id is bookkeeping and never stored).
type wireMsg struct {
	Role   core.Role    `json:"role"`
	Blocks []core.Block `json:"blocks"`
}

// msgHashes resolves the full message hash list of a request through its Base
// chain, without touching any blob. Results are memoised per request, so a whole
// agent's chain is resolved in linear time.
func (r *Run) msgHashes(id string) ([]core.Hash, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.msgHashesLocked(id)
}

func (r *Run) msgHashesLocked(id string) ([]core.Hash, error) {
	if h, ok := r.msgMemo[id]; ok {
		return h, nil
	}
	if err, ok := r.msgErr[id]; ok {
		return nil, err
	}
	// Walk down to the nearest memoised ancestor (or a request without a base),
	// then unwind. Iterative: chains are as long as the run, and a hostile log may
	// contain a cycle.
	var chain []*reqInfo
	seen := map[string]bool{}
	var base []core.Hash
	cur := id
	var failure error
	for {
		if h, ok := r.msgMemo[cur]; ok {
			base = h
			break
		}
		q, ok := r.reqs[cur]
		if !ok {
			failure = &Mismatch{Req: id, Kind: KindBase, Detail: fmt.Sprintf("base request %q is not in the log", cur)}
			break
		}
		if seen[cur] {
			failure = &Mismatch{Req: id, Kind: KindBase, Detail: fmt.Sprintf("base chain loops at %q", cur)}
			break
		}
		seen[cur] = true
		chain = append(chain, q)
		if q.man.Base == "" {
			break
		}
		cur = q.man.Base
	}
	if failure != nil {
		r.msgErr[id] = failure
		return nil, failure
	}
	for i := len(chain) - 1; i >= 0; i-- {
		q := chain[i]
		msgs, err := q.man.Messages(base)
		if err != nil {
			mm := &Mismatch{Req: q.id, Kind: KindBase, Detail: err.Error()}
			for _, c := range chain[:i+1] {
				r.msgErr[c.id] = mm
			}
			return nil, mm
		}
		r.msgMemo[q.id] = msgs
		base = msgs
	}
	return base, nil
}

// Prompt returns the exact model-visible prompt of a model.request (model, tools,
// system blocks and messages), rebuilt from its manifest and verified against the
// recorded wire hash. Breakpoints, cache key and sampling parameters are request
// mechanics and are not part of it.
//
// The result shares immutable decoded content with the Run: callers must treat it
// as read-only below the top-level slices (copy a Block before changing it).
// Errors are *[Mismatch] values.
func (r *Run) Prompt(req string) (*core.Prompt, error) {
	q, ok := r.reqs[req]
	if !ok {
		return nil, Mismatch{Req: req, Kind: KindManifest, Detail: "no such request in the log"}
	}
	p, mm := r.expand(q)
	if mm != nil {
		return nil, *mm
	}
	return p, nil
}

func (r *Run) expand(q *reqInfo) (*core.Prompt, *Mismatch) {
	m := q.man
	if q.noManifest || m.Wire == "" {
		return nil, &Mismatch{Req: q.id, Kind: KindManifest, Detail: "request has no manifest, so its prompt cannot be rebuilt"}
	}
	msgs, err := r.msgHashes(q.id)
	if err != nil {
		var mm *Mismatch
		if errors.As(err, &mm) {
			return nil, mm
		}
		return nil, &Mismatch{Req: q.id, Kind: KindBase, Detail: err.Error()}
	}
	if got := core.WireHash(m.Model, m.Tools, m.System, msgs); got != m.Wire {
		return nil, &Mismatch{Req: q.id, Kind: KindWire, Detail: fmt.Sprintf("manifest says %s, its parts hash to %s", m.Wire.Short(), got.Short())}
	}
	if q.wire != "" && q.wire != m.Wire {
		return nil, &Mismatch{Req: q.id, Kind: KindWire, Detail: fmt.Sprintf("event wire_hash %s differs from manifest wire %s", q.wire.Short(), m.Wire.Short())}
	}
	p := &core.Prompt{Model: m.Model}
	if m.Tools != "" {
		tools, err := r.toolsOf(m.Tools)
		if err != nil {
			return nil, &Mismatch{Req: q.id, Kind: KindBlob, Detail: "tools: " + err.Error()}
		}
		p.Tools = tools
	}
	for _, h := range m.System {
		b, err := r.systemOf(h)
		if err != nil {
			return nil, &Mismatch{Req: q.id, Kind: KindBlob, Detail: "system: " + err.Error()}
		}
		p.System = append(p.System, b)
	}
	p.Messages = make([]core.Message, len(msgs))
	for i, h := range msgs {
		msg, err := r.messageOf(h)
		if err != nil {
			return nil, &Mismatch{Req: q.id, Kind: KindBlob, Detail: fmt.Sprintf("message %d: %v", i, err)}
		}
		// Fresh Blocks slice per prompt so callers cannot append into the cache.
		msg.Blocks = append([]core.Block(nil), msg.Blocks...)
		p.Messages[i] = msg
	}
	return p, nil
}

func (r *Run) toolsOf(h core.Hash) ([]core.ToolSpec, error) {
	r.mu.Lock()
	t, ok := r.toolsMem[h]
	r.mu.Unlock()
	if ok {
		return append([]core.ToolSpec(nil), t...), nil
	}
	b, err := r.blob(h)
	if err != nil {
		return nil, err
	}
	var specs []core.ToolSpec
	if err := json.Unmarshal(b, &specs); err != nil {
		return nil, fmt.Errorf("blob %s is not a tool list: %w", h.Short(), err)
	}
	r.mu.Lock()
	r.account(len(b))
	r.toolsMem[h] = specs
	r.mu.Unlock()
	return append([]core.ToolSpec(nil), specs...), nil
}

// systemOf loads and decodes a system block by hash, memoizing it under the run's cache lock;
// missing or malformed blobs return an error.
func (r *Run) systemOf(h core.Hash) (core.Block, error) {
	r.mu.Lock()
	blk, ok := r.sysMem[h]
	r.mu.Unlock()
	if ok {
		return blk, nil
	}
	b, err := r.blob(h)
	if err != nil {
		return core.Block{}, err
	}
	if err := json.Unmarshal(b, &blk); err != nil {
		return core.Block{}, fmt.Errorf("blob %s is not a block: %w", h.Short(), err)
	}
	r.mu.Lock()
	r.account(len(b))
	r.sysMem[h] = blk
	r.mu.Unlock()
	return blk, nil
}

func (r *Run) messageOf(h core.Hash) (core.Message, error) {
	r.mu.Lock()
	m, ok := r.msgMem[h]
	r.mu.Unlock()
	if ok {
		return m, nil
	}
	b, err := r.blob(h)
	if err != nil {
		return core.Message{}, err
	}
	var w wireMsg
	if err := json.Unmarshal(b, &w); err != nil {
		return core.Message{}, fmt.Errorf("blob %s is not a message: %w", h.Short(), err)
	}
	if w.Role == "" {
		return core.Message{}, fmt.Errorf("blob %s is not a message: no role", h.Short())
	}
	m = core.Message{Role: w.Role, Blocks: w.Blocks}
	r.mu.Lock()
	r.account(len(b))
	r.msgMem[h] = m
	r.mu.Unlock()
	return m, nil
}

// completionOf loads the assistant turn a response recorded.
func (r *Run) completionOf(s *respInfo) (core.Turn, *Mismatch) {
	var t core.Turn
	if s.completion == "" {
		return t, &Mismatch{Req: s.id, Kind: KindCompletion, Detail: "response has no completion blob"}
	}
	b, err := r.blob(s.completion)
	if err != nil {
		return t, &Mismatch{Req: s.id, Kind: KindCompletion, Detail: err.Error()}
	}
	if err := json.Unmarshal(b, &t); err != nil {
		return t, &Mismatch{Req: s.id, Kind: KindCompletion, Detail: "not a turn: " + err.Error()}
	}
	if t.Role != core.RoleAssistant {
		return t, &Mismatch{Req: s.id, Kind: KindCompletion, Detail: fmt.Sprintf("completion has role %q, want assistant", t.Role)}
	}
	return t, nil
}

// traceOf loads a response's token trace. A response without one returns
// (nil, nil, false): most endpoints cannot provide it. present reports whether
// the response referenced a trace at all.
func (r *Run) traceOf(s *respInfo) (tr *core.TokenTrace, mm *Mismatch, present bool) {
	if s.tokens == "" {
		return nil, nil, false
	}
	b, err := r.blob(s.tokens)
	if err != nil {
		return nil, &Mismatch{Req: s.id, Kind: KindTokens, Detail: err.Error()}, true
	}
	var t core.TokenTrace
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, &Mismatch{Req: s.id, Kind: KindTokens, Detail: "not a token trace: " + err.Error()}, true
	}
	return &t, nil, true
}

// Verify is the replay check the training data rests on. For every request in
// the log it rebuilds the prompt from the manifest chain, checks every blob
// against its hash and the whole against the wire hash, and for every response
// checks that the completion blob is an assistant turn and that a referenced
// token trace parses. An empty result means the log reproduces what the models
// saw. The result is computed once and cached.
func (r *Run) Verify() []Mismatch {
	r.verifyOnce.Do(func() {
		out := append([]Mismatch(nil), r.issues...)
		for _, q := range r.reqOrder {
			if _, mm := r.expand(q); mm != nil {
				out = append(out, *mm)
			}
			s, ok := r.resps[q.id]
			if !ok {
				continue
			}
			if _, mm := r.completionOf(s); mm != nil {
				out = append(out, *mm)
			}
			if _, mm, _ := r.traceOf(s); mm != nil {
				out = append(out, *mm)
			}
		}
		for _, s := range r.respOrder {
			if _, ok := r.reqs[s.id]; !ok {
				out = append(out, Mismatch{Req: s.id, Kind: KindResponse, Detail: fmt.Sprintf("seq %d: response to a request that is not in the log", s.seq)})
			}
		}
		r.verified = out
	})
	return append([]Mismatch(nil), r.verified...)
}

// mismatchSummary renders the first n mismatches for error messages.
func mismatchSummary(ms []Mismatch, n int) string {
	var parts []string
	for i, m := range ms {
		if i == n {
			parts = append(parts, fmt.Sprintf("... and %d more", len(ms)-n))
			break
		}
		parts = append(parts, m.Error())
	}
	return strings.Join(parts, "; ")
}
