package swarm

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/agent"
)

// Automatic restarts.
//
// A worker whose run fails while it holds work (a task in doing, the pool it leads, or its place in an open pool) is restarted
// by the harness on that work: a fresh run, so the per-run guards (the step windows, the repetition guard) start again, with a
// harness note that says why the last run stopped and what to do differently. The work stays the worker's while it waits, so
// nobody else takes it and the board does not show it unowned; a task's failed attempt is counted on the task in place
// (Board.retryInPlace). The manager is told in one line, so it neither reassigns the work nor starts another worker for it.
//
// What is never restarted: a run that was interrupted (a shutdown, the person, the manager's revise), one stopped by the
// harness for a reason that is not a failure, one that spent its own budget or the swarm's, one the watchdog stopped while it
// waited for the person's answer (a restart would only ask again), a step-limit stop under FixedSteps (the limit is a ceiling
// on the assignment there), a worker that is to be retired, and a worker that holds nothing. Restarts of one worker on the
// same work are bounded: at most maxAutoRestarts in a row (a run that ends on its own starts a new row), and without
// persistent pools only while the task keeps an attempt for the manager after the restarted run (Config.MaxAttempts). Past the
// bound the stop is settled as before, and the manager's line says what was tried.

// maxAutoRestarts bounds the automatic restarts of one worker on the same work after runs that failed in a row.
const maxAutoRestarts = 2

// restartBackoff is the wait before the first automatic restart of a row; each further one waits twice as long.
const restartBackoff = 5 * time.Second

// eventRestart records the automatic restarts of a worker (payload: phase = scheduled | start | dropped | exhausted, and the
// restart's n, max, tasks, pool and why, or the reason a restart was dropped).
const eventRestart = "swarm.restart"

// restartCause says what ended a failed run, for the advice the restarted run is given.
type restartCause int

const (
	causeError  restartCause = iota // an error that is not the work's (a provider that failed, ...)
	causeSteps                      // the step limit
	causeRepeat                     // the repetition guard: one failing call, over and over
	causeQuiet                      // the watchdog: no sign of life for too long
	causeOutput                     // responses cut off by the output limit
	causeCrash                      // a panic the harness contained
)

// pendingRestart is an automatic restart the harness has scheduled for a worker whose run failed.
type pendingRestart struct {
	due   time.Time
	n     int      // which restart of the row this is (1-based)
	tasks []string // the tasks it keeps: work it does, or a pool it leads (sorted)
	// revs are the revisions it keeps them at (0: any): a task revised and assigned again since, even to the same worker, is a
	// new assignment, which the restart neither starts nor returns to the board.
	revs  map[string]uint64
	pool  string // the open pool it is a member of, when it keeps no task
	why   string // why the run stopped
	cause restartCause
	// logged: its scheduled event is in the log. The supervisor does not start it before, so the log never shows a restart
	// starting before it was scheduled.
	logged bool
}

// event is the payload of a swarm.restart event of the restart.
func (p *pendingRestart) event(phase string) map[string]any {
	return map[string]any{"phase": phase, "n": p.n, "max": maxAutoRestarts, "tasks": p.tasks, "pool": p.pool, "why": p.why}
}

// restartCauseOf says whether a run that ended as kind (with count, the run's error, and whether the watchdog stopped it while
// it waited for the person) is one the harness restarts on its own, and why it ended.
func restartCauseOf(kind stopKind, count bool, err error, asked, fixedSteps bool) (restartCause, bool) {
	switch kind {
	case stopHarness:
		// The watchdog is the one stop of the harness that counts as a failure.
		return causeQuiet, count && !asked
	case stopFailed:
	default:
		return 0, false
	}
	var pe *panicError
	switch {
	case errors.Is(err, agent.ErrBudget), errors.Is(err, agent.ErrClosed):
		return 0, false
	case errors.As(err, &pe):
		return causeCrash, true
	case errors.Is(err, agent.ErrStepLimit) || (err != nil && strings.Contains(err.Error(), "step limit")):
		return causeSteps, !fixedSteps
	case errors.Is(err, agent.ErrStuck):
		return causeRepeat, true
	case errors.Is(err, agent.ErrOutputLimit):
		return causeOutput, true
	}
	return causeError, true
}

// planRestart decides whether the harness restarts m on its own after its run rs failed (tasks are the run's assignments and
// the revs it held them at, kind/why/count how it stopped, err what the run returned). It returns the restart, unscheduled and
// uncounted, or nil and the number of restarts already made when the bound is what stops one more. Nothing changes here but
// the member's count when the work it holds is not the work it was counted for.
func (s *Swarm) planRestart(m *member, rs *runState, tasks map[string]uint64, kind stopKind, why string, count bool, err error) (*pendingRestart, int) {
	if m.manager || m.service {
		return nil, 0
	}
	cause, ok := restartCauseOf(kind, count, err, rs.asked, s.cfg.FixedSteps)
	if !ok || s.alive() != nil || s.budgetErr() != nil || s.wk.heldNow() {
		return nil, 0
	}
	m.mu.Lock()
	retire := m.retireOnIdle
	m.mu.Unlock()
	if retire {
		return nil, 0
	}
	snap := s.Board.Snapshot()
	var keep []string
	for _, id := range sortedKeys(tasks) {
		t, ok := snap.Task(id)
		if !ok || t.Owner != m.id || t.Status != StatusDoing {
			continue
		}
		if t.Pool == nil {
			if t.Rev != tasks[id] {
				continue // a newer assignment: not this run's to settle
			}
			if s.cfg.MaxAttempts > 0 && t.Attempts+2 >= s.cfg.MaxAttempts {
				continue // a restart would spend the last attempt, which belongs to the manager's choice of worker
			}
		}
		keep = append(keep, id)
	}
	pool, key := "", strings.Join(keep, ",")
	if len(keep) == 0 {
		if pool = openPoolOf(snap, m.id); pool == "" {
			return nil, 0 // it holds nothing: there is nothing to restart it on
		}
		key = "pool " + pool
	}
	m.mu.Lock()
	if m.restartKey != key {
		m.restartKey, m.restarts = key, 0
	}
	n := m.restarts
	m.mu.Unlock()
	if n >= maxAutoRestarts {
		return nil, n
	}
	return &pendingRestart{due: s.deps.Now().Add(restartBackoff << n), n: n + 1, tasks: keep, pool: pool, why: why, cause: cause}, 0
}

// openPoolOf is the open pool (a pool task in doing) that agent is a member of, or "".
func openPoolOf(snap *Snapshot, agent string) string {
	for _, t := range snap.Tasks {
		if t.Pool != nil && t.Status == StatusDoing && slices.Contains(t.Pool.Members, agent) {
			return t.ID
		}
	}
	return ""
}

// settleFailed settles a run that did not end cleanly (finishRun). When the harness restarts the worker (planRestart), the
// tasks it keeps stay its own, each with a failed attempt counted in place; the rest are settled as settleStopped settles them.
// It returns the restart (nil: none), the restarts already made when the bound stopped one more, and the manager's line.
func (s *Swarm) settleFailed(m *member, rs *runState, tasks map[string]uint64, kind stopKind, why string, count bool, err error) (*pendingRestart, int, string) {
	p, tried := s.planRestart(m, rs, tasks, kind, why, count, err)
	if p == nil {
		return nil, tried, s.settleStopped(m, tasks, kind, why, count, tried > 0)
	}
	rest := maps.Clone(tasks)
	snap := s.Board.Snapshot()
	var kept []string
	p.revs = make(map[string]uint64, len(p.tasks))
	for _, id := range p.tasks {
		delete(rest, id)
		if t, ok := snap.Task(id); ok && t.Pool != nil {
			kept = append(kept, id) // a pool it leads: its members' work, not an attempt of its own
			p.revs[id] = t.Rev
			continue
		}
		if t, ok := s.Board.retryInPlace(m.id, id, tasks[id], why); ok {
			kept = append(kept, id)
			p.revs[id] = t.Rev
		} else {
			rest[id] = tasks[id] // changed since the plan was made: settled as any other
		}
	}
	notes := s.settleStopped(m, rest, kind, why, count, false)
	if len(kept) == 0 && p.pool == "" {
		return nil, 0, notes // the board changed under the stop: the worker holds none of it now
	}
	p.tasks = kept
	m.mu.Lock()
	m.restarts = p.n
	m.mu.Unlock()
	line := fmt.Sprintf("%s stopped (%s). The harness restarts it on its own (automatic restart %d of %d), and it keeps %s: do not reassign that work or start another worker for it.",
		m.id, why, p.n, maxAutoRestarts, p.holding(snap))
	if notes != "" {
		line += " " + notes
	}
	return p, 0, line
}

// holding names, for the manager, the work a restarted worker keeps.
func (p *pendingRestart) holding(snap *Snapshot) string {
	if p.pool != "" {
		return "its place in pool " + p.pool
	}
	var parts []string
	for _, id := range p.tasks {
		if t, ok := snap.Task(id); ok && t.Pool != nil {
			parts = append(parts, "the lead of pool "+id)
		} else {
			parts = append(parts, id)
		}
	}
	return joinAnd(parts)
}

// restartTick starts the automatic restarts that are due at now. It runs on the supervisor's pass.
func (s *Swarm) restartTick(now time.Time) {
	s.mu.Lock()
	ms := make([]*member, 0, len(s.members))
	for _, m := range s.members {
		ms = append(ms, m)
	}
	s.mu.Unlock()
	slices.SortFunc(ms, func(a, b *member) int { return strings.Compare(a.id, b.id) })
	for _, m := range ms {
		m.mu.Lock()
		p := m.restart
		retired := m.life == lifeRetired
		if p == nil || (!retired && (now.Before(p.due) || !p.logged)) {
			m.mu.Unlock()
			continue
		}
		m.restart = nil
		m.mu.Unlock()
		if retired {
			continue // its tasks went back to the board when it was retired
		}
		s.startRestart(m, p)
	}
}

// startRestart restarts m on the work p kept for it: the note goes to its inbox as harness mail, which starts a run (a worker
// that some other path started meanwhile reads it in that run). Work that has left the worker since (a reassignment, a revise,
// even one that gave the task back to it, a failed or closed task, a pool that finished) is not restarted on; a swarm that may
// not run work now drops the restart.
func (s *Swarm) startRestart(m *member, p *pendingRestart) {
	switch {
	case s.alive() != nil:
		s.dropRestart(m, p, "the swarm stopped")
		return
	case s.budgetErr() != nil:
		s.dropRestart(m, p, "swarm budget exhausted")
		return
	case s.wk.heldNow():
		s.dropRestart(m, p, "interrupted")
		return
	}
	snap := s.Board.Snapshot()
	var tasks []string
	for _, id := range p.tasks {
		if t, ok := snap.Task(id); ok && t.Owner == m.id && t.Status == StatusDoing && (p.revs[id] == 0 || t.Rev == p.revs[id]) {
			tasks = append(tasks, id)
		}
	}
	pool := p.pool
	if pool != "" {
		if t, ok := snap.Task(pool); !ok || t.Pool == nil || t.Status != StatusDoing || !slices.Contains(t.Pool.Members, m.id) {
			pool = ""
		}
	}
	if len(tasks) == 0 && pool == "" {
		s.emitAs(m.id, eventRestart, map[string]any{"phase": "dropped", "reason": "its work has moved on", "n": p.n})
		m.setIdleState(s, "idle", "") // what it held is in other hands or closed: it is free, and its failure is settled
		return
	}
	// What is still the worker's goes into a copy: p is the restart the settling run published, and that run may still read it.
	started := *p
	started.tasks, started.pool = tasks, pool
	s.emitAs(m.id, eventRestart, started.event("start"))
	s.notify(m.id, "request", restartNote(snap, &started, s.cfg.PersistentPools))
}

// dropRestarts calls off every automatic restart that waits (dropRestart), for reason.
func (s *Swarm) dropRestarts(reason string) {
	s.mu.Lock()
	ms := make([]*member, 0, len(s.members))
	for _, m := range s.members {
		ms = append(ms, m)
	}
	s.mu.Unlock()
	slices.SortFunc(ms, func(a, b *member) int { return strings.Compare(a.id, b.id) })
	for _, m := range ms {
		m.mu.Lock()
		p := m.restart
		m.restart = nil
		m.mu.Unlock()
		if p != nil {
			s.dropRestart(m, p, reason)
		}
	}
}

// dropRestart calls off a restart the harness had scheduled for m, which is then a worker that failed and is not restarted:
// the tasks it kept go back to the board (their attempt was counted when the run failed), each only at the revision it was kept
// at, and its state says that it failed.
func (s *Swarm) dropRestart(m *member, p *pendingRestart, reason string) {
	for _, id := range p.tasks {
		s.Board.Requeue(m.id, id, p.revs[id], reason, false, s.cfg.MaxAttempts)
	}
	s.emitAs(m.id, eventRestart, map[string]any{"phase": "dropped", "reason": reason, "n": p.n})
	m.setIdleState(s, "failed", p.why)
}

// restartNote is the harness's note to a restarted worker: why its last run stopped, what it still holds, and what to do
// differently. It names ids, counts and the stop's reason only.
func restartNote(snap *Snapshot, p *pendingRestart, pools bool) string {
	var held []string
	var work []string
	for _, id := range p.tasks {
		if t, ok := snap.Task(id); ok && t.Pool != nil {
			held = append(held, fmt.Sprintf("you still lead pool %s (phase %s)", id, t.Pool.Phase))
		} else {
			work = append(work, id)
		}
	}
	switch len(work) {
	case 0:
	case 1:
		held = append(held, work[0]+" is still yours")
	default:
		held = append(held, joinAnd(work)+" are still yours")
	}
	look := slices.Clone(p.tasks)
	if p.pool != "" {
		phase := ""
		if t, ok := snap.Task(p.pool); ok && t.Pool != nil {
			phase = " (phase " + t.Pool.Phase + ")"
		}
		held = append(held, "you are still a member of pool "+p.pool+phase)
		look = append(look, p.pool)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Your last run stopped (%s), and the harness restarts you on your work (automatic restart %d of %d). %s. ",
		p.why, p.n, maxAutoRestarts, upperFirst(strings.Join(held, "; ")))
	b.WriteString(restartAdvice(p.cause, len(work) > 0, pools))
	fmt.Fprintf(&b, " Read task get %s for where it stands.", strings.Join(look, ", "))
	return b.String()
}

// restartAdvice is what a restarted worker is told to do differently, by what ended its last run; work says it holds a task
// to do (not only a pool to lead or take part in).
func restartAdvice(c restartCause, work, pools bool) string {
	switch c {
	case causeSteps:
		if !work {
			return "That run used its steps up without a change: this one has a fresh allowance; do not repeat those steps."
		}
		record := "record where the task stands with task update, or hand it over (task handover)"
		if pools {
			record = "record where the task stands with task update"
		}
		return "That run's last stretch of steps changed little, and this run has a fresh step allowance: do not repeat that " +
			"reading. Make the next concrete change now (an edit or a test run); if the task cannot move, " + record +
			", or block it with what it waits for (task block)."
	case causeRepeat:
		return "It repeated a call that kept failing: take a different approach instead of repeating it."
	case causeQuiet:
		return "It showed no sign of progress for too long, as when a command hangs: do not run the same command the same way " +
			"again; give a long command a timeout or run it in the background."
	case causeOutput:
		return "Its responses were cut off by the output limit: keep each response short, and make a large change in several smaller edits."
	case causeCrash:
		return "The harness contained a crash in it. Carry on from where your work stands."
	}
	return "Carry on from where your work stands."
}

// timesText says how many times: "once", "twice", "3 times".
func timesText(n int) string {
	switch n {
	case 1:
		return "once"
	case 2:
		return "twice"
	}
	return fmt.Sprintf("%d times", n)
}

// joinAnd joins items as "a", "a and b", or "a, b and c".
func joinAnd(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

// upperFirst capitalises the first letter of s.
func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
