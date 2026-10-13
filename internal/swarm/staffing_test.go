package swarm

import (
	"context"
	"strings"
	"testing"
)

func TestStaleReviewDoesNotCoverLaterMerge(t *testing.T) {
	for _, kind := range []string{"", TaskKindPlan} {
		r := newRVRigWith(t, Config{MaxWorkers: 8, Team: []string{"backend", "reviewer"}}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "noted"} }, nil)
		r.sw.noteAccepted(Task{ID: "T1", Role: "backend"})
		review := Task{ID: "T2", Role: ReviewerRole, Kind: kind}
		if err := r.sw.reviewSubmission(review)(nil, review); err != nil {
			t.Fatal(err)
		}
		r.sw.noteAccepted(Task{ID: "T3", Role: "backend"})
		r.sw.noteAccepted(review)
		if got := r.sw.reviewPending(); got != "T3" {
			t.Fatalf("kind %q: stale review cleared later merge: %q", kind, got)
		}
		if err := r.sw.reviewSubmission(review)(nil, review); err != nil {
			t.Fatal(err)
		}
		r.sw.noteAccepted(review)
		if got := r.sw.reviewPending(); got != "" {
			t.Fatalf("kind %q: fresh review did not clear gate: %q", kind, got)
		}
	}
}

func TestCleanStopReviewCapturesMergeGeneration(t *testing.T) {
	for _, laterMerge := range []bool{false, true} {
		r := newRVRigWith(t, Config{MaxWorkers: 8, Team: []string{"backend", "reviewer"}}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "noted"} }, nil)
		r.sw.noteAccepted(Task{ID: "T1", Role: "backend"})
		review := Task{ID: "T2", Title: "review", Role: ReviewerRole, Owner: "rv-1", Status: StatusDoing}
		r.sw.Board.Restore(&Snapshot{Tasks: []Task{review}})
		if err := r.sw.Board.Claim(review.Owner, review.ID); err != nil {
			t.Fatal(err)
		}
		review, _ = r.sw.Board.Snapshot().Task(review.ID)
		if !r.sw.submitSettledTask(&member{id: review.Owner}, review, "reviewed", "read-only review") {
			t.Fatal("clean-stop review was not submitted")
		}
		if laterMerge {
			r.sw.noteAccepted(Task{ID: "T3", Role: "backend"})
		}
		if err := r.sw.Board.Accept("mgr", review.ID, ""); err != nil {
			t.Fatal(err)
		}
		r.sw.noteAccepted(review)
		want := ""
		if laterMerge {
			want = "T3"
		}
		if got := r.sw.reviewPending(); got != want {
			t.Fatalf("later merge %v: pending = %q, want %q", laterMerge, got, want)
		}
	}
}

func TestRejectedCleanStopReviewPreservesMergeGeneration(t *testing.T) {
	for _, stale := range []string{"scope", "revision", "status"} {
		t.Run(stale, func(t *testing.T) {
			r := newRVRigWith(t, Config{MaxWorkers: 8, Team: []string{"backend", "reviewer"}}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "noted"} }, nil)
			r.sw.noteAccepted(Task{ID: "T1", Role: "backend"})
			review := Task{ID: "T2", Title: "review", Role: ReviewerRole, Status: StatusTodo}
			r.sw.Board.Restore(&Snapshot{Tasks: []Task{review}})
			if err := r.sw.Board.Claim("rv-1", review.ID); err != nil {
				t.Fatal(err)
			}
			review, _ = r.sw.Board.Snapshot().Task(review.ID)
			if err := r.sw.reviewSubmission(review)(nil, review); err != nil {
				t.Fatal(err)
			}
			switch stale {
			case "scope":
				review.Files = []string{"stale.go"}
			case "revision":
				review.Rev++
			case "status":
				if !r.sw.submitSettledTask(&member{id: review.Owner}, review, "reviewed", "read-only review") {
					t.Fatal("initial submission failed")
				}
			}
			r.sw.noteAccepted(Task{ID: "T3", Role: "backend"})
			if r.sw.submitSettledTask(&member{id: review.Owner}, review, "stale", "stale") {
				t.Fatal("stale submission succeeded")
			}
			r.sw.noteAccepted(review)
			if got := r.sw.reviewPending(); got != "T3" {
				t.Fatalf("rejected submission covered later merge: %q", got)
			}
		})
	}
}

func TestReadOnlyTasksDoNotStartDocs(t *testing.T) {
	for _, resume := range []bool{false, true} {
		r := newRVRigWith(t, Config{MaxWorkers: 8, AutoDocs: true, Team: []string{"backend", "reviewer", "docs"}}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "noted"} }, nil)
		if !resume {
			r.sw.docsTick(r.sw.deps.Now())
		}
		r.sw.Board.Restore(&Snapshot{Tasks: []Task{{ID: "T1", Title: "review", Role: ReviewerRole, Status: StatusDone}}})
		r.sw.docsTick(r.sw.deps.Now())
		if r.sw.docs.coded {
			t.Fatalf("resume %v: read-only result counted as code", resume)
		}
		for _, task := range r.sw.Board.Snapshot().Tasks {
			if strings.HasPrefix(task.Title, DocsTaskTitle) {
				t.Fatalf("resume %v: read-only result started docs", resume)
			}
		}
	}
}

// An agreement accepted before any code exists starts no docs pass and holds nobody: it waits, and the first accepted implementation
// task starts one pass that covers both.
func TestAutoDocsWaitsForTheFirstImplementation(t *testing.T) {
	cfg := Config{MaxWorkers: 8, AutoDocs: true, Team: []string{"backend", "docs"}}
	r := newRVRigWith(t, cfg, func(ctx context.Context, c *rvCall) rvReply { return rvReply{Text: "noted"} }, nil)
	r.sw.docsTick(r.sw.deps.Now())
	plan := Task{ID: "T1", Title: "agree on the notes API", Status: StatusDone, Kind: TaskKindPlan}
	r.sw.Board.Restore(&Snapshot{Tasks: []Task{plan, {ID: "T2", Title: "add the notes store", Status: StatusTodo, Role: "backend"}}})
	r.sw.docsTick(r.sw.deps.Now())
	for _, tk := range r.sw.Board.Snapshot().Tasks {
		if strings.HasPrefix(tk.Title, DocsTaskTitle) {
			t.Fatalf("a pass started on an agreement alone: %+v", tk)
		}
	}
	if r.sw.docsPending() || !strings.Contains(r.sw.DocsState(), "1 agreement(s) pending until the first implementation task is accepted") {
		t.Errorf("pending %v, state %q", r.sw.docsPending(), r.sw.DocsState())
	}
	if err := r.sw.Board.claim("be-1", "T2", nil); err != nil {
		t.Fatal(err)
	}
	t2, _ := r.sw.Board.Snapshot().Task("T2")
	if err := r.sw.Board.submitAgreementAt("be-1", "T2", t2.Rev, "done", "", "", func(*Snapshot, Task) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := r.sw.Board.Accept("mgr", "T2", ""); err != nil {
		t.Fatal(err)
	}
	r.sw.docsTick(r.sw.deps.Now())
	var passes []Task
	for _, tk := range r.sw.Board.Snapshot().Tasks {
		if strings.HasPrefix(tk.Title, DocsTaskTitle) {
			passes = append(passes, tk)
		}
	}
	if len(passes) != 1 || !strings.Contains(passes[0].Title, "T1, T2") {
		t.Fatalf("want one pass over the agreement and the change, got %+v", passes)
	}
}

// The manager's live view says when slots are free while ready tasks have no owner, and which idle worker could take a ready task
// of its role.
func TestTheManagerIsToldOfUnusedSlots(t *testing.T) {
	cfg := Config{MaxWorkers: 8, Team: []string{"backend", "devops", "tester"}}
	r := newRVRigWith(t, cfg, func(ctx context.Context, c *rvCall) rvReply { return rvReply{Text: "noted"} }, nil)
	if got := r.sw.staffingLines(r.sw.Board.Snapshot()); got != nil {
		t.Fatalf("an empty board: %q", got)
	}
	id, err := r.sw.Spawn(SpawnReq{Role: "devops", Title: "set up CI", By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	rvWait(t, "devops idle", func() bool { return r.idle(id) })
	next, err := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "write the Dockerfile", Role: "devops"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "e2e smoke test", Role: "tester"}); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(r.sw.staffingLines(r.sw.Board.Snapshot()), "\n")
	for _, want := range []string{"2 free slot(s) (backend, tester), 2 ready task(s) with no owner: start workers", id + " idle while " + next.ID + " (devops) can start"} {
		if !strings.Contains(got, want) {
			t.Errorf("live view lines %q lack %q", got, want)
		}
	}
}

// In a team with a reviewer slot, an accepted change of a writer is due a review: the manager's live view says so, its wait is refused
// once nothing else runs, and an accepted task of the reviewer clears it. A team without a reviewer has no such gate.
func TestAMergedChangeIsDueAReview(t *testing.T) {
	cfg := Config{MaxWorkers: 8, Team: []string{"backend", "reviewer"}}
	r := newRVRigWith(t, cfg, func(ctx context.Context, c *rvCall) rvReply { return rvReply{Text: "noted"} }, nil)
	r.sw.StartManager()
	if r.sw.reviewPending() != "" {
		t.Fatal("due a review before anything was merged")
	}
	r.sw.noteAccepted(Task{ID: "T1", Kind: TaskKindPlan})
	if r.sw.reviewPending() != "" {
		t.Fatal("an agreement is not code")
	}
	r.sw.noteAccepted(Task{ID: "T2", Role: "backend"})
	if got := r.sw.reviewPending(); got != "T2" {
		t.Fatalf("reviewPending = %q", got)
	}
	why := r.sw.stalledWaitReason(r.sw.ManagerID())
	if !strings.Contains(why, "Cannot wait") || !strings.Contains(why, "T2 and the work merged before it have not been reviewed") {
		t.Errorf("wait refusal %q", why)
	}
	review := Task{ID: "T3", Role: ReviewerRole}
	if err := r.sw.reviewSubmission(review)(nil, review); err != nil {
		t.Fatal(err)
	}
	r.sw.noteAccepted(review)
	if got := r.sw.reviewPending(); got != "" {
		t.Errorf("a reviewer's accepted task did not clear the gate: %q", got)
	}
	r.sw.noteAccepted(Task{ID: "T4", Role: "backend"})
	if got := r.sw.reviewPending(); got != "T4" {
		t.Errorf("a later merge is due a review again: %q", got)
	}

	plain := newRVRigWith(t, Config{MaxWorkers: 8, Team: []string{"backend"}}, func(ctx context.Context, c *rvCall) rvReply { return rvReply{Text: "noted"} }, nil)
	plain.sw.noteAccepted(Task{ID: "T1", Role: "backend"})
	if plain.sw.reviewPending() != "" {
		t.Error("a team without a reviewer has a review gate")
	}
}

// A batch run's manager is held at its final answer while a merged change waits for review, and let go once a reviewer's task is
// accepted.
func TestABatchRunIsHeldUntilTheMergedResultIsReviewed(t *testing.T) {
	r := newRVRig(t, Config{HoldManager: true, MaxWorkers: 8, Team: []string{"backend", "reviewer"}}, func(ctx context.Context, c *rvCall) rvReply {
		return rvReply{Text: "ok"}
	})
	r.sw.StartManager()
	h := &holdHooks{s: r.sw, m: r.sw.get(r.sw.ManagerID())}
	if o := h.BeforeStop(context.Background(), "mgr", "manager", "done", false); o.Veto {
		t.Fatalf("held with nothing merged: %+v", o)
	}
	r.sw.noteAccepted(Task{ID: "T1", Role: "backend"})
	o := h.BeforeStop(context.Background(), "mgr", "manager", "done", false)
	if !o.Veto || !strings.Contains(o.Reason, "T1 and the work merged before it have not been reviewed") {
		t.Fatalf("not held for the review: %+v", o)
	}
	review := Task{ID: "T2", Role: ReviewerRole}
	if err := r.sw.reviewSubmission(review)(nil, review); err != nil {
		t.Fatal(err)
	}
	r.sw.noteAccepted(review)
	if o := h.BeforeStop(context.Background(), "mgr", "manager", "done", false); o.Veto {
		t.Fatalf("held after the review: %+v", o)
	}
}
