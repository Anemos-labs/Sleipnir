package swarm

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/events"
)

func TestRepeatedDoneFailuresReturnTheTaskToTheManager(t *testing.T) {
	for _, maxAttempts := range []int{1, 3} {
		t.Run(fmt.Sprint(maxAttempts), func(t *testing.T) {
			var verifies atomic.Int32
			r := newRVRig(t, Config{MaxAttempts: maxAttempts, VerifyCmd: "go test ./...", Verify: func(context.Context, string, string) (string, int, error) {
				verifies.Add(1)
				return strings.Repeat("unrelated diagnostic\n", 100) + "--- FAIL: TestRequirement\nexpected 10 got 11", 1, nil
			}}, func(ctx context.Context, c *rvCall) rvReply {
				if c.Role == "manager" {
					return rvReply{Text: "waiting"}
				}
				return rvReply{Tools: []rvToolCall{{"task", map[string]any{"action": "done", "id": "T1", "text": "finished"}}}}
			})
			r.sw.StartManager()
			id, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "Fix requirement", By: "mgr"})
			if err != nil {
				t.Fatal(err)
			}
			want := StatusTodo
			if maxAttempts == 1 {
				want = StatusFailed
			}
			rvWait(t, "bounded verification recovery", func() bool {
				task, _ := r.sw.Board.Snapshot().Task("T1")
				return task.Status == want && r.idle(id)
			})
			task, _ := r.sw.Board.Snapshot().Task("T1")
			if verifies.Load() != 3 || task.Attempts != 1 || !strings.Contains(task.Evidence, "expected 10 got 11") {
				t.Fatalf("verifier ran %d times; task = %+v", verifies.Load(), task)
			}
			if n := mailSent(r, "verification `go test ./...` failed 3 times"); n != 1 {
				t.Fatalf("manager recovery notices = %d, want 1", n)
			}
			if n := mailSent(r, "expected 10 got 11"); n != 1 {
				t.Fatalf("the final diagnostic did not reach the manager: %d notices", n)
			}
		})
	}
}

func TestDoneAndImplicitStopShareTheVerificationBudget(t *testing.T) {
	var verifies atomic.Int32
	r := newRVRig(t, Config{VerifyCmd: "go test ./...", Verify: func(context.Context, string, string) (string, int, error) {
		verifies.Add(1)
		return "expected 10 got 11", 1, nil
	}}, func(ctx context.Context, c *rvCall) rvReply {
		if c.Role != "manager" && c.Assistants < 2 {
			return rvReply{Tools: []rvToolCall{{"task", map[string]any{"action": "done", "id": "T1"}}}}
		}
		return rvReply{Text: "finished"}
	})
	r.sw.StartManager()
	id, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "Fix requirement", By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	rvWait(t, "mixed verification failures to be bounded", func() bool {
		task, _ := r.sw.Board.Snapshot().Task("T1")
		return task.Status == StatusTodo && r.idle(id)
	})
	if verifies.Load() != 3 {
		t.Fatalf("verifier ran %d times, want 3", verifies.Load())
	}
}

func TestVerificationFailureCounterSurvivesReplayAndRecovery(t *testing.T) {
	log := events.NewMemLog()
	board := NewBoard(log)
	task, err := board.CreateTask("mgr", TaskSpec{Title: "Fix requirement"})
	if err != nil {
		t.Fatal(err)
	}
	if err := board.Assign("mgr", "be-1", task.ID); err != nil {
		t.Fatal(err)
	}
	task, _ = board.Snapshot().Task(task.ID)
	for i := 0; i < 2; i++ {
		if _, applied := board.FailVerification("be-1", task.ID, task.Rev, "failed", "verifier output", 3, 3); !applied {
			t.Fatal("verification failure was not recorded")
		}
	}
	previous, err := ReplayBoard(log.All())
	if err != nil || !reflect.DeepEqual(previous.Tasks, board.Snapshot().Tasks) {
		t.Fatalf("replay differs from the live board: %v", err)
	}
	restored := NewBoard(log)
	restored.RestoreOwners(previous, map[string]string{task.ID: "be-1"})
	if err := restored.Assign("mgr", "be-1", task.ID); err != nil {
		t.Fatal(err)
	}
	task, _ = restored.Snapshot().Task(task.ID)
	if task.VerificationFailures != 2 {
		t.Fatalf("recovery lost failed verifications: %+v", task)
	}
	next, applied := restored.FailVerification("be-1", task.ID, task.Rev, "failed", "verifier output", 3, 3)
	if !applied || next.Status != StatusTodo || next.Attempts != 1 || next.VerificationFailures != 0 {
		t.Fatalf("recovered retry budget was not exhausted: %+v", next)
	}
	replayed, err := ReplayBoard(log.All())
	if err != nil || !reflect.DeepEqual(replayed.Tasks, restored.Snapshot().Tasks) {
		t.Fatalf("recovered replay differs: %v", err)
	}
}

func TestVerificationFailuresApplyOnceAndOnlyToTheirAssignment(t *testing.T) {
	board := NewBoard(events.NewMemLog())
	task, _ := board.CreateTask("mgr", TaskSpec{Title: "Fix requirement"})
	if err := board.Assign("mgr", "be-1", task.ID); err != nil {
		t.Fatal(err)
	}
	task, _ = board.Snapshot().Task(task.ID)
	var applied atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok := board.FailVerification("be-1", task.ID, task.Rev, "failed", "output", 3, 3); ok {
				applied.Add(1)
			}
		}()
	}
	wg.Wait()
	next, _ := board.Snapshot().Task(task.ID)
	if applied.Load() != 3 || next.Attempts != 1 || next.Status != StatusTodo {
		t.Fatalf("applied %d failures: %+v", applied.Load(), next)
	}
	if err := board.Assign("mgr", "be-1", task.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := board.FailVerification("be-1", task.ID, task.Rev, "late failure", "output", 3, 3); ok {
		t.Fatal("a stale verifier altered a new assignment")
	}
	next, _ = board.Snapshot().Task(task.ID)
	if next.VerificationFailures != 0 || next.Status != StatusDoing || next.Attempts != 1 {
		t.Fatalf("new assignment was changed: %+v", next)
	}
}

func TestVerificationInfrastructureAndCancellationDoNotSpendRepairRetries(t *testing.T) {
	var mode atomic.Int32
	r := newRVRig(t, Config{VerifyCmd: "go test ./...", Verify: func(context.Context, string, string) (string, int, error) {
		switch mode.Load() {
		case 0:
			return "test failure", 1, nil
		case 1:
			return "", 0, fmt.Errorf("verifier unavailable")
		default:
			return "ok", 0, nil
		}
	}}, func(ctx context.Context, c *rvCall) rvReply {
		<-ctx.Done()
		return rvReply{}
	})
	r.sw.StartManager()
	id, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "Fix requirement", By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	done := func(ctx context.Context) bool {
		return r.callTool(ctx, "task", id, "backend", map[string]any{"action": "done", "id": "T1"}).IsError
	}
	if !done(context.Background()) {
		t.Fatal("failed verification was accepted")
	}
	mode.Store(1)
	if !done(context.Background()) {
		t.Fatal("infrastructure failure was accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !done(ctx) {
		t.Fatal("canceled verification was accepted")
	}
	task, _ := r.sw.Board.Snapshot().Task("T1")
	if task.VerificationFailures != 1 || task.Attempts != 0 || task.Status != StatusDoing {
		t.Fatalf("non-verdicts spent repair retries: %+v", task)
	}
	mode.Store(2)
	if done(context.Background()) {
		t.Fatal("passing verification was rejected")
	}
	task, _ = r.sw.Board.Snapshot().Task("T1")
	if task.VerificationFailures != 0 || task.Status != StatusReview {
		t.Fatalf("successful submission retained failed retries: %+v", task)
	}
}

func TestAStaleVerificationCannotCancelANewerRun(t *testing.T) {
	var canceled atomic.Bool
	m := &member{life: lifeRunning, run: &runState{tasks: map[string]uint64{"T1": 2}, cancel: func() { canceled.Store(true) }}}
	s := &Swarm{}
	if s.stopRunFor(m, "old verifier", false, "T1", 1) || s.stopRunFor(m, "missing assignment", false, "T2", 0) || canceled.Load() {
		t.Fatal("a stale verifier canceled the new run")
	}
	if !s.stopRunFor(m, "current verifier", false, "T1", 2) || !canceled.Load() {
		t.Fatal("the current assignment could not stop its run")
	}
}
