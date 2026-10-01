package inspect

import (
	"reflect"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/swarm"
)

// The swarm logs the full state of a task on every board.op, and the inspector
// must show what the live board held. This drives the real board through a
// typical life (create, assign, progress, submit with the harness's evidence,
// send back, accept, fail, requeue) and compares the two.
func TestInspectorShowsWhatTheLiveBoardHeld(t *testing.T) {
	dir := t.TempDir()
	log, err := events.Open(dir, "contract")
	if err != nil {
		t.Fatal(err)
	}
	log.Emit("", events.TypeSessionStart, map[string]any{"swarm": true})
	b := swarm.NewBoard(log)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	mk := func(spec swarm.TaskSpec) swarm.Task {
		t.Helper()
		tk, err := b.CreateTask("mgr", spec)
		if err != nil {
			t.Fatal(err)
		}
		return tk
	}
	t1 := mk(swarm.TaskSpec{Title: "Add the retry helper", Desc: "with jitter", Role: "backend", Files: []string{"internal/client/**"}})
	t2 := mk(swarm.TaskSpec{Title: "Cover it with tests", Role: "tester", Deps: []string{t1.ID}})
	t3 := mk(swarm.TaskSpec{Title: "Update the docs"})
	t4 := mk(swarm.TaskSpec{Title: "Flaky one"})

	_, err = b.Assign("mgr", "be-1", t1.ID), error(nil)
	must(err)
	must(b.Update("be-1", t1.ID, "reading the client"))
	must(b.Submit("be-1", t1.ID, "retry helper added", "edited internal/client/retry.go; go test exit 0"))
	if _, err := b.SendBack("mgr", t1.ID, "also handle context cancellation"); err != nil {
		t.Fatal(err)
	}
	must(b.Submit("be-1", t1.ID, "cancellation handled", "edited internal/client/retry.go; go test exit 0"))
	must(b.Accept("mgr", t1.ID, "good"))

	must(b.Assign("mgr", "te-1", t2.ID))
	must(b.Block("te-1", t2.ID, "waiting for the fixture"))

	must(b.Assign("mgr", "dw-1", t3.ID))
	must(b.Fail("mgr", t3.ID, "the docs move to another repository"))

	must(b.Assign("mgr", "be-2", t4.ID))
	live := b.Snapshot()
	if tk, _ := live.Task(t4.ID); tk.Owner != "be-2" {
		t.Fatalf("setup: %+v", tk)
	}
	if _, ok := b.Requeue("be-2", t4.ID, 0, "worker stopped: budget", true, 3); !ok {
		t.Fatal("setup: requeue did not apply")
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}

	want := b.Snapshot()
	sess := mustLoad(t, dir)
	rep := sess.Swarm()
	if got := rep.Tasksrc; got != "replayed from the board.op events, which carry the full task state" {
		t.Errorf("tasks source = %q", got)
	}
	if len(rep.Tasks) != len(want.Tasks) {
		t.Fatalf("the inspector shows %d tasks, the board held %d", len(rep.Tasks), len(want.Tasks))
	}
	for i, w := range want.Tasks {
		g := rep.Tasks[i]
		type view struct {
			ID, Title, Status, Owner, Role, Line, Result, Evidence string
			Attempts                                               int
			Deps, Files                                            []string
		}
		wv := view{w.ID, w.Title, string(w.Status), w.Owner, w.Role, w.Line, w.Result, w.Evidence, w.Attempts, w.Deps, w.Files}
		gv := view{g.ID, g.Title, g.Status, g.Owner, g.Role, g.Line, g.Result, g.Evidence, g.Attempts, g.Deps, g.Files}
		if len(wv.Deps) == 0 {
			wv.Deps = nil
		}
		if len(gv.Deps) == 0 {
			gv.Deps = nil
		}
		if len(wv.Files) == 0 {
			wv.Files = nil
		}
		if len(gv.Files) == 0 {
			gv.Files = nil
		}
		if !reflect.DeepEqual(wv, gv) {
			t.Errorf("task %s:\n  board     %+v\n  inspector %+v", w.ID, wv, gv)
		}
	}
	// The harness's evidence and the requeue are visible.
	if tk := rep.Tasks[0]; tk.Evidence == "" || tk.Status != "done" {
		t.Errorf("T1: %+v", tk)
	}
	if tk := rep.Tasks[3]; tk.Status != "todo" || tk.Owner != "" || tk.Attempts != 1 {
		t.Errorf("T4 after a requeue: %+v", tk)
	}
}

// A worker that goes away takes its tasks back to the queue in one operation; the
// event names the tasks, and the inspector must not keep showing them as owned.
func TestInspectorFollowsARequeueOfEveryTaskAWorkerOwned(t *testing.T) {
	dir := t.TempDir()
	log, err := events.Open(dir, "requeue")
	if err != nil {
		t.Fatal(err)
	}
	b := swarm.NewBoard(log)
	a, _ := b.CreateTask("mgr", swarm.TaskSpec{Title: "one"})
	c, _ := b.CreateTask("mgr", swarm.TaskSpec{Title: "two"})
	for _, id := range []string{a.ID, c.ID} {
		if err := b.Assign("mgr", "w1", id); err != nil {
			t.Fatal(err)
		}
	}
	if got := b.RequeueOwned("w1", "worker stopped"); len(got) != 2 {
		t.Fatalf("setup: requeued %d", len(got))
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	for _, tk := range mustLoad(t, dir).Swarm().Tasks {
		if tk.Status != "todo" || tk.Owner != "" || tk.Line != "worker stopped" {
			t.Errorf("%s after the requeue: %+v", tk.ID, tk)
		}
	}
}
