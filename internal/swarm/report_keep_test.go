package swarm

import (
	"context"
	"strings"
	"testing"
)

// A read-only worker's report reaches the manager whole: task get shows it, and the board's one-line result says where it is.
// Cut to the line, a reviewer's findings ended in "No bl…" and its manager recalled the reviewer's history for the verdict.
func TestAReviewersWholeReportIsKeptForTheManager(t *testing.T) {
	report := "Reviewed lru, svc, wordfreq and jsonpath against their READMEs; all four suites pass with -race.\n" +
		"No blocking findings.\nNit: jsonpath/jsonpath.go:41 keeps an unused rawIdx field."
	r := newRVRig(t, Config{Team: []string{"backend", "reviewer"}}, func(_ context.Context, c *rvCall) rvReply {
		if c.Agent == "rv-1" && c.Assistants == 0 {
			return rvReply{Tools: []rvToolCall{{Name: "task", Args: map[string]any{"action": "done", "id": "T1", "text": report}}}}
		}
		return rvReply{Text: "ok"}
	})
	b := r.sw.Board
	task, err := b.CreateTask("mgr", TaskSpec{Title: "review the merged result", Role: "reviewer"})
	if err != nil {
		t.Fatal(err)
	}
	if res := r.callTool(context.Background(), "spawn", "mgr", "manager", map[string]any{"role": "reviewer", "task": task.ID}); res.IsError {
		t.Fatal(res.Text)
	}
	rvWait(t, task.ID+" in review", func() bool {
		cur, _ := b.Snapshot().Task(task.ID)
		return cur.Status == StatusReview
	})
	got := r.callTool(context.Background(), "task", "mgr", "manager", map[string]any{"action": "get", "id": task.ID}).Text
	if !strings.Contains(got, "unused rawIdx field") {
		t.Fatalf("task get lost the end of the report:\n%s", got)
	}
	if cur, _ := b.Snapshot().Task(task.ID); !strings.HasSuffix(cur.Result, "(whole report: task get "+task.ID+")") {
		t.Fatalf("the result does not say where the report is: %q", cur.Result)
	}
	if _, err := b.SendBack("mgr", task.ID, "check the svc clock too"); err != nil {
		t.Fatal(err)
	}
	if cur, _ := b.Snapshot().Task(task.ID); cur.Report != "" {
		t.Fatalf("a task sent back keeps its old report: %q", cur.Report)
	}
}
