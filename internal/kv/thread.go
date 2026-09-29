package kv

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/reee344/sleipnir/internal/core"
)

// Thread is one agent's verbatim recent history (layer G5).
//
// Between commits it is strictly append-only: existing turns are never touched,
// so the provider's cached prefix and preserved-thinking bindings stay valid.
// Compaction replaces the whole slice atomically via Commit, which bumps Epoch;
// a bumped epoch is the marker for "this request is a declared rebase".
type Thread struct {
	mu    sync.Mutex
	turns []core.Turn
	next  core.TurnID
	epoch uint64
	now   func() time.Time
}

// NewThread returns an empty thread. Turn ids start at 1.
func NewThread() *Thread { return &Thread{next: 1, now: time.Now} }

// SetClock overrides the timestamp source (tests, simulation).
func (t *Thread) SetClock(now func() time.Time) { t.mu.Lock(); t.now = now; t.mu.Unlock() }

// Append adds a turn, assigning its id and timestamp, and returns it.
func (t *Thread) Append(turn core.Turn) core.Turn {
	t.mu.Lock()
	defer t.mu.Unlock()
	turn.ID = t.next
	t.next++
	if turn.At.IsZero() {
		turn.At = t.now().UTC()
	}
	t.turns = append(t.turns, turn)
	return turn
}

// Snapshot is an immutable view of the thread. Because turns are only ever
// appended, sharing the backing array is safe: a snapshot never observes
// elements beyond its own length.
type Snapshot struct {
	Turns  []core.Turn
	Epoch  uint64
	NextID core.TurnID
}

// Snapshot returns the current view.
func (t *Thread) Snapshot() Snapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	return Snapshot{Turns: t.turns[:len(t.turns):len(t.turns)], Epoch: t.epoch, NextID: t.next}
}

// ErrStaleEpoch is returned by Commit when the thread was rebased since the
// snapshot the caller worked from.
var ErrStaleEpoch = errors.New("thread epoch moved")

// Commit atomically replaces the retained turns. expect is the epoch of the
// snapshot the replacement was derived from; if another commit landed in
// between, the caller must re-derive. Turns appended after the snapshot are
// carried over verbatim behind the replacement, which is what lets a
// background compactor work on a snapshot while the agent keeps running.
func (t *Thread) Commit(expect uint64, replacement []core.Turn, snapLen int) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.epoch != expect {
		return ErrStaleEpoch
	}
	if snapLen > len(t.turns) {
		return fmt.Errorf("kv: snapshot longer than thread (%d > %d)", snapLen, len(t.turns))
	}
	tail := t.turns[snapLen:]
	out := make([]core.Turn, 0, len(replacement)+len(tail))
	out = append(out, replacement...)
	out = append(out, tail...)
	t.turns = out
	t.epoch++
	return nil
}

// Tokens estimates the thread size.
func (s Snapshot) Tokens(est core.Estimator) int {
	n := 0
	for _, tr := range s.Turns {
		n += TurnTokens(tr, est)
	}
	return n
}

// TurnTokens estimates the size of one turn.
func TurnTokens(tr core.Turn, est core.Estimator) int {
	n := 8 // role + framing overhead
	for _, b := range tr.Blocks {
		n += BlockTokens(b, est)
	}
	return n
}

// BlockTokens estimates the size of one block.
func BlockTokens(b core.Block, est core.Estimator) int {
	switch b.Kind {
	case core.BlockToolUse:
		return est.Tokens(b.ToolName) + est.Tokens(string(b.Input)) + 6
	case core.BlockToolResult:
		n := 6
		for _, c := range b.Result {
			n += BlockTokens(c, est)
		}
		return n
	case core.BlockImage:
		return 1500
	case core.BlockThinking, core.BlockRedactedThinking:
		if b.Text != "" {
			return est.Tokens(b.Text)
		}
		return est.Tokens(string(b.Wire))
	}
	return est.Tokens(b.Text)
}

// Unit is the smallest span that compaction may cut around: an assistant turn
// that calls tools together with the user turn carrying their results, or a
// single turn otherwise. Splitting a unit would leave a tool_result without its
// tool_use (or the reverse), which every provider rejects.
type Unit struct {
	Start, End int // half-open indices into the turn slice
	From, To   core.TurnID
}

// Units partitions turns into indivisible units.
func Units(turns []core.Turn) []Unit {
	var out []Unit
	for i := 0; i < len(turns); {
		j := i + 1
		if turns[i].Role == core.RoleAssistant && len(turns[i].ToolCalls()) > 0 && j < len(turns) {
			j++ // the results turn
		}
		out = append(out, Unit{Start: i, End: j, From: turns[i].ID, To: turns[j-1].ID})
		i = j
	}
	return out
}

// Validate checks the structural invariants providers enforce: every tool_use
// is answered by a tool_result in the next turn, and no tool_result is orphaned.
// A final assistant turn may have unanswered tool calls (a request in flight).
func Validate(turns []core.Turn) error {
	for i, tr := range turns {
		calls := tr.ToolCalls()
		if tr.Role == core.RoleAssistant && len(calls) > 0 && i+1 < len(turns) {
			want := map[string]bool{}
			for _, c := range calls {
				want[c.ToolID] = true
			}
			next := turns[i+1]
			if next.Role != core.RoleUser {
				return fmt.Errorf("turn %d: tool_use not followed by a user turn", tr.ID)
			}
			for _, b := range next.Blocks {
				if b.Kind == core.BlockToolResult {
					delete(want, b.ToolID)
				}
			}
			if len(want) > 0 {
				return fmt.Errorf("turn %d: unanswered tool_use %v", tr.ID, keys(want))
			}
		}
		for _, b := range tr.Blocks {
			if b.Kind != core.BlockToolResult {
				continue
			}
			if i == 0 || turns[i-1].Role != core.RoleAssistant {
				return fmt.Errorf("turn %d: orphan tool_result %s", tr.ID, b.ToolID)
			}
			found := false
			for _, c := range turns[i-1].ToolCalls() {
				if c.ToolID == b.ToolID {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("turn %d: tool_result %s has no matching tool_use", tr.ID, b.ToolID)
			}
		}
	}
	return nil
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
