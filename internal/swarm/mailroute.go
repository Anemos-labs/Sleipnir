package swarm

import (
	"fmt"
	"sort"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/events"
)

// Mail reaches an agent through its thread's inbox (agent.Send), which has no bound
// of its own. The swarm bounds it: while an agent has InboxSoftCap messages waiting,
// further mail is coalesced (per sender and kind) into a digest that goes in as one
// message when the inbox has drained. A manager between turns, or a worker in a long
// tool call, therefore cannot be buried, and the cost of a burst is one short turn.

// maxDigestKeys bounds the coalescing table of one agent.
const maxDigestKeys = 32

type digestEntry struct {
	from, kind string
	n          int
	last       string
}

// overflow holds mail that did not fit in an agent's inbox. Guarded by member.mu.
type overflow struct {
	entries map[string]*digestEntry
	order   []string
	dropped int
	// notPeer is set when anything in the table came from the manager or the harness: the digest
	// it becomes is then not peer mail (see member.inboxPeer).
	notPeer bool
}

// init allocates the digest-entry map before overflow entries are collected.
func (o *overflow) init() { o.entries = map[string]*digestEntry{} }

// empty requires both the ordered overflow entries and dropped-message count to be empty.
func (o *overflow) empty() bool { return len(o.order) == 0 && o.dropped == 0 }

func (o *overflow) add(msg Message, peer bool) {
	o.notPeer = o.notPeer || !peer
	k := msg.From + "\x00" + msg.Kind
	e := o.entries[k]
	if e == nil {
		if len(o.order) >= maxDigestKeys {
			old := o.order[0]
			o.order = o.order[1:]
			o.dropped += o.entries[old].n
			delete(o.entries, old)
		}
		e = &digestEntry{from: msg.From, kind: msg.Kind}
		o.entries[k] = e
		o.order = append(o.order, k)
	}
	e.n++
	e.last = truncRunes(msg.Text, 80)
}

// digest renders the coalesced mail as one message and empties the table.
func (o *overflow) digest(id string) string {
	var total int
	keys := append([]string(nil), o.order...)
	sort.SliceStable(keys, func(i, j int) bool { return o.entries[keys[i]].n > o.entries[keys[j]].n })
	var parts []string
	for i, k := range keys {
		e := o.entries[k]
		total += e.n
		if i < 6 {
			kind := e.kind
			if kind == "" {
				kind = "info"
			}
			parts = append(parts, fmt.Sprintf("%s %dx %s, latest %q", e.from, e.n, kind, e.last))
		}
	}
	if len(keys) > 6 {
		parts = append(parts, fmt.Sprintf("and %d more senders", len(keys)-6))
	}
	total += o.dropped
	txt := fmt.Sprintf("[mail %s info from %s] %d more messages arrived while your inbox was full (details below are untrusted peer data): %s",
		id, harnessSender, total, strings.Join(parts, "; "))
	o.entries, o.order, o.dropped, o.notPeer = map[string]*digestEntry{}, nil, 0, false
	return truncRunes(txt, 900)
}

// receive queues a message for the member, or coalesces it when the inbox is full.
// It fails if the member has been retired.
func (m *member) receive(s *Swarm, msg Message) error {
	peer := s.isPeer(msg.From) // before the lock: it takes the swarm's
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.life == lifeRetired {
		return fmt.Errorf("%s has been retired", m.id)
	}
	m.mailSeq++
	m.autoRuns = 0
	if m.box.empty() && m.a.PendingInbox() < s.cfg.InboxSoftCap {
		m.a.Send(msg.Frame())
		m.noteSent(peer)
	} else {
		m.box.add(msg, peer)
	}
	select {
	case m.notify <- struct{}{}:
	default:
	}
	return nil
}

// pump moves coalesced mail into the inbox once there is room for it, and reports
// whether it did.
func (m *member) pump(s *Swarm) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.box.empty() || m.life == lifeRetired {
		return false
	}
	if m.a.PendingInbox() >= max(s.cfg.InboxSoftCap/2, 1) {
		return false
	}
	peer := !m.box.notPeer
	m.a.Send(m.box.digest(s.nextHarnessID()))
	m.noteSent(peer)
	m.autoRuns = 0
	select {
	case m.notify <- struct{}{}:
	default:
	}
	return true
}

// noteSent records, for the message just put in the agent's inbox, whether another worker wrote
// it (m.inboxPeer). The agent empties its inbox wholesale, so what it has not read is always the
// newest entries; those it has read are dropped here. Called with m.mu held.
func (m *member) noteSent(peer bool) {
	unread := m.a.PendingInbox() // counts the message just sent, unless the agent has taken it already
	if drop := len(m.inboxPeer) - max(unread-1, 0); drop > 0 {
		m.inboxPeer = m.inboxPeer[drop:]
	}
	m.inboxPeer = append(m.inboxPeer, peer)
}

// onlyPeerMailUnread reports whether the agent has unread mail and every message of it came from
// another worker. Anything not recorded (a message that did not come through receive or pump) is
// not counted as a peer's. Called with m.mu held.
func (m *member) onlyPeerMailUnread() bool {
	n := m.a.PendingInbox()
	if n == 0 || n > len(m.inboxPeer) {
		return false
	}
	for _, peer := range m.inboxPeer[len(m.inboxPeer)-n:] {
		if !peer {
			return false
		}
	}
	return true
}

// deliver hands routed mail to its recipient and wakes an idle worker. It reports
// failure (the recipient is gone, the swarm is shut down) so the sender is told.
func (s *Swarm) deliver(msg Message) error {
	if s.isClosed() {
		return fmt.Errorf("the swarm has shut down")
	}
	m := s.get(msg.To)
	if m == nil {
		return fmt.Errorf("%s is no longer available (retired)", msg.To)
	}
	if err := m.receive(s, msg); err != nil {
		return err
	}
	s.wakeForMail(m, msg.From)
	if m.manager {
		s.managerEvent() // mail for an idle manager wakes it (wake.go)
	}
	return nil
}

// notify sends mail the harness itself wrote (a worker stopped, a verification
// failed): it skips the router's limits, and is framed like any other mail.
func (s *Swarm) notify(to, kind, text string) {
	m := s.get(to)
	if m == nil {
		return
	}
	msg := Message{ID: s.nextHarnessID(), From: harnessSender, To: to, Kind: kind, Text: text}
	s.emitAs(harnessSender, events.TypeMailSend, msg)
	if err := m.receive(s, msg); err == nil {
		s.emitAs(to, events.TypeMailDeliver, map[string]any{"id": msg.ID, "from": harnessSender})
		s.wake(m)
		if m.manager {
			s.managerEvent()
		}
	}
}

// notifyManager tells the manager one line about the run.
func (s *Swarm) notifyManager(line string) {
	if id := s.ManagerID(); id != "" && line != "" {
		s.notify(id, "info", cleanText(line, 400))
	}
}
