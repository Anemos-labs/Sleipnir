package session

import (
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

// checkpointNotifier is a checkpoint store that tells a function when a checkpoint begins and when its file set changes.
type checkpointNotifier interface {
	OnChange(func(checkpoint.Info))
}

// logCheckpoints writes a "checkpoint" event for every change the session's checkpoint store reports: {id, label, files, agents,
// time}. A store that does not report changes writes nothing.
func (s *Session) logCheckpoints() {
	n, ok := any(s.Ckpt).(checkpointNotifier)
	if !ok || s.Log == nil {
		return
	}
	log := s.Log
	n.OnChange(func(c checkpoint.Info) {
		files, agents := c.Files, c.Agents
		if files == nil {
			files = []string{}
		}
		if agents == nil {
			agents = []string{}
		}
		_, _ = log.Emit("", checkpointEvent, map[string]any{
			"id": c.ID, "label": c.Label, "files": files, "agents": agents, "time": c.Time.UTC().Format(time.RFC3339Nano),
		})
	})
}
