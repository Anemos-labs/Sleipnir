package swarm

import (
	"context"
	"slices"
	"testing"
)

// A large role does not plan one parent with every member: a pool takes at most PoolMax of them (DefaultPoolMax when 0), and the
// others form the next parent's pool at once, even while the first is still agreeing on its plan. In a 32-worker run all ten
// backend members joined the first backend parent and planned it together while every other backend parent waited.
func TestAPoolTakesAtMostPoolMaxMembers(t *testing.T) {
	team := []string{"backend", "backend", "backend", "backend", "backend", "backend"}
	r := newRVRig(t, Config{PersistentPools: true, MaxWorkers: 6, Team: team},
		func(ctx context.Context, _ *rvCall) rvReply { <-ctx.Done(); return rvReply{Text: "stopped"} })
	if _, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "tsdb", Files: []string{"tsdb/**"}, By: "mgr"}); err != nil {
		t.Fatal(err)
	}
	first := r.sw.Board.Snapshot().Tasks[0]
	if len(first.Pool.Members) != DefaultPoolMax {
		t.Fatalf("the first pool has %v, want %d members", first.Pool.Members, DefaultPoolMax)
	}
	second, err := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "agent", Role: "backend", Files: []string{"agent/**"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.sw.Spawn(SpawnReq{Role: "backend", TaskID: second.ID, By: "mgr"}); err != nil {
		t.Fatalf("a second parent while the first plans with %d of 6 members: %v", DefaultPoolMax, err)
	}
	snap := r.sw.Board.Snapshot()
	f, _ := snap.Task(first.ID)
	s, _ := snap.Task(second.ID)
	if len(s.Pool.Members) != 2 || !slices.Equal(f.Pool.Members, first.Pool.Members) {
		t.Errorf("first pool %v, second pool %v; want the first unchanged and the two others in the second", f.Pool.Members, s.Pool.Members)
	}
	for _, m := range s.Pool.Members {
		if slices.Contains(f.Pool.Members, m) {
			t.Errorf("%s is in both pools", m)
		}
	}
}

// A negative PoolMax sets no cap: every free member joins the parent's pool.
func TestANegativePoolMaxLetsEveryMemberJoin(t *testing.T) {
	team := []string{"backend", "backend", "backend", "backend", "backend", "backend"}
	r := newRVRig(t, Config{PersistentPools: true, MaxWorkers: 6, PoolMax: -1, Team: team},
		func(ctx context.Context, _ *rvCall) rvReply { <-ctx.Done(); return rvReply{Text: "stopped"} })
	if _, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "tsdb", Files: []string{"tsdb/**"}, By: "mgr"}); err != nil {
		t.Fatal(err)
	}
	if got := r.sw.Board.Snapshot().Tasks[0].Pool.Members; len(got) != 6 {
		t.Fatalf("the pool has %v, want all 6 members", got)
	}
}

// capPool drops from release the members it leaves out, so a pool spares only the members that move.
func TestCapPoolKeepsReleaseToTheMembersThatMove(t *testing.T) {
	ms := []*member{{id: "be-3"}, {id: "be-4"}, {id: "be-5"}}
	got, rel := capPool(ms, map[string][]string{"T1": {"be-3", "be-4", "be-5"}}, 2)
	if len(got) != 2 || !slices.Equal(rel["T1"], []string{"be-3", "be-4"}) {
		t.Fatalf("members %d, release %v", len(got), rel)
	}
}

// A requeued parent keeps the members its manager excused, and they never rejoin it: they do not take a place under the cap
// that an eligible member would have had.
func TestExcusedMembersDoNotCountAgainstPoolMax(t *testing.T) {
	team := []string{"backend", "backend", "backend", "backend", "backend", "backend"}
	r := newRVRig(t, Config{PersistentPools: true, MaxWorkers: 6, Team: team},
		func(ctx context.Context, _ *rvCall) rvReply { <-ctx.Done(); return rvReply{Text: "stopped"} })
	task, err := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "tsdb", Role: "backend", Files: []string{"tsdb/**"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.sw.ensurePool("backend"); err != nil {
		t.Fatal(err)
	}
	// the parent as a resume leaves it: todo, its pool kept, be-1 and be-2 excused
	if err := r.sw.Board.mutate("mgr", "update", func(d *draft) error {
		i := taskIdx(d.Snapshot, task.ID)
		tk := d.Tasks[i]
		tk.Pool = &PoolState{Members: []string{"be-3", "be-4"}, Phase: "planning", Revision: 1, Excused: []string{"be-1", "be-2"}}
		return d.put(i, tk)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.sw.Spawn(SpawnReq{Role: "backend", TaskID: task.ID, By: "mgr"}); err != nil {
		t.Fatal(err)
	}
	got, _ := r.sw.Board.Snapshot().Task(task.ID)
	if !slices.Equal(got.Pool.Members, []string{"be-3", "be-4", "be-5", "be-6"}) {
		t.Fatalf("the restarted pool has %v, want the four members that were not excused", got.Pool.Members)
	}
}

// The member a request names leads the pool, so the cap keeps it even when it comes after the first PoolMax members, as it does
// for the first pool, where no other pool spares members.
func TestPoolMaxKeepsTheRequestedMember(t *testing.T) {
	team := []string{"backend", "backend", "backend", "backend", "backend", "backend"}
	r := newRVRig(t, Config{PersistentPools: true, MaxWorkers: 6, Team: team},
		func(ctx context.Context, _ *rvCall) rvReply { <-ctx.Done(); return rvReply{Text: "stopped"} })
	if _, err := r.sw.Spawn(SpawnReq{Role: "backend", Agent: "be-6", Title: "tsdb", Files: []string{"tsdb/**"}, By: "mgr"}); err != nil {
		t.Fatal(err)
	}
	p := r.sw.Board.Snapshot().Tasks[0].Pool
	if len(p.Members) != DefaultPoolMax || !slices.Contains(p.Members, "be-6") {
		t.Fatalf("the pool has %v, want %d members including be-6", p.Members, DefaultPoolMax)
	}
}
