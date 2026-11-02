package swarm

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/anemos-labs/sleipnir/internal/events"
)

// Stopping the whole team on the person's word.
//
// Interrupting the manager's turn (Esc, the Stop button) stops the running workers but leaves the board as it was: their tasks
// stay open, so the person's next message starts a manager run that the stop guard (hold.go), the review gate and a standing goal
// send straight back to that work, and mail from the manager wakes the idle workers that hold it. A person who says "stop" means
// the opposite. StopAll is that stop, and it does not depend on the manager's judgement:
//
//   - the manager's turn ends (as an interrupt does) and every running worker is stopped, without counting an attempt;
//   - the restarts that wait and the spawns queued for a free member are called off;
//   - every open task (todo, doing, review, blocked) is closed as canceled ("stopped by the person"), so nothing is left that a
//     guard could send the manager back to; a later goal can reopen what it still wants;
//   - the auto-docs pass and the review gate let go of the work merged before the stop;
//   - until the person writes again, nothing starts: no worker run (spawn, mail, restart, docs pass, queued spawn), no automatic
//     manager run, no new or reopened task, and the stop guard does not veto the manager's answer.
//
// The stop is lifted by the person's next goal (RunManager), not by the manager. The harness tells the manager what happened in
// one note, delivered with its next turn.

// ErrStopped is the error of a start refused because the person stopped the team.
var ErrStopped = errors.New("the person stopped the team: nothing starts until they write again. Do not spawn, assign, reopen or wake workers; answer the person, and say where things stand")

// stopReason is the reason a run stopped by StopAll gives (finishRun), and the note on the tasks it cancels.
const stopReason = "stopped by the person"

// teamStop is the swarm's stop state. Guarded by its own mutex.
type teamStop struct {
	mu sync.Mutex
	on bool
	// turns end the manager's turns that run now (runManager): one, or two for the moment a person's goal supersedes a wake.
	turns map[*turnEnd]struct{}
}

// turnEnd ends one turn of the manager.
type turnEnd struct{ end context.CancelFunc }

// StopReport is what StopAll did, for the person.
type StopReport struct {
	Workers  []string // the workers whose runs were stopped, sorted
	Canceled []string // the tasks closed as canceled, in board order
	Queued   int      // spawns that waited for a free member, called off
	Turn     bool     // the manager's turn was ended
}

// Line is the report as one sentence.
func (r StopReport) Line() string {
	var parts []string
	switch n := len(r.Workers); n {
	case 0:
		parts = append(parts, "no worker was running")
	case 1:
		parts = append(parts, "stopped 1 worker ("+r.Workers[0]+")")
	default:
		parts = append(parts, "stopped "+strconv.Itoa(n)+" workers ("+listCap(r.Workers, holdListCap)+")")
	}
	if n := len(r.Canceled); n > 0 {
		parts = append(parts, "canceled "+strconv.Itoa(n)+" open "+pluralWord(n, "task", "tasks")+" ("+listCap(r.Canceled, holdListCap)+")")
	}
	if r.Queued > 0 {
		parts = append(parts, "called off "+strconv.Itoa(r.Queued)+" queued "+pluralWord(r.Queued, "spawn", "spawns"))
	}
	if r.Turn {
		parts = append(parts, "ended the manager's turn")
	}
	return "team stopped: " + strings.Join(parts, ", ") + ". Nothing starts until you write again."
}

// listCap joins ids, at most n of them, then "+N more".
func listCap(ids []string, n int) string {
	if len(ids) <= n {
		return strings.Join(ids, ", ")
	}
	return strings.Join(ids[:n], ", ") + " +" + strconv.Itoa(len(ids)-n) + " more"
}

// Stopped reports whether the person stopped the team and has not written since.
func (s *Swarm) Stopped() bool {
	s.halt.mu.Lock()
	defer s.halt.mu.Unlock()
	return s.halt.on
}

// LiftStop takes the stop off: the person wrote again. It reports whether the team was stopped.
func (s *Swarm) LiftStop() bool {
	s.halt.mu.Lock()
	on := s.halt.on
	s.halt.on = false
	s.halt.mu.Unlock()
	if on {
		s.emit(events.TypeSwarmStop, map[string]any{"phase": "lifted"})
	}
	return on
}

// setManagerTurn records what ends a turn of the manager that starts now, for StopAll, and returns what forgets it when the turn is over.
func (s *Swarm) setManagerTurn(end context.CancelFunc) (done func()) {
	t := &turnEnd{end: end}
	s.halt.mu.Lock()
	if s.halt.turns == nil {
		s.halt.turns = map[*turnEnd]struct{}{}
	}
	s.halt.turns[t] = struct{}{}
	s.halt.mu.Unlock()
	return func() {
		s.halt.mu.Lock()
		delete(s.halt.turns, t)
		s.halt.mu.Unlock()
	}
}

// StopAll stops the whole team on the person's behalf (see the head of this file). words is what the person said, if they said
// it in a message ("" for a command or a button); it is quoted to the manager as theirs. It is safe to call while stopped: it
// stops again whatever started since.
func (s *Swarm) StopAll(words string) StopReport {
	var rep StopReport
	s.halt.mu.Lock()
	s.halt.on = true
	ends := make([]context.CancelFunc, 0, len(s.halt.turns))
	for t := range s.halt.turns {
		ends = append(ends, t.end)
	}
	s.halt.mu.Unlock()

	// The manager first: a turn that goes on would spawn and assign while the rest is stopped.
	for _, end := range ends {
		end()
		rep.Turn = true
	}
	s.wk.hold(s.wk.epochNow()) // no automatic manager run either; the person's next input lifts it

	// The queued spawns: nothing is started for them.
	s.mu.Lock()
	rep.Queued = len(s.queued)
	s.queued = nil
	s.mu.Unlock()

	// The open tasks are closed before the runs are stopped: a run that ends meanwhile then finds its assignment changed, and
	// neither submits it nor puts it back on the board.
	c, _ := NewClosure(CloseCanceled, "")
	for _, t := range s.Board.Snapshot().Tasks {
		switch t.Status {
		case StatusTodo, StatusDoing, StatusReview, StatusBlocked:
			if s.Board.Fail("person", t.ID, c, stopReason) == nil {
				rep.Canceled = append(rep.Canceled, t.ID)
			}
		}
	}

	// The workers. A worker the harness meant to restart is called off with them (stopWorkers).
	s.mu.Lock()
	var ms []*member
	for _, m := range s.members {
		if !m.manager && !m.service {
			ms = append(ms, m)
		}
	}
	s.mu.Unlock()
	for _, m := range ms {
		m.mu.Lock()
		if rs := m.run; rs != nil && m.life == lifeRunning {
			rs.stopped = true // never restarted on what it held (planRestart)
		}
		m.mu.Unlock()
		if s.stopRun(m, stopReason, false) {
			rep.Workers = append(rep.Workers, m.id)
		}
	}
	s.dropRestarts(stopReason)
	sort.Strings(rep.Workers)

	// What was merged before the stop no longer waits for a docs pass or a review: the person ended that work.
	s.waiveGates()

	s.emit(events.TypeSwarmStop, map[string]any{"phase": "stopped", "workers": rep.Workers, "canceled": rep.Canceled, "queued": rep.Queued, "turn": rep.Turn})
	if id := s.ManagerID(); id != "" {
		note := "[harness] The person stopped the team: " + strings.TrimPrefix(rep.Line(), "team stopped: ")
		if w := cleanText(words, 300); w != "" {
			note += " Their words: \"" + w + "\"."
		}
		note += " Do not spawn, assign, reopen or wake workers while the team is stopped; if the person asks for more work, start only what they ask for."
		if m := s.get(id); m != nil && m.a != nil {
			m.a.Send(note)
		}
	}
	return rep
}

// waiveGates lets go of the docs pass and the review that the work merged so far is waiting for: the person stopped the team.
func (s *Swarm) waiveGates() {
	if k := s.docs; k != nil {
		snap := s.Board.Snapshot()
		k.mu.Lock()
		k.pending, k.edited, k.open, k.waiting = nil, nil, "", ""
		if k.seen != nil {
			for _, t := range snap.Tasks {
				if t.Status == StatusDone || t.Status == StatusFailed {
					k.seen[t.ID] = t.Rev // not news to the keeper once the person writes again
				}
			}
		}
		k.mu.Unlock()
	}
	g := &s.review
	g.mu.Lock()
	if g.merges > g.reviewed {
		g.reviewed = g.merges
	}
	g.mu.Unlock()
}
