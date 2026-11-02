package swarm

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
)

// Stopping a worker that is stuck. The watchdog (supervisor.go) stops a worker that shows no sign of life, and the stall sweep
// (stall.go) names a worker that is busy without advancing; neither stops a worker that keeps calling tools on a task it will never
// finish. Two paths end such a run: the person stops it (StopAgent: /agents stop ID in both interfaces), and the harness stops it
// itself when a finding about it still holds StallStop after it was escalated to the manager (stopStalled). Either way the run is
// cancelled with a reason, its tasks go back to todo with the attempt counted (a task stopped MaxAttempts times fails), the
// harness does not restart the worker on them on its own, and the manager is told in one line. A run that ignores the
// cancellation is abandoned by the watchdog after StuckGrace, as a run the watchdog cancelled is.

// defaultStallStop is Config.StallStop when it is zero.
const defaultStallStop = 20 * time.Minute

// stallStop is how long an escalated finding about a worker may hold before the harness stops the worker (0: never).
func (s *Swarm) stallStop() time.Duration {
	switch {
	case s.cfg.StallStop < 0:
		return 0
	case s.cfg.StallStop == 0:
		return defaultStallStop
	}
	return s.cfg.StallStop
}

// stoppable reports whether a finding of kind k is about a worker's own run, which stopping that run ends: the kinds the worker
// is told of. The others (an orphan, a cycle, a review that waits, idle capacity, a refused wait) are the manager's to decide.
func stoppable(k StallKind) bool {
	switch k {
	case StallClaimedNoProgress, StallReviewOverrun, StallTaskOverrun:
		return true
	}
	return false
}

// ErrNotRunning is StopAgent's error for an agent that has no run to stop.
var ErrNotRunning = errors.New("not running")

// StopAgent stops the running worker id on the person's behalf: its run is cancelled, the tasks it holds go back to todo (the
// attempt counts), the harness does not restart it on them, and the manager is told. It returns the tasks the run held, sorted.
// The manager and the harness's own agents are not stopped this way: the manager's turn is interrupted instead.
func (s *Swarm) StopAgent(id string) ([]string, error) {
	m := s.get(id)
	if m == nil {
		return nil, fmt.Errorf("no agent %q", cleanText(id, 40))
	}
	if m.manager {
		return nil, fmt.Errorf("%s is the manager: interrupt its turn instead", id)
	}
	if m.service {
		return nil, fmt.Errorf("%s is the harness's own agent", id)
	}
	tasks, ok := s.stopStuck(m, "stopped by the person")
	if !ok {
		return nil, fmt.Errorf("%s is %w", id, ErrNotRunning)
	}
	s.emitAs(id, "agent.stop", map[string]any{"id": id, "by": "person", "tasks": tasks})
	return tasks, nil
}

// stopStuck cancels m's current run with reason, counting the attempt, and marks it so that the harness neither restarts the
// worker on its tasks (planRestart) nor waits for a run that ignores the cancellation longer than StuckGrace (watch). It returns
// the run's tasks and whether there was a run to stop.
func (s *Swarm) stopStuck(m *member, reason string) ([]string, bool) {
	m.mu.Lock()
	rs := m.run
	if rs == nil || m.life != lifeRunning {
		m.mu.Unlock()
		return nil, false
	}
	if rs.reason == "" {
		rs.reason, rs.count = reason, true
	}
	rs.stopped = true
	if rs.abortAt.IsZero() {
		rs.abortAt = s.deps.Now()
	}
	tasks := make([]string, 0, len(rs.tasks))
	for id := range rs.tasks {
		tasks = append(tasks, id)
	}
	m.mu.Unlock()
	sort.Slice(tasks, func(i, j int) bool { return lessTaskID(tasks[i], tasks[j]) })
	rs.cancel()
	return tasks, true
}

// stopStalled stops the workers of the escalated findings that have held StallStop since their escalation: the manager was told
// and the worker was told, and the run still does not advance. Each finding stops its worker once (a finding that clears and holds
// again starts a new clock). It is called by escalateStalls with st.sweepMu held.
func (s *Swarm) stopStalled() {
	after := s.stallStop()
	if after <= 0 {
		return
	}
	st := &s.stalls
	now := s.deps.Now()
	var due []Stall
	var held []time.Duration
	st.mu.Lock()
	for k, f := range st.active {
		at, ok := st.since[k]
		if !ok || !st.escalated[k] || st.stopped[k] || !stoppable(f.Kind) || f.Agent == "" {
			continue
		}
		if now.Sub(at) < stallEscalateAfter+after {
			continue
		}
		st.stopped[k] = true
		due = append(due, f)
		held = append(held, now.Sub(at))
	}
	for k := range st.stopped {
		if _, ok := st.active[k]; !ok {
			delete(st.stopped, k)
		}
	}
	st.mu.Unlock()
	for i, f := range due {
		m := s.get(f.Agent)
		if m == nil || m.manager || m.service {
			continue
		}
		why := fmt.Sprintf("stopped by the harness: %s held for %d minutes", f.Kind, int(held[i]/time.Minute))
		if f.Task != "" {
			why = fmt.Sprintf("stopped by the harness: %s on %s held for %d minutes", f.Kind, f.Task, int(held[i]/time.Minute))
		}
		tasks, ok := s.stopStuck(m, why)
		if !ok {
			continue
		}
		ev := stallEvent("stop", f)
		ev["held_s"] = int(held[i].Seconds())
		s.emit(events.TypeSwarmStall, ev)
		s.emitAs(m.id, "agent.stop", map[string]any{"id": m.id, "by": "harness", "tasks": tasks, "kind": string(f.Kind)})
	}
}
