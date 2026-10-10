package translate

import (
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/tui/state"
)

// Some events have no row of their own in the page's vocabulary but a line in the terminal's feed: a compaction that was rejected,
// the shared prefix re-written, a panic of an output sink, the supervision of the manager (a hold, a run that ended with work left,
// a wake, the bound on wakes, a worker whose mail no longer wakes it, a shutdown with agents still running) and a background job that
// ended. The State writes that line for the terminal; the translator reads the lines an event wrote from the same State and sends
// them as system rows, so the page says what the terminal says and the two cannot drift.

// feedRows sends the lines the State's feed received for the log event being applied as system rows of the manager's channel, within
// the rate limit of notices (past it the rows are counted into one). Each row carries the line's agent, and its small print after
// the text. In a hosted session a few events have a notice twin that says the same or more (pairs.go): the line of such an event is
// shown, or held for its notice, as the pair decides.
func (t *Translator) feedRows(e events.Event, ts float64, at int64) {
	p := t.pairOf(e, ts)
	for i, l := range t.d.fed {
		who := ""
		if !nonAgent(l.Agent) {
			who = uiID(l.Agent)
		}
		row := heldRow{glyph: feedGlyph(l.Glyph), text: feedText(l), who: who, ts: ts, at: at}
		if i == 0 && p != nil {
			t.pairFeedRow(p, row)
			continue
		}
		t.sendFeedRow(row)
	}
}

// sendFeedRow sends a feed line as a system row of the manager's channel, if the rate limit of notices lets one more through.
func (t *Translator) sendFeedRow(r heldRow) {
	if t.pass(r.ts) {
		t.sysRow("mgr", r.glyph, r.text, r.who, "", r.ts, r.at)
	}
}

// feedText is the text of a feed line with its small print after it.
func feedText(l state.FeedLine) string {
	if l.Detail == "" {
		return l.Text
	}
	return l.Text + " · " + l.Detail
}

// feedGlyph is the mark of a system row for a feed line's glyph: the line's own, and the page's mark for a plain note (the
// terminal's middle dot) in its place.
func feedGlyph(g string) string {
	if g == "" || g == state.GlyphInfo {
		return "◇"
	}
	return g
}
