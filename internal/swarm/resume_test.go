package swarm

import (
	"context"
	"strings"
	"testing"
)

func TestResumeLeavesReleasedTasksAvailableForOtherWorkers(t *testing.T) {
	for _, retire := range []bool{false, true} {
		name := "failed attempt"
		if retire {
			name = "retired owner"
		}
		t.Run(name, func(t *testing.T) {
			r := newIsoRig(t, isoOpts{}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "idle"} })
			previous := NewBoard(nil)
			task, err := previous.CreateTask("mgr", TaskSpec{Title: "released assignment", Role: "backend"})
			if err != nil {
				t.Fatal(err)
			}
			if err := previous.Assign("mgr", "be-1", task.ID); err != nil {
				t.Fatal(err)
			}
			if retire {
				previous.RequeueOwned("be-1", "its worker was retired")
			} else {
				previous.Requeue("be-1", task.ID, 0, "verification failed", true, 3)
			}
			if err := r.sw.RestoreTeam(context.Background(), previous.Snapshot(), []RecoveredWorker{{ID: "be-1", Role: "backend", Task: task.ID}}, map[string]string{"be-1": "backend"}, 0); err != nil {
				t.Fatal(err)
			}
			worker := r.sw.get("be-1")
			if worker.task != "" || strings.Contains(worker.a.Stack().Notes.Text(), task.Title) {
				t.Fatal("released task was restored as an assignment")
			}
			if err := r.sw.Board.Assign("mgr", "be-2", task.ID); err != nil {
				t.Fatalf("released task was reserved for its previous owner: %v", err)
			}
		})
	}
}

func TestRecoveredTaskReservationIsReleasedWithItsWorker(t *testing.T) {
	for _, retire := range []bool{false, true} {
		name := "resume without owner"
		if retire {
			name = "retire idle recovered owner"
		}
		t.Run(name, func(t *testing.T) {
			previous := NewBoard(nil)
			task, err := previous.CreateTask("mgr", TaskSpec{Title: "interrupted work", Role: "backend"})
			if err != nil {
				t.Fatal(err)
			}
			if err := previous.Assign("mgr", "be-1", task.ID); err != nil {
				t.Fatal(err)
			}
			recovered := NewBoard(nil)
			recovered.RestoreOwners(previous.Snapshot(), map[string]string{task.ID: "be-1"})
			if retire {
				recovered.RequeueOwned("be-1", "its worker was retired")
			} else {
				next := NewBoard(nil)
				next.Restore(recovered.Snapshot())
				recovered = next
			}
			if got, _ := recovered.Snapshot().Task(task.ID); got.Status != StatusTodo || got.Owner != "" {
				t.Fatalf("missing worker kept its reservation: %+v", got)
			}
			if err := recovered.Assign("mgr", "be-2", task.ID); err != nil {
				t.Fatalf("released reservation blocked another worker: %v", err)
			}
		})
	}
}

func TestResumePoolKeepsParentAndChildAssignments(t *testing.T) {
	previous := NewBoard(nil)
	parent, err := previous.CreateTask("mgr", TaskSpec{Title: "Agreed feature", Role: "backend"})
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{"be-1", "be-2"}
	if err := previous.beginPool("mgr", parent.ID, ids); err != nil {
		t.Fatal(err)
	}
	if err := previous.poolUpdate("be-1", parent.ID, func(_ *Snapshot, _ *Task, p *PoolState) error {
		p.Phase = "executing"
		p.Agreement = "Keep the agreed interface and existing child ownership."
		p.Revision = 7
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	child, err := previous.CreateTask("be-1", TaskSpec{Title: "Existing child", Role: "backend", Parent: parent.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := previous.Claim("be-1", child.ID); err != nil {
		t.Fatal(err)
	}
	r := newRVRig(t, Config{PersistentPools: true, Team: []string{"backend", "backend"}, MaxWorkers: 2}, func(ctx context.Context, _ *rvCall) rvReply { <-ctx.Done(); return rvReply{Text: "stopped"} })
	if err := r.sw.RestoreTeam(context.Background(), previous.Snapshot(), []RecoveredWorker{{ID: "be-1", Role: "backend", Task: child.ID}, {ID: "be-2", Role: "backend"}}, map[string]string{"be-1": "backend", "be-2": "backend"}, 0); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{parent.ID, child.ID} {
		task, _ := r.sw.Board.Snapshot().Task(id)
		if task.Owner != "be-1" {
			t.Fatalf("lost assignment %s: %+v", id, task)
		}
	}
	if _, err := r.sw.Spawn(SpawnReq{Role: "backend", TaskID: parent.ID, By: "mgr"}); err != nil {
		t.Fatal(err)
	}
	got, _ := r.sw.Board.Snapshot().Task(parent.ID)
	if got.Pool.Phase != "executing" || got.Pool.Revision != 7 || got.Pool.Agreement == "" {
		t.Fatalf("resume discarded agreed plan: %+v", got.Pool)
	}
	if len(r.sw.Board.Snapshot().Tasks) != 2 {
		t.Fatal("resume replaced existing tasks")
	}
}

func TestResumePoolReconciliationRequiresFreshConfirmation(t *testing.T) {
	previous := NewBoard(nil)
	parent, err := previous.CreateTask("mgr", TaskSpec{Title: "Review combined work", Role: "reviewer"})
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{"rv-1", "rv-2"}
	if err := previous.beginPool("mgr", parent.ID, ids); err != nil {
		t.Fatal(err)
	}
	if err := previous.poolUpdate("rv-1", parent.ID, func(_ *Snapshot, _ *Task, p *PoolState) error {
		p.Phase = "reconciling"
		p.Agreement = "Final combined report"
		p.Revision = 3
		p.Votes = append([]string(nil), ids...)
		p.Inspected = append([]string(nil), ids...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before, _ := previous.Snapshot().Task(parent.ID)
	restored := NewBoard(nil)
	restored.RestoreOwners(previous.Snapshot(), map[string]string{parent.ID: "rv-1"})
	if err := restored.beginPool("mgr", parent.ID, ids); err != nil {
		t.Fatal(err)
	}
	got, _ := restored.Snapshot().Task(parent.ID)
	if got.Pool.Phase != "executing" || got.Pool.Agreement != before.Pool.Agreement || got.Pool.Revision != 4 || len(got.Pool.Votes) != 0 || len(got.Pool.Inspected) != 0 {
		t.Fatalf("resumed reconciliation reused old confirmations: %+v", got.Pool)
	}
	if before.Pool.Phase != "reconciling" || len(before.Pool.Votes) != 2 {
		t.Fatal("resume mutated previous snapshot")
	}
}
