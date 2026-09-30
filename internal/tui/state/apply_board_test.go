package state

import (
	"fmt"
	"strings"
	"testing"

	"github.com/reee344/sleipnir/internal/events"
)

func taskByID(t testing.TB, sn *Snapshot, id string) Task {
	t.Helper()
	for _, tk := range sn.Board.Tasks {
		if tk.ID == id {
			return tk
		}
	}
	t.Fatalf("no task %s", id)
	return Task{}
}

func TestBoardRebuildsTasksAndDerivesTheKanbanColumn(t *testing.T) {
	b := newB()
	st := New()
	op := func(o string, id, status, owner string, rev uint64, extra map[string]any) events.Event {
		return b.Emit("mgr", events.TypeBoardOp, taskOp(o, id, status, owner, rev, extra))
	}
	for _, id := range []string{"w-1", "w-2", "w-3"} {
		apply(t, st, b.Spawn(id, "backend", "", "mgr"))
	}
	for i := 1; i <= 10; i++ {
		extra := map[string]any{"title": fmt.Sprintf("Task number %d\x1b[2J with an escape", i), "role": "backend", "deps": []string{}, "desc": "long text"}
		if i == 2 {
			extra["deps"] = []string{"T1"}
			extra["files"] = []string{"b/**", "a/x.go"}
		}
		apply(t, st, op("create", fmt.Sprintf("T%d", i), "todo", "", uint64(i), extra))
	}
	apply(t, st, op("claim", "T1", "doing", "w-1", 11, map[string]any{"files": []string{"a/**"}}))
	apply(t, st, op("assign", "T2", "doing", "w-2", 12, map[string]any{"files": []string{"b/**", "a/x.go"}}))
	apply(t, st, op("finish", "T3", "review", "w-3", 13, map[string]any{"result": "did it", "evidence": "edited 2 files; last test passed"}))
	apply(t, st, op("finish", "T4", "done", "w-4", 14, nil))
	apply(t, st, op("finish", "T5", "failed", "w-5", 15, map[string]any{"result": "gave up"}))
	apply(t, st, op("block", "T6", "blocked", "w-6", 16, map[string]any{"line": "waiting for T1"}))
	apply(t, st, op("update", "T7", "doing", "w-7", 17, map[string]any{"line": "half way", "attempts": 1}))

	sn := st.Snapshot()
	var ids []string
	for _, tk := range sn.Board.Tasks {
		ids = append(ids, tk.ID)
	}
	if strings.Join(ids, " ") != "T1 T2 T3 T4 T5 T6 T7 T8 T9 T10" {
		t.Errorf("tasks are ordered by number: %v", ids)
	}
	want := map[string]TaskState{"T1": TaskRunning, "T2": TaskRunning, "T3": TaskVerifying, "T4": TaskMerged, "T5": TaskFailed, "T6": TaskRunning, "T7": TaskRunning, "T8": TaskTodo, "T9": TaskTodo, "T10": TaskTodo}
	for id, w := range want {
		if got := taskByID(t, sn, id).State; got != w {
			t.Errorf("%s is %s, want %s", id, got, w)
		}
	}
	c := sn.Board.Counts
	if c != (TaskCounts{Todo: 3, Running: 4, Verifying: 1, Merged: 1, Failed: 1, Blocked: 1}) {
		t.Errorf("counts %+v", c)
	}
	t2 := taskByID(t, sn, "T2")
	if t2.Title != "Task number 2 [2J with an escape" || t2.Role != "backend" || strings.Join(t2.Deps, ",") != "T1" || strings.Join(t2.Files, ",") != "b/**,a/x.go" || t2.Owner != "w-2" || t2.Status != "doing" || t2.Rev != 12 {
		t.Errorf("T2 %+v", t2)
	}
	t3 := taskByID(t, sn, "T3")
	if t3.Result != "did it" || t3.Evidence != "edited 2 files; last test passed" || t3.Seq == 0 {
		t.Errorf("T3 %+v", t3)
	}
	if t7 := taskByID(t, sn, "T7"); t7.Line != "half way" || t7.Attempts != 1 {
		t.Errorf("T7 %+v", t7)
	}
	if sn.Board.Version != 17 {
		t.Errorf("version %d", sn.Board.Version)
	}
	// What the agents are bound to: the scope of the tasks they are doing.
	if a := agentOf(t, sn, "w-1"); strings.Join(a.Scope, ",") != "a/**" {
		t.Errorf("w-1 scope %v", a.Scope)
	}
	if a := agentOf(t, sn, "w-2"); strings.Join(a.Scope, ",") != "a/x.go,b/**" {
		t.Errorf("w-2 scope %v (sorted)", a.Scope)
	}
	if a := agentOf(t, sn, "w-3"); len(a.Scope) != 0 {
		t.Errorf("w-3 has nothing in progress: %v", a.Scope)
	}

	// An event that carries only the operands that changed (an older log) changes only those.
	apply(t, st, b.Emit("mgr", events.TypeBoardOp, map[string]any{"op": "update", "task": "T2", "line": "only the line", "version": 18}))
	t2 = taskByID(t, st.Snapshot(), "T2")
	if t2.Line != "only the line" || t2.Owner != "w-2" || t2.Status != "doing" || len(t2.Files) != 2 || t2.Title == "" {
		t.Errorf("a partial event must leave the rest alone: %+v", t2)
	}
	// A full event is the whole state: an empty owner or line is cleared.
	apply(t, st, op("requeue", "T2", "todo", "", 19, map[string]any{"attempts": 1, "line": ""}))
	t2 = taskByID(t, st.Snapshot(), "T2")
	if t2.Owner != "" || t2.Line != "" || t2.Status != "todo" || t2.State != TaskTodo || t2.Attempts != 1 {
		t.Errorf("requeued: %+v", t2)
	}
	if !feedHas(st.Snapshot(), FeedBoard, "", "T2 went back to the queue") {
		t.Errorf("feed: %s", js(st.Snapshot().Feed))
	}
	// RequeueOwned names the tasks it returned and carries no task of its own.
	apply(t, st, b.Emit("harness", events.TypeBoardOp, map[string]any{"op": "requeue", "tasks": []string{"T1", "T7", "T404"}, "line": "w-1 stopped", "version": 20}))
	sn = st.Snapshot()
	for _, id := range []string{"T1", "T7"} {
		if tk := taskByID(t, sn, id); tk.Status != "todo" || tk.Owner != "" || tk.Line != "w-1 stopped" {
			t.Errorf("%s %+v", id, tk)
		}
	}
	for _, tk := range sn.Board.Tasks {
		if tk.ID == "T404" {
			t.Error("a requeue names a task that was never seen: nothing is invented")
		}
	}
	// An operation on a task whose creation was not seen (a log that starts mid-session) creates it.
	apply(t, st, op("finish", "T77", "review", "w-9", 21, map[string]any{"result": "late"}))
	if tk := taskByID(t, st.Snapshot(), "T77"); tk.State != TaskVerifying || tk.Title != "" {
		t.Errorf("T77 %+v", tk)
	}
	// A status this package does not know is a todo, and an operation with no task at all is counted as bad.
	bad := st.Stats().Bad
	apply(t, st, b.Emit("mgr", events.TypeBoardOp, map[string]any{"op": "update", "task": "T8", "status": "frobnicated", "rev": 22, "version": 22}))
	apply(t, st, b.Emit("mgr", events.TypeBoardOp, map[string]any{"op": "update", "version": 23}))
	if tk := taskByID(t, st.Snapshot(), "T8"); tk.Status != "todo" || tk.State != TaskTodo {
		t.Errorf("T8 %+v", tk)
	}
	if st.Stats().Bad != bad+1 {
		t.Errorf("bad = %d, want %d", st.Stats().Bad, bad+1)
	}
}

func TestBoardAlertsNotesAndDroppedOps(t *testing.T) {
	b := newB()
	st := New()
	op := func(m map[string]any) { t.Helper(); apply(t, st, b.Emit("harness", events.TypeBoardOp, m)) }
	op(map[string]any{"op": "alert", "kind": "lease", "text": "w-1 wanted a file leased to w-2", "key": "a.go", "version": 1})
	op(map[string]any{"op": "alert", "kind": "scope", "text": "w-3 wrote outside its scope", "key": "scope:w-3", "version": 2})
	op(map[string]any{"op": "alert", "kind": "lease", "text": "same key, newer text", "key": "a.go", "version": 3})
	al := st.Snapshot().Board.Alerts
	if len(al) != 2 || al[1].Text != "same key, newer text" || al[0].Kind != "scope" {
		t.Errorf("an alert with the same key replaces the old one: %s", js(al))
	}
	op(map[string]any{"op": "alert-clear", "kind": "scope", "key": "scope:w-3", "version": 4})
	if al = st.Snapshot().Board.Alerts; len(al) != 1 || al[0].Key != "a.go" {
		t.Errorf("alerts %s", js(al))
	}
	for i := 0; i < 12; i++ {
		op(map[string]any{"op": "alert", "kind": "stuck", "text": fmt.Sprint("stuck ", i), "key": fmt.Sprint("k", i), "version": 10 + i})
	}
	if al = st.Snapshot().Board.Alerts; len(al) != MaxAlerts || al[len(al)-1].Text != "stuck 11" {
		t.Errorf("alerts are bounded to the newest %d: %d", MaxAlerts, len(al))
	}
	op(map[string]any{"op": "alert-expire", "dropped": 3, "version": 30})
	if al = st.Snapshot().Board.Alerts; len(al) != MaxAlerts-3 {
		t.Errorf("after expiry %d", len(al))
	}
	op(map[string]any{"op": "alert-clear", "kind": "stuck", "version": 31})
	if al = st.Snapshot().Board.Alerts; len(al) != 0 {
		t.Errorf("a clear by kind removes them all: %s", js(al))
	}

	op(map[string]any{"op": "note", "note": 1, "text": "a fact", "scope": "shared", "version": 40})
	op(map[string]any{"op": "note", "note": 2, "text": "another", "scope": "role", "role": "backend", "version": 41})
	op(map[string]any{"op": "note", "note": 3, "text": "third", "scope": "shared", "evicted": []int{1}, "version": 42})
	if n := st.Snapshot().Board.Notes; n != 2 {
		t.Errorf("notes %d", n)
	}
	op(map[string]any{"op": "notes-take", "notes": []int{2, 3, 99}, "version": 43})
	if n := st.Snapshot().Board.Notes; n != 0 {
		t.Errorf("notes %d", n)
	}
	for i := 1; i <= MaxNotes+10; i++ {
		op(map[string]any{"op": "note", "note": 100 + i, "text": "n", "version": 100 + i})
	}
	if n := st.Snapshot().Board.Notes; n != MaxNotes {
		t.Errorf("the set of pending notes is bounded: %d", n)
	}
	op(map[string]any{"op": "dropped", "n": 7, "version": 999})
	if d := st.Snapshot().Board.Dropped; d != 7 {
		t.Errorf("dropped %d", d)
	}
	// An agent the swarm removed from its roster is marked retired, and stays in the table.
	apply(t, st, b.Spawn("w-1", "backend", "T1", "mgr"))
	op(map[string]any{"op": "agent-remove", "agent": "w-1", "version": 1000})
	if a := agentOf(t, st.Snapshot(), "w-1"); !a.Retired {
		t.Error("retired")
	}
}

func TestTasksAreCappedAndTheLowestFinishedOneMakesRoom(t *testing.T) {
	b := newB()
	st := New()
	for i := 1; i <= MaxTasks; i++ {
		apply(t, st, b.Emit("mgr", events.TypeBoardOp, taskOp("create", fmt.Sprintf("T%d", i), "todo", "", uint64(i), map[string]any{"title": "x"})))
	}
	if n := len(st.Snapshot().Board.Tasks); n != MaxTasks {
		t.Fatalf("%d tasks", n)
	}
	// None is finished, so a new task is dropped.
	apply(t, st, b.Emit("mgr", events.TypeBoardOp, taskOp("create", "T5000", "todo", "", 5000, map[string]any{"title": "x"})))
	if n := len(st.Snapshot().Board.Tasks); n != MaxTasks || st.Stats().Dropped != 1 {
		t.Fatalf("%d tasks, %d dropped", n, st.Stats().Dropped)
	}
	// Two finish; the lowest-numbered finished one makes room.
	apply(t, st, b.Emit("mgr", events.TypeBoardOp, taskOp("finish", "T500", "done", "w", 6000, nil)))
	apply(t, st, b.Emit("mgr", events.TypeBoardOp, taskOp("finish", "T20", "failed", "w", 6001, nil)))
	apply(t, st, b.Emit("mgr", events.TypeBoardOp, taskOp("create", "T5001", "todo", "", 6002, map[string]any{"title": "new"})))
	sn := st.Snapshot()
	if n := len(sn.Board.Tasks); n != MaxTasks {
		t.Fatalf("%d tasks", n)
	}
	have := map[string]bool{}
	for _, tk := range sn.Board.Tasks {
		have[tk.ID] = true
	}
	if have["T20"] || !have["T500"] || !have["T5001"] {
		t.Errorf("T20 (finished, lowest) must have made room: T20=%v T500=%v T5001=%v", have["T20"], have["T500"], have["T5001"])
	}
}

func TestMergeQueueFollowsASubmissionFromQueuedToItsOutcome(t *testing.T) {
	b := newB()
	st := New()
	apply(t, st, b.Emit("", events.TypeSessionStart, startPayload(map[string]any{"isolation": "worktree"})))
	for i := 1; i <= 6; i++ {
		id := fmt.Sprintf("T%d", i)
		apply(t, st, b.Emit("mgr", events.TypeBoardOp, taskOp("create", id, "todo", "", uint64(i), map[string]any{"title": "work " + id})))
		apply(t, st, b.Emit("mgr", events.TypeBoardOp, taskOp("assign", id, "doing", fmt.Sprintf("w-%d", i), uint64(10+i), nil)))
	}
	ws := func(typ string, agent string, data map[string]any) events.Event { return b.Emit(agent, typ, data) }
	apply(t, st, ws(events.TypeWorkspaceCreate, "w-1", map[string]any{"path": "/t/w-1", "branch": "sleipnir/s/w-1", "base": "abc", "mode": "worktree"}))
	apply(t, st, ws(events.TypeWorkspaceCreate, "w-2", map[string]any{"path": "/t/w-2"}))
	apply(t, st, ws(events.TypeWorkspaceCommit, "w-1", map[string]any{"commit": "deadbeef"}))
	apply(t, st, ws(events.TypeWorkspaceRemove, "w-2", map[string]any{"path": "/t/w-2"}))
	if q := st.Snapshot().Merge; q.Trees != 1 || !q.Seen {
		t.Errorf("trees %d seen %v", q.Trees, q.Seen)
	}

	apply(t, st, ws(events.TypeMergeQueued, "w-1", map[string]any{"task": "T1: work T1", "position": 1}))
	apply(t, st, ws(events.TypeMergeQueued, "w-2", map[string]any{"task": "T2: work T2", "position": 2}))
	sn := st.Snapshot()
	if len(sn.Merge.Waiting) != 2 || sn.Merge.Waiting[0].Task != "T1" || sn.Merge.Waiting[0].Agent != "w-1" || sn.Merge.Waiting[1].Position != 2 || sn.Merge.Counts.Queued != 2 {
		t.Errorf("waiting %s", js(sn.Merge.Waiting))
	}
	if got := taskByID(t, sn, "T1").State; got != TaskVerifying {
		t.Errorf("a doing task whose submission waits in the queue is verifying, not %s", got)
	}

	// T1 merges, verified; the swarm records it, and the task goes to review: merged.
	apply(t, st, ws(events.TypeMergeMerged, "w-1", map[string]any{"task": "T1: work T1", "before": "b0", "after": "0123456789abcdef0123456789abcdef01234567", "commits": 1,
		"files": []string{"a.go", "b.go"}, "file_count": 2, "strategy": "merge", "verified": true}))
	apply(t, st, b.Emit("w-1", events.TypeTaskMerge, map[string]any{"task": "T1", "outcome": "merged", "commit": "0123456789abcdef0123456789abcdef01234567", "files": []string{"a.go", "b.go"}}))
	apply(t, st, b.Emit("w-1", events.TypeBoardOp, taskOp("finish", "T1", "review", "w-1", 30, map[string]any{"evidence": "merged into integration 0123456789ab"})))
	sn = st.Snapshot()
	if len(sn.Merge.Waiting) != 1 || sn.Merge.Waiting[0].Task != "T2" {
		t.Errorf("waiting %s", js(sn.Merge.Waiting))
	}
	r := sn.Merge.Recent
	if len(r) != 1 || r[0].Stage != MergeMerged || r[0].Commit != "0123456789ab" || !r[0].Verified || r[0].FileCount != 2 || strings.Join(r[0].Files, ",") != "a.go,b.go" || r[0].Position != 1 || r[0].Task != "T1" {
		t.Errorf("recent %s", js(r))
	}
	if tk := taskByID(t, sn, "T1"); tk.State != TaskMerged || tk.Merge != "merged" || tk.Commit != "0123456789ab" {
		t.Errorf("T1 %+v: in review, with its merge recorded, it is merged", tk)
	}
	// The same task in review without a merge is verifying (the manager has not accepted it).
	apply(t, st, b.Emit("w-3", events.TypeBoardOp, taskOp("finish", "T3", "review", "w-3", 31, nil)))
	if tk := taskByID(t, st.Snapshot(), "T3"); tk.State != TaskVerifying {
		t.Errorf("T3 %+v", tk)
	}

	// T2 conflicts and is sent back to its worker, still doing.
	apply(t, st, ws(events.TypeMergeConflict, "w-2", map[string]any{"task": "T2: work T2", "files": []string{"x.go", "y.go"}, "hunks": 3, "tip": "t", "theirs": "h"}))
	apply(t, st, b.Emit("w-2", events.TypeTaskMerge, map[string]any{"task": "T2", "outcome": "conflict", "files": []string{"x.go"}}))
	sn = st.Snapshot()
	r = sn.Merge.Recent
	if len(sn.Merge.Waiting) != 0 || len(r) != 2 || r[1].Stage != MergeConflict || r[1].Reason != "2 files, 3 hunks" || !r[1].Stage.Bounced() {
		t.Errorf("conflict %s", js(sn.Merge))
	}
	if tk := taskByID(t, sn, "T2"); tk.State != TaskRunning {
		t.Errorf("T2 is back to running, not %s", tk.State)
	}

	// T4: verification fails, the merge is rolled back.
	apply(t, st, ws(events.TypeMergeQueued, "w-4", map[string]any{"task": "T4: work T4", "position": 1}))
	apply(t, st, ws(events.TypeMergeVerifyFail, "w-4", map[string]any{"task": "T4: work T4", "cmd": "go test ./a", "exit_code": 1, "timed_out": false, "output": "FAIL"}))
	apply(t, st, ws(events.TypeMergeRolledBack, "w-4", map[string]any{"task": "T4: work T4", "tip": "t"}))
	// T5: refused. T6: nothing to merge.
	apply(t, st, ws(events.TypeMergeQueued, "w-5", map[string]any{"task": "T5: work T5", "position": 1}))
	apply(t, st, ws(events.TypeMergeRejected, "w-5", map[string]any{"task": "T5: work T5", "reason": "src/x.go is outside the scope of the task"}))
	apply(t, st, ws(events.TypeMergeQueued, "w-6", map[string]any{"task": "T6: work T6", "position": 1}))
	apply(t, st, ws(events.TypeMergeRejected, "w-6", map[string]any{"task": "T6: work T6", "reason": "the merge changed nothing", "empty": true}))
	apply(t, st, b.Emit("w-6", events.TypeTaskMerge, map[string]any{"task": "T6", "outcome": "empty"}))
	// A submission whose queued event was never seen, and one the queue could not run at all.
	apply(t, st, ws(events.TypeMergeRejected, "w-7", map[string]any{"task": "T7: orphan", "reason": "an oversized file"}))
	apply(t, st, b.Emit("w-8", events.TypeTaskMerge, map[string]any{"task": "T8", "outcome": "error", "error": "git: could not lock the index"}))
	apply(t, st, ws(events.TypeMergeFastFwd, "", map[string]any{"branch": "main", "from": "a", "to": "0123456789abcdef0123"}))
	apply(t, st, ws(events.TypeSwarmIntegration, "swarm", map[string]any{"branch": "sleipnir/s/integration", "tip": "0123456789abcdef", "applied": true, "files": []string{"a", "b", "c"}}))

	sn = st.Snapshot()
	m := sn.Merge
	wantCounts := MergeCounts{Queued: 5, Merged: 1, Empty: 1, Conflicts: 1, VerifyFail: 1, RolledBack: 1, Rejected: 2, Errors: 1, Bounced: 4}
	if m.Counts != wantCounts {
		t.Errorf("counts %+v, want %+v", m.Counts, wantCounts)
	}
	var stages []string
	for _, e := range m.Recent {
		stages = append(stages, string(e.Stage))
	}
	if strings.Join(stages, " ") != "merged conflict verify_failed rejected empty rejected error" {
		t.Errorf("stages %v", stages)
	}
	vf := m.Recent[2]
	if vf.ExitCode == nil || *vf.ExitCode != 1 || vf.Reason != "go test ./a (exit 1) · rolled back" || !vf.Stage.Bounced() {
		t.Errorf("verify failure %s", js(vf))
	}
	if m.Recent[5].Task != "T7" || m.Recent[6].Task != "T8" || m.Recent[6].Reason != "git: could not lock the index" {
		t.Errorf("orphans %s %s", js(m.Recent[5]), js(m.Recent[6]))
	}
	if tk := taskByID(t, sn, "T6"); tk.Merge != "empty" {
		t.Errorf("T6 %+v", tk)
	}
	if m.Integration == nil || !m.Integration.Applied || m.Integration.Files != 3 || m.Integration.Tip != "0123456789ab" || m.Integration.Branch != "sleipnir/s/integration" {
		t.Errorf("integration %s", js(m.Integration))
	}
	apply(t, st, ws(events.TypeSwarmIntegration, "swarm", map[string]any{"branch": "b", "tip": "zz", "applied": false, "reason": "you edited the same files"}))
	if in := st.Snapshot().Merge.Integration; in.Applied || in.Reason != "you edited the same files" || in.Tip != "" {
		t.Errorf("the newest integration report stands, and a tip that is not hex is not shown: %s", js(in))
	}
	for _, frag := range []string{"T1 merged (0123456789ab)", "conflicts with what was merged", "failed verification after the merge", "was refused by the merge queue", "had nothing to merge", "the merge could not run", "NOT applied"} {
		if !feedHas(st.Snapshot(), FeedMerge, "", frag) {
			t.Errorf("feed lacks %q:\n%s", frag, js(st.Snapshot().Feed))
		}
	}
}

func TestTaskRefReadsTheTaskIDOfASubmissionName(t *testing.T) {
	for in, want := range map[string]string{"T3: title": "T3", "T12": "T12", "T7 something": "T7", "T": "", "Tx": "", "T3x: no": "", "": "", "task 3": "", "T99999: big": "T99999"} {
		if got := taskRef(in); got != want {
			t.Errorf("taskRef(%q) = %q, want %q", in, got, want)
		}
	}
}
