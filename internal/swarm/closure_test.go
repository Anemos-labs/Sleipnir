package swarm

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/events"
)

// A closure reason that needs a target cannot be made without one, a reason that
// takes none cannot carry one, and the decoder is held to the same rules.
func TestClosureReasonsCarryTheirTargets(t *testing.T) {
	for _, tc := range []struct {
		kind   ClosureKind
		target string
		ok     bool
	}{
		{CloseVerified, "", true},
		{CloseVerified, "T1", false},
		{CloseBlockedOn, "", false},
		{CloseBlockedOn, "be-1", false},
		{CloseBlockedOn, "T3", true},
		{CloseSuperseded, "T12", true},
		{CloseSuperseded, " ", false},
		{CloseCanceled, "", true},
		{CloseDenied, "T1", false},
		{CloseHandedOff, "", false},
		{CloseHandedOff, "be-2", true},
		{CloseHandedOff, "be 2", false},
		{"retired", "", false},
	} {
		c, err := NewClosure(tc.kind, tc.target)
		if (err == nil) != tc.ok {
			t.Errorf("NewClosure(%q, %q) = %v, %v", tc.kind, tc.target, c, err)
			continue
		}
		if !tc.ok {
			if !c.IsZero() {
				t.Errorf("a refused closure is not zero: %+v", c)
			}
			continue
		}
		raw, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		var back Closure
		if err := json.Unmarshal(raw, &back); err != nil || back != c {
			t.Errorf("round trip of %s: %s -> %+v, %v", c, raw, back, err)
		}
	}
	var c Closure
	for _, bad := range []string{`{"kind":"blocked_on"}`, `{"kind":"canceled","target":"T1"}`, `{"kind":"nope"}`} {
		if err := json.Unmarshal([]byte(bad), &c); err == nil {
			t.Errorf("decoded an ill-formed closure %s as %+v", bad, c)
		}
	}
	if raw, _ := json.Marshal(Closure{}); string(raw) != "null" {
		t.Errorf("zero closure marshals as %s", raw)
	}
	if s := mustClose(t, CloseSuperseded, "T5").String(); s != "superseded(T5)" {
		t.Errorf("String = %q", s)
	}
}

func mustClose(t *testing.T, k ClosureKind, target string) Closure {
	t.Helper()
	c, err := NewClosure(k, target)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// Every way a task closes records a typed reason, the board keeps a closure exactly on
// done and failed tasks, and the log rebuilds it.
func TestEveryClosureIsTypedAndReplayed(t *testing.T) {
	log := events.NewMemLog()
	b := NewBoard(log)
	mk := func(spec TaskSpec) Task {
		tk, err := b.CreateTask("mgr", spec)
		if err != nil {
			t.Fatal(err)
		}
		return tk
	}
	impl, plan, old, flaky, broken, waiting := mk(TaskSpec{Title: "api"}), mk(TaskSpec{Title: "contract", Kind: TaskKindPlan}),
		mk(TaskSpec{Title: "old api"}), mk(TaskSpec{Title: "flaky"}), mk(TaskSpec{Title: "broken"}), mk(TaskSpec{Title: "waits"})

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(b.Assign("mgr", "be-1", impl.ID))
	must(b.Submit("be-1", impl.ID, "done", "edited api.go"))
	must(b.Accept("mgr", impl.ID, ""))
	must(b.Assign("mgr", "sc-1", plan.ID))
	must(b.submitAgreementAt("sc-1", plan.ID, 0, "contract", "planning", "GET /users returns pages"))
	must(b.Accept("mgr", plan.ID, ""))

	// A reason that does not fail a task, a target that does not exist and a task
	// that would supersede itself are refused, and change nothing.
	v := b.Snapshot().Version
	if err := b.Fail("mgr", old.ID, closeAs(CloseVerified), ""); err == nil {
		t.Error("verified accepted as a failure reason")
	}
	if err := b.Fail("mgr", old.ID, mustClose(t, CloseSuperseded, "T99"), ""); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("superseded by a missing task: %v", err)
	}
	if err := b.Fail("mgr", old.ID, mustClose(t, CloseSuperseded, old.ID), ""); err == nil {
		t.Error("a task superseded itself")
	}
	if err := b.Fail("mgr", old.ID, Closure{}, ""); err == nil {
		t.Error("a failure without a reason")
	}
	if err := b.Finish("mgr", old.ID, StatusFailed, "no reason"); err == nil {
		t.Error("Finish failed a task without a reason")
	}
	if b.Snapshot().Version != v {
		t.Fatal("a refused closure changed the board")
	}
	must(b.Fail("mgr", old.ID, mustClose(t, CloseSuperseded, impl.ID), "replaced"))

	// Harness closures: attempts exhausted, verification exhausted.
	must(b.Assign("mgr", "be-2", flaky.ID))
	if nt, ok := b.Requeue("be-2", flaky.ID, 0, "stopped", true, 1); !ok || nt.Status != StatusFailed {
		t.Fatalf("requeue: %+v %v", nt, ok)
	}
	must(b.Assign("mgr", "be-3", broken.ID))
	bt, _ := b.Snapshot().Task(broken.ID)
	if nt, ok := b.FailVerification("be-3", broken.ID, bt.Rev, "tests fail", "exit 1", 1, 1); !ok || nt.Status != StatusFailed {
		t.Fatalf("verification: %+v %v", nt, ok)
	}

	// Blocked on another task; resuming forgets it.
	must(b.Assign("mgr", "be-4", waiting.ID))
	if err := b.BlockOn("be-4", waiting.ID, waiting.ID, "self"); err == nil {
		t.Error("a task blocked on itself")
	}
	must(b.BlockOn("be-4", waiting.ID, impl.ID, "needs the api"))
	if wt, _ := b.Snapshot().Task(waiting.ID); wt.BlockedOn != impl.ID {
		t.Fatalf("blocked_on = %q", wt.BlockedOn)
	}

	snap := b.Snapshot()
	want := map[string]string{impl.ID: "verified", plan.ID: "agreed", old.ID: "superseded(T1)", flaky.ID: "exhausted", broken.ID: "verifier", waiting.ID: ""}
	for _, tk := range snap.Tasks {
		if got := tk.Closure.String(); got != want[tk.ID] {
			t.Errorf("%s (%s): closure %q, want %q", tk.ID, tk.Status, got, want[tk.ID])
		}
		if (tk.Status == StatusDone || tk.Status == StatusFailed) != !tk.Closure.IsZero() || (!tk.Closure.IsZero() && tk.Closure.Status() != tk.Status) {
			t.Errorf("%s: status %s with closure %q", tk.ID, tk.Status, tk.Closure)
		}
	}
	got, err := ReplayBoard(log.All())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Tasks, snap.Tasks) {
		t.Errorf("replay differs:\n replay: %+v\n live:   %+v", got.Tasks, snap.Tasks)
	}

	// Reopening forgets the closure; resuming forgets what it was blocked on.
	must(b.Reopen("mgr", old.ID))
	must(b.Resume("be-4", waiting.ID))
	if ot, _ := b.Snapshot().Task(old.ID); !ot.Closure.IsZero() {
		t.Errorf("reopened task keeps %s", ot.Closure)
	}
	if wt, _ := b.Snapshot().Task(waiting.ID); wt.BlockedOn != "" {
		t.Errorf("resumed task still blocked on %s", wt.BlockedOn)
	}
	got, err = ReplayBoard(log.All())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Tasks, b.Snapshot().Tasks) {
		t.Errorf("replay after reopen differs:\n replay: %+v\n live:   %+v", got.Tasks, b.Snapshot().Tasks)
	}
}

// The board tool refuses a failure without a valid reason, says which reasons there
// are and what they need, and leaves the board as it was; a valid one is recorded in
// the board event and shown on the board.
func TestTaskFailNeedsATypedReason(t *testing.T) {
	r := newRVRig(t, Config{}, func(ctx context.Context, c *rvCall) rvReply { return rvReply{Text: "ok"} })
	b := r.sw.Board
	t1, _ := b.CreateTask("mgr", TaskSpec{Title: "new api"})
	t2, _ := b.CreateTask("mgr", TaskSpec{Title: "old api"})
	ctx := context.Background()
	v := b.Snapshot().Version
	for _, in := range []map[string]any{
		{"action": "fail", "id": t2.ID, "text": "replaced"},
		{"action": "fail", "id": t2.ID, "reason": "superseded", "text": "replaced"},
		{"action": "fail", "id": t2.ID, "reason": "exhausted"},
		{"action": "fail", "id": t2.ID, "reason": "canceled", "target": t1.ID},
	} {
		res := r.callTool(ctx, "task", "mgr", "manager", in)
		if !res.IsError {
			t.Fatalf("%v was accepted: %s", in, res.Text)
		}
		if in["reason"] == nil {
			for _, w := range []string{"reason", "blocked_on", "superseded", "canceled", "denied", "verifier", "target"} {
				if !strings.Contains(res.Text, w) {
					t.Errorf("the refusal does not say %q: %s", w, res.Text)
				}
			}
		}
	}
	if b.Snapshot().Version != v {
		t.Fatal("a refused fail changed the board")
	}
	res := r.callTool(ctx, "task", "mgr", "manager", map[string]any{"action": "fail", "id": t2.ID, "reason": "superseded", "target": t1.ID, "text": "replaced"})
	if res.IsError || !strings.Contains(res.Text, "superseded(T1)") {
		t.Fatalf("fail: %s", res.Text)
	}
	var seen bool
	for _, e := range r.log.OfType(events.TypeBoardOp) {
		var d struct {
			Task    string
			Status  string
			Closure struct{ Kind, Target string }
		}
		_ = json.Unmarshal(e.Data, &d)
		if d.Task == t2.ID && d.Status == "failed" {
			seen = d.Closure.Kind == "superseded" && d.Closure.Target == t1.ID
		}
	}
	if !seen {
		t.Error("the board event does not carry the closure")
	}
	if list := r.callTool(ctx, "task", "mgr", "manager", map[string]any{"action": "list"}).Text; !strings.Contains(list, "T2 failed:superseded(T1)") {
		t.Errorf("the board does not show the closure:\n%s", list)
	}
}
