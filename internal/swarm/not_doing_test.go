package swarm

import (
	"context"
	"strings"
	"testing"
)

// A change that needs a task in progress, refused, says what the task's status asks for instead. A manager told only "T30 is done,
// not doing" tried to record a new verification on its finished task three times.
func TestNotDoingRefusalsSayWhatToDoInstead(t *testing.T) {
	r := newRVRig(t, Config{}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "ok"} })
	if _, err := r.sw.StartManager(); err != nil {
		t.Fatal(err)
	}
	b := r.sw.Board
	task, err := b.CreateTask("mgr", TaskSpec{Title: "verify the build", Role: "backend"})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Assign("mgr", "be-1", task.ID); err != nil {
		t.Fatal(err)
	}
	if err := b.Block("be-1", task.ID, "needs a decision"); err != nil {
		t.Fatal(err)
	}
	call := func(action string) string {
		return r.callTool(context.Background(), "task", "be-1", "backend", map[string]any{"action": action, "id": task.ID, "text": "verified again"}).Text
	}
	if got := call("update"); !strings.Contains(got, "is blocked, not doing: resume it first") {
		t.Errorf("update of a blocked task: %s", got)
	}
	if err := b.Resume("be-1", task.ID); err != nil {
		t.Fatal(err)
	}
	if err := b.Finish("be-1", task.ID, StatusReview, "ready"); err != nil {
		t.Fatal(err)
	}
	if got := call("done"); !strings.Contains(got, "is in review, not doing: it waits for the manager") {
		t.Errorf("done of a task in review: %s", got)
	}
	if err := b.Accept("mgr", task.ID, "ok"); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"update", "done"} {
		if got := call(action); !strings.Contains(got, "is done, not doing: its result stands") || !strings.Contains(got, "new task") {
			t.Errorf("%s of a done task: %s", action, got)
		}
	}

	// a task reserved for the worker is todo until it claims it; a failed one is settled as a done one is
	other, err := b.CreateTask("mgr", TaskSpec{Title: "paginate users", Role: "backend"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.assignTask(assignReq{by: "mgr", agent: "be-1", id: other.ID, reserve: true}); err != nil {
		t.Fatal(err)
	}
	callOn := func(action, id string) string {
		return r.callTool(context.Background(), "task", "be-1", "backend", map[string]any{"action": action, "id": id, "text": "started"}).Text
	}
	if got := callOn("update", other.ID); !strings.Contains(got, "is todo, not doing: claim it first (task claim)") {
		t.Errorf("update of a reserved todo task: %s", got)
	}
	if err := b.Fail("mgr", other.ID, closeAs(CloseCanceled), "not needed"); err != nil {
		t.Fatal(err)
	}
	if got := callOn("done", other.ID); !strings.Contains(got, "is failed, not doing: its result stands") {
		t.Errorf("done of a failed task: %s", got)
	}
}
