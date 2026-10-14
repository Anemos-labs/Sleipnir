package swarm

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// A role's members can work two parents side by side: the members an executing pool can spare (not its leader, no open subtask of
// it) leave it for the new parent's pool. A pool still agreeing on its plan needs every member, and one whose members are all busy
// spares none: both say what to do instead of "wait before assigning another parent", which a manager in a 32-worker run retried
// for an hour while seven backend workers sat in one pool.
func TestASecondParentTakesTheMembersAnExecutingPoolCanSpare(t *testing.T) {
	r := newRVRig(t, Config{PersistentPools: true, MaxWorkers: 4, Team: []string{"backend", "backend", "backend", "backend"}},
		func(ctx context.Context, _ *rvCall) rvReply { <-ctx.Done(); return rvReply{Text: "stopped"} })
	if _, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "control plane", Files: []string{"server/**"}, By: "mgr"}); err != nil {
		t.Fatal(err)
	}
	first := r.sw.Board.Snapshot().Tasks[0]
	members := slices.Clone(first.Pool.Members)
	if len(members) != 4 {
		t.Fatalf("the first pool has %v", members)
	}
	second, err := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "agent", Role: "backend", Files: []string{"agent/**"}})
	if err != nil {
		t.Fatal(err)
	}
	// planning: every member is needed
	if _, err := r.sw.Spawn(SpawnReq{Role: "backend", TaskID: second.ID, By: "mgr"}); err == nil || !strings.Contains(err.Error(), "still agreeing on "+first.ID) {
		t.Fatalf("a second parent while the first plans: %v", err)
	}
	// executing, with the leader and one member busy
	if err := r.sw.Board.poolUpdate("mgr", first.ID, func(_ *Snapshot, _ *Task, p *PoolState) error { p.Phase = "executing"; return nil }); err != nil {
		t.Fatal(err)
	}
	child, err := r.sw.Board.CreateTask(first.Owner, TaskSpec{Parent: first.ID, Title: "store", Role: "backend", Files: []string{"server/store/**"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.sw.Board.assignTask(assignReq{by: first.Owner, agent: members[1], id: child.ID}); err != nil {
		t.Fatal(err)
	}
	// a second parent that cannot begin (no such task) takes nobody from the first
	if _, err := r.sw.Spawn(SpawnReq{Role: "backend", TaskID: "T99", By: "mgr"}); err == nil {
		t.Fatal("a pool began for a task that does not exist")
	}
	if f, _ := r.sw.Board.Snapshot().Task(first.ID); !slices.Equal(f.Pool.Members, members) {
		t.Fatalf("a failed second parent took members from the first: %v", f.Pool.Members)
	}
	if _, err := r.sw.Spawn(SpawnReq{Role: "backend", TaskID: second.ID, By: "mgr"}); err != nil {
		t.Fatalf("a second parent while the first executes: %v", err)
	}
	snap := r.sw.Board.Snapshot()
	f, _ := snap.Task(first.ID)
	s, _ := snap.Task(second.ID)
	if !slices.Equal(f.Pool.Members, members[:2]) || !slices.Equal(s.Pool.Members, members[2:]) {
		t.Errorf("first pool %v, second pool %v; want %v and %v", f.Pool.Members, s.Pool.Members, members[:2], members[2:])
	}
	if s.Owner != members[2] || len(f.Pool.Excused) != 0 {
		t.Errorf("the second pool's leader is %s (want %s); the first excused %v (want none)", s.Owner, members[2], f.Pool.Excused)
	}
	// every member now busy: the leader and the child owner on the first, the second pool planning
	third, err := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "docs site", Role: "backend", Files: []string{"site/**"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.sw.Spawn(SpawnReq{Role: "backend", TaskID: third.ID, By: "mgr"}); err == nil || !strings.Contains(err.Error(), "call wait") {
		t.Fatalf("a third parent with nobody to spare: %v", err)
	}
}

// A member chosen to leave a pool may take work in it before the split happens (a subtask claimed or assigned meanwhile): the split
// re-checks in the same change of the board that begins the new pool, and such a member stays where it is.
func TestAMemberThatTookWorkMeanwhileStaysInItsPool(t *testing.T) {
	r := newRVRig(t, Config{PersistentPools: true, MaxWorkers: 4, Team: []string{"backend", "backend", "backend", "backend"}},
		func(ctx context.Context, _ *rvCall) rvReply { <-ctx.Done(); return rvReply{Text: "stopped"} })
	if _, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "control plane", Files: []string{"server/**"}, By: "mgr"}); err != nil {
		t.Fatal(err)
	}
	first := r.sw.Board.Snapshot().Tasks[0]
	members := slices.Clone(first.Pool.Members)
	if err := r.sw.Board.poolUpdate("mgr", first.ID, func(_ *Snapshot, _ *Task, p *PoolState) error { p.Phase = "executing"; return nil }); err != nil {
		t.Fatal(err)
	}
	second, err := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "agent", Role: "backend", Files: []string{"agent/**"}})
	if err != nil {
		t.Fatal(err)
	}
	spare, release, err := r.sw.spareMembers(SpawnReq{Role: "backend", TaskID: second.ID, By: "mgr"}, r.sw.poolMembers("backend"))
	if err != nil || len(spare) != 3 || len(release[first.ID]) != 3 {
		t.Fatalf("the plan: %d spare, release %v, %v", len(spare), release, err)
	}
	// the race: the leader gives one of the chosen members a subtask before the split
	child, err := r.sw.Board.CreateTask(first.Owner, TaskSpec{Parent: first.ID, Title: "store", Role: "backend", Files: []string{"server/store/**"}})
	if err != nil {
		t.Fatal(err)
	}
	busy := release[first.ID][0]
	if _, err := r.sw.Board.assignTask(assignReq{by: first.Owner, agent: busy, id: child.ID}); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, len(spare))
	for i, m := range spare {
		ids[i] = m.id
	}
	released, err := r.sw.Board.splitPool("mgr", second.ID, ids, release)
	if err != nil {
		t.Fatal(err)
	}
	snap := r.sw.Board.Snapshot()
	f, _ := snap.Task(first.ID)
	s, _ := snap.Task(second.ID)
	if !slices.Contains(f.Pool.Members, busy) || slices.Contains(s.Pool.Members, busy) || slices.Contains(released[first.ID], busy) {
		t.Errorf("%s took a subtask of %s before the split: first pool %v, second pool %v, released %v", busy, first.ID, f.Pool.Members, s.Pool.Members, released)
	}
	if len(s.Pool.Members) != 2 || !slices.Contains(f.Pool.Members, members[0]) {
		t.Errorf("first pool %v, second pool %v", f.Pool.Members, s.Pool.Members)
	}
}
