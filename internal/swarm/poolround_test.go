package swarm

import (
	"context"
	"slices"
	"testing"
	"time"
)

// A planning round that waits on one member's vote holds the whole pool, and the parents queued for the role with it. Once the
// round has waited poolRoundWait since its first vote, the manager is told once which member it waits on, what is queued
// meanwhile, and how to excuse the member; a new revision starts a new round, and a completed round says nothing.
func TestAPoolRoundWaitingOnOneMemberIsToldToTheManager(t *testing.T) {
	// SuperviseEvery is an hour: the test drives the ticks with its own clock, and no background pass runs on the real one.
	r := newRVRig(t, Config{PersistentPools: true, MaxWorkers: 3, Team: []string{"frontend", "frontend", "frontend"}, SuperviseEvery: time.Hour},
		func(ctx context.Context, _ *rvCall) rvReply { <-ctx.Done(); return rvReply{Text: "stopped"} })
	r.sw.StartManager()
	if _, err := r.sw.Spawn(SpawnReq{Role: "frontend", Title: "logic engine", Files: []string{"web/src/logic.ts"}, By: "mgr"}); err != nil {
		t.Fatal(err)
	}
	pool := r.sw.Board.Snapshot().Tasks[0]
	members := slices.Clone(pool.Pool.Members)
	if len(members) != 3 || pool.Pool.Phase != "planning" {
		t.Fatalf("the pool: %+v", pool.Pool)
	}
	site, err := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "site", Role: "frontend", Files: []string{"site/**"}})
	if err != nil {
		t.Fatal(err)
	}
	r.sw.queueSpawn(SpawnReq{Role: "frontend", TaskID: site.ID, By: "mgr"}, site.Rev)
	vote := func(rev int, by ...string) {
		t.Helper()
		if err := r.sw.Board.poolUpdate("mgr", pool.ID, func(_ *Snapshot, _ *Task, p *PoolState) error {
			p.Revision, p.Votes = rev, by
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	told := func() int { return mailSent(r, "Pool "+pool.ID+" has waited") }
	start := time.Date(2026, 11, 3, 17, 25, 0, 0, time.UTC)

	// no vote yet: the leader may still be writing the plan, nothing is timed
	r.sw.poolRoundTick(start)
	r.sw.poolRoundTick(start.Add(time.Hour))
	if told() != 0 {
		t.Fatal("told before any vote")
	}
	// two of three agreed: timed from here, told once past the wait
	vote(2, members[0], members[1])
	r.sw.poolRoundTick(start)
	r.sw.poolRoundTick(start.Add(poolRoundWait - time.Second))
	if told() != 0 {
		t.Fatal("told before the wait was over")
	}
	r.sw.poolRoundTick(start.Add(poolRoundWait + 2*time.Minute))
	r.sw.poolRoundTick(start.Add(poolRoundWait + 5*time.Minute))
	rvWait(t, "the manager to be told", func() bool { return told() == 1 })
	for _, want := range []string{
		"has waited 12 minutes for " + members[2] + " to agree to revision 2 (" + members[0] + ", " + members[1] + " did)",
		site.ID + " waits in the queue for a free frontend member",
		"pool action=excuse id=" + pool.ID + " member=" + members[2],
	} {
		if mailSent(r, want) != 1 {
			t.Errorf("the notice does not say %q", want)
		}
	}
	// a new revision is a new round: timed again, told again
	later := start.Add(time.Hour)
	vote(3, members[0])
	r.sw.poolRoundTick(later)
	r.sw.poolRoundTick(later.Add(poolRoundWait))
	rvWait(t, "the new round to be told", func() bool { return told() == 2 })
	// every member voted: the round is over and forgotten
	vote(3, members...)
	r.sw.poolRoundTick(later.Add(2 * poolRoundWait))
	r.sw.mu.Lock()
	left := len(r.sw.rounds)
	r.sw.mu.Unlock()
	if left != 0 || told() != 2 {
		t.Errorf("after the round completed: %d rounds kept, %d notices", left, told())
	}
}
