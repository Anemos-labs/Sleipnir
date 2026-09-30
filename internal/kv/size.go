package kv

import "github.com/reee344/sleipnir/internal/core"

// Sizer estimates prompt sizes the way Render sends them.
//
// The planner has to price what the provider actually receives. A thinking
// block that Render drops (the provider does not replay reasoning, the block has
// no provider-native wire form, or it belongs to another dialect) is stored in
// the thread but is never sent, so it must not count towards pressure, towards
// what a compaction "removes", or towards what a commit rewrites: with a
// reasoning model that difference is a factor of ten or more. Likewise a
// turn-scoped system message that a later user message has cleared renders
// nothing.
type Sizer struct {
	Est  core.Estimator
	Caps Caps
}

// replays mirrors renderBlocks: would this thinking block be sent?
func (z Sizer) replays(b core.Block) bool {
	return z.Caps.ReplayThinking && b.WireFormat == z.Caps.Dialect && len(b.Wire) > 0
}

// Block is the size of one block as sent.
func (z Sizer) Block(b core.Block) int {
	switch b.Kind {
	case core.BlockThinking, core.BlockRedactedThinking:
		if !z.replays(b) {
			return 0
		}
		return z.Est.Tokens(string(b.Wire))
	case core.BlockToolResult:
		n := 6
		for _, c := range b.Result {
			n += z.Block(c)
		}
		return n
	}
	return BlockTokens(b, z.Est)
}

// Turn is the size of one turn as sent (role and framing overhead included).
func (z Sizer) Turn(t core.Turn) int {
	n := 8
	for _, b := range t.Blocks {
		n += z.Block(b)
	}
	return n
}

// Turns is the size of a run of turns as sent.
func (z Sizer) Turns(turns []core.Turn) int {
	lastUser := -1
	for i, t := range turns {
		if t.Role == core.RoleUser {
			lastUser = i
		}
	}
	n := 0
	for i, t := range turns {
		if t.Role == core.RoleSystem && z.Caps.HotMode == HotTurnScoped && i < lastUser {
			continue // cleared by a later user message: renders nothing
		}
		n += z.Turn(t)
	}
	return n
}

// Snapshot is the size of a thread snapshot as sent.
func (z Sizer) Snapshot(s Snapshot) int { return z.Turns(s.Turns) }

// SentBlockTokens sizes a block that is already part of a rendered prompt:
// whatever Render kept is sent, thinking included (by its wire form).
func SentBlockTokens(b core.Block, est core.Estimator) int {
	switch b.Kind {
	case core.BlockThinking, core.BlockRedactedThinking:
		if len(b.Wire) > 0 {
			return est.Tokens(string(b.Wire))
		}
		return est.Tokens(b.Text)
	case core.BlockToolResult:
		n := 6
		for _, c := range b.Result {
			n += SentBlockTokens(c, est)
		}
		return n
	}
	return BlockTokens(b, est)
}
