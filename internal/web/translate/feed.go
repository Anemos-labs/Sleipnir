package translate

import (
	"github.com/anemos-labs/sleipnir/internal/tui/state"
)

// Some events have no row of their own in the page's vocabulary but a line in the terminal's feed: a compaction that was rejected,
// the shared prefix re-written, a panic of an output sink, the supervision of the manager (a hold, a run that ended with work left,
// a wake, the bound on wakes, a worker whose mail no longer wakes it, a shutdown with agents still running) and a background job that
// ended. The State writes that line for the terminal; the translator reads the lines an event wrote from the same State and sends
// them as system rows, so the page says what the terminal says and the two cannot drift.

// feedRows sends the lines the State's feed received for the log event being applied as system rows of the manager's channel, within
// the rate limit of notices (past it the rows are counted into one). Each row carries the line's agent, and its small print after
// the text.
func (t *Translator) feedRows(ts float64, at int64) {
	for _, l := range t.d.fed {
		if !t.pass(ts) {
			continue
		}
		who := ""
		if !nonAgent(l.Agent) {
			who = uiID(l.Agent)
		}
		t.sysRow("mgr", feedGlyph(l.Glyph), feedText(l), who, "", ts, at)
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
