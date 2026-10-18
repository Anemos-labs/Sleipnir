package swarm

import (
	"fmt"
	"strconv"
	"strings"
)

// Waking a pool's leader when its children are settled.
//
// A pool's leader reconciles the combined result and submits the pool task once every child is accepted. Nothing else moves a
// pool on: when the last child is accepted while the leader is idle, or the leader's turn ends after that without reconciling,
// the pool stays executing, its members stay held by it, and work queued for their role waits for ever. A live run's three
// backend pools sat so for an hour, each leader idle with every child merged, while the manager kept asking for a free member.
//
// The supervisor's poolWakeTick reads the board: an executing pool whose children are all accepted or waiting in review, whose
// leader is idle, gets one harness request to the leader saying what is left (accept the children in review, reconcile, have the
// members confirm, submit). The request is sent once per state of the children: a child that is accepted, revised or added, or a
// new revision of the pool, is a new state; another idle turn of the leader with nothing changed is not.

// poolWakeTick wakes the idle leader of each executing pool whose children are settled (poolSettled), once per state of them.
func (s *Swarm) poolWakeTick() {
	s.poolWakeMu.Lock()
	defer s.poolWakeMu.Unlock()
	if s.wk.heldNow() || s.alive() != nil || s.budgetErr() != nil {
		return
	}
	if s.poolWoken == nil {
		s.poolWoken = map[string]string{}
	}
	snap := s.Board.Snapshot()
	settled := map[string]bool{}
	for _, t := range snap.Tasks {
		if t.Pool == nil || t.Status != StatusDoing || t.Pool.Phase != "executing" || t.Owner == "" {
			continue
		}
		children, ok := poolSettled(snap, t.ID)
		if !ok {
			continue
		}
		settled[t.ID] = true
		state := poolWakeState(t, children)
		if s.poolWoken[t.ID] == state || !s.leaderIdle(t.Owner) {
			continue
		}
		s.poolWoken[t.ID] = state
		s.notify(t.Owner, "request", poolSettledNote(t, children))
	}
	for id := range s.poolWoken {
		if !settled[id] {
			delete(s.poolWoken, id)
		}
	}
}

// poolSettled returns the children of parent in board order, and whether there is at least one and every one is done or waiting
// in review: nothing is left for a member to do but the leader's acceptance and reconciliation.
func poolSettled(sn *Snapshot, parent string) ([]Task, bool) {
	var out []Task
	for _, c := range sn.Tasks {
		if c.Parent != parent {
			continue
		}
		if c.Status != StatusDone && c.Status != StatusReview {
			return nil, false
		}
		out = append(out, c)
	}
	return out, len(out) > 0
}

// poolWakeState names the state of a pool and its children that a wake answers: the pool's assignment and revision, and each
// child's id, assignment and status. It is a key in harness memory only, never text a model reads.
func poolWakeState(t Task, children []Task) string {
	var b strings.Builder
	b.WriteString(strconv.FormatUint(t.Rev, 10))
	b.WriteByte('/')
	b.WriteString(strconv.Itoa(t.Pool.Revision))
	for _, c := range children {
		b.WriteByte('|')
		b.WriteString(c.ID)
		b.WriteByte(':')
		b.WriteString(strconv.FormatUint(c.Rev, 10))
		b.WriteByte(':')
		b.WriteString(string(c.Status))
	}
	return b.String()
}

// leaderIdle reports whether agent is a registered worker that is idle, not waiting for an automatic restart and not waiting for
// an answer: one whose turn has ended and that nothing else will start.
func (s *Swarm) leaderIdle(agent string) bool {
	m := s.get(agent)
	if m == nil || m.manager || m.service {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.life == lifeIdle && m.restart == nil && m.asking == 0
}

// poolSettledNote is the request to a pool's leader whose children are settled: the children still in review to accept first,
// then reconcile, confirm and submit. It holds task ids and statuses only.
func poolSettledNote(t Task, children []Task) string {
	var review []string
	for _, c := range children {
		if c.Status == StatusReview {
			review = append(review, c.ID)
		}
	}
	steps := fmt.Sprintf("reconcile it (pool reconcile id=%s), have every member inspect and confirm the combined result (pool inspect, pool confirm), then submit it (task done id=%s). "+
		"The pool's members stay held by it, and work queued for their role waits, until it is submitted.", t.ID, t.ID)
	if len(review) == 0 {
		return fmt.Sprintf("Every child of pool %s is accepted and the pool is still executing: %s", t.ID, steps)
	}
	return fmt.Sprintf("Every child of pool %s is finished; %s still in review. As the leader, check each and accept it (task accept id=%s), "+
		"or mail the manager what to fix so it rejects it. Once every child is accepted, %s", t.ID, joinWords(review, "and")+" "+isAre(len(review)), review[0], steps)
}

// isAre is the verb that agrees with a count of n things.
func isAre(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}
