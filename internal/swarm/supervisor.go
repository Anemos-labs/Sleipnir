package swarm

import (
	"context"
	"fmt"
	"time"
)

// The supervisor is one goroutine that does the swarm's periodic housekeeping:
// enforcing the budget, expiring alerts and stale leases, moving coalesced mail
// into inboxes, retiring long-idle workers, and watching for stuck ones. Each step
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
	quiet := now.Sub(time.Unix(0, m.progress.Load()))
	m.mu.Lock()
	warned := m.stuckWarn
	aborted := rs.abortAt
	question := ""
	if m.asking > 0 {
		question = m.askWhat
	}
	m.mu.Unlock()
	switch {
	case !aborted.IsZero():
		if now.Sub(aborted) > s.cfg.StuckGrace {
			s.abandon(m, rs)
		}
	case quiet > 2*s.cfg.StuckAfter:
		why := fmt.Sprintf("stuck: no progress for %s", quiet.Round(time.Second))
		if question != "" {
			// Nobody is at fault in the worker: the person it asked did not answer. Say what it was waiting for, so that the manager
			// (and the person, through it) can tell it from a worker that hung.
			why = fmt.Sprintf("stopped after %s without an answer to its question to the person: %s", quiet.Round(time.Second), question)
		}
		if s.stopRun(m, why, true) {
			m.mu.Lock()
			rs.abortAt = now
			m.mu.Unlock()
		}
	case quiet > s.cfg.StuckAfter && !warned:
		m.mu.Lock()
		m.stuckWarn = true
		m.mu.Unlock()
		msg := fmt.Sprintf("%s has made no progress for %s", m.id, quiet.Round(time.Second))
		if question != "" {
			msg = fmt.Sprintf("%s has waited %s for the person to answer: %s", m.id, quiet.Round(time.Second), question)
		}
		s.Board.RaiseAlertKey("stuck", "stuck:"+m.id, msg)
	}
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
	s.Board.ClearAlertKey("stuck", "stuck:"+m.id)
	s.detach(m)
	s.emitAs(m.id, "agent.abandon", map[string]any{"id": m.id})
	if line != "" {
		s.notifyManager(line)
	}
}
