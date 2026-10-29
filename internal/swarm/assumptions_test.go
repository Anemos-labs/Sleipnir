package swarm

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/events"
)

// A worker's declared assumptions reach the manager with the task in review: task get shows them and the board digest the
// manager waits on names them, so a wrong guess is rejected in review. A task sent back drops them with its result.
func TestDeclaredAssumptionsReachTheManagerInReview(t *testing.T) {
	r := newRVRig(t, Config{Team: []string{"backend", "reviewer"}}, func(_ context.Context, c *rvCall) rvReply {
		if c.Agent == "rv-1" && c.Assistants == 0 {
			return rvReply{Tools: []rvToolCall{{Name: "task", Args: map[string]any{"action": "done", "id": "T1", "text": "no blocking findings",
				"assumptions": []any{"- reviewed only the merged result, not the earlier drafts", "", "treated the CLI flags as internal"}}}}}
		}
		return rvReply{Text: "ok"}
	})
	b := r.sw.Board
	task, err := b.CreateTask("mgr", TaskSpec{Title: "review the merged result", Role: "reviewer"})
	if err != nil {
		t.Fatal(err)
	}
	before := b.Snapshot()
	if res := r.callTool(context.Background(), "spawn", "mgr", "manager", map[string]any{"role": "reviewer", "task": task.ID}); res.IsError {
		t.Fatal(res.Text)
	}
	rvWait(t, task.ID+" in review", func() bool {
		cur, _ := b.Snapshot().Task(task.ID)
		return cur.Status == StatusReview
	})
	want := []string{"reviewed only the merged result, not the earlier drafts", "treated the CLI flags as internal"}
	if cur, _ := b.Snapshot().Task(task.ID); !slices.Equal(cur.Assumptions, want) {
		t.Fatalf("assumptions = %q, want %q", cur.Assumptions, want)
	}
	if got := r.callTool(context.Background(), "task", "mgr", "manager", map[string]any{"action": "get", "id": task.ID}).Text; !strings.Contains(got, "treated the CLI flags as internal") {
		t.Fatalf("task get does not show the assumptions:\n%s", got)
	}
	digest := strings.Join(diffSnapshots(before, b.Snapshot()), "\n")
	if !strings.Contains(digest, "assumed: reviewed only the merged result, not the earlier drafts; treated the CLI flags as internal") {
		t.Fatalf("the manager's digest does not name the assumptions:\n%s", digest)
	}
	if _, err := b.SendBack("mgr", task.ID, "check the earlier drafts too"); err != nil {
		t.Fatal(err)
	}
	if cur, _ := b.Snapshot().Task(task.ID); cur.Assumptions != nil {
		t.Fatalf("a task sent back keeps its old assumptions: %q", cur.Assumptions)
	}
}

// Assumptions are bounded like everything else on the board: one line each, distinct, at most maxAssumptions of them. A board
// restored after a restart sends review tasks back without them, and a replay of its log agrees; a replay of a submission
// rebuilds them.
func TestAssumptionsAreBoundedAndFollowTheTaskLifetime(t *testing.T) {
	log := events.NewMemLog()
	b := NewBoard(log)
	task, err := b.CreateTask("mgr", TaskSpec{Title: "review", Role: ReviewerRole})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Claim("rv-1", task.ID); err != nil {
		t.Fatal(err)
	}
	in := []string{"two\nlines", "two lines", "-1 is a valid offset", "* --no-cache stays supported", strings.Repeat("x", 3*maxAssumeRunes)}
	for i := range 2 * maxAssumptions {
		in = append(in, "choice "+string(rune('a'+i)))
	}
	if err := b.submitReportAt("rv-1", task.ID, 0, submission{result: "done", assumptions: in}); err != nil {
		t.Fatal(err)
	}
	cur, _ := b.Snapshot().Task(task.ID)
	if len(cur.Assumptions) != maxAssumptions {
		t.Fatalf("kept %d assumptions, want %d: %q", len(cur.Assumptions), maxAssumptions, cur.Assumptions)
	}
	for _, a := range cur.Assumptions {
		if strings.ContainsAny(a, "\n\r") || len([]rune(a)) > maxAssumeRunes {
			t.Fatalf("an assumption is not one bounded line: %q", a)
		}
	}
	if cur.Assumptions[0] != "two lines" || cur.Assumptions[1] == "two lines" {
		t.Fatalf("duplicates were not merged: %q", cur.Assumptions)
	}
	if cur.Assumptions[1] != "-1 is a valid offset" || cur.Assumptions[2] != "--no-cache stays supported" {
		t.Fatalf("a list marker was not told from the text: %q", cur.Assumptions[1:3])
	}
	replayed, err := ReplayBoard(log.All())
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := replayed.Task(task.ID); !slices.Equal(got.Assumptions, cur.Assumptions) {
		t.Fatalf("replay rebuilt %q, want %q", got.Assumptions, cur.Assumptions)
	}
	log2 := events.NewMemLog()
	restored := NewBoard(log2)
	restored.Restore(b.Snapshot())
	if got, _ := restored.Snapshot().Task(task.ID); got.Status != StatusTodo || got.Assumptions != nil {
		t.Fatalf("the restored task is %s with assumptions %q", got.Status, got.Assumptions)
	}
	replayed, err = ReplayBoard(log2.All())
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := replayed.Task(task.ID); got.Assumptions != nil {
		t.Fatalf("the replayed restored task kept assumptions %q", got.Assumptions)
	}
}
