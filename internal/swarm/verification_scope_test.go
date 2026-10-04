package swarm

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/tools"
)

func TestDoneRejectsVerificationForAnOldScope(t *testing.T) {
	for _, exitCode := range []int{0, 1} {
		t.Run(fmt.Sprintf("exit_%d", exitCode), func(t *testing.T) {
			var s *Swarm
			var calls atomic.Int32
			s = New(Config{VerifyCmd: "verify {dirs}", Verify: func(context.Context, string, string) (string, int, error) {
				if calls.Add(1) == 1 {
					if err := s.Board.SetScope("mgr", "T1", []string{"api/a.go", "web/b.go"}, nil); err != nil {
						return "", 0, err
					}
					return "result for the old scope", exitCode, nil
				}
				return "checked the new scope", 0, nil
			}}, Deps{}, nil)
			task, err := s.Board.CreateTask("mgr", TaskSpec{Title: "implement", Files: []string{"api/a.go"}})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Board.Assign("mgr", "be-1", task.ID); err != nil {
				t.Fatal(err)
			}
			tool := &taskTool{s: s}
			call := &tools.Call{Env: &tools.Env{Agent: "be-1", Role: "backend", Cwd: t.TempDir()}}
			if res := tool.done(context.Background(), call, taskIn{ID: task.ID, Text: "implemented"}); !res.IsError {
				t.Fatalf("old scope was submitted: %s", res.Text)
			}
			current, _ := s.Board.Snapshot().Task(task.ID)
			if current.Status != StatusDoing || current.VerificationFailures != 0 || current.Attempts != 0 {
				t.Fatalf("old scope result changed the current task: %+v", current)
			}
			if res := tool.done(context.Background(), call, taskIn{ID: task.ID, Text: "implemented"}); res.IsError {
				t.Fatalf("fresh verification was rejected: %s", res.Text)
			}
			current, _ = s.Board.Snapshot().Task(task.ID)
			if current.Status != StatusReview || calls.Load() != 2 {
				t.Fatalf("fresh verification did not submit the task: %+v, calls=%d", current, calls.Load())
			}
		})
	}
}

func TestMergeEvidenceMatchesTheCurrentScope(t *testing.T) {
	s := New(Config{}, Deps{}, nil)
	task, err := s.Board.CreateTask("mgr", TaskSpec{Title: "implement", Files: []string{"api/a.go"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Board.Assign("mgr", "be-1", task.ID); err != nil {
		t.Fatal(err)
	}
	old, _ := s.Board.Snapshot().Task(task.ID)
	s.recordMerge(old, mergeRec{rev: old.Rev, commit: "old scope"})
	if err := s.Board.SetScope("mgr", task.ID, []string{"api/a.go", "web/b.go"}, nil); err != nil {
		t.Fatal(err)
	}
	current, _ := s.Board.Snapshot().Task(task.ID)
	if _, ok := s.mergedFor(current); ok {
		t.Fatal("old merge evidence applies to the expanded scope")
	}
	s.recordMerge(current, mergeRec{rev: current.Rev, commit: "current scope"})
	s.recordMerge(old, mergeRec{rev: old.Rev, commit: "late old scope"})
	if rec, ok := s.mergedFor(current); !ok || rec.commit != "current scope" {
		t.Fatalf("late merge erased the current scope's evidence: %+v, found=%v", rec, ok)
	}
}

func TestExhaustedMergeCannotRequeueAChangedScope(t *testing.T) {
	s := New(Config{MaxAttempts: 3}, Deps{}, nil)
	task, err := s.Board.CreateTask("mgr", TaskSpec{Title: "implement", Files: []string{"api/a.go"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Board.Assign("mgr", "be-1", task.ID); err != nil {
		t.Fatal(err)
	}
	old, _ := s.Board.Snapshot().Task(task.ID)
	if err := s.Board.SetScope("mgr", task.ID, []string{"api/a.go", "web/b.go"}, nil); err != nil {
		t.Fatal(err)
	}
	before := s.Board.Snapshot()
	line := s.giveUpMerge(&member{id: "be-1"}, old, "old scope failed verification")
	if s.Board.Snapshot() != before || !strings.Contains(line, "changed") {
		t.Fatalf("old merge failure requeued the new scope: %s, task=%+v", line, s.Board.Snapshot().Tasks)
	}
}

func TestAcceptRejectsVerificationForAChangedTask(t *testing.T) {
	for _, change := range []string{"scope", "assignment"} {
		t.Run(change, func(t *testing.T) {
			var s *Swarm
			var calls atomic.Int32
			s = New(Config{VerifyCmd: "verify", Verify: func(context.Context, string, string) (string, int, error) {
				if calls.Add(1) == 1 {
					if change == "scope" {
						if err := s.Board.SetScope("mgr", "T1", []string{"api/a.go", "web/b.go"}, nil); err != nil {
							return "", 0, err
						}
					} else {
						if err := s.Board.Unassign("mgr", "T1", "reassign"); err != nil {
							return "", 0, err
						}
						if err := s.Board.Assign("mgr", "be-2", "T1"); err != nil {
							return "", 0, err
						}
						if err := s.Board.Submit("be-2", "T1", "replacement", "new evidence"); err != nil {
							return "", 0, err
						}
					}
				}
				return "ok", 0, nil
			}}, Deps{}, nil)
			task, err := s.Board.CreateTask("mgr", TaskSpec{Title: "implement", Files: []string{"api/a.go"}})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Board.Assign("mgr", "be-1", task.ID); err != nil {
				t.Fatal(err)
			}
			if err := s.Board.Submit("be-1", task.ID, "ready", "checked"); err != nil {
				t.Fatal(err)
			}
			tool := &taskTool{s: s}
			call := &tools.Call{Env: &tools.Env{Agent: "mgr", Role: "manager", Cwd: t.TempDir()}}
			if res := tool.review(context.Background(), call, taskIn{Action: "accept", ID: task.ID}); !res.IsError || !strings.Contains(res.Text, "changed "+change) {
				t.Fatalf("obsolete review was accepted: %+v", res)
			}
			current, _ := s.Board.Snapshot().Task(task.ID)
			if current.Status != StatusReview {
				t.Fatalf("obsolete review changed the task: %+v", current)
			}
			if res := tool.review(context.Background(), call, taskIn{Action: "accept", ID: task.ID}); res.IsError {
				t.Fatalf("fresh review failed: %s", res.Text)
			}
			current, _ = s.Board.Snapshot().Task(task.ID)
			if current.Status != StatusDone || calls.Load() != 2 {
				t.Fatalf("fresh review did not finish: %+v, calls=%d", current, calls.Load())
			}
		})
	}
}

func TestImplicitCompletionRetriesAfterScopeChanges(t *testing.T) {
	for _, exitCode := range []int{0, 1, -1} {
		t.Run(fmt.Sprintf("exit_%d", exitCode), func(t *testing.T) {
			var r *rvRig
			var calls atomic.Int32
			commands := make(chan string, 8)
			r = newRVRig(t, Config{VerifyCmd: "verify {dirs}", Verify: func(_ context.Context, _, cmd string) (string, int, error) {
				commands <- cmd
				if calls.Add(1) == 1 {
					if err := r.sw.Board.SetScope("mgr", "T1", []string{"api/a.go", "web/b.go"}, nil); err != nil {
						return "", 0, err
					}
					if exitCode < 0 {
						return "", 0, fmt.Errorf("runner unavailable")
					}
					return "old scope result", exitCode, nil
				}
				return "ok", 0, nil
			}}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "implemented"} })
			r.sw.StartManager()
			id, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "implement", Files: []string{"api/a.go"}, By: "mgr"})
			if err != nil {
				t.Fatal(err)
			}
			rvWait(t, "fresh scope verified and submitted", func() bool {
				task, _ := r.sw.Board.Snapshot().Task("T1")
				return task.Status == StatusReview && r.idle(id)
			})
			if calls.Load() != 2 || r.prov.callsFor(id) != 2 {
				t.Fatalf("scope change did not trigger a fresh worker run: verifies=%d runs=%d", calls.Load(), r.prov.callsFor(id))
			}
			if first, second := <-commands, <-commands; first != "verify ./api" || second != "verify ./api ./web" {
				t.Fatalf("verified commands %q then %q", first, second)
			}
			task, _ := r.sw.Board.Snapshot().Task("T1")
			if task.VerificationFailures != 0 || task.Attempts != 0 {
				t.Fatalf("obsolete verification spent a retry: %+v", task)
			}
			if !r.prov.sawEver("changed scope while verification or merging ran") {
				t.Fatal("worker did not receive the scope-change recovery instructions")
			}
		})
	}
}

func TestIsolatedAcceptRequiresMergeEvidenceForTheCurrentScope(t *testing.T) {
	sc := script{"be-1": {act(writeCall("a.txt", "alpha edited\n")), act(doneCall("T1", "edited a"))}}
	r := newIsoRig(t, isoOpts{verify: true}, sc.fn())
	r.sw.StartManager()
	if _, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "edit a", Files: []string{"a.txt"}, By: "mgr"}); err != nil {
		t.Fatal(err)
	}
	r.waitStatus("T1", StatusReview)
	if err := r.sw.Board.SetScope("mgr", "T1", []string{"a.txt", "b.txt"}, nil); err != nil {
		t.Fatal(err)
	}
	if res := r.accept("T1"); !res.IsError || !strings.Contains(res.Text, "scope") {
		t.Fatalf("accepted an expanded scope without matching merge evidence: %+v", res)
	}
	if r.task("T1").Status != StatusReview {
		t.Fatal("refused acceptance changed task status")
	}
	if res := r.callTool(context.Background(), "task", "mgr", "manager", map[string]any{"action": "reject", "id": "T1", "text": "verify the expanded scope"}); res.IsError {
		t.Fatalf("could not request fresh verification: %s", res.Text)
	}
	r.waitStatus("T1", StatusReview)
	if res := r.accept("T1"); res.IsError {
		t.Fatalf("fresh merge evidence was not accepted: %s", res.Text)
	}
}
