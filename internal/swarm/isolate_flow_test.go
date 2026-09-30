package swarm

// More isolation tests: conflicts, verifier failures, scope rejection, bounded
// bounces, what accept requires, what Finish does when it cannot apply, commit mode,
// snapshot bases, and the guards that keep every writer in its own tree.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/agent"
	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/perm"
)

// alertTexts lists the texts of the alerts raised so far (from the log: alerts expire).
func alertTexts(r *isoRig, kind string) []string {
	var out []string
	for _, e := range r.log.OfType(events.TypeBoardOp) {
		var d struct{ Op, Kind, Text string }
		_ = json.Unmarshal(e.Data, &d)
		if d.Op == "alert" && d.Kind == kind {
			out = append(out, d.Text)
		}
	}
	return out
}

// Two workers edit the same lines of one file. The second to finish is sent back with
// the conflict and the hunks, its tree holds the markers, and after it resolves them
// the merge lands and the user's checkout has the resolved file.
func TestSameFileConflictIsSentBackAndResolved(t *testing.T) {
	var r *isoRig
	sc := script{
		"be-1": {
			act(writeCall("shared.txt", "line one\nline two by be-1\nline three\n")),
			func(*rvCall) rvReply { // finish after fe-1 has written too
				poll(10*time.Second, func() bool { return r.cwd("fe-1") != "" })
				return rvReply{Tools: []rvToolCall{doneCall("T1", "changed line two")}}
			},
		},
		"fe-1": {
			act(writeCall("shared.txt", "line one\nline two by fe-1\nline three\n")),
			func(*rvCall) rvReply { // finish second: be-1's task is merged and in review
				poll(10*time.Second, func() bool { return r.task("T1").Status == StatusReview })
				return rvReply{Tools: []rvToolCall{doneCall("T2", "changed line two")}}
			},
			act(catCall("shared.txt")), // the conflict came back as the result of done
			act(writeCall("shared.txt", "line one\nline two by be-1 and fe-1\nline three\n")),
			act(doneCall("T2", "resolved together with be-1")),
		},
	}
	r = newIsoRig(t, isoOpts{cfg: Config{MaxWriters: 4}, verify: true}, sc.fn())
	r.sw.StartManager()
	r.mustSpawn("backend", "first", "shared.txt")
	// Overlapping scopes are legal in an isolated run: the merge queue settles them.
	r.mustSpawn("frontend", "second", "shared.txt")
	r.waitStatus("T1", StatusReview)
	r.waitStatus("T2", StatusReview)

	if got := r.q.Status().Conflicts; got != 1 {
		t.Fatalf("queue conflicts = %d, want 1", got)
	}
	for _, want := range []string{
		"your changes conflict with work that was already merged", // the bounce
		"ours is your version and theirs is the integrated one",   // how to read the markers
		"<<<<<<< ours", // the hunk
	} {
		if !r.prov.sawEver(want) {
			t.Fatalf("fe-1 was never told %q", want)
		}
	}
	if got := r.sawText("fe-1", "shared.txt"); !strings.Contains(got, "<<<<<<<") || !strings.Contains(got, "line two by be-1") || !strings.Contains(got, "line two by fe-1") {
		t.Fatalf("fe-1's tree should hold both versions in conflict markers after the bounce:\n%s", got)
	}
	// Advisory only: two writers on one file raised an alert that names the agents and
	// not the file, and nothing was refused.
	alerts := alertTexts(r, "lease")
	if len(alerts) == 0 || !strings.Contains(alerts[0], "be-1, fe-1 edit the same file in separate trees") || strings.Contains(alerts[0], "shared.txt") {
		t.Fatalf("lease alerts: %q", alerts)
	}
	for _, id := range []string{"T1", "T2"} {
		if res := r.accept(id); res.IsError {
			t.Fatalf("accept %s: %s", id, res.Text)
		}
	}
	rep := r.finish()
	if !rep.Applied {
		t.Fatalf("report %+v", rep)
	}
	if got := readText(t, r.repo+"/shared.txt"); got != "line one\nline two by be-1 and fe-1\nline three\n" {
		t.Fatalf("the user's checkout has %q", got)
	}
}

// The merged result fails the verifier although each tree passed on its own: the
// worker is sent back with the output, its tree holds the merged state, and after it
// fixes the problem the merge lands.
func TestMergedResultThatFailsTheVerifierIsSentBack(t *testing.T) {
	var r *isoRig
	sc := script{
		"be-1": {
			act(writeCall("part-x.txt", "x\n")),
			act(doneCall("T1", "added part x")),
		},
		"fe-1": {
			act(writeCall("part-y.txt", "y\n")),
			func(*rvCall) rvReply {
				poll(10*time.Second, func() bool { return r.task("T1").Status == StatusReview })
				return rvReply{Tools: []rvToolCall{doneCall("T2", "added part y")}}
			},
			// Sent back: its tree now holds both parts. Move its own to a notes file.
			act(rvToolCall{"rm", map[string]any{"path": "part-y.txt"}}, writeCall("notes-y.txt", "y\n")),
			act(catCall("part-x.txt")),
			act(doneCall("T2", "moved y to notes")),
		},
	}
	r = newIsoRig(t, isoOpts{cfg: Config{MaxWriters: 4}, verify: true, files: map[string]string{"limit.txt": "max=1\n"}}, sc.fn())
	r.sw.StartManager()
	r.mustSpawn("backend", "part x")
	r.mustSpawn("frontend", "part y")
	r.waitStatus("T1", StatusReview)
	r.waitStatus("T2", StatusReview)

	st := r.q.Status()
	if st.VerifyFailures != 1 || st.RolledBack != 1 || st.Merged != 2 {
		t.Fatalf("queue: %+v", st)
	}
	for _, want := range []string{"merged cleanly", "FAIL: 2 part files, at most 1 allowed", "merged result"} {
		if !r.prov.sawEver(want) {
			t.Fatalf("fe-1 was never told %q", want)
		}
	}
	// The failure reproduced in its own tree: it could see the other worker's merged part.
	if got := r.sawText("fe-1", "part-x.txt"); got != "x\n" {
		t.Fatalf("fe-1's tree should hold the merged state after the bounce, part-x.txt = %q", got)
	}
	for _, id := range []string{"T1", "T2"} {
		if res := r.accept(id); res.IsError {
			t.Fatalf("accept %s: %s", id, res.Text)
		}
	}
	rep := r.finish()
	if !rep.Applied {
		t.Fatalf("report %+v", rep)
	}
	for _, f := range []string{"part-x.txt", "notes-y.txt"} {
		if !fileExists(r.repo + "/" + f) {
			t.Errorf("%s is missing from the user's checkout", f)
		}
	}
	if fileExists(r.repo + "/part-y.txt") {
		t.Error("the failing change reached the user's checkout")
	}
}

// A submission that changed files outside its task's scope is refused by the queue and
// goes back to the worker with the reason. (The write guard already refuses such a
// write; a shell command is what gets around it, so this test uses one.)
func TestOutOfScopeSubmissionIsRejectedBackToTheWorker(t *testing.T) {
	sc := script{
		"be-1": {
			act(writeCall("a.txt", "alpha edited\n"), rvToolCall{"sneak", map[string]any{"path": "stray.txt", "content": "not mine\n"}}),
			act(doneCall("T1", "edited a")),
			act(rvToolCall{"sneakrm", map[string]any{"path": "stray.txt"}}),
			act(doneCall("T1", "edited a, cleaned up")),
		},
	}
	r := newIsoRig(t, isoOpts{cfg: Config{MaxWriters: 4}, verify: true}, sc.fn())
	r.sw.StartManager()
	r.mustSpawn("backend", "edit a", "a.txt")
	r.waitStatus("T1", StatusReview)
	if !r.prov.sawEver("outside its scope: stray.txt") {
		t.Fatal("the worker was not told which file left its scope")
	}
	if st := r.q.Status(); st.Rejected != 1 || st.Merged != 1 {
		t.Fatalf("queue: %+v", st)
	}
	if res := r.accept("T1"); res.IsError {
		t.Fatal(res.Text)
	}
	r.finish()
	if fileExists(r.repo + "/stray.txt") {
		t.Fatal("a file outside the scope reached the user's checkout")
	}
	if got := readText(t, r.repo+"/a.txt"); got != "alpha edited\n" {
		t.Fatalf("a.txt = %q", got)
	}
}

// A worker whose work keeps coming back is not bounced for ever: after MaxAttempts the
// task returns to the pool, the worker is stopped and the manager is told once.
func TestBouncesAreBoundedAndTheTaskReturnsToThePool(t *testing.T) {
	sc := script{
		"be-1": {
			act(writeCall("a.txt", "alpha edited\n")),
			act(doneCall("T1", "try 1")),
			act(doneCall("T1", "try 2")),
			act(doneCall("T1", "try 3")),
		},
	}
	always := func(dir string) (string, int) { return "FAIL: the merged result never passes", 1 }
	r := newIsoRig(t, isoOpts{cfg: Config{MaxWriters: 4, MaxAttempts: 2}, queueVerify: always, verify: false}, sc.fn())
	r.sw.StartManager()
	r.mustSpawn("backend", "edit a")
	rvWait(t, "the task to return to the pool", func() bool { return r.task("T1").Status == StatusTodo })
	tk := r.task("T1")
	if tk.Attempts != 1 || tk.Owner != "" {
		t.Fatalf("T1 = %+v, want todo with one attempt and no owner", tk)
	}
	rvWait(t, "the worker to stop", func() bool { return r.idle("be-1") })
	if n := r.q.Status().VerifyFailures; n != 2 {
		t.Fatalf("%d merge attempts, want 2", n)
	}
	if mailSent(r.rvRig, "returned to todo") == 0 || mailSent(r.rvRig, "its work could not be merged") == 0 {
		t.Fatal("the manager was not told the task returned to the pool, and why")
	}
	// The worker was stopped at once: the last bounce is its tool result, and no
	// request follows it.
	if n := r.prov.callsFor("be-1"); n != 3 {
		t.Fatalf("the worker made %d requests, want 3 (write, done, done)", n)
	}
}

// A task whose worker made no change has nothing to merge: it is reported as such and
// proceeds, and the manager can accept it.
func TestEmptyMergeIsReportedAndProceeds(t *testing.T) {
	sc := script{"be-1": {act(doneCall("T1", "there was nothing to do"))}}
	r := newIsoRig(t, isoOpts{verify: true}, sc.fn())
	r.sw.StartManager()
	r.mustSpawn("backend", "check something")
	r.waitStatus("T1", StatusReview)
	if ev := r.task("T1").Evidence; !strings.Contains(ev, "nothing to merge") {
		t.Fatalf("evidence %q", ev)
	}
	if res := r.accept("T1"); res.IsError {
		t.Fatal(res.Text)
	}
	rep := r.finish()
	if !rep.Applied || len(rep.Files) != 0 || !strings.Contains(rep.Message, "no changes") {
		t.Fatalf("report %+v", rep)
	}
}

// In an isolated run a task is accepted only when its work is in the integration
// branch: a merge that could not run is reported, and accept refuses.
func TestAcceptNeedsTheWorkInTheIntegrationBranch(t *testing.T) {
	sc := script{"be-1": {act(writeCall("a.txt", "alpha edited\n")), act(doneCall("T1", "edited a"))}}
	r := newIsoRig(t, isoOpts{verify: true}, sc.fn())
	if err := r.q.Close(context.Background()); err != nil { // the merge cannot run
		t.Fatal(err)
	}
	r.sw.StartManager()
	r.mustSpawn("backend", "edit a")
	r.waitStatus("T1", StatusReview)
	if !r.prov.sawEver("Not done yet: the merge could not run") {
		t.Fatal("the worker was not told the merge could not run")
	}
	if ev := r.task("T1").Evidence; !strings.Contains(ev, "NOT MERGED") {
		t.Fatalf("evidence %q must say the work is not merged", ev)
	}
	res := r.accept("T1")
	if !res.IsError || !strings.Contains(res.Text, "not in the integration branch") {
		t.Fatalf("accept of unmerged work: %+v", res)
	}
	if r.task("T1").Status != StatusReview {
		t.Fatalf("T1 = %s after a refused accept", r.task("T1").Status)
	}
}

// Every writer is confined to its own tree, and the manager does not edit files in an
// isolated run: anything written into the shared checkout would bypass the merge queue.
func TestWritesOutsideTheOwnTreeAreRefused(t *testing.T) {
	var r *isoRig
	sc := script{
		"be-1": {
			func(*rvCall) rvReply {
				return rvReply{Tools: []rvToolCall{writeCall(r.repo+"/a.txt", "written into the shared checkout\n")}}
			},
		},
	}
	r = newIsoRig(t, isoOpts{verify: true}, sc.fn())
	r.sw.StartManager()
	r.mustSpawn("backend", "edit a")
	rvWait(t, "the worker to try", func() bool { return r.prov.sawEver("outside your working tree") })
	if got := readText(t, r.repo+"/a.txt"); got != "alpha\n" {
		t.Fatalf("a write reached the shared checkout: %q", got)
	}
	err := r.sw.Leases.BeforeWrite("mgr", r.repo+"/a.txt")
	if err == nil || !strings.Contains(err.Error(), "spawn a worker") {
		t.Fatalf("the manager's write in an isolated run: %v", err)
	}
	// And through the permission wrapper, whatever the engine would say.
	rr := roleRequester{inner: perm.AllowAll{}, role: BuiltinRoles()["manager"], denyWrites: isolatedManagerMsg, strictShell: true}
	for _, q := range []perm.Request{
		{Tool: "write", Writes: true, Paths: []string{r.repo + "/a.txt"}},
		{Tool: "bash", Command: "echo hi > a.txt", Writes: true},
		{Tool: "bash", Command: "git checkout main -- a.txt && rm -rf x", Writes: true},
	} {
		if d := rr.Check(context.Background(), q); d.Allow || !strings.Contains(d.Reason, "spawn a worker") {
			t.Errorf("manager request %+v: %+v", q, d)
		}
	}
	if d := rr.Check(context.Background(), perm.Request{Tool: "bash", Command: "git log --oneline", Writes: true}); !d.Allow {
		t.Errorf("the manager may still inspect: %+v", d)
	}
	if d := rr.Check(context.Background(), perm.Request{Tool: "read", Paths: []string{r.repo + "/a.txt"}}); !d.Allow {
		t.Errorf("the manager may still read: %+v", d)
	}
}

// When the user edited the same file while the swarm worked, the result cannot be
// applied: nothing is changed, the branch is kept, and the report says how to get it.
func TestFinishKeepsTheBranchWhenTheCheckoutChangedMeanwhile(t *testing.T) {
	sc := script{"be-1": {act(writeCall("a.txt", "alpha edited by the worker\n")), act(doneCall("T1", "edited a"))}}
	r := newIsoRig(t, isoOpts{verify: true}, sc.fn())
	r.sw.StartManager()
	r.mustSpawn("backend", "edit a")
	r.waitStatus("T1", StatusReview)
	if res := r.accept("T1"); res.IsError {
		t.Fatal(res.Text)
	}
	// The user gets to a.txt first.
	if err := writeFileString(r.repo+"/a.txt", "alpha edited by the user\n"); err != nil {
		t.Fatal(err)
	}
	rep := r.finish()
	if rep.Applied || !rep.BranchKept {
		t.Fatalf("the result must not be applied over the user's edit: %+v", rep)
	}
	if got := readText(t, r.repo+"/a.txt"); got != "alpha edited by the user\n" {
		t.Fatalf("the user's file was touched: %q", got)
	}
	if !strings.HasPrefix(rep.Hint, "git diff --binary ") || !strings.HasSuffix(rep.Hint, "| git apply --3way") || !strings.Contains(rep.Hint, rep.Branch) {
		t.Fatalf("hint %q", rep.Hint)
	}
	if !strings.Contains(rep.Message, "NOT applied") || !strings.Contains(rep.Message, rep.Branch) {
		t.Fatalf("message %q", rep.Message)
	}
	// The branch really holds the verified result, and nothing else is left over.
	if b := gitOut(t, r.repo, "branch", "--list", "sleipnir/*"); !strings.Contains(b, "_integration") {
		t.Fatalf("the integration branch was not kept:\n%s", b)
	}
	if d := gitOut(t, r.repo, "diff", "--binary", rep.Base, rep.Branch); !strings.Contains(d, "alpha edited by the worker") {
		t.Fatalf("the kept branch does not hold the result:\n%s", d)
	}
	if ents := dirEntries(r.trees); len(ents) != 0 {
		t.Fatalf("trees left behind: %v", ents)
	}
}

// When the manager stops the person is told about files that arrived and about a
// failure to apply, not about a check that found nothing to do, and a failure that
// persists is told once. Finish then accounts for the whole run, not for its last step.
func TestApplyMergedTellsWhatIsNewAndAFailureOnce(t *testing.T) {
	sink := &noticeSink{}
	sc := script{
		"be-1": {act(writeCall("a.txt", "alpha edited\n")), act(doneCall("T1", "edited a"))},
		"fe-1": {act(writeCall("b.txt", "bravo edited\n")), act(doneCall("T2", "edited b"))},
	}
	r := newIsoRig(t, isoOpts{verify: true, tweak: func(d *Deps) { d.NewSink = func(string) agent.Sink { return sink } }}, sc.fn())
	r.sw.StartManager()
	mgr := r.sw.get(r.sw.ManagerID())
	count := func(sub string) int {
		n := 0
		for _, l := range sink.all() {
			if strings.Contains(l, sub) {
				n++
			}
		}
		return n
	}

	r.mustSpawn("backend", "edit a", "a.txt")
	r.waitStatus("T1", StatusReview)
	// Nothing had been applied yet; the user edits the file meanwhile, so it cannot be.
	if err := writeFileString(r.repo+"/a.txt", "alpha edited by the user\n"); err != nil {
		t.Fatal(err)
	}
	r.sw.applyMerged(mgr)
	r.sw.applyMerged(mgr)
	if n := count("warn: The result was NOT applied"); n != 1 {
		t.Fatalf("a failure that persists must be told once, was told %d times: %v", n, sink.all())
	}
	// The user gives the file back; what is waiting now arrives, and only once.
	if err := writeFileString(r.repo+"/a.txt", "alpha\n"); err != nil {
		t.Fatal(err)
	}
	r.sw.applyMerged(mgr)
	r.sw.applyMerged(mgr)
	if n := count("integrate: Applied 1 file(s) to your working tree as uncommitted changes: a.txt."); n != 1 {
		t.Fatalf("the arrival of a.txt was told %d times: %v", n, sink.all())
	}
	if got := readText(t, r.repo+"/a.txt"); got != "alpha edited\n" {
		t.Fatalf("a.txt = %q", got)
	}
	// More work merges later; the notice names only what is new.
	r.mustSpawn("frontend", "edit b", "b.txt")
	r.waitStatus("T2", StatusReview)
	r.sw.applyMerged(mgr)
	if n := count("integrate: Applied 1 file(s) to your working tree as uncommitted changes: b.txt."); n != 1 {
		t.Fatalf("the second application must name b.txt alone: %v", sink.all())
	}
	rep := r.finish()
	if !rep.Applied || strings.Join(rep.Files, ",") != "a.txt,b.txt" || !strings.Contains(rep.Message, "Applied 2 file(s)") {
		t.Fatalf("the account of the whole run: %+v", rep)
	}
}

// Commit mode moves the user's branch to the integration tip when the checkout is
// clean, and refuses (keeping the branch, with a merge command) when it is not.
func TestCommitModeFastForwardsACleanCheckoutAndRefusesADirtyOne(t *testing.T) {
	for _, dirty := range []bool{false, true} {
		sc := script{"be-1": {act(writeCall("a.txt", "alpha committed by the swarm\n")), act(doneCall("T1", "edited a"))}}
		r := newIsoRig(t, isoOpts{verify: true, commit: true}, sc.fn())
		r.sw.StartManager()
		r.mustSpawn("backend", "edit a")
		r.waitStatus("T1", StatusReview)
		if res := r.accept("T1"); res.IsError {
			t.Fatal(res.Text)
		}
		if dirty {
			if err := writeFileString(r.repo+"/scratch.txt", "the user is working\n"); err != nil {
				t.Fatal(err)
			}
		}
		rep := r.finish()
		if dirty {
			if rep.Applied || rep.Hint != "git merge "+rep.Branch {
				t.Fatalf("dirty checkout: %+v", rep)
			}
			if !strings.Contains(gitOut(t, r.repo, "branch", "--list", "sleipnir/*"), "_integration") {
				t.Fatal("the integration branch must be kept")
			}
			if got := readText(t, r.repo+"/a.txt"); got != "alpha\n" {
				t.Fatalf("a.txt = %q", got)
			}
			continue
		}
		if !rep.Applied || !rep.Committed {
			t.Fatalf("clean checkout: %+v", rep)
		}
		if got := readText(t, r.repo+"/a.txt"); got != "alpha committed by the swarm\n" {
			t.Fatalf("a.txt = %q", got)
		}
		if st := gitOut(t, r.repo, "status", "--porcelain"); st != "" {
			t.Fatalf("commit mode must leave a clean checkout, got:\n%s", st)
		}
		if log := gitOut(t, r.repo, "log", "--format=%an: %s", "-3"); !strings.Contains(log, "be-1: T1: edit a") {
			t.Fatalf("the agent's commit is not in history:\n%s", log)
		}
		if b := gitOut(t, r.repo, "branch", "--list", "sleipnir/*"); b != "" {
			t.Fatalf("branches left behind after a successful fast-forward:\n%s", b)
		}
	}
}

// With a snapshot base (what a session uses for patch mode) the user's uncommitted
// edits are part of what the workers start from, and the result applies on top of them.
func TestSnapshotBaseIncludesTheUsersUncommittedEdits(t *testing.T) {
	sc := script{"be-1": {act(catCall("c.txt")), act(writeCall("a.txt", "alpha by the worker\n")), act(doneCall("T1", "edited a"))}}
	r := newIsoRig(t, isoOpts{verify: true, snapshot: true, dirty: map[string]string{"c.txt": "charlie by the user\n"}}, sc.fn())
	r.sw.StartManager()
	r.mustSpawn("backend", "edit a", "a.txt")
	r.waitStatus("T1", StatusReview)
	if got := r.sawText("be-1", "c.txt"); got != "charlie by the user\n" {
		t.Fatalf("the worker's tree should start from what the user sees, c.txt = %q", got)
	}
	if res := r.accept("T1"); res.IsError {
		t.Fatal(res.Text)
	}
	rep := r.finish()
	if !rep.Applied {
		t.Fatalf("report %+v", rep)
	}
	if got := readText(t, r.repo+"/a.txt"); got != "alpha by the worker\n" {
		t.Fatalf("a.txt = %q", got)
	}
	if got := readText(t, r.repo+"/c.txt"); got != "charlie by the user\n" {
		t.Fatalf("the user's own edit was lost: %q", got)
	}
	if len(rep.Files) != 1 || rep.Files[0] != "a.txt" {
		t.Fatalf("the patch must hold only what the agents changed: %v", rep.Files)
	}
	if b := gitOut(t, r.repo, "branch", "--list", "sleipnir/*"); b != "" {
		t.Fatalf("the base snapshot branch was left behind:\n%s", b)
	}
}

// A worker started after work was merged starts from it; a reused worker is brought up
// to date when it is given its new task.
func TestNewAndReusedWorkersStartFromMergedWork(t *testing.T) {
	sc := script{
		"be-1": {
			act(writeCall("a.txt", "alpha by be-1\n")),
			act(doneCall("T1", "edited a")),
			fixed("summary"),
			// reused for T3 after fe-1's b.txt was merged:
			act(catCall("b.txt")),
			act(doneCall("T3", "read b")),
		},
		"fe-1": {act(catCall("a.txt")), act(writeCall("b.txt", "bravo by fe-1\n")), act(doneCall("T2", "edited b"))},
	}
	r := newIsoRig(t, isoOpts{verify: true}, sc.fn())
	r.sw.StartManager()
	r.mustSpawn("backend", "edit a", "a.txt")
	r.waitStatus("T1", StatusReview)
	if res := r.accept("T1"); res.IsError {
		t.Fatal(res.Text)
	}
	rvWait(t, "be-1 to be idle", func() bool { return r.idle("be-1") })
	// A worker started now begins at the integration tip.
	r.mustSpawn("frontend", "edit b", "b.txt")
	r.waitStatus("T2", StatusReview)
	if got := r.sawText("fe-1", "a.txt"); got != "alpha by be-1\n" {
		t.Fatalf("a worker started after the merge must see merged work, a.txt = %q", got)
	}
	if res := r.accept("T2"); res.IsError {
		t.Fatal(res.Text)
	}
	// Reuse be-1 (its tree predates b.txt).
	tk, err := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "read b", Role: "backend"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.sw.Spawn(SpawnReq{Role: "backend", TaskID: tk.ID, Agent: "be-1", By: "mgr"}); err != nil {
		t.Fatal(err)
	}
	r.waitStatus("T3", StatusReview)
	if got := r.sawText("be-1", "b.txt"); got != "bravo by fe-1\n" {
		t.Fatalf("a reused worker must be brought up to date, b.txt = %q", got)
	}
}

func fixed(text string) func(*rvCall) rvReply {
	return func(*rvCall) rvReply { return rvReply{Text: text} }
}

// A blocked worker is brought up to date when its task is resumed: it can say what it
// is waiting for (block), the manager resumes it once that has been merged, and the
// worker is told and finds the work in its tree.
func TestResumeBringsTheBlockedWorkersTreeUpToDate(t *testing.T) {
	sc := script{
		"be-1": {
			act(writeCall("a.txt", "alpha in progress\n"), rvToolCall{"task", map[string]any{"action": "block", "id": "T1", "text": "need b.txt from the frontend"}}),
			fixed("blocked, waiting for b.txt"),
			act(catCall("b.txt")), // after the manager's resume
			act(doneCall("T1", "finished with b in hand")),
		},
		"fe-1": {act(writeCall("b.txt", "bravo by fe-1\n")), act(doneCall("T2", "edited b"))},
	}
	r := newIsoRig(t, isoOpts{verify: true}, sc.fn())
	r.sw.StartManager()
	r.mustSpawn("backend", "edit a", "a.txt")
	r.waitStatus("T1", StatusBlocked)
	rvWait(t, "be-1 to be idle", func() bool { return r.idle("be-1") })
	r.mustSpawn("frontend", "edit b", "b.txt")
	r.waitStatus("T2", StatusReview)
	res := r.callTool(context.Background(), "task", "mgr", "manager", map[string]any{"action": "resume", "id": "T1"})
	if res.IsError || !strings.Contains(res.Text, "brought up to date") {
		t.Fatalf("resume: %+v", res)
	}
	r.waitStatus("T1", StatusReview)
	if got := r.sawText("be-1", "b.txt"); got != "bravo by fe-1\n" {
		t.Fatalf("the resumed worker's tree must hold the merged work, b.txt = %q", got)
	}
	if !r.prov.sawEver("your tree now includes the work merged so far") {
		t.Fatal("the worker was not told")
	}
}

// Isolation changes no byte a worker sends before its own tool results: the tool list
// and the system prompt are the same for every agent, the layers do not depend on the
// tree, and nothing in any prompt names a tree or the shared checkout.
func TestPromptBytesDoNotDependOnTheTreePath(t *testing.T) {
	sc := script{
		"be-1": {act(writeCall("a.txt", "alpha by be-1\n")), act(doneCall("T1", "edited a"))},
		"be-2": {act(writeCall("b.txt", "bravo by be-2\n")), act(doneCall("T2", "edited b"))},
	}
	r := newIsoRig(t, isoOpts{verify: true, cfg: Config{MaxWriters: 4}}, sc.fn())
	r.sw.StartManager()
	r.mustSpawn("backend", "edit a", "a.txt")
	r.mustSpawn("backend", "edit b", "b.txt")
	r.waitStatus("T1", StatusReview)
	r.waitStatus("T2", StatusReview)
	if r.cwd("be-1") == r.cwd("be-2") || r.cwd("be-1") == "" {
		t.Fatalf("setup: the workers must have different trees: %q %q", r.cwd("be-1"), r.cwd("be-2"))
	}
	r.prov.mu.Lock()
	calls := append([]*rvCall(nil), r.prov.calls...)
	r.prov.mu.Unlock()
	if len(calls) < 4 {
		t.Fatalf("only %d requests recorded", len(calls))
	}
	var tools0, system0 string
	for i, c := range calls {
		b, _ := json.Marshal(c.Prompt)
		text := string(b)
		for _, path := range []string{r.trees, r.repo, r.cwd("be-1"), r.cwd("be-2")} {
			if path != "" && strings.Contains(text, path) {
				t.Fatalf("request %d (%s) contains the path %s", i, c.Agent, path)
			}
		}
		tj, _ := json.Marshal(c.Prompt.Tools)
		sj, _ := json.Marshal(c.Prompt.System)
		if i == 0 {
			tools0, system0 = string(tj), string(sj)
		}
		if string(tj) != tools0 || string(sj) != system0 {
			t.Fatalf("request %d (%s) sends a different tool list or system prompt", i, c.Agent)
		}
	}
	a1, a2 := r.sw.get("be-1").a.Stack(), r.sw.get("be-2").a.Stack()
	if a1.Const.Hash() != a2.Const.Hash() || a1.Shared.Hash() != a2.Shared.Hash() || a1.RoleL.Hash() != a2.RoleL.Hash() {
		t.Fatal("the shared and role layers of two workers differ")
	}
	// The private notes carry the isolation paragraph, and it names no path.
	notes := a1.Notes.Text()
	if !strings.Contains(notes, "Isolation: your working directory is a private git worktree") {
		t.Fatalf("notes lack the isolation paragraph:\n%s", notes)
	}
	if strings.Contains(notes, r.trees) || strings.Contains(notes, r.repo) {
		t.Fatalf("notes name a path:\n%s", notes)
	}
}

// The lease guard in isolated mode never blocks a write, keeps each writer in its own
// tree, matches scopes against paths inside that tree, and only advises about overlap.
func TestIsolatedLeasesNeverBlockButConfineAndAdvise(t *testing.T) {
	b := NewBoard(nil)
	l := NewLeases(time.Minute, b)
	l.Isolate()
	l.BindTree("be-1", "/trees/be-1")
	l.BindTree("be-2", "/trees/be-2")
	task, _ := b.CreateTask("mgr", TaskSpec{Title: "scoped", Files: []string{"src/**"}})
	if err := b.Assign("mgr", "be-1", task.ID); err != nil {
		t.Fatal(err)
	}
	if err := l.BeforeWrite("be-1", "/trees/be-1/src/x.go"); err != nil {
		t.Fatalf("a write inside the tree and the scope was refused: %v", err)
	}
	// The same repository path in another tree: never blocked, advisory alert.
	if err := l.BeforeWrite("be-2", "/trees/be-2/src/x.go"); err != nil {
		t.Fatalf("a lease blocked a write in an isolated tree: %v", err)
	}
	al := b.Snapshot().Alerts
	if len(al) != 1 || al[0].Kind != "lease" || al[0].Text != "be-1, be-2 edit the same file in separate trees; expect a merge conflict" {
		t.Fatalf("alerts: %+v", al)
	}
	// Scope is judged on the path inside the writer's own tree.
	err := l.BeforeWrite("be-1", "/trees/be-1/docs/a.md")
	if err == nil || !strings.Contains(err.Error(), "outside the scope of your task") {
		t.Fatalf("scope: %v", err)
	}
	for _, p := range []string{"/trees/be-2/src/x.go", "/repo/src/x.go", "/trees/be-1/../be-2/src/x.go"} {
		err := l.BeforeWrite("be-1", p)
		if err == nil || !strings.Contains(err.Error(), "outside your working tree") {
			t.Fatalf("a write to %s from be-1's tree: %v", p, err)
		}
	}
	if err := l.BeforeWrite("mgr", "/repo/src/x.go"); err == nil || !strings.Contains(err.Error(), "spawn a worker") {
		t.Fatalf("the manager: %v", err)
	}
	// Releasing the writer ends the warning (the scope alert about be-1 is another one).
	l.ReleaseAll("be-2")
	for _, a := range b.Snapshot().Alerts {
		if a.Kind == "lease" {
			t.Fatalf("the overlap alert outlived the second writer: %+v", a)
		}
	}
	l.UnbindTree("be-1")
	if err := l.BeforeWrite("be-1", "/trees/be-1/src/x.go"); err == nil {
		t.Fatal("an agent whose tree was unbound may not write")
	}
}

// The refusal of a write outside the tree names the path the model itself gave, never
// the writer's own tree.
func TestOutsideTreeRefusalDoesNotNameTheAgentsOwnTree(t *testing.T) {
	l := NewLeases(time.Minute, NewBoard(nil))
	l.Isolate()
	l.BindTree("be-1", "/state/trees/s1/be-1")
	err := l.BeforeWrite("be-1", "/state/trees/s1/be-2/src/x.go")
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if strings.Contains(err.Error(), "/state/trees/s1/be-1") {
		t.Fatalf("the refusal names the agent's own tree: %v", err)
	}
}
