package kv

import (
	"github.com/reee344/sleipnir/internal/core"
)

// RendererVersion identifies the prompt layout. Bump it whenever Render would
// produce different bytes for the same stack, so logged prompts and training data
// can be tied to the layout the model was actually served with.
const RendererVersion = "sleipnir-kv/2"

// RenderOpts are the per-request inputs to Render that are not part of the
// agent's persistent stack.
type RenderOpts struct {
	// Hot is the always-fresh tail (board view, mailbox headers) in HotInline
	// mode: rendered after the last cache breakpoint and never persisted. In the
	// other modes the hot text lives in the thread and Hot is ignored.
	Hot []core.Block
	// Caps describes the target provider (Caps.HotMode is the resolved mode).
	Caps   Caps
	Policy Policy
	Params core.Params
	// CacheKey is the provider routing key for this request, if the provider
	// uses one. The scheduler shards it to spread load across cache hosts.
	CacheKey string
	// StripThinking omits stored thinking blocks (used on the first request
	// after a declared rebase, where their bindings are void by construction).
	StripThinking bool
	// PrevRolling is where the previous request of this agent put its rolling
	// marker, when nothing has been rebased since (so the blocks up to there are
	// byte-identical). The planner uses it to keep that cache entry within the
	// provider's lookback window of the new one.
	PrevRolling *core.BlockRef
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
	// Caps are the provider capabilities the prompt was rendered for.
	Caps Caps
	// Rolling is the block that carries the rolling marker, when one was planned.
	Rolling *core.BlockRef
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
//	tail:     G6 hot blocks appended to the final user message (HotInline only)
//
// Turn-scoped system messages (HotTurnScoped): a thread turn with role system is
// rendered as its own message with ClearAt set, kept in the array for every later
// request byte for byte. It never carries a cache marker (the rolling marker
// moves to the block before it). Without HotTurnScoped such a turn is folded into
// the neighbouring user message like any other user text.
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

	turnScoped := o.Caps.HotMode == HotTurnScoped
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
		role := tr.Role
		if role == core.RoleSystem && !turnScoped {
			role = core.RoleUser // adapters without the feature would fold it anyway
		}
		if role == core.RoleSystem {
			msgs = append(msgs, core.Message{Role: core.RoleSystem, ClearAt: ClearAtNextUser, Blocks: blocks, Turn: tr.ID})
			continue
		}
		if n := len(msgs); n > 0 && msgs[n-1].Role == role {
			// Defensive merge: the agent loop keeps roles alternating, but a
			// stray double user turn (a retry after a failed first request, a
			// retained region that opens with two user turns) must not reach the
			// provider as two adjacent messages. Appending to the previous message
			// keeps the render append-only.
			msgs[n-1].Blocks = append(msgs[n-1].Blocks, blocks...)
			continue
		}
		msgs = append(msgs, core.Message{Role: role, Blocks: blocks, Turn: tr.ID})
	}

	// Ephemeral hot tail, after the last persistent block: the planner puts the
	// rolling marker before it and it is billed at the uncached rate every
	// request. It is only appended in HotInline mode.
	if len(o.Hot) > 0 && o.Caps.HotMode == HotInline {
		hot := make([]core.Block, len(o.Hot))
		for k, b := range o.Hot {
			b.Ephemeral = true
			hot[k] = b
		}
		last := len(msgs) - 1
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
	bps := planBreakpoints(p, refs, sections, o)
	p.Breakpoints = bps
	var rolling *core.BlockRef
	for i := range sections {
		for _, b := range bps {
			if b.Label == sections[i].Name {
				sections[i].Breakpoint = true
			}
		}
	}
	for _, b := range bps {
		if b.Label == "thread" {
			r := b.After
			rolling = &r
		}
	}
	return &Rendered{Prompt: p, Sections: sections, ThreadFrom: threadFrom, PrefixKey: s.PrefixKey(), Caps: o.Caps, Rolling: rolling}
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
