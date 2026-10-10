package translate

import (
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/tui/state/statetest"
)

// translateLog runs a recorded log through a translator as a hosted session's log (logOnly false: the rows only the sink gives are
// absent), with the clock at each event's time and three seconds after the last, and returns the journal; or (logOnly true) as
// Replay reads a recorded log: every row from the log, tool rows paired with their output however long their calls took, and the
// rows still waiting sent at the end of a log whose last run ended.
func translateLog(tb testing.TB, evs []events.Event, root string, logOnly bool) *harness {
	tb.Helper()
	h := newHarness(tb, Config{Root: root, StartedAt: evs[0].TS})
	h.tr.d.logOnly, h.tr.d.exactPend = logOnly, logOnly
	h.feed(evs...)
	h.advance(3 * time.Second)
	if logOnly && h.tr.d.ended {
		h.tr.mu.Lock()
		h.tr.flushPend(time.Time{}, true)
		h.tr.mu.Unlock()
	}
	return h
}

// The demo's handbook session (statetest's recording: a manager, scouts, writers and a reviewer, 312 events) translates to the
// same UI events on every run, as a hosted session's log and as a followed one. The golden files were read once; a change shows up
// as its diff.
func TestGoldenDemoHandbook(t *testing.T) {
	for _, tc := range []struct {
		name    string
		logOnly bool
	}{{"demo-handbook.ui.jsonl", false}, {"demo-handbook-watch.ui.jsonl", true}} {
		t.Run(tc.name, func(t *testing.T) {
			h := translateLog(t, statetest.DemoEvents(), "/work/handbook", tc.logOnly)
			raws := h.raws()
			checkStream(t, raws)
			golden(t, tc.name, lines(raws))
		})
	}
}

// The demo's shop session (a recording of `sleipnir demo --scenario shop`: worktrees, the merge queue, a verification that fails and
// is retried, mail, cache breaks, compactions, checkpoints) translates to the same UI events on every run.
func TestGoldenDemoShop(t *testing.T) {
	for _, tc := range []struct {
		name    string
		logOnly bool
	}{{"demo-shop.ui.jsonl", false}, {"demo-shop-watch.ui.jsonl", true}} {
		t.Run(tc.name, func(t *testing.T) {
			h := translateLog(t, shopEvents(t), "/work/shop", tc.logOnly)
			raws := h.raws()
			checkStream(t, raws)
			golden(t, tc.name, lines(raws))
		})
	}
}
