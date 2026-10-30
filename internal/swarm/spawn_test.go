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
