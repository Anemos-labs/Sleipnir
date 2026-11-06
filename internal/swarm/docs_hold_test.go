package swarm

import (
	"strings"
	"testing"
)

// A docs catch-up pass waits while the team is still building: a pass started mid-flight documented defects as the current
// behaviour and was rewritten twice as review fixed them. It starts once no implementation task is open, or after docsHoldMax.
func TestDocsPassWaitsForTheBuildToSettle(t *testing.T) {
	cfg := Config{MaxWorkers: 8, AutoDocs: true, Team: []string{"backend", "tester", "docs"}}
	r := newRVRigWith(t, cfg, idleModel, nil)
	now := r.sw.deps.Now()
	r.sw.docsTick(now)
	r.sw.Board.Restore(&Snapshot{Tasks: []Task{
		{ID: "T1", Title: "add the month report", Status: StatusDone, Role: "backend", Files: []string{"report/**"}},
		{ID: "T2", Title: "fix the CSV amounts", Status: StatusTodo, Role: "backend", Files: []string{"csv/**"}},
		{ID: "T3", Title: "e2e suite", Status: StatusTodo, Role: "tester", Files: []string{"e2e/**"}},
	}})
	r.sw.docsTick(now)
	if _, made := r.sw.Board.Snapshot().Task("T4"); made {
		t.Fatal("a pass started while T2 was still open")
	}
	if st := r.sw.DocsState(); !strings.Contains(st, "T2 todo") {
		t.Errorf("state = %q", st)
	}
	if err := r.sw.Board.resolve("mgr", "T2", "not needed"); err != nil {
		t.Fatal(err)
	}
	r.sw.docsTick(now.Add(docsRetry))
	if pass, made := r.sw.Board.Snapshot().Task("T4"); !made || !strings.HasPrefix(pass.Title, DocsTaskTitle) {
		t.Fatalf("no pass once the build settled (only test work open): %+v; state %q", pass, r.sw.DocsState())
	}
}
