package swarm

import (
	"context"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

func TestLateMergeRecordPreservesTheCurrentAssignment(t *testing.T) {
	s := New(Config{}, Deps{}, nil)
	task, err := s.Board.CreateTask("mgr", TaskSpec{Title: "implement"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Board.Assign("mgr", "be-1", task.ID); err != nil {
		t.Fatal(err)
	}
	old, _ := s.Board.Snapshot().Task(task.ID)
	s.recordMerge(old, mergeRec{rev: old.Rev, commit: "old integration"})
	if err := s.Board.Unassign("mgr", task.ID, "reassign"); err != nil {
		t.Fatal(err)
	}
	if err := s.Board.Assign("mgr", "be-1", task.ID); err != nil {
		t.Fatal(err)
	}
	current, _ := s.Board.Snapshot().Task(task.ID)
	s.recordMerge(current, mergeRec{rev: current.Rev, commit: "new integration"})
	// An older queue result can finish publishing after the replacement's merge.
	// Accepting the replacement must still find its own successful merge record.
	s.recordMerge(old, mergeRec{rev: old.Rev, commit: "old integration"})
	if got, ok := s.mergedFor(current); !ok || got.commit != "new integration" {
		t.Fatalf("late result replaced the current merge record: %+v, found=%v", got, ok)
	}
}

func TestTaskFailureRejectsAReplacedAssignment(t *testing.T) {
	for _, owner := range []string{"be-1", "be-2"} {
		t.Run(owner, func(t *testing.T) {
			b := NewBoard(events.Discard{})
			task, err := b.CreateTask("mgr", TaskSpec{Title: "implement", Role: "backend"})
			if err != nil {
				t.Fatal(err)
			}
			if err := b.Assign("mgr", "be-1", task.ID); err != nil {
				t.Fatal(err)
			}
			old, _ := b.Snapshot().Task(task.ID)
			if _, applied := b.Requeue("be-1", old.ID, old.Rev, "worker stopped", true, 3); !applied {
				t.Fatal("could not requeue the old assignment")
			}
			if err := b.Assign("mgr", owner, task.ID); err != nil {
				t.Fatal(err)
			}
			before := b.Snapshot()
			if err := b.FailAt("mgr", old.ID, old.Rev, "late failure"); err == nil {
				t.Fatal("failure of the old assignment was applied to its replacement")
			}
			if b.Snapshot() != before {
				t.Fatal("stale failure published a new board state")
			}
			current, _ := before.Task(task.ID)
			if err := b.FailAt("mgr", current.ID, current.Rev, "current failure"); err != nil {
				t.Fatal(err)
			}
			got, _ := b.Snapshot().Task(task.ID)
			if got.Status != StatusFailed || got.Result != "current failure" {
				t.Fatalf("matching failure was not applied: %+v", got)
			}
		})
	}
}

func TestLateTaskFailureDoesNotCancelReplacement(t *testing.T) {
	for _, scenario := range []struct {
		name             string
		afterPublication bool
		managerFailure   bool
	}{
		{name: "assignment replaced before merge failure"},
		{name: "assignment replaced during requeue publication", afterPublication: true},
		{name: "assignment replaced during manager failure publication", afterPublication: true, managerFailure: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			b := NewBoard(events.Discard{})
			task, err := b.CreateTask("mgr", TaskSpec{Title: "implement", Role: "backend"})
			if err != nil {
				t.Fatal(err)
			}
			if err := b.Assign("mgr", "be-1", task.ID); err != nil {
				t.Fatal(err)
			}
			old, _ := b.Snapshot().Task(task.ID)
			_, oldCancel := context.WithCancel(context.Background())
			defer oldCancel()
			m := &member{id: "be-1", life: lifeRunning, run: &runState{cancel: oldCancel, tasks: map[string]uint64{old.ID: old.Rev}}}
			s := &Swarm{Board: b, cfg: Config{MaxAttempts: 3}, members: map[string]*member{m.id: m}}
			fail := func() string {
				if scenario.managerFailure {
					return (&taskTool{s: s}).review(context.Background(), &tools.Call{Env: &tools.Env{Agent: "mgr"}},
						taskIn{Action: "fail", ID: old.ID, Text: "abandon this attempt"}).Text
				}
				return s.giveUpMerge(m, old, "merge verification failed")
			}
			var hold *holdEvents
			finished := make(chan string, 1)
			if scenario.afterPublication {
				hold = newHoldEvents(events.Discard{}, events.TypeBoardOp)
				t.Cleanup(hold.open)
				b.ev = hold // all setup events have completed before installing the barrier
				go func() { finished <- fail() }()
				select {
				case <-hold.waiting:
				case <-time.After(5 * time.Second):
					t.Fatal("failure did not reach the publication barrier")
				}
			} else if err := b.Unassign("mgr", old.ID, "reassign"); err != nil {
				t.Fatal(err)
			}
			if scenario.managerFailure {
				if err := b.Reopen("mgr", old.ID); err != nil {
					t.Fatal(err)
				}
			}
			if err := b.Assign("mgr", m.id, old.ID); err != nil {
				t.Fatal(err)
			}
			current, _ := b.Snapshot().Task(old.ID)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			next := &runState{cancel: cancel, tasks: map[string]uint64{current.ID: current.Rev}}
			m.mu.Lock()
			m.run = next
			m.mu.Unlock()
			if scenario.afterPublication {
				hold.open()
				select {
				case <-finished:
				case <-time.After(5 * time.Second):
					t.Fatal("late failure did not return")
				}
			} else {
				fail()
			}
			if ctx.Err() != nil || next.reason != "" {
				t.Fatalf("old failure canceled the replacement: err=%v reason=%q", ctx.Err(), next.reason)
			}
			got, _ := b.Snapshot().Task(old.ID)
			if got.Status != StatusDoing || got.Owner != current.Owner || got.Rev != current.Rev || got.Attempts != current.Attempts {
				t.Fatalf("old failure changed the replacement assignment: got=%+v want=%+v", got, current)
			}
		})
	}
}
