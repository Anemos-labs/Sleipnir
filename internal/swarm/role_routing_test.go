package swarm

import (
	"context"
	"strings"
	"testing"
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
