package swarm

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// A pool round (agree on a plan, confirm a combined result) ends only when every member has voted. A planning pool holds every
// one of its members until then, so one slow vote keeps the voters idle and the parents queued for the pool's role waiting for a
// free member, while the manager sees only busy members. The manager can excuse the slow member (pool excuse) once it knows.
//
// poolRoundTick tells the manager, once per round, when a round has waited poolRoundWait since its first vote for the members
// that have not voted, naming the parents queued for the pool's role. A notice the manager's inbox would fold into a digest is
// held back and tried again on a later pass.

// poolRoundWait is how long a pool round waits on its missing votes after the first one before the manager is told.
const poolRoundWait = 10 * time.Minute

// poolRound is a round of a pool's votes as the supervisor saw it: the revision and phase it is for, when its first vote was
// seen, and whether the manager was told about it.
type poolRound struct {
	key   string
	since time.Time
	told  bool
}

// poolRoundTick looks at every open pool that is agreeing or confirming (planning or reconciling) with some votes in and some
// missing, and tells the manager about the rounds that have waited poolRoundWait since their first vote was seen.
func (s *Swarm) poolRoundTick(now time.Time) {
	sn := s.Board.Snapshot()
	seen := map[string]bool{}
	type notice struct{ id, key, line string }
	var tell []notice
	s.mu.Lock()
	if s.rounds == nil {
		s.rounds = map[string]poolRound{}
	}
	for _, t := range sn.Tasks {
		p := t.Pool
		if p == nil || t.Status == StatusDone || t.Status == StatusFailed || (p.Phase != "planning" && p.Phase != "reconciling") {
			continue
		}
		missing := missingVotes(*p)
		if len(p.Votes) == 0 || len(missing) == 0 {
			continue
		}
		seen[t.ID] = true
		key := fmt.Sprintf("%s/%d", p.Phase, p.Revision)
		r, ok := s.rounds[t.ID]
		if !ok || r.key != key {
			s.rounds[t.ID] = poolRound{key: key, since: now}
			continue
		}
		if r.told || now.Sub(r.since) < poolRoundWait {
			continue
		}
		tell = append(tell, notice{t.ID, key, poolRoundNote(t, *p, missing, now.Sub(r.since), s.queuedFor(t.Role))})
	}
	for id := range s.rounds {
		if !seen[id] {
			delete(s.rounds, id) // the round ended (every vote is in, the pool moved on, or it closed)
		}
	}
	s.mu.Unlock()
	for _, n := range tell {
		// A digest keeps one harness notice of a kind, so a later one could replace this: it is sent whole or not yet, and a
		// round counts as told only once it was.
		if !s.notifyWhole(s.ManagerID(), "info", n.line) {
			continue
		}
		s.mu.Lock()
		if r, ok := s.rounds[n.id]; ok && r.key == n.key {
			r.told = true
			s.rounds[n.id] = r
		}
		s.mu.Unlock()
	}
}

// queuedFor lists the ids of the parents queued for a free member of role, oldest first. The caller holds s.mu.
func (s *Swarm) queuedFor(role string) []string {
	var qs []queuedSpawn
	for _, q := range s.queued {
		if q.req.Role == role {
			qs = append(qs, q)
		}
	}
	slices.SortFunc(qs, func(a, b queuedSpawn) int {
		if c := a.at.Compare(b.at); c != 0 {
			return c
		}
		return strings.Compare(a.req.TaskID, b.req.TaskID)
	})
	ids := make([]string, len(qs))
	for i, q := range qs {
		ids[i] = q.req.TaskID
	}
	return ids
}

// poolRoundNote is the manager's notice that pool t has waited for long on the members in missing.
func poolRoundNote(t Task, p PoolState, missing []string, waited time.Duration, queued []string) string {
	vote := "agree to"
	if p.Phase == "reconciling" {
		vote = "confirm"
	}
	who := strings.Join(missing, ", ")
	line := fmt.Sprintf("Pool %s has waited %d minutes for %s to %s revision %d (%s did).", t.ID, int(waited.Minutes()), who, vote, p.Revision,
		strings.Join(p.Votes, ", "))
	if len(queued) > 0 && p.Phase == "planning" {
		line += fmt.Sprintf(" A planning pool holds every one of its members meanwhile, and %s waits in the queue for a free %s member.", strings.Join(queued, ", "), t.Role)
	}
	return line + fmt.Sprintf(" Mail %s, or take it out of the pool with pool action=excuse id=%s member=%s text=<why> so the round can complete.", who, t.ID, missing[0])
}
