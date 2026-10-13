package swarm

import (
	"context"
	"testing"
)

func TestPoolRequiresEveryModelAndReconciliation(t *testing.T) {
	r := newRVRig(t, Config{PersistentPools: true, MaxWorkers: 3, Team: []string{"backend", "backend", "backend"}}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "waiting for agreement"} })
	id, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "build API", Files: []string{"api/**"}, By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	parent := r.sw.Board.Snapshot().Tasks[0]
	if parent.Owner != id || parent.Pool == nil || len(parent.Pool.Members) != 3 {
		t.Fatalf("pool: %+v", parent)
	}
	ids := parent.Pool.Members
	call := func(as, action string, rev int, text string) bool {
		return r.callTool(context.Background(), "pool", as, "backend", map[string]any{"id": parent.ID, "action": action, "revision": rev, "text": text}).IsError
	}
	if !call(ids[1], "propose", 0, "invent a plan") {
		t.Fatal("nonleader proposed")
	}
	if call(ids[0], "propose", 0, "Use existing API conventions; divide handlers, validation and tests; reconcile together.") {
		t.Fatal("proposal refused")
	}
	p, _ := r.sw.Board.Snapshot().Task(parent.ID)
	old := p.Pool
	if !call(ids[0], "execute", 0, "") {
		t.Fatal("execution before agreement")
	}
	for _, id := range ids {
		if call(id, "agree", p.Pool.Revision, "") {
			t.Fatal("agreement refused")
		}
	}
	if len(old.Votes) != 0 {
		t.Fatal("published snapshot mutated")
	}
	if call(ids[0], "execute", 0, "") {
		t.Fatal("execution refused")
	}
	child, err := r.sw.Board.CreateTask(ids[0], TaskSpec{Parent: parent.ID, Title: "handler", Role: "backend", Files: []string{"api/handler.go"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.sw.Board.CreateTask(ids[0], TaskSpec{Parent: parent.ID, Title: "escape", Role: "backend", Files: []string{"other/file.go"}}); err == nil {
		t.Fatal("child escaped parent scope")
	}
	if err := r.sw.Board.revise("mgr", child.ID, TaskSpec{Files: []string{"outside.go"}}, "widen child"); err == nil {
		t.Fatal("revision escaped parent scope")
	}
	if err := r.sw.Board.SetScope("mgr", child.ID, []string{"outside.go"}, nil); err == nil {
		t.Fatal("scope update escaped parent")
	}
	if !call(ids[0], "reconcile", 0, "") {
		t.Fatal("reconciled unfinished child")
	}
	if err := r.sw.Board.resolve("mgr", child.ID, "already supplied by existing implementation"); err != nil {
		t.Fatal(err)
	}
	if call(ids[0], "reconcile", 0, "") {
		t.Fatal("reconciliation refused")
	}
	p, _ = r.sw.Board.Snapshot().Task(parent.ID)
	if poolComplete(r.sw.Board.Snapshot(), p) == nil {
		t.Fatal("parent completed before peer confirmation")
	}
	for _, id := range ids {
		if call(id, "confirm", p.Pool.Revision, "") {
			t.Fatal("confirmation refused")
		}
	}
	p, _ = r.sw.Board.Snapshot().Task(parent.ID)
	if err := poolComplete(r.sw.Board.Snapshot(), p); err != nil {
		t.Fatal(err)
	}
}

func TestPersistentReuseAndUnboundedRepair(t *testing.T) {
	r := newRVRig(t, Config{PersistentPools: true, MaxWorkers: 1, Team: []string{"backend"}}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "done"} })
	first, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "first", By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	rvWait(t, "idle", func() bool { return r.idle(first) })
	second, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "second", By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Fatalf("lost context: %s -> %s", first, second)
	}
	if r.sw.cfg.MaxAttempts != 0 || r.sw.cfg.IdleRetire >= 0 {
		t.Fatal("persistent workers still expire")
	}
	task := Task{ID: "T1", Status: StatusDoing, Owner: first}
	for i := 0; i < 20; i++ {
		task = requeuedTask(task, uint64(i), "test failed", true, 0, CloseVerifier)
		task.Owner = first
	}
	if task.Status == StatusFailed || task.Attempts != 20 {
		t.Fatalf("repair exhausted: %+v", task)
	}
}

func TestPoolAssignmentCannotBypassConsensusAndRetainsModels(t *testing.T) {
	r := newRVRigWith(t, Config{PersistentPools: true, MaxWorkers: 3, Team: []string{"reviewer", "reviewer", "reviewer"}}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "discussing"} }, func(d *Deps) {
		for _, id := range []string{"model-a", "model-b", "model-c"} {
			m := d.Model
			m.ID = id
			d.SlotModels = append(d.SlotModels, RoleModel{Provider: d.Provider, Model: m})
		}
	})
	if _, err := r.sw.Spawn(SpawnReq{Role: "reviewer", Title: "review", Agent: "rv-1", By: "mgr"}); err != nil {
		t.Fatal(err)
	}
	members := r.sw.poolMembers("reviewer")
	for i, id := range []string{"model-a", "model-b", "model-c"} {
		if members[i].model.ID != id {
			t.Fatalf("slot %d model=%s", i, members[i].model.ID)
		}
	}
	parent := r.sw.Board.Snapshot().Tasks[0]
	if parent.Pool == nil || parent.Owner != members[0].id {
		t.Fatalf("individual assignment escaped pool: %+v", parent)
	}
	if err := r.sw.Retire(members[0].id); err == nil {
		t.Fatal("persistent context retired")
	}
	if _, err := r.sw.Spawn(SpawnReq{Role: "reviewer", Title: "another", By: "mgr"}); err == nil {
		t.Fatal("busy pool allocated replacements")
	}
	if len(r.sw.poolMembers("reviewer")) != 3 {
		t.Fatal("roster changed")
	}
}

func TestRevisionRejectsDependencyCyclesAndReplaysMetadata(t *testing.T) {
	r := newRVRig(t, Config{}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "idle"} })
	a, _ := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "first", Role: "backend"})
	b, _ := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "second", Role: "backend", Deps: []string{a.ID}})
	if err := r.sw.Board.revise("mgr", a.ID, TaskSpec{Deps: []string{b.ID}}, "new requirements"); err == nil {
		t.Fatal("dependency cycle accepted")
	}
	if err := r.sw.Board.revise("mgr", a.ID, TaskSpec{Title: "revised", Desc: "new acceptance"}, "repair"); err != nil {
		t.Fatal(err)
	}
	replay, err := ReplayBoard(r.log.All())
	if err != nil {
		t.Fatal(err)
	}
	task, _ := replay.Task(a.ID)
	if task.Title != "revised" || task.Desc != "new acceptance" {
		t.Fatalf("replay lost revision: %+v", task)
	}
}

func TestPoolReconciliationRejectsNewerChanges(t *testing.T) {
	r := newRVRig(t, Config{PersistentPools: true, MaxWorkers: 2, Team: []string{"reviewer", "reviewer"}}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "waiting"} })
	_, err := r.sw.Spawn(SpawnReq{Role: "reviewer", Title: "review", By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	parent := r.sw.Board.Snapshot().Tasks[0]
	err = r.sw.Board.poolUpdate("mgr", parent.ID, func(_ *Snapshot, _ *Task, p *PoolState) error {
		p.Phase = "reconciling"
		p.Agreement = "review merged result"
		p.Votes = append([]string(nil), p.Members...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	parent, _ = r.sw.Board.Snapshot().Task(parent.ID)
	if err = r.sw.poolCurrent(nil, parent); err != nil {
		t.Fatal(err)
	}
	r.sw.review.mu.Lock()
	r.sw.review.merges++
	r.sw.review.mu.Unlock()
	if err = r.sw.poolCurrent(nil, parent); err == nil {
		t.Fatal("stale reconciliation accepted")
	}
	res := r.callTool(context.Background(), "task", parent.Owner, "reviewer", map[string]any{"action": "done", "id": parent.ID, "text": "looks good"})
	if !res.IsError {
		t.Fatal("stale pool submitted")
	}
}
