package kv

import (
	"sort"
	"time"

	"github.com/anemos-labs/sleipnir/internal/core"
)

// Caps is what the renderer needs to know about the target provider. Adapters
// build it from their static profile (and refine it after a capability probe),
// which keeps kv free of any provider import.
type Caps struct {
	// Dialect names the wire format ("anthropic", "openai-chat", ...). Opaque
	// blocks (thinking signatures, encrypted reasoning) are replayed only when
	// their WireFormat equals it.
	Dialect string
	// MaxBreakpoints is the number of explicit cache markers allowed. Zero means
	// the provider caches prefixes automatically and markers are meaningless.
	MaxBreakpoints int
	// LookbackBlocks is how far a breakpoint searches backwards for an older
	// entry (Anthropic: 20 positions). Zero when not applicable.
	LookbackBlocks int
	// MinPrefixTokens is the shortest prefix the provider will cache.
	MinPrefixTokens int
	// ReplayThinking replays stored thinking blocks verbatim.
	ReplayThinking bool
	// CacheKeys marks providers that route by an explicit key.
	CacheKeys bool
	// MessageBoundaries requires preserving message endings for implicit caching.
	MessageBoundaries bool
	// TurnScopedSystem: the provider accepts role=system messages with
	// clear_at "next_user_message" (Message.ClearAt). Adapters without it fold
	// such a message into ordinary user content.
	TurnScopedSystem bool
	// HotMode is how the hot tail is delivered. Callers resolve it with
	// ResolveHot; Render only reads the resolved value.
	HotMode HotMode
}

// Policy tunes breakpoint placement.
type Policy struct {
	// SharedTTL applies to breakpoints on the shared and role layers. Those are
	// read by many agents; a long TTL survives idle gaps for a higher write
	// price. Zero uses the provider default.
	SharedTTL time.Duration
	// MinLayerForBreakpoint skips the notes marker when the notes layer is
	// smaller than this: the slot is worth more elsewhere and the entry would
	// save less than it costs. The shared and role markers do not use it: they
	// only need the prefix before them to reach the provider's minimum (and the
	// role pin itself to reach it), because they serve every agent of the swarm.
	MinLayerForBreakpoint int
}

// DefaultPolicy is the baseline used unless configuration overrides it.
func DefaultPolicy() Policy { return Policy{MinLayerForBreakpoint: 1500} }

// Stack is one agent's complete prompt state. The layer pointers reference
// immutable values, so a Stack copy is a consistent snapshot even while other
// agents' commits replace the shared layers.
type Stack struct {
	Agent string
	Role  string
	Model string

	Tools  []core.ToolSpec // frozen for the session, sorted by name
	Const  *Layer
	Shared *Layer
	RoleL  *Layer
	Notes  *Layer
	Spine  *Layer

	Thread Snapshot
}

// SortTools returns tools ordered by name with canonical schemas. Every agent
// in a session must send the same bytes here or nothing caches across agents.
func SortTools(in []core.ToolSpec) ([]core.ToolSpec, error) {
	out := make([]core.ToolSpec, len(in))
	for i, t := range in {
		cs, err := core.Canonical(t.InputSchema)
		if err != nil {
			return nil, err
		}
		t.InputSchema = cs
		out[i] = t
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// PrefixKey identifies the prefix shared by every agent that has the same
// constitution, tools, shared pin and role pin. It is the cache trie node used
// for prewarming, fan-out gating and provider routing keys.
func (s *Stack) PrefixKey() core.Hash {
	var b []byte
	b = append(b, s.Model...)
	b = append(b, 0)
	for _, t := range s.Tools {
		b = append(b, t.Name...)
		b = append(b, 0)
		b = append(b, t.Description...)
		b = append(b, 0)
		b = append(b, t.InputSchema...)
		b = append(b, 0)
		if t.Strict {
			b = append(b, 's')
		}
		b = append(b, 0)
	}
	b = append(b, s.Const.Hash()...)
	b = append(b, 0)
	b = append(b, s.Shared.Hash()...)
	b = append(b, 0)
	b = append(b, s.RoleL.Hash()...)
	return core.HashBytes(b)
}

// GlobalKey identifies the prefix every agent of a session shares regardless of
// role: model, tools, constitution and shared pin. It keys provider affinity,
// so all agents co-locate on the engine that holds the biggest shared prefix.
func (s *Stack) GlobalKey() core.Hash {
	var b []byte
	b = append(b, s.Model...)
	b = append(b, 0)
	for _, t := range s.Tools {
		b = append(b, t.Name...)
		b = append(b, 0)
		b = append(b, t.Description...)
		b = append(b, 0)
		b = append(b, t.InputSchema...)
		b = append(b, 0)
		if t.Strict {
			b = append(b, 's')
		}
		b = append(b, 0)
	}
	b = append(b, s.Const.Hash()...)
	b = append(b, 0)
	b = append(b, s.Shared.Hash()...)
	return core.HashBytes(b)
}

// PromptBytes totals the bytes of persistent prompt content. Paired with the
// provider's reported input tokens it calibrates the token estimator.
func PromptBytes(p *core.Prompt) int {
	n := 0
	p.WalkBlocks(func(_ core.BlockRef, tool *core.ToolSpec, b *core.Block) {
		switch {
		case tool != nil:
			n += len(tool.Name) + len(tool.Description) + len(tool.InputSchema)
		case b != nil && !b.Ephemeral:
			n += blockBytes(*b)
		case b != nil:
			n += blockBytes(*b) // ephemeral blocks are billed too
		}
	})
	return n
}

// PromptTokens estimates the size of the whole prompt as sent, hot tail
// included. The agent scales the guard's estimates with it: provider-reported
// input tokens over this is the estimator's error on the previous request.
func PromptTokens(p *core.Prompt, est core.Estimator) int {
	n := 0
	p.WalkBlocks(func(_ core.BlockRef, tool *core.ToolSpec, b *core.Block) {
		switch {
		case tool != nil:
			n += est.Tokens(tool.Name) + est.Tokens(tool.Description) + est.Tokens(string(tool.InputSchema)) + 8
		case b != nil:
			n += SentBlockTokens(*b, est)
		}
	})
	return n
}

// blockBytes estimates content bytes recursively, including tool names and input but excluding
// block metadata.
func blockBytes(b core.Block) int {
	switch b.Kind {
	case core.BlockToolUse:
		return len(b.ToolName) + len(b.Input)
	case core.BlockToolResult:
		n := 0
		for _, c := range b.Result {
			n += blockBytes(c)
		}
		return n
	}
	return len(b.Text)
}
