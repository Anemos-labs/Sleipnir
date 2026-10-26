package swarm

import (
	"slices"
	"strings"
	"testing"
)

// A resumed parent whose pool had accepted its plan goes on with that pool and that plan: the members of the saved pool that are
// free form it again, in their order, and are told to continue the plan and its child tasks. It used to be rebuilt from the role's
// first free members under the cap, which differed from the saved pool, so the pool went back to planning with no agreement: three
// idle backend members were called to plan a two-member parent again while its leader's child task, half done in its tree, waited.
func TestAResumedExecutingPoolKeepsItsMembersAndPlan(t *testing.T) {
	team := []string{"backend", "backend", "backend", "backend"}
	rv := newRVRig(t, Config{PersistentPools: true, MaxWorkers: 4, PoolMax: 2, Team: team}, idleModel)
	r := &poolRig{rvRig: rv}
	if _, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "tsdb", Files: []string{"tsdb/**"}, By: "mgr"}); err != nil {
		t.Fatal(err)
	}
	first := r.sw.Board.Snapshot().Tasks[0]
	second, err := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "agent", Role: "backend", Files: []string{"agent/**"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.sw.Spawn(SpawnReq{Role: "backend", TaskID: second.ID, By: "mgr"}); err != nil {
		t.Fatal(err)
	}
	second, _ = r.sw.Board.Snapshot().Task(second.ID)
	if !slices.Equal(first.Pool.Members, []string{"be-1", "be-2"}) || !slices.Equal(second.Pool.Members, []string{"be-3", "be-4"}) {
		t.Fatalf("pools %v and %v", first.Pool.Members, second.Pool.Members)
	}
	lead := second.Owner
	if err := r.sw.Board.poolUpdate("mgr", second.ID, func(_ *Snapshot, _ *Task, p *PoolState) error {
		p.Phase, p.Revision, p.Agreement = "executing", 2, "One child (be-3, leader): the perf test and its benchmarks."
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	child, err := r.sw.Board.CreateTask(lead, TaskSpec{Parent: second.ID, Title: "perf test", Role: "backend", Files: []string{"agent/perf/**"}})
	if err != nil {
		t.Fatal(err)
	}
	owners := map[string]string{first.ID: first.Owner, second.ID: lead, child.ID: lead}
	if err := r.sw.Board.mutate("harness", "requeue", func(d *draft) error { // what Board.RestoreKeeping leaves after a resume
		for id, owner := range owners {
			i := taskIdx(d.Snapshot, id)
			p := d.Tasks[i]
			p.Status, p.Owner, p.Line = StatusTodo, owner, resumedLine
			if err := d.put(i, p); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"be-1", "be-2", "be-3", "be-4"} {
		endTurn(r.rvRig, id)
	}
	before := map[string]int{}
	for _, id := range []string{"be-1", "be-2", "be-3", "be-4"} {
		before[id] = len(r.mailTo(id))
	}
	if _, err := r.sw.Spawn(SpawnReq{Agent: lead, TaskID: second.ID, By: "mgr"}); err != nil {
		t.Fatalf("starting the resumed parent again: %v", err)
	}
	snap := r.sw.Board.Snapshot()
	got, _ := snap.Task(second.ID)
	if got.Status != StatusDoing || got.Owner != lead || !slices.Equal(got.Pool.Members, second.Pool.Members) ||
		got.Pool.Phase != "executing" || got.Pool.Revision != 2 || got.Pool.Agreement == "" {
		t.Fatalf("the resumed parent: %+v, want its saved pool %v executing revision 2 with its agreement", got.Pool, second.Pool.Members)
	}
	if f, _ := snap.Task(first.ID); !slices.Equal(f.Pool.Members, first.Pool.Members) {
		t.Errorf("the other parent's pool is %v, want %v untouched", f.Pool.Members, first.Pool.Members)
	}
	for _, id := range []string{"be-1", "be-2"} {
		for _, m := range r.mailTo(id)[before[id]:] {
			if strings.Contains(m, "Pool task "+second.ID) {
				t.Errorf("%s, of the other pool, was called to %s: %q", id, second.ID, m)
			}
		}
	}
	for _, id := range second.Pool.Members {
		mail := r.mailTo(id)[before[id]:]
		if len(mail) == 0 || !strings.Contains(mail[len(mail)-1], "Continue the accepted plan and existing child tasks") {
			t.Errorf("%s's wake: %q, want it to continue the accepted plan", id, mail)
		}
	}
}

// endTurn ends id's turn and waits until it is idle, as a recovered worker is until its task starts again.
func endTurn(r *rvRig, id string) {
	r.t.Helper()
	m := r.sw.get(id)
	m.mu.Lock()
	rs := m.run
	m.mu.Unlock()
	if rs != nil {
		rs.cancel()
	}
	rvWait(r.t, id+" idle", func() bool { return r.idle(id) })
}

// A saved member that is away when its parent starts again (on a task of its own) is left out, and the pool goes on without it:
// the plan was accepted by it, and the child tasks keep their owners.
func TestAResumedExecutingPoolGoesOnWithoutAnAbsentMember(t *testing.T) {
	r := newPoolRig(t)
	r.executing()
	before, _ := r.sw.Board.Snapshot().Task(r.parent.ID)
	solo, err := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "alone", Role: "backend", Files: []string{"lib/**"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.sw.Board.mutate("harness", "requeue", func(d *draft) error { // what Board.RestoreKeeping leaves after a resume
		i := taskIdx(d.Snapshot, r.parent.ID)
		p := d.Tasks[i]
		p.Status, p.Owner, p.Line = StatusTodo, r.lead, resumedLine
		if err := d.put(i, p); err != nil {
			return err
		}
		i = taskIdx(d.Snapshot, solo.ID)
		s := d.Tasks[i]
		s.Status, s.Owner, s.Solo = StatusDoing, r.third, true
		return d.put(i, s)
	}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{r.lead, r.second} {
		endTurn(r.rvRig, id)
	}
	if _, err := r.sw.Spawn(SpawnReq{Agent: r.lead, TaskID: r.parent.ID, By: "mgr"}); err != nil {
		t.Fatalf("starting the resumed parent again: %v", err)
	}
	got, _ := r.sw.Board.Snapshot().Task(r.parent.ID)
	if !slices.Equal(got.Pool.Members, []string{r.lead, r.second}) || got.Pool.Phase != "executing" || got.Pool.Revision != before.Pool.Revision || got.Pool.Agreement != before.Pool.Agreement {
		t.Fatalf("the resumed parent: %+v, want %s and %s executing revision %d with the agreement", got.Pool, r.lead, r.second, before.Pool.Revision)
	}
}
