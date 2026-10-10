package session

import (
	"sort"
	"sync"
	"time"

	"github.com/anemos-labs/sleipnir/internal/checkpoint"
)

// What a host of several sessions in one process (`sleipnir web`) reads of a session, beside what the terminal chat already uses.

// Root is the project root the session works in.
func (s *Session) Root() string { return s.opts.Root }

// Turn reports how many turns the session has run (Run calls, the goal's continuations included).
func (s *Session) Turn() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.turn
}

// checkpointEvent is the log record of a checkpoint that began or whose files changed: the history of a session shows its
// checkpoints from it (a checkpoint store keeps them in its own directory, which the log does not otherwise mention).
const checkpointEvent = "checkpoint"

// checkpointSource is a checkpoint store that reports its changes as cheap events and names a checkpoint's writers.
type checkpointSource interface {
	OnEvent(func(checkpoint.Event))
	Agents(id string) []string
}

// checkpointLog writes the "checkpoint" events of a session: {id, label, count, file, agents, time}, where count is how many
// files the checkpoint holds and file the one recorded last (never the whole list, so the log grows with the number of files,
// not with its square). A change is written when the checkpoint's count has grown by checkpointStep since the last event
// written for it; the changes in between wait, and what waits is written when the turn ends, the next checkpoint begins or the
// session closes. Nothing depends on time: the same changes give the same events in the same places of the log, however fast
// the machine is.
type checkpointLog struct {
	emit   func(data map[string]any)
	agents func(id string) []string

	mu      sync.Mutex
	written map[string]int // the count of the last event written for a checkpoint
	pending map[string]checkpoint.Event
	closed  bool
}

// checkpointStep is how much a checkpoint's count must grow since the count last written for it before a change is written:
// every count up to 16, then steps of about an eighth (a turn that records 3000 files writes about 60 events).
func checkpointStep(written int) int { return max(1, written/8) }

// logCheckpoints writes a "checkpoint" event for the changes the session's checkpoint store reports (see checkpointLog). A store
// that does not report changes writes nothing.
func (s *Session) logCheckpoints() {
	src, ok := any(s.Ckpt).(checkpointSource)
	if !ok || s.Log == nil {
		return
	}
	log := s.Log
	cl := &checkpointLog{
		emit:    func(d map[string]any) { _, _ = log.Emit("", checkpointEvent, d) },
		agents:  src.Agents,
		written: map[string]int{}, pending: map[string]checkpoint.Event{},
	}
	s.ckptLog = cl
	src.OnEvent(cl.event)
}

// flushCheckpoints writes the checkpoint changes still waiting: a turn that ends writes its checkpoint's last state.
func (s *Session) flushCheckpoints() {
	if s.ckptLog != nil {
		s.ckptLog.flush()
	}
}

// event takes one change of the store. A checkpoint that begins, or that a restore changed, is written at once, after what the
// others still owe; a change is written when the count has grown by checkpointStep, and otherwise waits.
func (c *checkpointLog) event(ev checkpoint.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	if ev.Path == "" {
		delete(c.pending, ev.ID)
		c.flushLocked()
		c.writeLocked(ev)
		return
	}
	if last := c.written[ev.ID]; ev.Files-last >= checkpointStep(last) {
		c.writeLocked(ev)
		return
	}
	c.pending[ev.ID] = ev
}

// flush writes every change still waiting.
func (c *checkpointLog) flush() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		c.flushLocked()
	}
}

// close writes what is still waiting and stops; later changes are not written. It runs before the log closes.
func (c *checkpointLog) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		c.flushLocked()
	}
	c.closed = true
}

// flushLocked writes what waits, in the order of the checkpoints' ids; the caller holds c.mu.
func (c *checkpointLog) flushLocked() {
	ids := make([]string, 0, len(c.pending))
	for id := range c.pending {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		c.writeLocked(c.pending[id])
	}
}

// writeLocked writes one event; the caller holds c.mu.
func (c *checkpointLog) writeLocked(ev checkpoint.Event) {
	delete(c.pending, ev.ID)
	c.written[ev.ID] = ev.Files
	agents := c.agents(ev.ID)
	if len(agents) > 16 {
		agents = agents[:16]
	}
	if agents == nil {
		agents = []string{}
	}
	d := map[string]any{"id": ev.ID, "label": ev.Label, "count": ev.Files, "agents": agents, "time": ev.Time.UTC().Format(time.RFC3339Nano)}
	if ev.Path != "" {
		d["file"] = ev.Path
	}
	c.emit(d)
}
