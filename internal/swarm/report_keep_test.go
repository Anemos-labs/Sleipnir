package swarm

import (
	"context"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/events"
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

// A report lives while its task is in review or done. A board restored after a restart sends review tasks back to todo, and a
// replay of that restored board's log rebuilds them without their old reports too.
func TestARestoredBoardDropsTheReportsOfTasksItSendsBack(t *testing.T) {
	b := NewBoard(nil)
	task, err := b.CreateTask("mgr", TaskSpec{Title: "review", Role: ReviewerRole})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Claim("rv-1", task.ID); err != nil {
		t.Fatal(err)
	}
	if err := b.submitReportAt("rv-1", task.ID, 0, "no blocking findings", "No blocking findings.\nNit: an unused field.", "", ""); err != nil {
		t.Fatal(err)
	}
	if cur, _ := b.Snapshot().Task(task.ID); cur.Report == "" {
		t.Fatal("the submission kept no report")
	}
	log := events.NewMemLog()
	restored := NewBoard(log)
	restored.Restore(b.Snapshot())
	if cur, _ := restored.Snapshot().Task(task.ID); cur.Status != StatusTodo || cur.Report != "" {
		t.Fatalf("the restored task is %s with report %q", cur.Status, cur.Report)
	}
	replayed, err := ReplayBoard(log.All())
	if err != nil {
		t.Fatal(err)
	}
	if cur, _ := replayed.Task(task.ID); cur.Report != "" {
		t.Fatalf("the replayed task kept report %q", cur.Report)
	}
}

// A read-only planner's text is kept whole beside its agreement, as a reviewer's is.
func TestAPlannersWholeTextIsKeptAsItsReport(t *testing.T) {
	text := "Agreement below. Findings: the svc clock has two constructors; lru needs a mutex; wordfreq and jsonpath are independent of the others and of each other, so they can run side by side."
	r := newRVRig(t, Config{Team: []string{"backend", "scout"}}, func(_ context.Context, c *rvCall) rvReply {
		if c.Agent == "sc-1" && c.Assistants == 0 {
			return rvReply{Tools: []rvToolCall{{Name: "task", Args: map[string]any{"action": "done", "id": "T1", "text": text, "agreement": "ratelimit.Clock{Now() time.Time}"}}}}
		}
		return rvReply{Text: "ok"}
	})
	b := r.sw.Board
	plan, err := b.CreateTask("mgr", TaskSpec{Title: "plan the clock", Role: "scout", Kind: TaskKindPlan})
	if err != nil {
		t.Fatal(err)
	}
	if res := r.callTool(context.Background(), "spawn", "mgr", "manager", map[string]any{"role": "scout", "task": plan.ID}); res.IsError {
		t.Fatal(res.Text)
	}
	rvWait(t, plan.ID+" in review", func() bool {
		cur, _ := b.Snapshot().Task(plan.ID)
		return cur.Status == StatusReview
	})
	if cur, _ := b.Snapshot().Task(plan.ID); cur.Report != text || !strings.HasSuffix(cur.Result, "(whole report: task get "+plan.ID+")") {
		t.Fatalf("the plan kept result %q and report %q", cur.Result, cur.Report)
	}
}
