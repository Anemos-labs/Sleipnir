package swarm

import (
	"context"
	"strings"
	"testing"
)

// A spawn that names a member which owns a solo task of its own is refused with what holds it, not answered for another member:
// in a team run the pool could take only be-2, which was in a run, and every "spawn agent=be-1" was answered "be-2 is still
// working" while be-1 waited on the manager's decision of its own task.
func TestASpawnOfANamedMemberSaysWhatHoldsIt(t *testing.T) {
	r := newRVRigWith(t, Config{PersistentPools: true, MaxWorkers: 8, Team: []string{"backend", "backend"}},
		func(context.Context, *rvCall) rvReply { return rvReply{Text: "noted"} }, nil)
	if _, err := r.sw.Spawn(SpawnReq{Role: "backend", Agent: "be-1", Title: "engine", Files: []string{"engine/**"}, Solo: true, By: "mgr"}); err != nil {
		t.Fatal(err)
	}
	rvWait(t, "be-1 idle", func() bool { return r.idle("be-1") })
	engine := r.sw.Board.Snapshot().Tasks[0]
	if cur, _ := r.sw.Board.Snapshot().Task(engine.ID); cur.Status == StatusDoing {
		if err := r.sw.Board.Submit("be-1", engine.ID, "done", ""); err != nil {
			t.Fatal(err)
		}
	}
	store, err := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "store", Role: "backend", Files: []string{"store/**"}})
	if err != nil {
		t.Fatal(err)
	}
	res := r.callTool(context.Background(), "spawn", "mgr", "manager", map[string]any{"agent": "be-1", "task": store.ID})
	if !res.IsError {
		t.Fatalf("a spawn of the busy be-1 went through: %s", res.Text)
	}
	for _, want := range []string{"be-1 owns " + engine.ID + " (review)", "decide " + engine.ID + " first", "spawn agent=be-2 task=" + store.ID} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("the refusal %q lacks %q", res.Text, want)
		}
	}
}

// A spawn that names the member already working on that very task says so and that nothing is to be started: after a task
// revise or reopen the member retries the task itself, and a manager told "be-1 works on T2: give the task to a free backend
// member" took it for a refusal to hand the reopened task back.
func TestASpawnOfTheMemberAlreadyOnTheTaskSaysItIsOnIt(t *testing.T) {
	hold := make(chan struct{})
	defer close(hold)
	r := newRVRigWith(t, Config{PersistentPools: true, MaxWorkers: 8, Team: []string{"backend", "backend"}},
		func(ctx context.Context, c *rvCall) rvReply {
			if c.Role == "backend" {
				rvBlock(ctx, hold)
			}
			return rvReply{Text: "noted"}
		}, nil)
	task, err := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "parser", Role: "backend", Files: []string{"parser/**"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.sw.Spawn(SpawnReq{Role: "backend", Agent: "be-1", TaskID: task.ID, Solo: true, By: "mgr"}); err != nil {
		t.Fatal(err)
	}
	rvWait(t, "be-1 on the task", func() bool { return !r.idle("be-1") })
	res := r.callTool(context.Background(), "spawn", "mgr", "manager", map[string]any{"agent": "be-1", "task": task.ID, "solo": true})
	if !res.IsError {
		t.Fatalf("a second spawn of be-1 on its own task went through: %s", res.Text)
	}
	if want := "be-1 already works on " + task.ID; !strings.Contains(res.Text, want) || strings.Contains(res.Text, "give the task to a free") {
		t.Errorf("the answer %q does not say %q, or sends the task elsewhere", res.Text, want)
	}
}
