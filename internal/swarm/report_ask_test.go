package swarm

import (
	"context"
	"sync/atomic"
	"testing"
)

// A read-only worker that stops on its task with no answer text is asked for its report instead of being submitted empty: its
// report is all the manager decides on. A reviewer whose last turn was reasoning alone stopped so, and its manager spent four
// minutes recalling the reviewer's history for the verdict.
func TestAReviewerThatStopsWithoutAReportIsAskedForIt(t *testing.T) {
	var calls atomic.Int32
	r := newRVRig(t, Config{Team: []string{"backend", "reviewer"}}, func(_ context.Context, c *rvCall) rvReply {
		if c.Agent != "rv-1" {
			return rvReply{Text: "ok"}
		}
		switch calls.Add(1) {
		case 1:
			return rvReply{Thinking: "all four modules match their READMEs"} // reasoning alone: no text, no done
		case 2:
			return rvReply{Tools: []rvToolCall{{Name: "task", Args: map[string]any{"action": "done", "id": "T1", "text": "verdict: ship; no findings"}}}}
		}
		return rvReply{Text: "reported"}
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
	if cur, _ := b.Snapshot().Task(task.ID); cur.Result != "verdict: ship; no findings" {
		t.Fatalf("the review reached the manager with result %q", cur.Result)
	}
}
