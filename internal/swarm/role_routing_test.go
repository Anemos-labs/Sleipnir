package swarm

import (
	"context"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/perm"
)

// A task goes to a member of its own role, whose model and instructions were chosen for its kind of work: queued behind the one
// backend member, a backend task names no other role, and neither a spawn on another role, nor one naming a member of another role,
// nor that member's own claim takes it. The manager can move the task to another role explicitly (task revise role=), and then it
// starts there. A manager that was told to spill a queued backend task onto the free frontend and devops slots ran the backend's work
// on models picked for other jobs.
func TestATaskGoesToAMemberOfItsOwnRole(t *testing.T) {
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	r := newRVRigWith(t, Config{MaxWorkers: 8, PersistentPools: true, Team: []string{"backend", "frontend", "devops", "tester", "reviewer"}},
		func(ctx context.Context, c *rvCall) rvReply {
			if c.Agent == "be-1" {
				select {
				case <-block:
				case <-ctx.Done():
				}
			}
			return rvReply{Text: "noted"}
		}, nil)
	b := r.sw.Board
	var ids []string
	for _, m := range []string{"lru", "svc", "wordfreq"} {
		task, err := b.CreateTask("mgr", TaskSpec{Title: m, Role: "backend", Files: []string{m + "/**"}})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, task.ID)
	}
	ui, err := b.CreateTask("mgr", TaskSpec{Title: "pages", Role: "frontend", Files: []string{"web/**"}})
	if err != nil {
		t.Fatal(err)
	}
	spawn := func(in map[string]any) (string, bool) {
		t.Helper()
		res := r.callTool(context.Background(), "spawn", "mgr", "manager", in)
		return res.Text, res.IsError
	}
	if got, bad := spawn(map[string]any{"role": "backend", "task": ids[0]}); bad {
		t.Fatalf("spawn %s: %s", ids[0], got)
	}
	got, bad := spawn(map[string]any{"role": "backend", "task": ids[1]})
	if bad || !strings.Contains(got, ids[1]+" is queued for the next free backend member") || strings.Contains(got, "frontend") || strings.Contains(got, "devops") {
		t.Fatalf("a queued backend spawn offers another role: %q", got)
	}
	if lines := strings.Join(r.sw.staffingLines(b.Snapshot()), "\n"); strings.Contains(lines, "free now") {
		t.Fatalf("live view lines %q offer the queued task to another role", lines)
	}
	got, bad = spawn(map[string]any{"role": "devops", "task": ids[2]})
	if !bad || !strings.Contains(got, ids[2]+" is a backend task, so a devops member does not take it") || !strings.Contains(got, "task revise id="+ids[2]+" role=devops") {
		t.Fatalf("a backend task spawned on devops: %q", got)
	}
	if got, bad := spawn(map[string]any{"role": "frontend", "task": ui.ID}); bad {
		t.Fatalf("spawn %s: %s", ui.ID, got)
	}
	got, bad = spawn(map[string]any{"agent": "fe-1", "task": ids[2]})
	if !bad || !strings.Contains(got, ids[2]+" is a backend task, so fe-1, a frontend member, does not take it") || !strings.Contains(got, "be-1 ") {
		t.Fatalf("a backend task given to a named frontend member: %q", got)
	}
	if err := b.claim("fe-1", ids[2], r.sw.claimCheck("fe-1", r.sw.roles["frontend"])); err == nil || !strings.Contains(err.Error(), "is a backend task") {
		t.Fatalf("a frontend member claimed a backend task: %v", err)
	}
	if cur, _ := b.Snapshot().Task(ids[2]); cur.Owner != "" {
		t.Fatalf("%s is owned by %s after refused assignments", ids[2], cur.Owner)
	}
	res := r.callTool(context.Background(), "task", "mgr", "manager", map[string]any{"action": "revise", "id": ids[2], "role": "devops", "text": "it is the build script"})
	if res.IsError || !strings.Contains(res.Text, "is now a devops task") {
		t.Fatalf("revise role=devops: %s", res.Text)
	}
	if got, bad := spawn(map[string]any{"role": "devops", "task": ids[2]}); bad {
		t.Fatalf("spawn of the revised task on devops: %s", got)
	}
	rvWait(t, ids[2]+" taken by devops", func() bool {
		cur, _ := b.Snapshot().Task(ids[2])
		return cur.Owner != "" && r.sw.roleOf(cur.Owner) == "devops"
	})
	res = r.callTool(context.Background(), "task", "mgr", "manager", map[string]any{"action": "revise", "id": ids[1], "role": "wizard", "text": "x"})
	if !res.IsError || !strings.Contains(res.Text, `unknown role "wizard"`) {
		t.Fatalf("revise to an unknown role: %s", res.Text)
	}
}

// An idle member that a pool keeps does not make its role free: a spawn of another task there would find it held.
func TestAnIdleMemberAPoolKeepsIsNotAFreeSlot(t *testing.T) {
	r := newRVRigWith(t, Config{MaxWorkers: 8, PersistentPools: true, Team: []string{"backend", "frontend", "frontend", "tester", "reviewer"}},
		func(ctx context.Context, c *rvCall) rvReply { return rvReply{Text: "noted"} }, nil)
	if _, err := r.sw.Spawn(SpawnReq{Role: "frontend", Title: "build the pages", Files: []string{"web/**"}, By: "mgr"}); err != nil {
		t.Fatal(err)
	}
	parent := r.sw.Board.Snapshot().Tasks[0]
	if parent.Pool == nil {
		t.Fatalf("the parent has no pool: %+v", parent)
	}
	rvWait(t, "the pool's members idle", func() bool {
		for _, m := range parent.Pool.Members {
			if !r.idle(m) {
				return false
			}
		}
		return true
	})
	if free := r.sw.freeWriters("backend"); len(free) != 0 {
		t.Fatalf("free writers %v include members a pool keeps", free)
	}
}

// Every built-in role writes: the architect, the scout and the reviewer have no read-only policy. A planning task holds its owner
// to reading while it plans, since its deliverable is the agreement and nothing written meanwhile would be merged; a work task of
// the same member writes.
func TestEveryBuiltinRoleWritesAndAPlanHoldsItsOwnerToReading(t *testing.T) {
	for name, role := range BuiltinRoles() {
		if role.ReadOnly {
			t.Errorf("the built-in %s role is read-only", name)
		}
	}
	if !BuiltinRoles()["reviewer"].Reports() || !BuiltinRoles()["scout"].Reports() || BuiltinRoles()["architect"].Reports() {
		t.Error("the reviewer and the scout report, the architect delivers changes")
	}
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	r := newRVRigWith(t, Config{MaxWorkers: 4, Team: []string{"architect", "architect", "backend"}},
		func(ctx context.Context, c *rvCall) rvReply {
			select { // the architect stays in its run on whatever it was given, so its task stays its current one
			case <-block:
			case <-ctx.Done():
			}
			return rvReply{Text: "thinking"}
		}, nil)
	plan, err := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "Design the API", Role: "architect", Kind: TaskKindPlan})
	if err != nil {
		t.Fatal(err)
	}
	id, err := r.sw.Spawn(SpawnReq{Role: "architect", TaskID: plan.ID, By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	write := perm.Request{Tool: "write", Writes: true, Paths: []string{"openapi.yaml"}}
	d, held := r.sw.planPermission(context.Background(), id, perm.AllowAll{}, write)
	if !held || d.Allow || !strings.Contains(d.Reason, plan.ID+" is a planning task") {
		t.Fatalf("a write while planning: held=%v %+v", held, d)
	}
	if _, held := r.sw.planPermission(context.Background(), id, perm.AllowAll{}, perm.Request{Tool: "read", Paths: []string{"go.mod"}}); held {
		t.Fatal("a read while planning was held back")
	}
	work, err := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "Write the OpenAPI contract", Role: "architect", Files: []string{"openapi.yaml"}})
	if err != nil {
		t.Fatal(err)
	}
	other, err := r.sw.Spawn(SpawnReq{Role: "architect", TaskID: work.ID, By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	rvWait(t, other+" on "+work.ID, func() bool { return r.sw.get(other) != nil && r.sw.get(other).currentTask() == work.ID })
	if _, held := r.sw.planPermission(context.Background(), other, perm.AllowAll{}, write); held {
		t.Fatal("an architect's write on a work task was held back")
	}
}
