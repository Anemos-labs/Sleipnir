package swarm

import (
	"context"
	"strings"
	"testing"
)

// A task can be assigned ahead of its dependencies, and to a member that is still working: the spawn is queued instead of refused,
// and the harness starts it once the dependencies are done and the member is free. A manager that pre-assigned a blocked task read
// "T3 waits for T2" after the fact, and one that re-tasked a busy worker read "still working" and had to guess when to try again.
func TestSpawnQueuesBehindDependenciesAndABusyMember(t *testing.T) {
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	r := newRVRigWith(t, Config{MaxWorkers: 4, PersistentPools: true, Team: []string{"backend", "frontend"}},
		func(ctx context.Context, c *rvCall) rvReply {
			if c.Agent == "fe-1" {
				select {
				case <-block:
				case <-ctx.Done():
				}
			}
			return rvReply{Text: "noted"}
		}, nil)
	b := r.sw.Board
	first, _ := b.CreateTask("mgr", TaskSpec{Title: "store", Role: "backend", Files: []string{"store/**"}})
	second, _ := b.CreateTask("mgr", TaskSpec{Title: "api", Role: "backend", Files: []string{"api/**"}, Deps: []string{first.ID}})
	spawn := func(in map[string]any) (string, bool) {
		t.Helper()
		res := r.callTool(context.Background(), "spawn", "mgr", "manager", in)
		return res.Text, res.IsError
	}
	got, bad := spawn(map[string]any{"role": "backend", "task": second.ID})
	if bad || !strings.Contains(got, second.ID+" waits for "+first.ID) || !strings.Contains(got, "is queued") {
		t.Fatalf("spawn ahead of a dependency: %q (error %v)", got, bad)
	}
	if got, _ := spawn(map[string]any{"role": "backend", "task": second.ID}); !strings.Contains(got, "was already queued") {
		t.Fatalf("a second spawn of the queued task: %q", got)
	}
	r.sw.queueTick(r.sw.deps.Now())
	if cur, _ := b.Snapshot().Task(second.ID); cur.Owner != "" || cur.Status != StatusTodo {
		t.Fatalf("%s started before its dependency was done: %+v", second.ID, cur)
	}
	if err := b.Assign("mgr", "be-1", first.ID); err != nil {
		t.Fatal(err)
	}
	if err := b.Submit("be-1", first.ID, "done", "edited store/a.go"); err != nil {
		t.Fatal(err)
	}
	if err := b.Accept("mgr", first.ID, "ok"); err != nil {
		t.Fatal(err)
	}
	r.sw.queueTick(r.sw.deps.Now())
	if cur, _ := b.Snapshot().Task(second.ID); cur.Owner != "be-1" {
		t.Fatalf("%s did not start once its dependency was done: %+v", second.ID, cur)
	}

	// fe-1 works on a page; a second page named for fe-1 is queued for it and kept while it works
	page, _ := b.CreateTask("mgr", TaskSpec{Title: "page", Role: "frontend", Files: []string{"web/a/**"}})
	if got, bad := spawn(map[string]any{"role": "frontend", "task": page.ID}); bad {
		t.Fatalf("spawn %s: %s", page.ID, got)
	}
	rvWait(t, "fe-1 to be at work", func() bool { return r.prov.callsFor("fe-1") >= 1 })
	next, _ := b.CreateTask("mgr", TaskSpec{Title: "next page", Role: "frontend", Files: []string{"web/b/**"}})
	got, bad = spawn(map[string]any{"agent": "fe-1", "task": next.ID})
	if bad || !strings.Contains(got, next.ID+" is queued for fe-1, which is still working") {
		t.Fatalf("spawn on a busy member: %q (error %v)", got, bad)
	}
	r.sw.queueTick(r.sw.deps.Now())
	queued := false
	for _, q := range r.sw.queuedTasks() {
		queued = queued || q.req.TaskID == next.ID
	}
	if !queued {
		t.Fatalf("%s left the queue while fe-1 was still working", next.ID)
	}
}
