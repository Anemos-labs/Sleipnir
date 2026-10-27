package swarm

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
)

func TestAcceptanceRecordsGenerationBeforePublication(t *testing.T) {
	r := newRVRigWith(t, Config{MaxWorkers: 8, VerifyCmd: "true", Verify: func(context.Context, string, string) (string, int, error) { return "ok", 0, nil }, Team: []string{"backend", "reviewer"}}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "noted"} }, nil)
	r.sw.Board.Restore(&Snapshot{Tasks: []Task{
		{ID: "T1", Title: "implement", Role: "backend", Status: StatusTodo},
		{ID: "T2", Title: "review", Role: ReviewerRole, Status: StatusTodo},
	}})
	if err := r.sw.Board.Claim("be-1", "T1"); err != nil {
		t.Fatal(err)
	}
	if err := r.sw.Board.submitAgreementAt("be-1", "T1", 0, "implemented", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := r.sw.Board.Claim("rv-1", "T2"); err != nil {
		t.Fatal(err)
	}
	review, _ := r.sw.Board.Snapshot().Task("T2")
	hold := newHoldEvents(events.Discard{}, events.TypeBoardOp)
	t.Cleanup(hold.open)
	r.sw.Board.ev = hold
	finished := make(chan string, 1)
	go func() {
		got := r.callTool(context.Background(), "task", "mgr", "manager", map[string]any{"action": "accept", "id": "T1"})
		if got.IsError {
			finished <- got.Text
		} else {
			finished <- ""
		}
	}()
	select {
	case <-hold.waiting:
	case result := <-finished:
		t.Fatalf("acceptance returned before publication: %s", result)
	case <-time.After(5 * time.Second):
		t.Fatal("acceptance did not reach publication")
	}
	if !r.sw.submitSettledTask(&member{id: review.Owner}, review, "reviewed", "read-only review") {
		t.Fatal("review submission failed")
	}
	hold.open()
	select {
	case failed := <-finished:
		if failed != "" {
			t.Fatal(failed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("acceptance did not return")
	}
	if got := r.callTool(context.Background(), "task", "mgr", "manager", map[string]any{"action": "accept", "id": "T2"}); got.IsError {
		t.Fatal(got.Text)
	}
	if got := r.sw.reviewPending(); got != "" {
		t.Fatalf("review missed the published acceptance: %q", got)
	}
	if r.sw.review.merges != 1 {
		t.Fatalf("acceptance counted %d times", r.sw.review.merges)
	}
}

func TestRejectedGateTransitionsDoNotRecordGeneration(t *testing.T) {
	s := New(Config{Team: []string{"backend", "reviewer"}}, Deps{}, nil)
	s.Board.Restore(&Snapshot{Tasks: []Task{
		{ID: "T1", Title: "implement", Role: "backend", Status: StatusTodo},
		{ID: "T2", Title: "plan review", Role: ReviewerRole, Kind: TaskKindPlan, Status: StatusTodo},
	}})
	if err := s.Board.Claim("be-1", "T1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Board.submitAgreementAt("be-1", "T1", 0, "done", "", ""); err != nil {
		t.Fatal(err)
	}
	task, _ := s.Board.Snapshot().Task("T1")
	stale := task
	stale.Rev++
	if err := s.Board.Accept("mgr", "T1", "", taskGateCheck(stale), s.noteAcceptedCheck(stale)); err == nil {
		t.Fatal("stale acceptance succeeded")
	}
	if s.review.merges != 0 {
		t.Fatal("failed acceptance recorded a merge")
	}
	s.noteAccepted(task)
	if err := s.Board.Claim("rv-1", "T2"); err != nil {
		t.Fatal(err)
	}
	review, _ := s.Board.Snapshot().Task("T2")
	if err := s.Board.submitAgreementAt("rv-1", "T2", review.Rev, "reviewed", "", "", s.reviewSubmission(review)); err == nil {
		t.Fatal("missing agreement accepted")
	}
	if _, ok := s.review.submitted["T2"]; ok {
		t.Fatal("failed submission recorded a generation")
	}
}

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

// The manager's live view agrees with the spawn tool: an idle member a pool keeps (its leader, the owner of an open subtask) is
// not offered, a truly free idle member is, and a ready task already queued for a free member is said to be queued, not offered
// again. A view that offered a pool's leader for a queued parent had the manager spawn it every fifteen seconds while each spawn
// answered that it was already queued.
func TestTheLiveViewDoesNotOfferPoolHeldMembersOrQueuedTasks(t *testing.T) {
	r := newRVRig(t, Config{PersistentPools: true, MaxWorkers: 3, Team: []string{"backend", "backend", "backend"}},
		func(ctx context.Context, _ *rvCall) rvReply { return rvReply{Text: "noted"} })
	r.sw.StartManager()
	if _, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "control plane", Files: []string{"server/**"}, By: "mgr"}); err != nil {
		t.Fatal(err)
	}
	first := r.sw.Board.Snapshot().Tasks[0]
	members := slices.Clone(first.Pool.Members)
	if len(members) != 3 || first.Owner != members[0] {
		t.Fatalf("the pool: %v led by %s", members, first.Owner)
	}
	if err := r.sw.Board.poolUpdate("mgr", first.ID, func(_ *Snapshot, _ *Task, p *PoolState) error { p.Phase = "executing"; return nil }); err != nil {
		t.Fatal(err)
	}
	holdChild := func(member, title, files string) {
		t.Helper()
		c, err := r.sw.Board.CreateTask(first.Owner, TaskSpec{Parent: first.ID, Title: title, Role: "backend", Files: []string{files}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.sw.Board.assignTask(assignReq{by: first.Owner, agent: member, id: c.ID}); err != nil {
			t.Fatal(err)
		}
	}
	holdChild(members[1], "store", "server/store/**")
	for _, id := range members {
		rvWait(t, id+" idle", func() bool { return r.idle(id) })
	}
	free, err := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "auth middleware", Role: "backend", Files: []string{"auth/**"}})
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "admin panel", Role: "backend", Files: []string{"admin/**"}})
	if err != nil {
		t.Fatal(err)
	}
	r.sw.queueSpawn(SpawnReq{Role: "backend", TaskID: waiting.ID, By: "mgr"}, waiting.Rev)

	got := strings.Join(r.sw.staffingLines(r.sw.Board.Snapshot()), "\n")
	for _, want := range []string{
		members[2] + " idle while " + free.ID + " (backend) can start: give it the task now (spawn agent=" + members[2] + " task=" + free.ID + ")",
		waiting.ID + " queued for the next free backend member: the harness starts them; do not spawn them again (wait)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("live view lines %q lack %q", got, want)
		}
	}
	for _, held := range members[:2] {
		if strings.Contains(got, held+" idle") || strings.Contains(got, held+", ") || strings.Contains(got, "agent="+held) {
			t.Errorf("live view lines %q offer %s, which the pool keeps", got, held)
		}
	}
	if strings.Contains(got, "task="+waiting.ID) || strings.Contains(got, "while "+waiting.ID) || strings.Contains(got, ", "+waiting.ID+" (") {
		t.Errorf("live view lines %q offer the queued %s", got, waiting.ID)
	}

	// the last free member takes a subtask too: nobody is offered, and the spawn the view no longer suggests is queued
	holdChild(members[2], "api", "server/api/**")
	got = strings.Join(r.sw.staffingLines(r.sw.Board.Snapshot()), "\n")
	if strings.Contains(got, "idle while") {
		t.Errorf("live view lines %q offer a member its pool keeps", got)
	}
	if res := r.callTool(context.Background(), "spawn", "mgr", "manager", map[string]any{"role": "backend", "task": free.ID}); res.IsError || !strings.Contains(res.Text, free.ID+" is queued") {
		t.Fatalf("spawn %s with nobody free: %+v", free.ID, res)
	}
	got = strings.Join(r.sw.staffingLines(r.sw.Board.Snapshot()), "\n")
	if want := free.ID + ", " + waiting.ID + " queued for the next free backend member"; !strings.Contains(got, want) {
		t.Errorf("live view lines %q lack %q", got, want)
	}
}

// Idle members of a role with nothing ready or queued for it are named to the manager in one line while work is open, so a team
// does not sit idle while one worker does the only open task: the line leaves out a member that still owns an open task and a role
// a ready task is offered to, and is silent once nothing on the board is open.
func TestTheLiveViewNamesIdleMembersWithNothingPlanned(t *testing.T) {
	r := newRVRigWith(t, Config{MaxWorkers: 8, Team: []string{"backend", "backend", "backend", "tester"}},
		func(context.Context, *rvCall) rvReply { return rvReply{Text: "noted"} }, nil)
	spawn := func(role, title, files string) (string, string) {
		t.Helper()
		id, err := r.sw.Spawn(SpawnReq{Role: role, Title: title, Files: []string{files}, By: "mgr"})
		if err != nil {
			t.Fatal(err)
		}
		rvWait(t, id+" idle", func() bool { return r.idle(id) })
		for _, task := range r.sw.Board.Snapshot().Tasks {
			if task.Owner == id && task.Title == title {
				return id, task.ID
			}
		}
		t.Fatalf("no task %q for %s", title, id)
		return "", ""
	}
	finish := func(member, task string) {
		t.Helper()
		if cur, _ := r.sw.Board.Snapshot().Task(task); cur.Status == StatusDoing {
			if err := r.sw.Board.Submit(member, task, "done", ""); err != nil {
				t.Fatal(err)
			}
		}
		if err := r.sw.Board.Accept("mgr", task, ""); err != nil {
			t.Fatal(err)
		}
	}
	busy, long := spawn("backend", "control plane", "server/**")
	be2, t2 := spawn("backend", "store", "store/**")
	be3, t3 := spawn("backend", "api", "api/**")
	te, t4 := spawn("tester", "smoke", "e2e/**")
	finish(be2, t2)
	finish(be3, t3)
	finish(te, t4)
	idleBackend := []string{be2, be3}
	slices.Sort(idleBackend)

	got := strings.Join(r.sw.staffingLines(r.sw.Board.Snapshot()), "\n")
	want := "idle with nothing planned for them: " + strings.Join(idleBackend, ", ") + " (backend); " + te + " (tester): " +
		"plan the next independent work for them (task create with file scopes disjoint from the open tasks, then spawn agent=" +
		idleBackend[0] + " task=<id>), or leave them idle if nothing independent remains"
	if !strings.Contains(got, want) {
		t.Fatalf("live view lines %q lack %q", got, want)
	}
	if strings.Contains(got, busy) {
		t.Errorf("live view lines %q name %s, which still owns %s", got, busy, long)
	}

	// a ready backend task is offered to the idle backend members instead; the tester is still named
	next, err := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "auth", Role: "backend", Files: []string{"auth/**"}})
	if err != nil {
		t.Fatal(err)
	}
	got = strings.Join(r.sw.staffingLines(r.sw.Board.Snapshot()), "\n")
	if want := "idle with nothing planned for them: " + te + " (tester): "; !strings.Contains(got, want) || strings.Contains(got, "(backend);") {
		t.Errorf("live view lines %q, want only the tester named as unplanned (%q)", got, want)
	}
	if !strings.Contains(got, " idle while "+next.ID+" (backend) can start") {
		t.Errorf("live view lines %q do not offer %s", got, next.ID)
	}

	// nothing open: the goal's work is finished, and the view says nothing about idle members
	if err := r.sw.Board.Claim(idleBackend[0], next.ID); err != nil {
		t.Fatal(err)
	}
	finish(idleBackend[0], next.ID)
	finish(busy, long)
	if got := r.sw.staffingLines(r.sw.Board.Snapshot()); len(got) != 0 {
		t.Errorf("live view lines with nothing open: %q", got)
	}
}

// A ready parent that kept its pool (returned to the board, spawn reuses its members) is offered its own idle members, and only it:
// members other pools keep are not offered, and a free member is offered a ready task that has no idle pool of its own.
func TestTheLiveViewOffersAReadyParentItsOwnPool(t *testing.T) {
	ready := []Task{{ID: "T1", Role: "backend"}, {ID: "T2", Role: "backend"}}
	idle := map[string][]string{"backend": {"be-3", "be-1", "be-2", "be-4"}}
	held := map[string]string{"be-1": "T1", "be-2": "T1", "be-3": "T9"}
	slots, used := map[string]int{"backend": 4}, map[string]int{"backend": 4}
	got, n := offerLines(ready, idle, held, slots, used, []string{"backend"})
	want := []string{
		"be-1, be-2 idle in the pool of T1 (backend), which can start: give it the task now (spawn agent=be-1 task=T1)",
		"be-4 idle while T2 (backend) can start: give it the task now (spawn agent=be-4 task=T2)",
	}
	if !slices.Equal(got, want) || n != 2 {
		t.Errorf("offerLines = %q (%d role lines), want %q", got, n, want)
	}
}

// The per-role lines stay within maxStaffingLines whatever mix of offers and queued notices fills them; the free-slot line is outside
// the bound.
func TestTheStaffingLinesStayWithinTheirBound(t *testing.T) {
	var ready []Task
	idle := map[string][]string{}
	var order []string
	for _, r := range []string{"a", "b", "c", "d"} {
		ready = append(ready, Task{ID: "T" + r, Role: r})
		idle[r] = []string{r + "-1"}
		order = append(order, r)
	}
	got, n := offerLines(ready, idle, nil, map[string]int{"a": 2}, map[string]int{"a": 1}, order)
	if n != maxStaffingLines || len(got) != maxStaffingLines+1 {
		t.Fatalf("offerLines = %q (%d role lines), want the free-slot line and %d role lines", got, n, maxStaffingLines)
	}
	if got, n := offerLines(ready, idle, nil, nil, nil, order); n != maxStaffingLines || len(got) != maxStaffingLines {
		t.Fatalf("offerLines with no free slot = %q (%d role lines), want %d", got, n, maxStaffingLines)
	}
	waiting := map[string][]string{"x": {"T7"}, "y": {"T8"}}
	if q := queuedLines(waiting, maxStaffingLines-n); len(q) != 0 {
		t.Errorf("queued notices %q past a full bound", q)
	}
	if q := queuedLines(waiting, 1); len(q) != 1 || !strings.HasPrefix(q[0], "T7 queued for the next free x member") {
		t.Errorf("queuedLines with room for one = %q", q)
	}
}

func TestAResumedTeamStillOwesTheReviewOfWorkMergedBeforeTheStop(t *testing.T) {
	cfg := Config{MaxWorkers: 8, VerifyCmd: "true", Team: []string{"backend", "reviewer"}}
	idle := func(context.Context, *rvCall) rvReply { return rvReply{Text: "noted"} }
	prev := &Snapshot{Version: 9, Tasks: []Task{
		{ID: "T1", Title: "implement", Role: "backend", Owner: "be-1", Status: StatusDone, Closure: closeAs(CloseVerified), Files: []string{"a.go"}},
		{ID: "T2", Title: "review", Role: ReviewerRole, Owner: "rv-1", Status: StatusDone, Closure: closeAs(CloseVerified)},
		{ID: "T3", Title: "implement more", Role: "backend", Owner: "be-1", Status: StatusDone, Closure: closeAs(CloseVerified), Files: []string{"b.go"}},
		{ID: "T4", Title: "not needed", Role: "backend", Status: StatusDone, Closure: closeAs(CloseUnneeded), Files: []string{"c.go"}},
		{ID: "T5", Title: "reopened after its acceptance", Role: "backend", Owner: "be-1", Status: StatusTodo, Files: []string{"d.go"}},
	}}
	review := func(id string) ReviewOp { return ReviewOp{Task: id, Status: StatusReview} }
	done := func(id string, k ClosureKind) ReviewOp { return ReviewOp{Task: id, Status: StatusDone, Closure: k} }
	land := func(id string) ReviewOp { return ReviewOp{Task: id, Land: true, Files: []string{"e.go"}} }
	cases := []struct {
		name string
		ops  []ReviewOp
		want string
	}{
		{"merged after the review", []ReviewOp{review("T1"), done("T1", CloseVerified), review("T2"), done("T2", CloseVerified), review("T3"), done("T3", CloseVerified)}, "T3"},
		{"reviewed last", []ReviewOp{done("T1", CloseVerified), done("T3", CloseVerified), review("T2"), done("T2", CloseVerified), done("T4", CloseUnneeded)}, ""},
		{"review written before the last merge", []ReviewOp{done("T1", CloseVerified), review("T2"), done("T3", CloseVerified), done("T2", CloseVerified)}, "T3"},
		{"accepted, then reopened", []ReviewOp{review("T2"), done("T2", CloseVerified), done("T5", CloseVerified)}, "T5"},
		{"manager changes after the review", []ReviewOp{done("T1", CloseVerified), review("T2"), done("T2", CloseVerified), land("manager changes")}, "manager changes"},
		{"a revision before the review", []ReviewOp{land("T1"), review("T2"), done("T2", CloseVerified)}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRVRigWith(t, cfg, idle, nil)
			if err := r.sw.RestoreTeam(context.Background(), prev, nil, map[string]string{"be-1": "backend", "rv-1": ReviewerRole}, 0); err != nil {
				t.Fatal(err)
			}
			if got := r.sw.reviewPending(); got != "" {
				t.Fatalf("pending before the replay: %q", got)
			}
			r.sw.RestoreReviewGate(prev, c.ops)
			if got := r.sw.reviewPending(); got != c.want {
				t.Fatalf("review pending after the resume: %q, want %q", got, c.want)
			}
		})
	}
	t.Run("accepted with the scope of its acceptance", func(t *testing.T) {
		r := newRVRigWith(t, cfg, idle, nil)
		if err := r.sw.RestoreTeam(context.Background(), prev, nil, map[string]string{"be-1": "backend", "rv-1": ReviewerRole}, 0); err != nil {
			t.Fatal(err)
		}
		// T5 was accepted while it covered old.go; a revision gave it d.go afterwards.
		r.sw.RestoreReviewGate(prev, []ReviewOp{{Task: "T5", Status: StatusDone, Closure: CloseVerified, Files: []string{"old.go"}}})
		if got := r.sw.review.landed[1]; len(got) != 1 || got[0] != "old.go" {
			t.Fatalf("the acceptance landed %v, want [old.go]", got)
		}
	})
}

// A writer's task that the merge queue found nothing to merge for (a tester's check that left no test behind) changed nothing a
// review has to cover: accepting it does not reopen the gate, so a review that ran beside it still stands.
func TestAnEmptyMergeIsNotDueAReview(t *testing.T) {
	r := newRVRigWith(t, Config{MaxWorkers: 8, Team: []string{"backend", "tester", "reviewer"}}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "noted"} }, nil)
	r.sw.noteAccepted(Task{ID: "T1", Role: "backend"})
	review := Task{ID: "T2", Role: ReviewerRole}
	if err := r.sw.reviewSubmission(review)(nil, review); err != nil {
		t.Fatal(err)
	}
	r.sw.noteAccepted(review)
	check := Task{ID: "T3", Role: "tester", Rev: 4, Files: []string{"lru/"}}
	r.sw.merged[check.ID] = mergeRec{rev: check.Rev, files: check.Files, empty: true}
	r.sw.noteAccepted(check)
	if got := r.sw.reviewPending(); got != "" {
		t.Fatalf("an empty merge reopened the review gate: %q", got)
	}
	changed := Task{ID: "T4", Role: "tester", Rev: 2, Files: []string{"lru/"}}
	r.sw.merged[changed.ID] = mergeRec{rev: changed.Rev, files: changed.Files, commit: "abc"}
	r.sw.noteAccepted(changed)
	if got := r.sw.reviewPending(); got != "T4" {
		t.Fatalf("a merge that changed the tree is not due a review: %q", got)
	}
}

// After an accepted review, the review line names the merges since it, so the next review covers them and not the
// whole result again; before one, it covers everything merged.
func TestReviewLineNamesTheMergesSinceTheAcceptedReview(t *testing.T) {
	r := newRVRigWith(t, Config{MaxWorkers: 8, Team: []string{"backend", "reviewer"}}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "noted"} }, nil)
	r.sw.noteAccepted(Task{ID: "T1", Role: "backend"})
	if got := r.sw.reviewLine(r.sw.reviewPending()); !strings.Contains(got, "T1 and the work merged before it have not been reviewed") || !strings.Contains(got, "review the merged result against") {
		t.Fatalf("first review line = %q", got)
	}
	review := Task{ID: "T2", Role: ReviewerRole}
	if err := r.sw.reviewSubmission(review)(nil, review); err != nil {
		t.Fatal(err)
	}
	r.sw.noteAccepted(review)
	if got := r.sw.reviewPending(); got != "" {
		t.Fatalf("review pending after the accepted review: %q", got)
	}
	r.sw.noteAccepted(Task{ID: "T3", Role: "backend"})
	r.sw.noteAccepted(Task{ID: "T4", Role: "backend"})
	got := r.sw.reviewLine(r.sw.reviewPending())
	for _, want := range []string{"the merges since the accepted review (T3, T4) have not been reviewed", "review that work (the accepted review stands for the rest) against"} {
		if !strings.Contains(got, want) {
			t.Fatalf("second review line = %q, want %q", got, want)
		}
	}
}

// The duration a review is measured against ends at the work's submission: the wait in review for the manager's decision is
// not build time.
func TestWorkDurationEndsAtSubmission(t *testing.T) {
	r := newRVRigWith(t, Config{MaxWorkers: 8, Team: []string{"backend", "reviewer"}}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "noted"} }, nil)
	g := &r.sw.review
	now := r.sw.deps.Now()
	task := Task{ID: "T1", Rev: 2, Role: "backend"}
	g.noteBegan(task.ID, task.Rev, now.Add(-20*time.Minute))
	g.mu.Lock()
	open := g.tookLocked(task, now)
	g.mu.Unlock()
	if open < 19*time.Minute || open > 21*time.Minute {
		t.Fatalf("unsubmitted work ran %v, want about 20m", open)
	}
	if err := r.sw.reviewSubmission(task)(nil, task); err != nil {
		t.Fatal(err)
	}
	g.mu.Lock()
	g.ended[task.ID+"#2"] = now.Add(-6 * time.Minute) // submitted six minutes ago, waiting on the manager since
	took := g.tookLocked(task, now)
	g.mu.Unlock()
	if took < 13*time.Minute || took > 15*time.Minute {
		t.Fatalf("submitted work took %v, want about 14m", took)
	}
	r.sw.noteAccepted(task)
	if work, ids := g.unreviewedWork(); work < 13*time.Minute || work > 15*time.Minute || len(ids) != 1 || ids[0] != "T1" {
		t.Fatalf("unreviewed work = %v %v", work, ids)
	}
}

// A merge that changes test files and docs alone after an accepted review leaves that review standing: the work tree is compared
// with the one the review began on. A change to anything else since then, an unreviewed merge before it, or no review yet keeps the
// review due. A tester's suite accepted after the review started a second review of the four test files, and the run hit its
// limit while that review ran.
func TestATestOnlyMergeAfterTheReviewLeavesItStanding(t *testing.T) {
	root := t.TempDir()
	write := func(rel, text string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("lru/lru.go", "package lru\n")
	write("lru/README.md", "# lru\n")
	gitOut(t, root, "init", "-q")
	gitOut(t, root, "add", "-A")
	gitOut(t, root, "commit", "-qm", "init")
	r := newRVRigWith(t, Config{MaxWorkers: 8, Team: []string{"backend", "tester", "reviewer"}}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "noted"} },
		func(d *Deps) { d.Root = root })
	// Tests alone with no review yet: the tests are the work, and are reviewed.
	write("lru/lru_test.go", "package lru\n")
	r.sw.noteAccepted(Task{ID: "T1", Role: "tester"})
	if r.sw.reviewPending() != "T1" {
		t.Fatal("a test-only merge with no review yet was not due for review")
	}
	write("lru/lru.go", "package lru\n\nfunc New() {}\n")
	r.sw.noteAccepted(Task{ID: "T2", Role: "backend"})
	review := Task{ID: "T3", Role: ReviewerRole}
	r.sw.noteReviewBegan(review)
	if err := r.sw.reviewSubmission(review)(nil, review); err != nil {
		t.Fatal(err)
	}
	r.sw.noteAccepted(review)
	if got := r.sw.reviewPending(); got != "" {
		t.Fatalf("review pending after the accepted review: %q", got)
	}
	// A tester's suite and a README after the review began: the review stands.
	write("lru/zz_extra_test.go", "package lru\n")
	write("lru/README.md", "# lru\n\nNew makes a cache.\n")
	write("lru/testdata/golden.txt", "1\n")
	r.sw.noteAccepted(Task{ID: "T4", Role: "tester"})
	if got := r.sw.reviewPending(); got != "" {
		t.Fatalf("a test-only merge after the accepted review is due for review: %q", got)
	}
	// Product code changed since the review began (a shell edit the task's scope does not show): due.
	write("lru/lru.go", "package lru\n\nfunc New() int { return 1 }\n")
	write("lru/more_test.go", "package lru\n")
	r.sw.noteAccepted(Task{ID: "T5", Role: "tester"})
	if got := r.sw.reviewPending(); got != "T5" {
		t.Fatalf("a merge whose tree changed product code since the review = %q, want T5", got)
	}
	// Once a merge is unreviewed, a test-only one after it is due too.
	write("lru/last_test.go", "package lru\n")
	r.sw.noteAccepted(Task{ID: "T6", Role: "tester"})
	if got := r.sw.reviewPending(); got != "T6" {
		t.Fatalf("a test-only merge after an unreviewed one = %q, want T6", got)
	}
}

// A review accepted after a test-only merge that landed while it waited for its verdict covers that merge too; one accepted
// after a merge that changed product code does not. A tester's suite accepted between the review's submission and its
// acceptance started a second review of the test files.
func TestAReviewAcceptedAfterATestOnlyMergeCoversIt(t *testing.T) {
	root := t.TempDir()
	write := func(rel, text string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("lru/lru.go", "package lru\n")
	gitOut(t, root, "init", "-q")
	gitOut(t, root, "add", "-A")
	gitOut(t, root, "commit", "-qm", "init")
	r := newRVRigWith(t, Config{MaxWorkers: 8, Team: []string{"backend", "tester", "reviewer"}}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "noted"} },
		func(d *Deps) { d.Root = root })
	review := func(id string) Task {
		rv := Task{ID: id, Role: ReviewerRole}
		r.sw.noteReviewBegan(rv)
		if err := r.sw.reviewSubmission(rv)(nil, rv); err != nil {
			t.Fatal(err)
		}
		return rv
	}
	write("lru/lru.go", "package lru\n\nfunc New() {}\n")
	r.sw.noteAccepted(Task{ID: "T1", Role: "backend"})
	rv := review("T2")
	write("lru/zz_extra_test.go", "package lru\n")
	r.sw.noteAccepted(Task{ID: "T3", Role: "tester"}) // while the review waits for its verdict
	r.sw.noteAccepted(rv)
	if got := r.sw.reviewPending(); got != "" {
		t.Fatalf("a review accepted after a test-only merge left %q unreviewed", got)
	}
	write("lru/lru.go", "package lru\n\nfunc New() int { return 1 }\n")
	r.sw.noteAccepted(Task{ID: "T4", Role: "backend"})
	rv = review("T5")
	write("lru/lru.go", "package lru\n\nfunc New() int { return 2 }\n")
	r.sw.noteAccepted(Task{ID: "T6", Role: "backend"}) // product code, while the review waits
	r.sw.noteAccepted(rv)
	if got := r.sw.reviewPending(); got != "T6" {
		t.Fatalf("review pending = %q, want T6", got)
	}
	// A merge that lands between the review's tree read and its acceptance stays due, whatever the tree showed.
	rv = review("T7")
	write("lru/late_test.go", "package lru\n")
	r.sw.noteAccepted(Task{ID: "T8", Role: "tester"})
	check := r.sw.noteAcceptedCheck(rv) // reads the tree: tests only since T7 began
	write("lru/lru.go", "package lru\n\nfunc New() int { return 3 }\n")
	r.sw.noteAccepted(Task{ID: "T9", Role: "backend"}) // lands before the acceptance publishes
	if err := check(nil, rv); err != nil {
		t.Fatal(err)
	}
	if got := r.sw.reviewPending(); got != "T9" {
		t.Fatalf("a merge that landed during the tree read = %q, want T9 due", got)
	}
}

// A review with no work tree to read (no git repository) stands for no later merge.
func TestATestOnlyMergeWithNoWorkTreeIsDueForReview(t *testing.T) {
	r := newRVRigWith(t, Config{MaxWorkers: 8, Team: []string{"backend", "tester", "reviewer"}}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "noted"} }, nil)
	r.sw.noteAccepted(Task{ID: "T1", Role: "backend"})
	review := Task{ID: "T2", Role: ReviewerRole}
	r.sw.noteReviewBegan(review)
	if err := r.sw.reviewSubmission(review)(nil, review); err != nil {
		t.Fatal(err)
	}
	r.sw.noteAccepted(review)
	r.sw.noteAccepted(Task{ID: "T3", Role: "tester"})
	if got := r.sw.reviewPending(); got != "T3" {
		t.Fatalf("review pending = %q, want T3", got)
	}
}
