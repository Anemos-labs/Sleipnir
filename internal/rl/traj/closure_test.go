package traj

import (
	"reflect"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/swarm"
)

// The episode outcome counts the typed closure reasons the swarm board recorded, by
// each task's final state: what a reward can use where free-text results say nothing.
func TestOutcomeCountsBoardClosureReasons(t *testing.T) {
	log := events.NewMemLog()
	b := swarm.NewBoard(log)
	mk := func(title string) swarm.Task {
		tk, err := b.CreateTask("mgr", swarm.TaskSpec{Title: title})
		if err != nil {
			t.Fatal(err)
		}
		return tk
	}
	t1, t2, t3 := mk("api"), mk("old api"), mk("docs")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(b.Assign("mgr", "be-1", t1.ID))
	cur, _ := b.Snapshot().Task(t1.ID)
	_, err := b.Handover("mgr", t1.ID, cur.Rev, "be-1", "be-2", nil)
	must(err)
	must(b.Submit("be-2", t1.ID, "done", "edited api.go"))
	must(b.Accept("mgr", t1.ID, ""))
	superseded, err := swarm.NewClosure(swarm.CloseSuperseded, t1.ID)
	must(err)
	must(b.Fail("mgr", t2.ID, superseded, "replaced by the new api"))
	canceled, err := swarm.NewClosure(swarm.CloseCanceled, "")
	must(err)
	must(b.Fail("mgr", t3.ID, canceled, "not needed"))
	must(b.Reopen("mgr", t3.ID)) // reopened: its earlier closure no longer counts

	r := newRun(log.All(), nil)
	bl := &builder{run: r, v: r.buildView(), agents: map[string]*agentInfo{}}
	got := bl.outcome().Closures
	want := map[string]int{"done:verified": 1, "failed:superseded": 1, "handed_off": 1}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("closures = %v, want %v", got, want)
	}
}
