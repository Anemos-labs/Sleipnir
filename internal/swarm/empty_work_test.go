package swarm

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

// sawEdit records in agent's evidence that it edited path, as the harness does when its edit tool succeeds.
func sawEdit(t *testing.T, r *rvRig, agent, path string) {
	t.Helper()
	m := r.sw.get(agent)
	if m == nil {
		t.Fatalf("no member %s", agent)
	}
	in, _ := json.Marshal(map[string]any{"path": path})
	m.ev.Observe(core.Block{ToolName: "edit", Input: in}, &tools.Result{Text: "edited " + path}, time.Now())
}

// inReview puts a task of the given role in review, submitted by owner with the harness evidence evid.
func inReview(t *testing.T, b *Board, role, owner, evid string) Task {
	t.Helper()
	task, err := b.CreateTask("mgr", TaskSpec{Title: "alerts and incidents pages", Role: role})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Assign("mgr", owner, task.ID); err != nil {
		t.Fatal(err)
	}
	if err := b.Submit(owner, task.ID, "", evid); err != nil {
		t.Fatal(err)
	}
	task, _ = b.Snapshot().Task(task.ID)
	return task
}

// An implementation task in which the harness saw no file edited is not accepted as verified work: a worker that stopped
// without doing anything is submitted by the gate as it is, and a manager that accepts it closes the work. The manager accepts it
// only by saying why no change was needed; real work, and tasks whose deliverable may be no change, are accepted as before.
func TestAcceptRefusesWorkWithNoEdits(t *testing.T) {
	r := newRVRig(t, Config{}, gatedWorkers(nil))
	ctx := context.Background()
	accept := func(id, text string) (string, bool) {
		res := r.callTool(ctx, "task", "mgr", "manager", map[string]any{"action": "accept", "id": id, "text": text})
		return res.Text, !res.IsError
	}
	status := func(id string) TaskStatus {
		task, _ := r.sw.Board.Snapshot().Task(id)
		return task.Status
	}

	empty := inReview(t, r.sw.Board, "frontend", "fe-4", "no files edited; no tests run")
	out, ok := accept(empty.ID, "Alerts and incidents flows reviewed and accepted.")
	if ok || status(empty.ID) != StatusReview {
		t.Fatalf("work with no edits was accepted: %s", out)
	}
	for _, want := range []string{"no file change", "no files edited", "reject", "nothing to change:"} {
		if !strings.Contains(out, want) {
			t.Errorf("the refusal does not say %q: %s", want, out)
		}
	}
	if out, ok := accept(empty.ID, "nothing to change:"); ok {
		t.Fatalf("a reason with nothing after it was taken: %s", out)
	}
	if out, ok := accept(empty.ID, "Nothing to change: the pages already filter by severity (T38 built them)"); !ok || status(empty.ID) != StatusDone {
		t.Fatalf("the manager's stated reason was refused: %s", out)
	}
	if task, _ := r.sw.Board.Snapshot().Task(empty.ID); !strings.Contains(task.Result, "already filter by severity") {
		t.Errorf("the reason is not on the board: %q", task.Result)
	}

	edited := inReview(t, r.sw.Board, "frontend", "fe-5", `edited 2 (Alerts.tsx, Incidents.tsx); last test "npm test" passed`)
	if out, ok := accept(edited.ID, "looks good"); !ok {
		t.Fatalf("work with edits was refused: %s", out)
	}
	review := inReview(t, r.sw.Board, "reviewer", "rv-1", "no files edited; last test \"go test ./...\" passed")
	if out, ok := accept(review.ID, "review done"); !ok {
		t.Fatalf("a read-only role's task was held to edits: %s", out)
	}
	docs := inReview(t, r.sw.Board, DocsRole, "dc-1", "no files edited; no tests run")
	if out, ok := accept(docs.ID, "docs already current"); !ok {
		t.Fatalf("a documentation task was held to edits: %s", out)
	}
}

// The T42 path end to end: a worker that stops without calling done is submitted by the harness's gate with the evidence it
// has, "no files edited", and the manager's accept is refused.
func TestAcceptRefusesAStoppedWorkerThatChangedNothing(t *testing.T) {
	r := newRVRig(t, Config{}, gatedWorkers(map[string][]rvReply{"be-1": {readReply, {Text: "I looked at the code; it seems fine."}}}))
	r.sw.StartManager()
	id, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "paginate users", By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	var task Task
	rvWait(t, "the gate to submit the task", func() bool {
		task, _ = r.sw.Board.Snapshot().Task(currentTask(r.sw.Board.Snapshot(), id))
		return task.Status == StatusReview
	})
	if !evidenceShowsNoEdits(task.Evidence) {
		t.Fatalf("evidence = %q", task.Evidence)
	}
	res := r.callTool(context.Background(), "task", "mgr", "manager", map[string]any{"action": "accept", "id": task.ID, "text": "accepted"})
	if !res.IsError {
		t.Fatalf("accepted: %s", res.Text)
	}
}

// A task that goes back to todo keeps nothing of an earlier assignment's submission on the board: not after the harness requeues
// it (its worker stopped), and not after the manager revises or reopens it for repair. The event log keeps them.
func TestRequeueAndReviseDropTheOldResultAndEvidence(t *testing.T) {
	b := NewBoard(nil)
	task := inReview(t, b, "frontend", "fe-4", "no files edited; no tests run")
	if err := b.Accept("mgr", task.ID, "reviewed and accepted"); err != nil {
		t.Fatal(err)
	}
	if err := b.revise("mgr", task.ID, TaskSpec{}, "reopened for repair"); err != nil {
		t.Fatal(err)
	}
	got, _ := b.Snapshot().Task(task.ID)
	if got.Status != StatusTodo || got.Result != "" || got.Evidence != "" {
		t.Fatalf("after revise: status %s, result %q, evidence %q", got.Status, got.Result, got.Evidence)
	}

	// A rejected submission keeps its result while its worker repairs it; when that worker stops, the task is requeued without it.
	again := inReview(t, b, "backend", "be-1", "edited 1 (a.go); no tests run")
	sent, err := b.SendBack("mgr", again.ID, "add the test")
	if err != nil {
		t.Fatal(err)
	}
	if sent.Evidence == "" {
		t.Fatal("setup: a rejected task keeps its evidence while its worker repairs it")
	}
	back, applied := b.Requeue("be-1", again.ID, sent.Rev, "step limit reached", true, 3)
	if !applied || back.Status != StatusTodo || back.Result != "" || back.Evidence != "" {
		t.Fatalf("after requeue: applied %v, status %s, result %q, evidence %q", applied, back.Status, back.Result, back.Evidence)
	}
	// The last attempt fails the task with the reason as its result, and no stale evidence.
	if err := b.Assign("mgr", "be-1", again.ID); err != nil {
		t.Fatal(err)
	}
	cur, _ := b.Snapshot().Task(again.ID)
	failed, applied := b.Requeue("be-1", again.ID, cur.Rev, "step limit reached", true, 2)
	if !applied || failed.Status != StatusFailed || failed.Result != "step limit reached" || failed.Evidence != "" {
		t.Fatalf("after the last attempt: %s, result %q, evidence %q", failed.Status, failed.Result, failed.Evidence)
	}
}
