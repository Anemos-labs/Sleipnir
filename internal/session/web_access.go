package session

import (
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

// checkpointGap is the least time between two "checkpoint" events of one checkpoint: the changes in between are folded into the
// next one, and the last is written when the turn ends, the next checkpoint begins or the session closes.
const checkpointGap = 250 * time.Millisecond

// checkpointSource is a checkpoint store that reports its changes as cheap events and names a checkpoint's writers.
type checkpointSource interface {
	OnEvent(func(checkpoint.Event))
	Agents(id string) []string
}

// checkpointLog writes the "checkpoint" events of a session: {id, label, count, file, agents, time}, where count is how many
// files the checkpoint holds and file the one recorded last (never the whole list, so the log grows with the number of files,
// not with its square), at most one event per checkpoint per checkpointGap.
type checkpointLog struct {
	emit   func(data map[string]any)
	agents func(id string) []string
	now    func() time.Time

	mu      sync.Mutex
	last    map[string]time.Time
	pending map[string]checkpoint.Event
	timer   *time.Timer
	closed  bool
}

// logCheckpoints writes a "checkpoint" event for the changes the session's checkpoint store reports (see checkpointLog). A store
// that does not report changes writes nothing.
func (s *Session) logCheckpoints() {
	src, ok := any(s.Ckpt).(checkpointSource)
	if !ok || s.Log == nil {
		return
	}
	log := s.Log
	cl := &checkpointLog{
		emit:   func(d map[string]any) { _, _ = log.Emit("", checkpointEvent, d) },
		agents: src.Agents, now: time.Now,
		last: map[string]time.Time{}, pending: map[string]checkpoint.Event{},
	}
	s.ckptLog = cl
	src.OnEvent(cl.event)
}

// flushCheckpoints writes the checkpoint changes still waiting for their gap: a turn that ends writes its checkpoint's last
// state.
func (s *Session) flushCheckpoints() {
	if s.ckptLog != nil {
		s.ckptLog.flush()
	}
}

// event takes one change of the store: a checkpoint that begins ends the others (their last change is written), a change of a
// checkpoint written within checkpointGap of its previous event waits for the gap to pass.
func (c *checkpointLog) event(ev checkpoint.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	now := c.now()
	if ev.Path == "" { // began, or a restore changed it: written at once, after what the others still owe
		for id, p := range c.pending {
			if id != ev.ID {
				c.writeLocked(p, now)
			}
		}
		c.writeLocked(ev, now)
		return
	}
	if now.Sub(c.last[ev.ID]) >= checkpointGap {
		c.writeLocked(ev, now)
		return
	}
	c.pending[ev.ID] = ev
	if c.timer == nil {
		c.timer = time.AfterFunc(checkpointGap-now.Sub(c.last[ev.ID]), c.flush)
	}
}

// flush writes every change still waiting (the timer's, and a turn's end).
func (c *checkpointLog) flush() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
	if c.closed {
		return
	}
	now := c.now()
	for _, p := range c.pending {
		c.writeLocked(p, now)
	}
}

// close writes what is still waiting and stops; later changes are not written. It runs before the log closes.
func (c *checkpointLog) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
	now := c.now()
	for _, p := range c.pending {
		c.writeLocked(p, now)
	}
	c.closed = true
}

// writeLocked writes one event; the caller holds c.mu.
func (c *checkpointLog) writeLocked(ev checkpoint.Event, now time.Time) {
	delete(c.pending, ev.ID)
	c.last[ev.ID] = now
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
