package kv

import (
	"strings"

	"github.com/anemos-labs/sleipnir/internal/core"
)

// HotMode says how the always-fresh tail (layer G6: board view, alerts) reaches
// the model. The right choice depends on what the provider binds to earlier
// bytes, so it is decided per route (ResolveHot), not per agent.
type HotMode uint8

const (
	// HotInline appends the hot blocks (ephemeral) to the last user message,
	// after the last cache breakpoint. They are rebuilt on every request and never
	// persisted. It is only suitable where the cache can reuse shorter token
	// prefixes and earlier bytes are not bound to anything:
	// the next request removes them from a message an assistant turn was already
	// produced against, which voids the signature of every thinking block after it.
	HotInline HotMode = iota
	// HotPersist ("persist-on-change") writes the hot text into the thread as a
	// frozen notice block on the user turn that is about to be sent, and only when
	// its content changed since the last one (and not more often than the agent's
	// minimum interval). History stays append-only, so cache entries and thinking
	// bindings survive; the price is that each notice stays in the thread until
	// compaction folds it (Apply drops all but the newest). It is forced on routes
	// with preserved thinking that have no turn-scoped system messages.
	HotPersist
	// HotTurnScoped appends the hot text after each tool_result turn as a
	// persisted role=system message with ClearAt "next_user_message". Earlier
	// copies stay in the array byte for byte and render nothing, so the cache and
	// thinking bindings keep matching and the tail costs nothing after its turn.
	// Renderer contract: see Render. Adapter contract: docs/CACHE-DESIGN.md §3.
	HotTurnScoped
)

// ClearAtNextUser is the Message.ClearAt value of a turn-scoped system message.
const ClearAtNextUser = "next_user_message"

func (m HotMode) String() string {
	switch m {
	case HotPersist:
		return "persist"
	case HotTurnScoped:
		return "turn-scoped"
	}
	return "inline"
}

// ResolveHot picks the hot-tail mechanism for a route. want is an explicit
// request (anything but HotInline wins, downgraded when the provider cannot do
// it); HotInline lets the harness choose:
//
//   - the provider has turn-scoped system messages: HotTurnScoped;
//   - the provider caches complete messages: HotPersist (removing an inline
//     tail would remove the previous request's cache boundary);
//   - the model enforces preserved thinking and its profile replays thinking
//     blocks: HotPersist (an inline tail would make every request after the first
//     a thinking-binding error);
//   - otherwise HotInline.
func ResolveHot(want HotMode, c Caps, preservedThinking bool) HotMode {
	switch want {
	case HotTurnScoped:
		if c.TurnScopedSystem {
			return HotTurnScoped
		}
		return HotPersist
	case HotPersist:
		return HotPersist
	}
	switch {
	case c.TurnScopedSystem:
		return HotTurnScoped
	case c.MessageBoundaries:
		return HotPersist
	case preservedThinking && c.ReplayThinking:
		return HotPersist
	}
	return HotInline
}

// systemPlacementOK reports whether a turn-scoped system turn can be sent as a
// role:system message where it stands. The API wants it after a user message and
// either last in the array or followed by an assistant turn (a system message
// followed by a user message is a 400). prev is the role of the message before it
// in the rendered prompt, next the thread turn after it (nil at the end). Where
// the placement is not allowed, the turn is folded into the neighbouring user
// message instead, exactly as an adapter without the feature would send it.
func systemPlacementOK(prev core.Role, next *core.Turn) bool {
	return prev == core.RoleUser && (next == nil || next.Role == core.RoleAssistant)
}

// Block markers. core.Block has no field for "who wrote this text", and the
// distinction matters: the harness's own notices must never be mistaken for
// user text, and steering typed by a human must survive compaction while mail
// from another agent must not become an instruction. The marker rides in
// MediaType, which adapters ignore for text blocks.
const (
	// MediaNotice tags a harness-authored notice (the persisted hot view).
	MediaNotice = "text/x-sleipnir-notice"
	// MediaSteer tags text a human sent to a running agent.
	MediaSteer = "text/x-sleipnir-steer"
	// MediaTask tags the assignment a task turn carries (see Task).
	MediaTask = "text/x-sleipnir-task"
)

// Notice returns a frozen harness notice block.
func Notice(text string) core.Block {
	return core.Block{Kind: core.BlockText, Text: text, MediaType: MediaNotice}
}

// IsNotice reports whether b is a harness notice.
func IsNotice(b core.Block) bool { return b.Kind == core.BlockText && b.MediaType == MediaNotice }

// Steer returns a block carrying human steering text.
func Steer(text string) core.Block {
	return core.Block{Kind: core.BlockText, Text: text, MediaType: MediaSteer}
}

// IsSteer reports whether b is human steering.
func IsSteer(b core.Block) bool { return b.Kind == core.BlockText && b.MediaType == MediaSteer }

// Task returns a block carrying an assignment the harness hands to an agent inside a
// task turn (core.OriginTask). The model reads it as text. When the turn is folded
// away, the harness-owned "assignment" section of the agent's notes takes the text
// over (see Apply), so a reused worker's notes describe the task it has now and not
// the one it was spawned for. Nothing else in a task turn is kept.
func Task(text string) core.Block {
	return core.Block{Kind: core.BlockText, Text: text, MediaType: MediaTask}
}

// IsTask reports whether b is a task assignment block.
func IsTask(b core.Block) bool { return b.Kind == core.BlockText && b.MediaType == MediaTask }

// AnswerText concatenates the text blocks of a turn: what the model said,
// without its reasoning, tool calls or results. Parsers of model output (the
// compactor's JSON patch) read this, never Turn.PlainText, which includes
// thinking text.
func AnswerText(t core.Turn) string {
	var sb strings.Builder
	for _, b := range t.Blocks {
		if b.Kind == core.BlockText {
			sb.WriteString(b.Text)
		}
	}
	return sb.String()
}

// isThinking reports whether b is a reasoning block.
func isThinking(b core.Block) bool {
	return b.Kind == core.BlockThinking || b.Kind == core.BlockRedactedThinking
}

// StripThinkingTurn returns t without its thinking blocks and whether anything
// was removed. The block slice is copied, never edited in place: snapshots share
// turn storage with the live thread.
func StripThinkingTurn(t core.Turn) (core.Turn, bool) {
	n := 0
	for _, b := range t.Blocks {
		if isThinking(b) {
			n++
		}
	}
	if n == 0 {
		return t, false
	}
	nb := make([]core.Block, 0, len(t.Blocks)-n)
	for _, b := range t.Blocks {
		if !isThinking(b) {
			nb = append(nb, b)
		}
	}
	t.Blocks = nb
	return t, true
}

// StripThinking removes thinking from every turn (a declared rebase: removing
// all thinking is always allowed, and it is the only edit that leaves no block
// bound to a prefix that no longer exists).
func StripThinking(turns []core.Turn) ([]core.Turn, bool) {
	out := make([]core.Turn, len(turns))
	changed := false
	for i, t := range turns {
		var c bool
		out[i], c = StripThinkingTurn(t)
		changed = changed || c
	}
	if !changed {
		return turns, false
	}
	return out, true
}

// HasThinking reports whether any turn carries a thinking block.
func HasThinking(turns []core.Turn) bool {
	for _, t := range turns {
		for _, b := range t.Blocks {
			if isThinking(b) {
				return true
			}
		}
	}
	return false
}
