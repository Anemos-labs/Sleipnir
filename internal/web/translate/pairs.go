package translate

import (
	"encoding/json"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/swarm"
)

// In a hosted session a few facts reach the translator twice: as a log event, which the shared State turns into a line of the terminal's
// feed (see feed.go), and as a notice that the swarm (or an agent) gives to the session's sink with the same words and more. The
// terminal shows them on two surfaces, the cockpit's feed and the chat's scrollback; the page has one conversation, so it shows each fact
// once.
//
// The pairs are the events of pairRules. In each the notice says everything the feed's line says and more, because a feed line is a
// short summary (its text is cut at 160 characters and its small print at 80) and the notice is the producer's own statement, so the page
// keeps the notice and drops the line, and only on the page: the terminal's notices and the log are as they were.
//
//	swarm.wake          the notice is the note the manager is woken with, whole; the line names the event and cuts the note at 80 characters
//	swarm.wake.paused   the notice says how many wakes there were and what the manager waits for; the line has neither
//	swarm.unfinished    the notice carries the whole list of what was left; the line cuts it at 80 characters
//
// These are the pairs whose two rows are in the same place, the manager's channel: the swarm gives them for the manager. A notice that an
// agent gives for itself is a row of that agent's channel, and the feed's line of an event of that agent is a row of the manager's, so
// the two are not read in one view (an emergency compaction that cannot make room is such a pair: its notice stays in the agent's
// channel and its line in the manager's, both as they were).
//
// A pair is made of the event and the notice its producer writes for it, and the match is made on the event, not on how the texts look:
// the notice is computed from the event with the producer's own function (swarm.WakePausedNotice, swarm.UnfinishedNotice; the wake
// notice is the event's note), and a sink notice is the event's only when its agent, its level and its words are those. The events that
// are in no pair, and every notice that is not the one of an event, are shown as they always were.
//
// A log that no sink goes with (a followed or replayed one, the history of a resumed one) has no notices, so there the line is the row.
// In a hosted session the line waits for the notice, which the producer gives right after it writes the event; the event and the notice
// reach the translator in either order, and the result does not depend on it. If the notice does not come within the wait, the line is
// shown after all, so that no fact is ever missing.

// pairRule is an event that has a notice twin: the level of the notice, and how to compute the notice from the event.
type pairRule struct {
	level string
	// says is the notice the producer gives with this event: its words, and whether they are only its beginning (the event does not carry
	// all of it); ok is false for an event of the type that has no such notice (a rejected compaction of another stage, say).
	says func(e events.Event) (text string, lead, ok bool)
}

// pairRules are the events whose feed row and sink notice say the same thing, by the event's type.
var pairRules = map[string]pairRule{
	events.TypeSwarmWake:       {level: "wake", says: saysWake},
	events.TypeSwarmWakePaused: {level: "warn", says: saysWakePaused},
	events.TypeSwarmUnfinished: {level: "warn", says: saysUnfinished},
}

// saysWake is the notice of a swarm.wake: the note the manager is woken with, word for word.
func saysWake(e events.Event) (string, bool, bool) {
	var p struct {
		Note string `json:"note"`
	}
	if json.Unmarshal(e.Data, &p) != nil || p.Note == "" {
		return "", false, false
	}
	return p.Note, false, true
}

// saysWakePaused is the notice of a swarm.wake.paused: the swarm's own words for the count, whose note of what the manager waits for
// the event does not carry (so only the beginning is known).
func saysWakePaused(e events.Event) (string, bool, bool) {
	var p struct {
		N int `json:"n"`
	}
	if json.Unmarshal(e.Data, &p) != nil || p.N <= 0 {
		return "", false, false
	}
	return swarm.WakePausedNotice(p.N, ""), true, true
}

// saysUnfinished is the notice of a swarm.unfinished: the swarm's own words around the list the event carries.
func saysUnfinished(e events.Event) (string, bool, bool) {
	var p struct {
		Unfinished string `json:"unfinished"`
	}
	if json.Unmarshal(e.Data, &p) != nil || p.Unfinished == "" {
		return "", false, false
	}
	return swarm.UnfinishedNotice(p.Unfinished), false, true
}

// Times, in seconds of session time, and bounds of the pairing.
const (
	// pairWait is how long the feed's row of an event waits for the notice that is to replace it. The producer writes the event and gives
	// the notice right after it, so the notice is there within a few milliseconds; if it never comes (a sink that lost it, a session that
	// is not this process's), the row is shown when the wait is over.
	pairWait = 2.0
	// pairKeep is how long a notice that was shown waits for its event.
	pairKeep = 10.0
	// maxPairs bounds the pairs waiting and the notices remembered.
	maxPairs = 64
)

// heldRow is a system row of the manager's channel that is not sent yet: the feed line of an event whose notice is to replace it.
type heldRow struct {
	glyph, text, who string
	ts               float64
	at               int64
}

// pair is an event of pairRules that was applied, the notice it expects, and its feed row, which is held until the notice comes.
type pair struct {
	rule pairRule
	uid  string  // the agent the notice is given for
	text string  // the notice, or its beginning (lead)
	lead bool    // text is the beginning of the notice
	at   float64 // session time of the event
	row  heldRow
}

// matches reports whether a sink notice is the one this event's producer gives.
func (p *pair) matches(uid, level, msg string) bool {
	if uid != p.uid || level != p.rule.level {
		return false
	}
	if p.lead {
		return strings.HasPrefix(msg, p.text)
	}
	return msg == p.text
}

// shownNotice is a notice of the sink that was shown, remembered for the event of a pair that may follow it.
type shownNotice struct {
	uid, level, msg string
	at              float64
}

// pairOf is the pair the log event belongs to, nil when it is in no pair or when no notice can come with it: the translation of a log
// that no sink goes with (a followed or replayed one, the history of a resumed one) has only its feed's rows.
func (t *Translator) pairOf(e events.Event, ts float64) *pair {
	rule, ok := pairRules[e.Type]
	if !ok || t.d.logOnly || t.d.history {
		return nil
	}
	text, lead, ok := rule.says(e)
	if !ok {
		return nil
	}
	uid := "mgr"
	if !nonAgent(e.Agent) {
		uid = uiID(e.Agent)
	}
	return &pair{rule: rule, uid: uid, text: cutBytes(text, 4*capReason), lead: lead, at: ts}
}

// pairFeedRow handles the feed's row of an event that has a notice twin: if the notice was shown before the event came, the row is
// dropped; otherwise it is held for the notice.
func (t *Translator) pairFeedRow(p *pair, row heldRow) {
	if t.takeShown(p) {
		return
	}
	p.row = row
	if len(t.d.pairs) >= maxPairs {
		t.sendFeedRow(t.d.pairs[0].row)
		t.d.pairs = t.d.pairs[1:]
	}
	t.d.pairs = append(t.d.pairs, p)
}

// pairTaken is called for a notice of the sink before it is shown. If it is the notice of an event whose feed row is held, the row is
// dropped: the notice says it.
func (t *Translator) pairTaken(uid, level, msg string) {
	for i, p := range t.d.pairs {
		if p.matches(uid, level, msg) {
			t.d.pairs = append(t.d.pairs[:i], t.d.pairs[i+1:]...)
			return
		}
	}
}

// noteShown remembers a notice of the sink that was shown, for the event of a pair that may follow it.
func (t *Translator) noteShown(uid, level, msg string, ts float64) {
	if len(t.d.shown) >= maxPairs {
		t.d.shown = t.d.shown[1:]
	}
	t.d.shown = append(t.d.shown, shownNotice{uid: uid, level: level, msg: msg, at: ts})
}

// takeShown reports whether the notice of the pair was shown already, and forgets it.
func (t *Translator) takeShown(p *pair) bool {
	for i, s := range t.d.shown {
		if p.matches(s.uid, s.level, s.msg) {
			t.d.shown = append(t.d.shown[:i], t.d.shown[i+1:]...)
			return true
		}
	}
	return false
}

// flushPairs ends the waits that are over (all of them when all is true: the translator closes): a feed row whose notice did not come is
// shown, and a notice that was shown stops expecting its event.
func (t *Translator) flushPairs(ts float64, all bool) {
	kept := t.d.pairs[:0]
	for _, p := range t.d.pairs {
		if all || ts-p.at >= pairWait {
			t.sendFeedRow(p.row)
		} else {
			kept = append(kept, p)
		}
	}
	for i := len(kept); i < len(t.d.pairs); i++ {
		t.d.pairs[i] = nil
	}
	t.d.pairs = kept
	shown := t.d.shown[:0]
	for _, s := range t.d.shown {
		if !all && ts-s.at < pairKeep {
			shown = append(shown, s)
		}
	}
	t.d.shown = shown
}
