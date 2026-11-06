package swarm

import (
	"context"
	"strings"
	"testing"
)

// A task the harness queued to start (spawnqueue.go) is the harness's to start: the manager's wait is not refused for it. In a
// team run a spawn said "T7 is queued: the harness starts it once its dependencies are done. Do not spawn it again; use wait",
// the wait then said "Cannot wait: ... not started: T7. Use spawn", and the spawn that followed was refused as busy.
func TestAQueuedTaskDoesNotRefuseTheManagersWait(t *testing.T) {
	r := newRVRig(t, Config{}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "ok"} })
	if _, err := r.sw.StartManager(); err != nil {
		t.Fatal(err)
	}
	b := r.sw.Board
	queued, _ := b.CreateTask("mgr", TaskSpec{Title: "tests", Role: "backend"})
	r.sw.queueSpawn(SpawnReq{Role: "backend", TaskID: queued.ID, By: "mgr"}, queued.Rev)
	if why := r.sw.stalledWaitReason("mgr"); why != "" {
		t.Fatalf("a wait with only a queued task left was refused: %q", why)
	}
	other, _ := b.CreateTask("mgr", TaskSpec{Title: "docs", Role: "backend"})
	if why := r.sw.stalledWaitReason("mgr"); !strings.Contains(why, "Cannot wait") || !strings.Contains(why, other.ID) {
		t.Fatalf("a task nobody started or queued did not refuse the wait: %q", why)
	}
}

// The manager's update of a task nobody holds names no owner: "the progress line of T2's owner, ." read as a garbled error.
func TestAManagersUpdateOfAnUnheldTaskSaysNobodyHoldsIt(t *testing.T) {
	r := newRVRig(t, Config{}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "ok"} })
	if _, err := r.sw.StartManager(); err != nil {
		t.Fatal(err)
	}
	task, _ := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "api", Role: "backend"})
	res := r.callTool(context.Background(), "task", "mgr", "manager", map[string]any{"action": "update", "id": task.ID, "text": "unblocked"})
	if !res.IsError || !strings.Contains(res.Text, "nobody holds "+task.ID+" yet") || strings.Contains(res.Text, "owner, .") || strings.Contains(res.Text, "mail it") {
		t.Fatalf("update of an unheld task: %q", res.Text)
	}
}

// Once a review is open, accepting more work names it instead of asking again for a reviewer task.
func TestAnAcceptWhileAReviewIsOpenNamesIt(t *testing.T) {
	r := newRVRig(t, Config{Team: []string{"backend", "reviewer"}}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "ok"} })
	if _, err := r.sw.StartManager(); err != nil {
		t.Fatal(err)
	}
	b := r.sw.Board
	accept := func(title string) string {
		t.Helper()
		task, _ := b.CreateTask("mgr", TaskSpec{Title: title, Role: "backend"})
		if err := b.Assign("mgr", "be-1", task.ID); err != nil {
			t.Fatal(err)
		}
		if err := b.Submit("be-1", task.ID, "done", "edited api.go"); err != nil {
			t.Fatal(err)
		}
		return r.callTool(context.Background(), "task", "mgr", "manager", map[string]any{"action": "accept", "id": task.ID}).Text
	}
	if got := accept("api"); !strings.Contains(got, "a reviewer task is due") {
		t.Fatalf("the first accept with no review: %q", got)
	}
	review, _ := b.CreateTask("mgr", TaskSpec{Title: "review the api", Role: ReviewerRole})
	if got := accept("fix"); strings.Contains(got, "a reviewer task is due") || !strings.Contains(got, "A review is open ("+review.ID+")") {
		t.Fatalf("an accept while a review is open: %q", got)
	}
}
