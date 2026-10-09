package swarm

import (
	"context"
	"fmt"
	"time"
)

// The supervisor is one goroutine that does the swarm's periodic housekeeping:
// enforcing the budget, expiring alerts and stale leases, moving coalesced mail
// into inboxes, retiring long-idle workers, watching for stuck ones, and sweeping for
// named stalls (stall.go). Each step
// is contained: a failure in one member's check does not stop the rest.

func (s *Swarm) supervise(ctx context.Context) {
	t := time.NewTicker(s.cfg.SuperviseEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.superviseOnce(s.deps.Now())
		}
	}
}

// guard recovers a supervisor callback panic and records its location and value as an event.
func (s *Swarm) guard(what string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			s.emit("supervisor.panic", map[string]any{"in": what, "panic": fmt.Sprint(r)})
		}
	}()
	fn()
}

// superviseOnce is one housekeeping pass at time now (tests call it directly).
func (s *Swarm) superviseOnce(now time.Time) {
	s.guard("budget", func() { _ = s.budgetErr() })
	s.guard("alerts", func() { s.Board.ExpireAlerts(); s.Leases.Sweep() })
	if s.mail != nil {
		s.guard("mailman", func() { s.mail.sweep(now) })
	}
	s.mu.Lock()
	ms := make([]*member, 0, len(s.members))
	for _, m := range s.members {
		ms = append(ms, m)
	}
	s.mu.Unlock()
	for _, m := range ms {
		m := m
		s.guard("member "+m.id, func() { s.superviseMember(m, now) })
	}
	s.guard("stalls", s.sweepStalls)
}

func (s *Swarm) superviseMember(m *member, now time.Time) {
	delivered := m.pump(s)
	if m.manager || m.service {
		return // the manager is never retired or watched; the mailman has its own watch (mailman.go)
	}
	if delivered {
		s.wakeForMail(m, "") // a digest of peer mail just arrived: no-op unless the worker is idle
	}
	m.mu.Lock()
	life, rs, idleAt := m.life, m.run, m.idleAt
	m.mu.Unlock()
	switch life {
	case lifeIdle:
		if s.cfg.IdleRetire > 0 && !idleAt.IsZero() && now.Sub(idleAt) > s.cfg.IdleRetire {
			_ = s.Retire(m.id)
		}
	case lifeRunning:
		s.watch(m, rs, now)
	}
}

// watch is the stuck-worker watchdog. A worker that shows no sign of life (no model
// or tool event) for StuckAfter gets an alert; at twice that its run is cancelled
// and its task requeued; if the run still has not returned after StuckGrace (a tool
// that ignores its context) the harness stops waiting for it: the worker is retired
// and the abandoned goroutine can finish on its own time.
func (s *Swarm) watch(m *member, rs *runState, now time.Time) {
	if s.cfg.StuckAfter <= 0 || rs == nil {
		return
	}
	m.mu.Lock()
	if m.run != rs || m.life != lifeRunning {
		m.mu.Unlock()
		return
	}
	quiet := now.Sub(time.Unix(0, m.progress.Load()))
	warned := m.stuckWarn
	aborted := rs.abortAt
	question := ""
	if m.asking > 0 {
		question = m.askWhat
	}
	var cancel, abandon bool
	var warning string
	switch {
	case !aborted.IsZero():
		abandon = now.Sub(aborted) > s.cfg.StuckGrace
	case quiet > 2*s.cfg.StuckAfter:
		why := fmt.Sprintf("stuck: no progress for %s", quiet.Round(time.Second))
		if question != "" {
			// Nobody is at fault in the worker: the person it asked did not answer. Say what it was waiting for, so that the manager
			// (and the person, through it) can tell it from a worker that hung.
			why = fmt.Sprintf("stopped after %s without an answer to its question to the person: %s", quiet.Round(time.Second), question)
		}
		if rs.reason == "" {
			rs.reason, rs.count = why, true
		}
		rs.abortAt = now
		cancel = true
	case quiet > s.cfg.StuckAfter && !warned:
		m.stuckWarn = true
		warning = fmt.Sprintf("%s has made no progress for %s", m.id, quiet.Round(time.Second))
		if question != "" {
			warning = fmt.Sprintf("%s has waited %s for the person to answer: %s", m.id, quiet.Round(time.Second), question)
		}
	}
	m.mu.Unlock()
	// Decisions belong to the run checked under the lock. A successor may reserve
	// this member before cancellation or board publication finishes.
	switch {
	case abandon:
		s.abandon(m, rs)
	case cancel:
		rs.cancel()
	case warning != "":
		key := stuckAlertKey(m.id, rs.id)
		s.Board.RaiseAlertKey("stuck", key, warning)
		m.mu.Lock()
		current := m.run == rs && m.life == lifeRunning
		m.mu.Unlock()
		if !current {
			s.Board.ClearAlertKey("stuck", key)
		}
	}
}

// stuckAlertKey identifies one run's warning so late cleanup cannot erase a
// successor's alert, and a late publication can remove only its own warning.
func stuckAlertKey(agentID string, runID uint64) string {
	return fmt.Sprintf("stuck:%s:%d", agentID, runID)
}

// abandon settles a run the harness has given up waiting for and retires its worker.
func (s *Swarm) abandon(m *member, rs *runState) {
	m.mu.Lock()
	if m.run != rs || m.life != lifeRunning {
		m.mu.Unlock()
		return
	}
	tasks := make(map[string]uint64, len(rs.tasks))
	for id, rev := range rs.tasks {
		tasks[id] = rev
	}
	m.run, m.life = nil, lifeRetired
	m.mu.Unlock()
	rs.cancel()
	line := s.settleStopped(m, tasks, stopFailed, "stuck: it did not stop when told to", true)
	s.Board.ClearAlertKey("stuck", stuckAlertKey(m.id, rs.id))
	s.detach(m)
	s.emitAs(m.id, "agent.abandon", map[string]any{"id": m.id})
	if line != "" {
		s.notifyManager(line)
	}
}
