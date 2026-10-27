package swarm

import (
	"context"
	"strings"
	"testing"
)

// A manager on a team with a reviewer, waiting on submissions in review, is told to decide them from their evidence: the reviewer
// tests the merged result. A manager wrote probe tests for each of four submissions in turn while every worker sat idle, and the
// reviewer it spawned afterwards tested the same code again. Without a reviewer slot the line is not added.
func TestAManagersWaitSaysToDecideSubmissionsFromTheirEvidence(t *testing.T) {
	for _, tc := range []struct {
		team []string
		want bool
	}{
		{[]string{"backend", "reviewer"}, true},
		{[]string{"backend", "tester"}, false},
	} {
		r := newRVRig(t, Config{Team: tc.team}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "ok"} })
		b := r.sw.Board
		task, err := b.CreateTask("mgr", TaskSpec{Title: "fix the cache", Role: "backend"})
		if err != nil {
			t.Fatal(err)
		}
		if err := b.Assign("mgr", "be-1", task.ID); err != nil {
			t.Fatal(err)
		}
		if err := b.Finish("be-1", task.ID, StatusReview, "fixed; go test ./... ok"); err != nil {
			t.Fatal(err)
		}
		res := r.callTool(context.Background(), "wait", "mgr", "manager", map[string]any{"until": []string{task.ID}, "timeout_sec": 3})
		line := task.ID + " in review: decide each from its evidence"
		if got := strings.Contains(res.Text, line); got != tc.want {
			t.Fatalf("team %v: the wait result %q has the decide line: %v, want %v", tc.team, res.Text, got, tc.want)
		}
		if res := r.callTool(context.Background(), "wait", "be-1", "backend", map[string]any{"until": []string{task.ID}, "timeout_sec": 3}); strings.Contains(res.Text, line) {
			t.Fatalf("a worker's wait carries the manager's decide line: %q", res.Text)
		}
	}
}
