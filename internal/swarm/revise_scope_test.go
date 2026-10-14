package swarm

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// A revise whose only change is a wider scope of a task under way widens it in place, as update files does: nobody is stopped,
// the children keep their owners, and a pool stays executing. In a trial the manager added .gitignore to a running backend pool's
// scope with revise: both workers were stopped, their children went back to the board, and the pool had to agree again.
func TestARevisionThatOnlyWidensTheScopeStopsNobody(t *testing.T) {
	r := newRVRig(t, Config{PersistentPools: true, MaxWorkers: 2, Team: []string{"backend", "backend"}}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "working"} })
	if _, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "backend", Files: []string{"app/**"}, By: "mgr"}); err != nil {
		t.Fatal(err)
	}
	parent := r.sw.Board.Snapshot().Tasks[0]
	lead := parent.Pool.Members[0]
	if err := r.sw.Board.poolUpdate("mgr", parent.ID, func(_ *Snapshot, _ *Task, p *PoolState) error { p.Phase = "executing"; return nil }); err != nil {
		t.Fatal(err)
	}
	child, err := r.sw.Board.CreateTask(lead, TaskSpec{Parent: parent.ID, Title: "api", Role: "backend", Files: []string{"app/api.py"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.sw.Board.assignTask(assignReq{by: lead, agent: lead, id: child.ID}); err != nil {
		t.Fatal(err)
	}
	revise := func(id string, files ...string) string {
		res := r.callTool(context.Background(), "task", "mgr", "manager", map[string]any{"action": "revise", "id": id, "files": files, "text": "the venv must be ignored"})
		if res.IsError {
			t.Fatalf("revise %s: %s", id, res.Text)
		}
		return res.Text
	}
	if got := revise(parent.ID, "app/**", ".gitignore"); !strings.Contains(got, "widened in place") {
		t.Fatalf("parent: %q", got)
	}
	if got := revise(child.ID, "app/api.py", ".gitignore"); !strings.Contains(got, "widened in place") {
		t.Fatalf("child: %q", got)
	}
	sn := r.sw.Board.Snapshot()
	p, _ := sn.Task(parent.ID)
	c, _ := sn.Task(child.ID)
	if p.Status != StatusDoing || p.Pool.Phase != "executing" || !slices.Contains(p.Files, ".gitignore") {
		t.Fatalf("parent after widening: %s %s %v", p.Status, p.Pool.Phase, p.Files)
	}
	if c.Status != StatusDoing || c.Owner != lead || !slices.Equal(c.Files, []string{"app/api.py", ".gitignore"}) {
		t.Fatalf("child after widening: %s owner=%s %v", c.Status, c.Owner, c.Files)
	}
	// A scope that drops a glob is a real revision: the child goes back to the board, with a new revision.
	before := c.Rev
	if got := revise(child.ID, ".gitignore"); strings.Contains(got, "widened in place") {
		t.Fatalf("a narrower scope was taken as a widening: %q", got)
	}
	if c, _ := r.sw.Board.Snapshot().Task(child.ID); c.Status != StatusTodo || c.Rev == before {
		t.Fatalf("the narrowing revise did not revise: status %s, rev %d (was %d)", c.Status, c.Rev, before)
	}
}

// A running planning task's scope widens in place too, under the read-only check it was assigned with (a writer's check refuses a
// planning task outright).
func TestARunningPlanningTasksScopeWidensInPlace(t *testing.T) {
	r := newRVRig(t, Config{MaxWorkers: 2}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "thinking"} })
	plan, err := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "contract", Role: "architect", Kind: TaskKindPlan, Files: []string{"docs/**"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.sw.Board.assignTask(assignReq{by: "mgr", agent: "ar-1", id: plan.ID}); err != nil {
		t.Fatal(err)
	}
	res := r.callTool(context.Background(), "task", "mgr", "manager", map[string]any{"action": "revise", "id": plan.ID, "files": []string{"docs/**", "api.md"}, "text": "the API reference too"})
	if res.IsError || !strings.Contains(res.Text, "widened in place") {
		t.Fatalf("revise: %q", res.Text)
	}
	if p, _ := r.sw.Board.Snapshot().Task(plan.ID); p.Status != StatusDoing || p.Owner != "ar-1" || !slices.Contains(p.Files, "api.md") {
		t.Fatalf("the planning task after widening: %s owner=%s %v", p.Status, p.Owner, p.Files)
	}
}
