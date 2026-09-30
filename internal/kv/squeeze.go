package kv

import (
	"errors"
	"fmt"
	"sort"

	"github.com/reee344/sleipnir/internal/core"
)

// ErrNothingToSqueeze is returned by SqueezeOnly when no tool result is big enough to
// excerpt.
var ErrNothingToSqueeze = errors.New("nothing worth excerpting")

const excerptPrefix = "⟦excerpt"

// squeeze is the last resort of an emergency patch (one the harness wrote with a
// Target). The newest units are protected from folding and from masking, because the
// agent is acting on them; but one exchange can be bigger than the whole window (forty
// parallel reads at the tool cap are ~240k tokens), and then protecting it protects
// nothing: the request cannot be sent. So when the retained thread is still over target,
// the bulkiest tool results anywhere in it are cut to their head and tail, largest
// first, until it fits or none is big enough (SqueezeMinTokens). What is left says how
// much was cut and where the whole result is ("recall t13.7"); the archive has it.
func squeeze(out []core.Turn, target int, est core.Estimator, pol ApplyPolicy, z Sizer, res *ApplyResult) []core.Turn {
	total := z.Turns(out)
	if total <= target {
		return out
	}
	type cand struct{ ti, bi, idx, tok int }
	var cs []cand
	for ti := range out {
		idx := 0
		for bi, b := range out[ti].Blocks {
			if b.Kind != core.BlockToolResult {
				continue
			}
			if tok := z.Block(b); tok >= pol.SqueezeMinTokens && !isMasked(b) && b.PlainText() != "" {
				cs = append(cs, cand{ti, bi, idx, tok})
			}
			idx++
		}
	}
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].tok > cs[j].tok })
	labels := toolLabels(out)
	for _, c := range cs {
		if total <= target {
			break
		}
		tr := &out[c.ti]
		b := tr.Blocks[c.bi]
		nb := excerptBlock(b, tr.ID, c.idx, labels[b.ToolID], pol.SqueezeKeepTokens, est)
		if saved := z.Block(b) - z.Block(nb); saved > 0 {
			tr.Blocks[c.bi] = nb
			total -= saved
			res.SqueezedResults++
			res.SqueezedTokens += saved
		}
	}
	return out
}

// excerptBlock replaces a tool result by its head and tail around a marker that says how
// much is missing and how to get it back.
func excerptBlock(b core.Block, turn core.TurnID, idx int, label string, keep int, est core.Estimator) core.Block {
	if label == "" {
		label = "tool result"
	}
	text := b.PlainText()
	head, tail := headTail(text, keep, est)
	all, shown := est.Tokens(text), est.Tokens(head)+est.Tokens(tail)
	body := fmt.Sprintf("%s: %s · ~%d of ~%d tokens shown · full result: recall t%d.%d⟧\n%s\n… [~%d tokens elided] …\n%s",
		excerptPrefix, label, shown, all, turn, idx, head, max(all-shown, 0), tail)
	return core.Block{Kind: core.BlockToolResult, ToolID: b.ToolID, IsError: b.IsError, Result: []core.Block{core.Text(body)}}
}

// SqueezeOnly is the emergency compaction for a thread that has nothing older to fold
// (a fresh agent whose first turn already carries an oversized exchange): no turn is
// removed, the spine and notes are untouched, and the bulkiest tool results are
// excerpted until the thread fits target. Like MaskOnly it is deterministic, needs no
// model and counts as a compaction only when it changed something.
func SqueezeOnly(s *Stack, est core.Estimator, pol ApplyPolicy, target int) (*ApplyResult, error) {
	pol = pol.WithDefaults()
	z := Sizer{Est: est, Caps: pol.Caps}
	turns := s.Thread.Turns
	units := Units(turns)
	if len(units) == 0 {
		return nil, ErrNothingToSqueeze
	}
	res := &ApplyResult{Spine: s.Spine, Notes: s.Notes, KeepFrom: turns[0].ID,
		SnapTokens: z.Turns(turns), SpineBefore: s.Spine.Tokens(est), SpineAfter: s.Spine.Tokens(est),
		NotesBefore: s.Notes.Tokens(est), NotesAfter: s.Notes.Tokens(est)}
	res.Replacement = retain(turns, units, 0, &Patch{}, pol, est, res)
	res.Replacement = squeeze(res.Replacement, target, est, pol, z, res)
	res.RetainedTokens = z.Turns(res.Replacement)
	res.RemovedTokens = res.SnapTokens - res.RetainedTokens
	if res.SqueezedResults == 0 {
		return nil, ErrNothingToSqueeze
	}
	return res, nil
}
