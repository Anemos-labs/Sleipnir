package swarm

import (
	"context"
	"testing"
)

// A resume keeps in review the tasks whose acceptance needs nothing the stop lost: a read-only role's result and a pool parent whose
// members confirmed its reconciliation. A writer's task in review with no merge record still goes back to its worker. A resumed team
// had a finished review, its second-model complement and a confirmed pool parent all put back to todo, "waiting to restart", while
// their results sat on the board; the manager spent ten minutes reading them before restarting work that was done.
func TestAResumeKeepsAFinishedReviewAndAConfirmedPoolInReview(t *testing.T) {
	previous := NewBoard(nil)
	review, err := previous.CreateTask("mgr", TaskSpec{Title: "Review the merged result", Role: ReviewerRole})
	if err != nil {
		t.Fatal(err)
	}
	if err := previous.Assign("mgr", "rv-1", review.ID); err != nil {
		t.Fatal(err)
	}
	if err := previous.Submit("rv-1", review.ID, "no blocking findings", "read the diff since 9a77eef"); err != nil {
		t.Fatal(err)
	}
	parent, err := previous.CreateTask("mgr", TaskSpec{Title: "Agreed feature", Role: "backend"})
	if err != nil {
		t.Fatal(err)
	}
	members := []string{"be-1", "be-2"}
	if err := previous.beginPool("mgr", parent.ID, members); err != nil {
		t.Fatal(err)
	}
	if err := previous.poolUpdate("be-1", parent.ID, func(_ *Snapshot, _ *Task, p *PoolState) error {
		p.Phase, p.Agreement, p.Revision = "reconciling", "combined result", 4
		p.Votes, p.Inspected = append([]string(nil), members...), append([]string(nil), members...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := previous.Submit("be-1", parent.ID, "combined result", "every member confirmed revision 4"); err != nil {
		t.Fatal(err)
	}
	solo, err := previous.CreateTask("mgr", TaskSpec{Title: "Unmerged work", Role: "backend"})
	if err != nil {
		t.Fatal(err)
	}
	if err := previous.Assign("mgr", "be-3", solo.ID); err != nil {
		t.Fatal(err)
	}
	if err := previous.Submit("be-3", solo.ID, "done", "edited api.go"); err != nil {
		t.Fatal(err)
	}
	r := newRVRig(t, Config{PersistentPools: true, Team: []string{"backend", "backend", "backend", ReviewerRole}, MaxWorkers: 4}, idleModel)
	workers := []RecoveredWorker{{ID: "be-1", Role: "backend", Task: parent.ID}, {ID: "be-2", Role: "backend"}, {ID: "be-3", Role: "backend", Task: solo.ID}, {ID: "rv-1", Role: ReviewerRole, Task: review.ID}}
	ids := map[string]string{"be-1": "backend", "be-2": "backend", "be-3": "backend", "rv-1": ReviewerRole}
	if err := r.sw.RestoreTeam(context.Background(), previous.Snapshot(), workers, ids, 0); err != nil {
		t.Fatal(err)
	}
	sn := r.sw.Board.Snapshot()
	for _, id := range []string{review.ID, parent.ID} {
		got, _ := sn.Task(id)
		if got.Status != StatusReview || got.Line == resumedLine {
			t.Errorf("%s (%s) came back %s %q, want it kept in review", id, got.Title, got.Status, got.Line)
		}
	}
	if got, _ := sn.Task(parent.ID); got.Pool == nil || got.Pool.Phase != "reconciling" || len(got.Pool.Votes) != 2 {
		t.Errorf("the kept pool lost its confirmations: %+v", got.Pool)
	}
	if got, _ := sn.Task(solo.ID); got.Status != StatusTodo || got.Owner != "be-3" || got.Line != resumedLine {
		t.Errorf("a writer's unmerged task in review = %s %s %q, want todo for be-3 waiting to restart", got.Status, got.Owner, got.Line)
	}
	for _, id := range []string{"rv-1", "be-1"} {
		m := r.sw.get(id)
		if m == nil {
			t.Fatalf("%s was not restored", id)
		}
		m.mu.Lock()
		line := m.line
		m.mu.Unlock()
		if line != "" {
			t.Errorf("%s waits for a task that stayed in review: %q", id, line)
		}
	}
	if res := r.callTool(context.Background(), "task", "mgr", "manager", map[string]any{"action": "accept", "id": parent.ID}); res.IsError {
		t.Errorf("the kept pool parent could not be accepted: %s", res.Text)
	}
}
