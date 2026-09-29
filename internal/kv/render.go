package kv

import (
	"github.com/reee344/sleipnir/internal/core"
)

// RenderOpts are the per-request inputs to Render that are not part of the
// agent's persistent stack.
type RenderOpts struct {
	// Hot is the always-fresh tail (board view, mailbox headers). It is
	// rendered after the last cache breakpoint and never persisted.
	Hot []core.Block
	// Caps describes the target provider.
	Caps   Caps
	Policy Policy
	Params core.Params
	// CacheKey is the provider routing key for this request, if the provider
	// uses one. The scheduler shards it to spread load across cache hosts.
	CacheKey string
	// StripThinking omits stored thinking blocks (used on the first request
	// after a declared rebase, where their bindings are void by construction).
	StripThinking bool
	// Est sizes layers for breakpoint planning.
	Est core.Estimator
}

// Section reports where a layer sits in a rendered prompt, for cache
// accounting and the inspector.
type Section struct {
	Name   string
	Hash   core.Hash
	Tokens int
	// Breakpoint is true when the planner placed a cache marker after it.
	Breakpoint bool
}

// Rendered bundles a prompt with the layer metadata that produced it.
type Rendered struct {
	Prompt   *core.Prompt
	Sections []Section
	// ThreadFrom is the message index where thread turns begin.
	ThreadFrom int
	// PrefixKey identifies the shared trie node (see Stack.PrefixKey).
	PrefixKey core.Hash
}

// Render turns a stack snapshot into a provider-neutral prompt.
//
// Layout, in prefix order:
//
//	system:   G0 constitution
//	tools:    frozen universal tool list (kept in Prompt.Tools; wire order is
//	          decided by the adapter, which must put tools before system)
//	msg[0]:   user, blocks [G1 shared][G2 role][G3 notes][G4 spine] + first
//	          thread turn if it is a user turn
//	msg[1..]: remaining thread turns
//	tail:     G6 hot blocks appended to the final user message
func Render(s *Stack, o RenderOpts) *Rendered {
	if o.Est == nil {
		o.Est = core.NewBytesEstimator()
	}
	p := &core.Prompt{
		Model:    s.Model,
		Tools:    s.Tools,
		Params:   o.Params,
		CacheKey: o.CacheKey,
	}
	if !s.Const.Empty() {
		p.System = []core.Block{core.Text(s.Const.Text())}
	}

	type mark struct {
		name string
		ref  core.BlockRef
	}
	var marks []mark
	var sections []Section

	// Message 0: the pinned preamble.
	pre := core.Message{Role: core.RoleUser}
	addLayer := func(name string, l *Layer) {
		if l.Empty() {
			return
		}
		pre.Blocks = append(pre.Blocks, core.Text(l.Text()))
		marks = append(marks, mark{name, core.BlockRef{Msg: 0, Blk: len(pre.Blocks) - 1}})
		sections = append(sections, Section{Name: name, Hash: l.Hash(), Tokens: l.Tokens(o.Est)})
	}
	addLayer("shared", s.Shared)
	addLayer("role", s.RoleL)
	addLayer("notes", s.Notes)
	addLayer("spine", s.Spine)

	msgs := []core.Message{pre}
	turns := s.Thread.Turns
	i := 0
	// A thread that opens with a user turn folds into the preamble so message
	// roles alternate without a synthetic assistant acknowledgement.
	if len(turns) > 0 && turns[0].Role == core.RoleUser {
		msgs[0].Blocks = append(msgs[0].Blocks, renderBlocks(turns[0], o)...)
		msgs[0].Turn = turns[0].ID
		i = 1
	}
	threadFrom := 1
	for ; i < len(turns); i++ {
		tr := turns[i]
		blocks := renderBlocks(tr, o)
		if len(blocks) == 0 {
			blocks = []core.Block{core.Text("(no output)")}
		}
		if n := len(msgs); n > 0 && msgs[n-1].Role == tr.Role && n > 1 {
			// Defensive merge: the agent loop keeps roles alternating, but a
			// stray double user turn must not reach the provider as two messages.
			msgs[n-1].Blocks = append(msgs[n-1].Blocks, blocks...)
			continue
		}
		msgs = append(msgs, core.Message{Role: tr.Role, Blocks: blocks, Turn: tr.ID})
	}

	// The rolling breakpoint sits after the last persistent block; the hot tail
	// follows it and is billed at the uncached rate every request.
	last := len(msgs) - 1
	rolling := core.BlockRef{Msg: last, Blk: len(msgs[last].Blocks) - 1}
	if len(o.Hot) > 0 {
		hot := make([]core.Block, len(o.Hot))
		for k, b := range o.Hot {
			b.Ephemeral = true
			hot[k] = b
		}
		if msgs[last].Role == core.RoleUser {
			msgs[last].Blocks = append(msgs[last].Blocks, hot...)
		} else {
			msgs = append(msgs, core.Message{Role: core.RoleUser, Blocks: hot})
		}
	}
	p.Messages = msgs

	refs := map[string]core.BlockRef{}
	for _, m := range marks {
		refs[m.name] = m.ref
	}
	refs["thread"] = rolling
	bps := planBreakpoints(p, refs, sections, o)
	p.Breakpoints = bps
	for i := range sections {
		for _, b := range bps {
			if b.Label == sections[i].Name {
				sections[i].Breakpoint = true
			}
		}
	}
	return &Rendered{Prompt: p, Sections: sections, ThreadFrom: threadFrom, PrefixKey: s.PrefixKey()}
}

// renderBlocks converts a turn's blocks to wire-ready blocks, dropping thinking
// blocks that cannot or should not be replayed.
func renderBlocks(tr core.Turn, o RenderOpts) []core.Block {
	out := make([]core.Block, 0, len(tr.Blocks))
	for _, b := range tr.Blocks {
		switch b.Kind {
		case core.BlockThinking, core.BlockRedactedThinking:
			if o.StripThinking || !o.Caps.ReplayThinking || b.WireFormat != o.Caps.Dialect || len(b.Wire) == 0 {
				continue
			}
		}
		out = append(out, b)
	}
	return out
}
