package swarm

import (
	"context"
	"strings"
	"testing"
)

// A task queued behind the one member of its role names the writer roles whose slots are free, and the manager can start it on one
// of them. A manager that gave four independent modules to backend, the team's one backend slot, ran them one after another while
// the frontend and devops slots stayed empty.
func TestAQueuedTaskNamesTheFreeSlotsOfOtherWriterRoles(t *testing.T) {
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
	spawn := func(role, id string) string {
		t.Helper()
		res := r.callTool(context.Background(), "spawn", "mgr", "manager", map[string]any{"role": role, "task": id})
		if res.IsError {
			t.Fatalf("spawn %s %s: %s", role, id, res.Text)
		}
		return res.Text
	}
	spawn("backend", ids[0])
	got := spawn("backend", ids[1])
	if want := "Free now: frontend, devops: one of them can take " + ids[1] + " at once if it needs nothing only backend knows (spawn role=frontend task=" + ids[1] + ")"; !strings.Contains(got, want) {
		t.Fatalf("queued spawn %q lacks %q", got, want)
	}
	if lines := strings.Join(r.sw.staffingLines(b.Snapshot()), "\n"); !strings.Contains(lines, "free now: frontend, devops") {
		t.Fatalf("live view lines %q do not name the free writer slots", lines)
	}
	if got := spawn("devops", ids[1]); !strings.Contains(got, "started") {
		t.Fatalf("a queued task started on another role's free slot: %q", got)
	}
	rvWait(t, ids[1]+" taken", func() bool {
		cur, _ := b.Snapshot().Task(ids[1])
		return cur.Owner != "" && r.sw.roleOf(cur.Owner) == "devops"
	})
}
