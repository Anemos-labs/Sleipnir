package swarm

import (
	"errors"
	"fmt"
	"sort"
	"time"
)

// A parent the manager assigns while every member of its role is busy (noSpareMember) used to be refused with "assign this parent
// again when one is free", and managers did not wait: in a 32-worker run one retried for an hour, and in a 13-worker run a manager
// spawned the same backend parent seventeen times in fifty minutes, each try a turn over its whole context. Such a parent is queued
// instead: the harness starts it on the first member of its role that is free (queueTick, on the supervisor's pass), and tells the
// manager when it does. A queued parent that is assigned, revised or closed meanwhile is dropped from the queue. The queue lives in
// the process: a resumed session starts with none, and the manager assigns again what is still todo.

// queuedSpawn is a spawn waiting for a free member of its role, of the task at revision rev.
type queuedSpawn struct {
	req SpawnReq
	rev uint64
	at  time.Time
}

// queueSpawn queues req (a spawn of the existing task req.TaskID, at revision rev, that found no free member of its role). It
// reports whether it was queued now, false when it already was.
func (s *Swarm) queueSpawn(req SpawnReq, rev uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.queued == nil {
		s.queued = map[string]queuedSpawn{}
	}
	if _, ok := s.queued[req.TaskID]; ok {
		return false
	}
	s.queued[req.TaskID] = queuedSpawn{req: req, rev: rev, at: s.deps.Now()}
	return true
}

// queuedTasks lists the queued spawns, oldest first.
func (s *Swarm) queuedTasks() []queuedSpawn {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]queuedSpawn, 0, len(s.queued))
	for _, q := range s.queued {
		out = append(out, q)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].at.Equal(out[j].at) {
			return out[i].at.Before(out[j].at)
		}
		return out[i].req.TaskID < out[j].req.TaskID
	})
	return out
}

// dropQueued removes the queued spawn of task id.
func (s *Swarm) dropQueued(id string) {
	s.mu.Lock()
	delete(s.queued, id)
	s.mu.Unlock()
}

// queueTick starts the queued spawns whose role has a free member now, oldest first. It runs on the supervisor's pass and starts
// nothing while the team is held (the person interrupted it), stopped, or over its budget.
func (s *Swarm) queueTick(now time.Time) {
	if s.wk.heldNow() || s.alive() != nil || s.budgetErr() != nil {
		return
	}
	for _, q := range s.queuedTasks() {
		id := q.req.TaskID
		if t, ok := s.Board.Snapshot().Task(id); !ok || t.Status != StatusTodo || t.Owner != "" || t.Rev != q.rev {
			// assigned, revised (a new revision even when it is still todo), closed or gone since it was queued: not the harness's
			// to start any more; the manager assigns it again if it wants it started
			s.dropQueued(id)
			continue
		}
		r, err := s.spawn(q.req)
		var busy *noSpareMember
		switch {
		case errors.As(err, &busy):
			continue // still nobody free
		case err != nil:
			s.dropQueued(id)
			s.notify(s.ManagerID(), "info", fmt.Sprintf("%s was queued for a free %s member and could not be started: %v", id, q.req.Role, err))
		default:
			s.dropQueued(id)
			s.notify(s.ManagerID(), "info", fmt.Sprintf("%s started on %s: a %s member was free (it was queued %s ago).", id, r.Agent, q.req.Role, now.Sub(q.at).Round(time.Second)))
		}
	}
}
