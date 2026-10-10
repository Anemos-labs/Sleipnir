package translate

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/swarm"
	"github.com/anemos-labs/sleipnir/internal/tui/state"
)

// pairCase is an event of a hosted session that the shared State has a feed line for and that its producer also gives the session's
// sink a notice for: the agent and type of the event, its data, the level and the words of the notice, and what the notice has that the
// feed's line (cut at 80 characters, or without the detail the event does not carry) does not.
type pairCase struct {
	name       string
	agent, typ string
	data       map[string]any
	level      string
	notice     string
	more       string // a part of the notice that the feed's line does not have
}

// longLeft is a list of what a run left, longer than the 80 characters the feed's line keeps of it.
const longLeft = "running: be-1 (T3), be-2 (T4), fe-1 (T6); in review: T5, T7; waiting: T8, T9, T10; blocked: T11"

// wakeNote is what the swarm tells a manager it wakes: what it missed, and what to do.
const wakeNote = "While you were idle: T1 is in review (be-1); T2 is in review (fe-1); T3 failed; T4 is blocked (be-2). Check the board, accept or reject what is in review, and tell the person briefly what happened."

// pairCases are the pairs, with the notices in the producers' own words (internal/swarm wake.go and swarm.go).
var pairCases = []pairCase{
	{"a wake", "mgr", "swarm.wake", map[string]any{"n": 1, "note": wakeNote}, "wake", wakeNote, "T4 is blocked (be-2)"},
	{"the bound on wakes", "mgr", "swarm.wake.paused", map[string]any{"n": 8}, "warn", swarm.WakePausedNotice(8, "T1 is in review (be-1); mail is waiting for you"), "woken 8 times"},
	{"a run that ended with work left", "mgr", "swarm.unfinished", map[string]any{"unfinished": longLeft}, "warn", swarm.UnfinishedNotice(longLeft), "blocked: T11"},
}

// pairLog is a small team's log up to the first event of a case, and the case's event.
func pairLog(tc pairCase) []events.Event {
	b := newLog(t0)
	b.add(time.Second, "", "session.start", map[string]any{"model": "m1", "swarm": true, "root": "/work"})
	b.add(time.Second, "mgr", "agent.spawn", map[string]any{"id": "mgr", "role": "manager", "model": "m1"})
	b.add(time.Second, "mgr", "agent.spawn", map[string]any{"id": "be-1", "role": "backend", "task": "T1", "model": "m1"})
	b.add(time.Second, tc.agent, tc.typ, tc.data)
	return b.evs
}

// feedRowText is the text the terminal's feed has for the case's event, folded by a State of its own.
func feedRowText(t *testing.T, tc pairCase) string {
	t.Helper()
	ref := state.New()
	for _, e := range pairLog(tc) {
		ref.Apply(e)
	}
	feed := ref.Snapshot().Feed
	last := feed[len(feed)-1]
	if last.Detail == "" {
		return last.Text
	}
	return last.Text + " · " + last.Detail
}

// hostedPair is a hosted session (a sink goes with the log) that has run up to the case's event, which is not applied yet.
type hostedPair struct {
	h     *harness
	event events.Event
}

func newHostedPair(t *testing.T, tc pairCase) *hostedPair {
	t.Helper()
	evs := pairLog(tc)
	h := newHarness(t, Config{Root: "/work", StartedAt: evs[0].TS})
	h.feed(evs[:len(evs)-1]...)
	return &hostedPair{h: h, event: evs[len(evs)-1]}
}

// apply is the log event reaching the translator; notice is a call of the sink, a millisecond later; wait moves the clock.
func (p *hostedPair) apply() { p.h.feed(p.event) }
func (p *hostedPair) notice(agent, level, text string) {
	p.h.set(p.h.now().Add(time.Millisecond))
	p.h.tr.Sink().Notice(agent, level, text)
	p.h.drain()
}
func (p *hostedPair) wait(d time.Duration) { p.h.advance(d) }

// rows are the texts of the system rows so far.
func (p *hostedPair) rows() (out []string) {
	for _, r := range ofKind(p.h.decoded(), "sys") {
		out = append(out, r["text"].(string))
	}
	return out
}

// sameRows fails when the rows are not the wanted ones, in this order.
func sameRows(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("%s: the rows are\n  %s\nwant\n  %s", what, strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// pastTheKeep is a time after the one a shown notice waits for its event.
const pastTheKeep = 11 * time.Second

// The feed's line and the producer's notice of one event are one row on the page, the notice, whichever of the two the translator hears
// first; and nothing of the line appears later.
func TestAnEventAndItsNoticeAreOneRow(t *testing.T) {
	for _, tc := range pairCases {
		t.Run(tc.name, func(t *testing.T) {
			for _, order := range []string{"event, notice", "notice, event"} {
				p := newHostedPair(t, tc)
				if order == "event, notice" {
					p.apply()
					sameRows(t, order+": after the event", p.rows()) // the line waits for the notice
					p.notice(tc.agent, tc.level, tc.notice)
				} else {
					p.notice(tc.agent, tc.level, tc.notice)
					p.apply()
				}
				sameRows(t, order, p.rows(), tc.notice)
				p.wait(pastTheKeep)
				sameRows(t, order+", later", p.rows(), tc.notice)
				if r := ofKind(p.h.decoded(), "sys")[0]; r["ch"] != "mgr" || r["glyph"] != "⚠" && r["glyph"] != "◇" {
					t.Errorf("%s: the row is %v", order, r)
				}
			}
		})
	}
}

// The notice is the one with more in it: it has what the feed's line cuts or does not carry.
func TestTheNoticeSaysMoreThanTheFeedLine(t *testing.T) {
	for _, tc := range pairCases {
		feed := feedRowText(t, tc)
		if strings.Contains(feed, tc.more) || !strings.Contains(tc.notice, tc.more) {
			t.Errorf("%s: %q should be in the notice and not in the feed's row %q", tc.name, tc.more, feed)
		}
	}
	// the words that make the notice of the bound on wakes worth keeping
	if wp := swarm.WakePausedNotice(8, "T1 is in review (be-1)"); !strings.Contains(wp, "woken 8 times") || !strings.Contains(wp, "T1 is in review (be-1)") {
		t.Errorf("the notice of the bound on wakes: %q", wp)
	}
}

// An event whose notice does not come is still said: the feed's row that waited for it is shown when the wait is over, and not before.
func TestAnEventWithoutItsNoticeIsStillSaid(t *testing.T) {
	for _, tc := range pairCases {
		t.Run(tc.name, func(t *testing.T) {
			p := newHostedPair(t, tc)
			p.apply()
			feed := feedRowText(t, tc)
			sameRows(t, "at once", p.rows())
			p.wait(time.Second)
			sameRows(t, "after a second", p.rows())
			p.wait(time.Second + 500*time.Millisecond)
			sameRows(t, "after the wait", p.rows(), feed)
			p.notice(tc.agent, tc.level, tc.notice) // a notice that comes after the wait is a notice
			sameRows(t, "a late notice", p.rows(), feed, tc.notice)
		})
	}
}

// A notice with no event is shown as ever, and a notice that is not the event's is shown at once whatever the event is waiting for: the
// notice of another agent, of another level, with other words, or with the beginning of the event's words. The event's own notice is
// still recognized after all of them.
func TestANoticeThatIsNotTheEventsIsShown(t *testing.T) {
	for _, tc := range pairCases {
		t.Run(tc.name, func(t *testing.T) {
			p := newHostedPair(t, tc)
			p.notice(tc.agent, tc.level, tc.notice)
			sameRows(t, "the notice alone", p.rows(), tc.notice)

			other := []struct{ what, agent, level, text string }{
				{"other words", tc.agent, tc.level, "the endpoint is slow today"},
				{"the same words for another agent", otherAgent(tc.agent), tc.level, tc.notice},
				{"the same words at another level", tc.agent, "info", tc.notice},
				{"the beginning of the words", tc.agent, tc.level, tc.notice[:len(tc.notice)/2]},
			}
			if tc.name != "the bound on wakes" { // this notice is told only by its beginning: the note of what the manager waits for follows
				other = append(other, struct{ what, agent, level, text string }{"the words and more", tc.agent, tc.level, tc.notice + " and more words"})
			}
			p = newHostedPair(t, tc)
			p.apply()
			var want []string
			for _, o := range other {
				p.notice(o.agent, o.level, o.text)
				want = append(want, o.text)
				sameRows(t, o.what, p.rows(), want...)
			}
			p.notice(tc.agent, tc.level, tc.notice)
			want = append(want, tc.notice)
			sameRows(t, "then its own notice", p.rows(), want...)
			p.wait(pastTheKeep)
			sameRows(t, "later: the feed's row is gone", p.rows(), want...)
		})
	}
}

// otherAgent is an agent that is not the case's.
func otherAgent(a string) string {
	if a == "mgr" {
		return "be-1"
	}
	return "mgr"
}

// Each event is paired with one notice: two events and one notice are one row (the other event keeps waiting for its own), a notice given
// again is shown, and the second event's row is shown when its wait is over without a notice.
func TestEachEventHasOneNotice(t *testing.T) {
	for _, tc := range pairCases {
		t.Run(tc.name, func(t *testing.T) {
			p := newHostedPair(t, tc)
			p.apply()
			again := p.event
			again.Seq++
			again.TS = again.TS.Add(time.Second)
			p.h.feed(again)
			p.notice(tc.agent, tc.level, tc.notice)
			sameRows(t, "one notice for two events", p.rows(), tc.notice)
			p.notice(tc.agent, tc.level, tc.notice)
			sameRows(t, "two notices for two events", p.rows(), tc.notice, tc.notice)
			p.notice(tc.agent, tc.level, tc.notice)
			sameRows(t, "a third notice", p.rows(), tc.notice, tc.notice, tc.notice)
			p.wait(pastTheKeep)
			sameRows(t, "later", p.rows(), tc.notice, tc.notice, tc.notice)

			q := newHostedPair(t, tc)
			q.apply()
			q.h.feed(again)
			q.notice(tc.agent, tc.level, tc.notice)
			q.wait(3 * time.Second)
			sameRows(t, "the event whose notice did not come", q.rows(), tc.notice, feedRowText(t, tc))
		})
	}
}

// In a followed or replayed log no sink goes with the log: the feed's row is shown at once, for every event, and nothing waits.
func TestNoPairsInALogThatNoSinkGoesWith(t *testing.T) {
	for _, tc := range pairCases {
		t.Run(tc.name, func(t *testing.T) {
			evs := pairLog(tc)
			h := newHarness(t, Config{Root: "/work", StartedAt: evs[0].TS})
			h.tr.d.logOnly, h.tr.d.exactPend = true, true
			h.feed(evs...)
			var rows []string
			for _, r := range ofKind(h.decoded(), "sys") {
				rows = append(rows, r["text"].(string))
			}
			sameRows(t, "at once", rows, feedRowText(t, tc))
		})
	}
}

// An event of a pair whose data cannot produce its notice is not held: its row is shown at once. Neither is an event whose notice is a row
// of another channel than its own row (an agent's notice for itself).
func TestAnEventThatHasNoNoticeIsNotHeld(t *testing.T) {
	for _, tc := range []pairCase{
		{agent: "mgr", typ: "swarm.wake", data: map[string]any{"n": 1}},                                                                                // no note
		{agent: "mgr", typ: "swarm.wake.paused", data: map[string]any{}},                                                                               // no count
		{agent: "mgr", typ: "swarm.unfinished", data: map[string]any{}},                                                                                // no list
		{agent: "be-1", typ: "compact.reject", data: map[string]any{"stage": "emergency", "reason": "x", "prefix_tokens": 120_000, "window": 128_000}}, // the agent's own notice is a row of its own channel
	} {
		p := newHostedPair(t, tc)
		p.apply()
		if len(p.rows()) != 1 {
			t.Errorf("%s %v: %d rows at once, want 1: %q", tc.typ, tc.data, len(p.rows()), p.rows())
		}
	}
}

// The translator ends the waits when it closes: a feed row that waited for a notice is not lost with the session.
func TestARowThatWaitsForItsNoticeIsShownWhenTheTranslatorCloses(t *testing.T) {
	tc := pairCases[1]
	dir := t.TempDir()
	log := openLog(t, dir, "s", time.Now)
	tr := New(Config{Tab: "t"})
	detach := tr.Attach(log, dir)
	defer detach()
	for _, e := range pairLog(tc)[:3] {
		log.Emit(e.Agent, e.Type, e.Data)
	}
	log.Emit(tc.agent, tc.typ, tc.data)
	waitFor(t, func() bool { _, evs, _, _ := tr.Journal(); return containsRaw(evs, `"id":"be-1"`) })
	time.Sleep(200 * time.Millisecond) // the event that follows is applied
	if _, evs, _, _ := tr.Journal(); containsRaw(evs, `"k":"sys"`) {
		t.Fatalf("the row did not wait for its notice: %s", lines(evs))
	}
	tr.Close()
	_, evs, _, _ := tr.Journal()
	if !containsRaw(evs, "the manager will not be woken again until you write to it") {
		t.Fatalf("the row was lost when the translator closed: %s", lines(evs))
	}
}

// With the translator's goroutine, a real log and the sink in the producer's order (the event, then the notice), every pair is one row,
// the notice, run after run, and no feed row of a pair appears after the wait.
func TestPairsAreOneRowWithTheGoroutine(t *testing.T) {
	dir := t.TempDir()
	log := openLog(t, dir, "s", time.Now)
	tr := New(Config{Tab: "t"})
	defer tr.Close()
	detach := tr.Attach(log, dir)
	defer detach()
	log.Emit("", "session.start", map[string]any{"model": "m1", "swarm": true, "root": "/work"})
	log.Emit("mgr", "agent.spawn", map[string]any{"id": "mgr", "role": "manager", "model": "m1"})
	log.Emit("be-1", "agent.spawn", map[string]any{"id": "be-1", "role": "backend", "task": "T1", "model": "m1"})
	sink := tr.Sink()
	var want []string
	for i := 0; i < 4; i++ {
		for _, tc := range pairCases {
			data, notice := tc.data, tc.notice
			if tc.name == "a wake" { // each wake has its own note, so that the rows can be told apart
				notice = strings.Replace(wakeNote, "T1", "T"+string(rune('1'+i)), 1)
				data = map[string]any{"n": i + 1, "note": notice}
			}
			log.Emit(tc.agent, tc.typ, data)
			sink.Notice(tc.agent, tc.level, notice)
			want = append(want, notice)
		}
	}
	rows := func() (out []string) {
		_, evs, _, _ := tr.Journal()
		for _, r := range ofKind(decodeAll(t, evs), "sys") {
			out = append(out, r["text"].(string))
		}
		return out
	}
	waitFor(t, func() bool { return len(rows()) >= len(want) })
	time.Sleep(2300 * time.Millisecond) // past the wait for a notice
	sameRows(t, "the rows of the pairs", rows(), want...)
}

// A notice that the rate limit holds back does not take the place of its event's row: the row is shown when its wait is over, so that
// something says what happened.
func TestANoticeTheRateLimitHoldsBackLeavesItsRow(t *testing.T) {
	tc := pairCases[0]
	p := newHostedPair(t, tc)
	for i := 0; i < gateMax; i++ { // the window's rows are used up
		p.notice("mgr", "info", fmt.Sprintf("filler %d", i))
	}
	p.apply()
	p.notice(tc.agent, tc.level, tc.notice) // held back by the limit
	p.wait(pastTheKeep)                     // the window is over: the row of the event passes
	feed := feedRowText(t, tc)
	var gotRow, gotNotice bool
	for _, r := range p.rows() {
		gotRow = gotRow || r == feed
		gotNotice = gotNotice || r == tc.notice
	}
	if !gotRow || gotNotice {
		t.Errorf("the feed's row shown: %v, the held notice shown: %v; the rows are\n  %s", gotRow, gotNotice, strings.Join(p.rows(), "\n  "))
	}
}
