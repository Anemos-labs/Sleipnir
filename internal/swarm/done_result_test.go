package swarm

import (
	"context"
	"strings"
	"testing"
)

// A worker's done text is the task's result as the worker wrote it: a result cut at 120 characters lost what the worker
// believed it had told the manager.
func TestADoneResultIsKeptWhole(t *testing.T) {
	long := "fixed the pagination: " + strings.Repeat("the cursor now carries the sort key, ", 20) + "END-OF-RESULT"
	r := newRVRig(t, Config{Team: []string{"backend"}}, func(_ context.Context, c *rvCall) rvReply {
		if c.Agent == "be-1" && c.Assistants == 0 {
			return rvReply{Tools: []rvToolCall{{Name: "task", Args: map[string]any{"action": "done", "id": "T1", "text": long}}}}
		}
		return rvReply{Text: "ok"}
	})
	b := r.sw.Board
	task, err := b.CreateTask("mgr", TaskSpec{Title: "fix pagination", Role: "backend"})
	if err != nil {
		t.Fatal(err)
	}
	if res := r.callTool(context.Background(), "spawn", "mgr", "manager", map[string]any{"role": "backend", "task": task.ID}); res.IsError {
		t.Fatal(res.Text)
	}
	rvWait(t, task.ID+" in review", func() bool {
		cur, _ := b.Snapshot().Task(task.ID)
		return cur.Status == StatusReview
	})
	if cur, _ := b.Snapshot().Task(task.ID); cur.Result != long {
		t.Fatalf("the result was cut: %d of %d characters\n%q", len(cur.Result), len(long), cur.Result)
	}
}

// A list shows the opening of a task's assumptions and how much more there is, counted without joining them all.
func TestListedJoinCountsWhatItLeavesOut(t *testing.T) {
	parts := []string{"short one"}
	for i := 0; i < 1000; i++ {
		parts = append(parts, strings.Repeat("a", 100))
	}
	whole := strings.Join(parts, "; ")
	if got, want := listedJoin(parts, "; ", "T1"), listed(whole, "T1"); got != want {
		t.Fatalf("listedJoin = %q\nlisted = %q", got, want)
	}
	if got := listedJoin([]string{"a", "b"}, "; ", "T1"); got != "a; b" {
		t.Fatalf("short: %q", got)
	}
}
