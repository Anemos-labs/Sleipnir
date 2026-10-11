package session

import (
	"context"
	"errors"
	"time"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/events"
)

// ErrNoSavepoints is returned by Savepoint for a session started without
// Options.Savepoints.
var ErrNoSavepoints = errors.New("session: savepoints are off (Options.Savepoints)")

// SavepointResult describes one savepoint.
type SavepointResult struct {
	// N numbers the session's savepoints from 1.
	N int `json:"n"`
	// Consistent is false when writes did not pause within the wait: nothing was
	// written or captured, and the savepoint cannot be resumed from.
	Consistent bool `json:"consistent"`
	// LogBytes is the size of the event log after the savepoint's event: what a
	// resume keeps.
	LogBytes int64 `json:"log_bytes"`
	// Seq is the savepoint event's sequence number.
	Seq uint64 `json:"seq"`
	// Agents maps each agent to the snapshot blob a resume restores it from.
	Agents map[string]core.Hash `json:"agents"`
	// Waited is how long the session waited for writes to pause.
	Waited time.Duration `json:"waited_ns"`
}

// Savepoint records a durable resume point. It waits, politely and then for at
// most firm while new writes wait, for a moment when no tool batch writes; then,
// with writes paused, it writes every agent's recoverable snapshot (the state
// after its last complete tool batch), emits session.savepoint, flushes the log
// to disk, and calls capture (which records the work tree) before letting
// writes continue. When writes do not pause in time the savepoint is skipped:
// a session.savepoint with consistent false is emitted and capture is not
// called.
func (s *Session) Savepoint(ctx context.Context, polite, firm time.Duration, capture func(context.Context) error) (SavepointResult, error) {
	if s.barrier == nil {
		return SavepointResult{}, ErrNoSavepoints
	}
	s.mu.Lock()
	s.savepoints++
	res := SavepointResult{N: s.savepoints, Agents: map[string]core.Hash{}}
	s.mu.Unlock()
	started := time.Now()
	ok := s.barrier.Acquire(ctx, polite, firm)
	res.Waited = time.Since(started)
	if !ok {
		seq, err := s.Log.Emit("", events.TypeSessionSavepoint, map[string]any{"n": res.N, "consistent": false, "waited_ms": res.Waited.Milliseconds()})
		res.Seq = seq
		return res, errors.Join(err, ctx.Err())
	}
	defer s.barrier.Release()
	for _, a := range s.liveAgents() {
		h, err := a.WriteRecoverable()
		if err != nil {
			return res, err
		}
		if h != "" {
			res.Agents[a.ID()] = h
		}
	}
	seq, err := s.Log.Emit("", events.TypeSessionSavepoint, map[string]any{"n": res.N, "consistent": true, "agents": res.Agents,
		"waited_ms": res.Waited.Milliseconds()})
	if err != nil {
		return res, err
	}
	res.Seq = seq
	if res.LogBytes, err = s.Log.FlushedSize(); err != nil {
		return res, err
	}
	if capture != nil {
		if err := capture(ctx); err != nil {
			return res, err
		}
	}
	res.Consistent = true
	return res, nil
}

// liveAgents are the agents a savepoint snapshots: the single agent, or every
// member of the swarm.
func (s *Session) liveAgents() []*agent.Agent {
	if s.Swarm != nil {
		return s.Swarm.LiveAgents()
	}
	if s.Agent != nil {
		return []*agent.Agent{s.Agent}
	}
	return nil
}

// agentBarrier is the barrier for agent.Config: nil (no interface holding a nil
// pointer) when savepoints are off.
func (s *Session) agentBarrier() agent.Barrier {
	if s.barrier == nil {
		return nil
	}
	return s.barrier
}
