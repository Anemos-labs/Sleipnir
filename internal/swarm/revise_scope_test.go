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
	// The members' runs last until they are stopped (idleModel): a leader whose run ended on its own with the child in doing would
	// hand the child in for review (settleClean), and a child in review is not under way, so whether the widening found it doing
	// depended on when the leader's run ended (it failed so on a slow CI runner).
	r := newRVRig(t, Config{PersistentPools: true, MaxWorkers: 2, Team: []string{"backend", "backend"}}, idleModel)
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

// The widening is made on the task as it was read: a revision or a requeue in between leaves the task as that change made it.
func TestAWideningDoesNotOverwriteAnInterveningChange(t *testing.T) {
	r := newRVRig(t, Config{MaxWorkers: 2}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "working"} })
	task, err := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "api", Role: "backend", Files: []string{"app/**"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.sw.Board.assignTask(assignReq{by: "mgr", agent: "be-1", id: task.ID}); err != nil {
		t.Fatal(err)
	}
	was, _ := r.sw.Board.Snapshot().Task(task.ID)
	// another revision lands between the read and the widening
	if err := r.sw.Board.revise("mgr", task.ID, TaskSpec{Files: []string{"app/api/**"}}, "narrower"); err != nil {
		t.Fatal(err)
	}
	if err := r.sw.Board.widenScope("mgr", was, []string{"app/**", ".gitignore"}, func(*Snapshot, Task) error { return nil }); err == nil || !strings.Contains(err.Error(), "changed while its scope was being widened") {
		t.Fatalf("widening over an intervening revision: %v", err)
	}
	if now, _ := r.sw.Board.Snapshot().Task(task.ID); !slices.Equal(now.Files, []string{"app/api/**"}) {
		t.Fatalf("the intervening revision's scope was overwritten: %v", now.Files)
	}
}

// The manager's update with only text, on a task someone else owns, says what the manager's own actions are, not "mail the manager".
func TestTheManagersTextUpdateSaysWhatToDoInstead(t *testing.T) {
	r := newRVRig(t, Config{MaxWorkers: 2}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "working"} })
	task, err := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "web", Role: "frontend", Files: []string{"web/js/**"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.sw.Board.assignTask(assignReq{by: "mgr", agent: "fe-1", id: task.ID}); err != nil {
		t.Fatal(err)
	}
	res := r.callTool(context.Background(), "task", "mgr", "manager", map[string]any{"action": "update", "id": task.ID, "text": "include web/css/styles.css"})
	if !res.IsError || !strings.Contains(res.Text, "files= widens or sets its scope") || strings.Contains(res.Text, "or the manager") {
		t.Fatalf("the manager's text update: %q", res.Text)
	}
}

// A manager's update with files and a reason widens the scope and says so, and a revise that repeats the scope the task already
// has stops nobody. In a team run the update's reason made it answer with an error after the scope had changed; the manager made
// the same change with revise, which found nothing to widen, stopped both pools' workers and put the pools back to planning.
func TestRepeatingAWideningStopsNobody(t *testing.T) {
	r := newRVRig(t, Config{PersistentPools: true, MaxWorkers: 2, Team: []string{"backend", "backend"}}, idleModel)
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
	files := []string{"app/**", "load/main.go"}
	res := r.callTool(context.Background(), "task", "mgr", "manager", map[string]any{"action": "update", "id": parent.ID, "files": files, "text": "the child needs load/main.go"})
	if res.IsError || !strings.Contains(res.Text, "scope set (app/**, load/main.go); its workers keep going") || !strings.Contains(res.Text, "not recorded") {
		t.Fatalf("update: %q", res.Text)
	}
	res = r.callTool(context.Background(), "task", "mgr", "manager", map[string]any{"action": "revise", "id": parent.ID, "files": files, "text": "the child needs load/main.go"})
	if res.IsError || !strings.Contains(res.Text, "nothing changed and its workers keep going") {
		t.Fatalf("revise: %q", res.Text)
	}
	sn := r.sw.Board.Snapshot()
	p, _ := sn.Task(parent.ID)
	c, _ := sn.Task(child.ID)
	if p.Status != StatusDoing || p.Pool.Phase != "executing" || !slices.Equal(p.Files, files) {
		t.Fatalf("parent: %s %s %v", p.Status, p.Pool.Phase, p.Files)
	}
	if c.Status != StatusDoing || c.Owner != lead {
		t.Fatalf("child: %s owner=%s", c.Status, c.Owner)
	}
}
