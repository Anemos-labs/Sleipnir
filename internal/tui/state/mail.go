package state

import (
	"strconv"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
)

// mailState is the ring of recent messages and the counts.
type mailState struct {
	ring    ring[MailMsg]
	byID    map[string]int // message id -> its absolute index in the ring
	counts  MailCounts
	mailman Mailman
}

func newMailState() mailState {
	return mailState{ring: newRing[MailMsg](MailCap), byID: map[string]int{}}
}

// estTokens is the size of a text in tokens at the 3.6 bytes a token the harness's own estimator (core.BytesEstimator) starts
// from. The log does not count the tokens of a message.
func estTokens(n int) int { return (n*10 + 35) / 36 }

// add puts a message in the ring, forgetting the id of the one it pushes out, and returns the stored copy.
func (m *mailState) add(msg MailMsg) *MailMsg {
	if m.ring.len() >= MailCap {
		if old := m.ring.at(0); m.byID[old.ID] == m.ring.total-m.ring.len() {
			delete(m.byID, old.ID)
		}
	}
	m.ring.push(msg)
	if msg.ID != "" {
		m.byID[msg.ID] = m.ring.total - 1
	}
	p, _ := m.ring.byAbs(m.ring.total - 1)
	return p
}

// find returns the message with the id, if the ring still holds it.
func (m *mailState) find(id string) *MailMsg {
	abs, ok := m.byID[id]
	if !ok || id == "" {
		return nil
	}
	p, _ := m.ring.byAbs(abs)
	return p
}

// mailWire is a message as mail.send carries it (swarm.Message).
type mailWire struct {
	ID      string   `json:"id"`
	From    string   `json:"from"`
	To      string   `json:"to"`
	Kind    string   `json:"kind"`
	Text    string   `json:"text"`
	Via     string   `json:"via"`
	Origins []string `json:"origins"`
}

func (s *State) onMailSend(e events.Event, t time.Time) {
	var p mailWire
	if !s.decode(e.Data, maxPayload, &p) {
		return
	}
	from := clip(firstOf(p.From, e.Agent), textID)
	msg := MailMsg{ID: clip(p.ID, textID), Seq: e.Seq, From: from, To: clip(p.To, textID), Kind: clip(p.Kind, textID),
		Summary: clean(p.Text, textLine), Tokens: estTokens(len(p.Text)), Stage: MailSent, Sent: t, Via: clip(p.Via, textID),
		Origins: clipList(p.Origins, 8, textID)}
	s.mail.add(msg)
	s.mail.counts.Sent++
	if a := s.agent(from, t); a != nil {
		a.row.markAt(t, ActMail)
		a.active(t)
	}
	detail := strconv.Itoa(msg.Tokens) + " tok"
	if msg.Kind != "" && msg.Kind != "info" {
		detail = msg.Kind + " · " + detail
	}
	to := msg.To
	if msg.Via != "" {
		to += " (digest)"
	}
	s.line(e.Seq, t, from, FeedMail, GlyphMail, from+" → "+to+": "+msg.Summary, detail)
}

// stub makes the record of a message whose mail.send was not seen (a log that starts mid-session, or an event out of order).
func (s *State) stub(id, from, to string, t time.Time, seq uint64) *MailMsg {
	return s.mail.add(MailMsg{ID: id, Seq: seq, From: from, To: to, Stage: MailSent, Sent: t})
}

func (s *State) onMailRoute(e events.Event, t time.Time) {
	var p mailWire
	if !s.decode(e.Data, maxPayload, &p) {
		return
	}
	id := clip(p.ID, textID)
	m := s.mail.find(id)
	if m == nil {
		m = s.stub(id, clip(firstOf(p.From, e.Agent), textID), clip(p.To, textID), t, e.Seq)
	}
	if m.Stage == MailSent {
		m.Stage = MailRouted
	}
	s.mail.counts.Routed++
}

func (s *State) onMailDeliver(e events.Event, t time.Time) {
	var p mailWire
	if !s.decode(e.Data, maxPayload, &p) {
		return
	}
	id := clip(p.ID, textID)
	m := s.mail.find(id)
	if m == nil {
		m = s.stub(id, clip(p.From, textID), clip(e.Agent, textID), t, e.Seq)
	}
	m.Stage, m.Delivered = MailDelivered, t
	if m.To == "" {
		m.To = clip(e.Agent, textID)
	}
	s.mail.counts.Delivered++
	if a := s.agent(e.Agent, t); a != nil {
		a.row.markAt(t, ActMail)
		a.active(t)
	}
}

func (s *State) onMailDrop(e events.Event, t time.Time) {
	var p struct {
		ID     string `json:"id"`
		From   string `json:"from"`
		Reason string `json:"reason"`
	}
	if !s.decode(e.Data, maxPayload, &p) {
		return
	}
	id := clip(p.ID, textID)
	m := s.mail.find(id)
	if m == nil {
		m = s.stub(id, clip(p.From, textID), clip(e.Agent, textID), t, e.Seq)
	}
	m.Stage, m.Reason = MailDropped, clean(p.Reason, textShort)
	s.mail.counts.Ignored++
	s.line(e.Seq, t, e.Agent, FeedMail, GlyphWarn, "mail "+id+" was not delivered to "+clip(e.Agent, textID), m.Reason)
}

func (s *State) onMailAck(e events.Event, t time.Time) {
	var p struct {
		ID string `json:"id"`
	}
	if !s.decode(e.Data, maxPayload, &p) {
		return
	}
	if m := s.mail.find(clip(p.ID, textID)); m != nil && !m.Acked {
		m.Acked = true
	}
	s.mail.counts.Acked++
}

func (s *State) onMailDigest(e events.Event, t time.Time) {
	var p struct {
		Parcels []string `json:"parcels"`
	}
	if !s.decode(e.Data, maxPayload, &p) {
		return
	}
	s.mail.counts.Digests++
	for i, id := range p.Parcels {
		if i >= 30 {
			break
		}
		if m := s.mail.find(clip(id, textID)); m != nil {
			m.Stage = MailDigested
		}
	}
}

func (s *State) onMailDirect(e events.Event, t time.Time) {
	var p struct {
		N   int      `json:"n"`
		IDs []string `json:"ids"`
	}
	if !s.decode(e.Data, maxPayload, &p) {
		return
	}
	n := p.N
	if n <= 0 {
		n = max(len(p.IDs), 1)
	}
	s.mail.counts.Direct += min(n, smallCount)
}

func (s *State) onMailBatch(e events.Event, t time.Time) { s.mail.counts.Batches++ }

func (s *State) onMailman(e events.Event, t time.Time) {
	var p struct {
		State  string `json:"state"`
		Reason string `json:"reason"`
	}
	if !s.decode(e.Data, maxPayload, &p) {
		return
	}
	mm := &s.mail.mailman
	mm.State, mm.Reason = clip(p.State, textID), clean(p.Reason, textShort)
	if p.State == "down" {
		mm.Outages++
		s.line(e.Seq, t, e.Agent, FeedMail, GlyphWarn, "the mailman is down: mail is delivered directly", mm.Reason)
	}
}

// mailSnapshot copies the mail out.
func (s *State) mailSnapshot() Mail {
	out := Mail{Recent: s.mail.ring.slice(), Counts: s.mail.counts, Mailman: s.mail.mailman}
	for i := range out.Recent {
		out.Recent[i].Origins = append([]string(nil), out.Recent[i].Origins...)
		if len(out.Recent[i].Origins) == 0 {
			out.Recent[i].Origins = nil
		}
	}
	return out
}
