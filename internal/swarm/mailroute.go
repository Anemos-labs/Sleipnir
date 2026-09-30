package swarm

import (
	"fmt"
	"sort"
	"strings"

	"github.com/reee344/sleipnir/internal/events"
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
}

func (o *overflow) init()       { o.entries = map[string]*digestEntry{} }
func (o *overflow) empty() bool { return len(o.order) == 0 && o.dropped == 0 }

func (o *overflow) add(msg Message) {
	k := msg.From + "\x00" + msg.Kind
	e := o.entries[k]
	if e == nil {
		if len(o.order) >= maxDigestKeys {
			old := o.order[0]
			o.order = o.order[1:]
			delete(o.entries, old)
			o.dropped++
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
	o.entries, o.order, o.dropped = map[string]*digestEntry{}, nil, 0
	return truncRunes(txt, 900)
}

// receive queues a message for the member, or coalesces it when the inbox is full.
// It fails if the member has been retired.
func (m *member) receive(s *Swarm, msg Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.life == lifeRetired {
		return fmt.Errorf("%s has been retired", m.id)
	}
	if m.box.empty() && m.a.PendingInbox() < s.cfg.InboxSoftCap {
		m.a.Send(msg.Frame())
	} else {
		m.box.add(msg)
	}
	select {
	case m.notify <- struct{}{}:
	default:
	}
	return nil
}

// pump moves coalesced mail into the inbox once there is room for it.
func (m *member) pump(s *Swarm) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.box.empty() || m.life == lifeRetired {
		return
	}
	if m.a.PendingInbox() >= max(s.cfg.InboxSoftCap/2, 1) {
		return
	}
	m.a.Send(m.box.digest(s.nextHarnessID()))
	select {
	case m.notify <- struct{}{}:
	default:
	}
}

// hasMail reports whether anything is waiting for the member.
func (m *member) hasMail() bool {
	m.mu.Lock()
	box := !m.box.empty()
	m.mu.Unlock()
	return box || m.a.PendingInbox() > 0
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
	s.wake(m)
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
	}
}

// notifyManager tells the manager one line about the run.
func (s *Swarm) notifyManager(line string) {
	if id := s.ManagerID(); id != "" && line != "" {
		s.notify(id, "info", cleanText(line, 400))
	}
}
