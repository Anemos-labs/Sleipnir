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
