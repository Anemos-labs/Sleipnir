package swarm

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/provider"
)

// A worker whose run ended on an outage of its model's endpoint is not restarted on that model while a member of its role runs on
// another: its task goes back on the board. Nor is it given work again while a member of its role on another model is free: the manager's spawn naming it is refused with the free member to use, and a spawn by role starts that member.
// In a long team run one provider had no endpoints for hours; the manager gave the work back to the workers on that model, and
// each new run failed minutes later while the members on another model sat idle.
func TestAssignmentsSteerAwayFromAWorkerWhoseModelIsOut(t *testing.T) {
	clock := newFakeClock()
	r := newClockRig(t, Config{PersistentPools: true, MaxWorkers: 2, Team: []string{"backend", "backend"}}, clock, func(_ context.Context, c *rvCall) rvReply {
		if c.Agent == "be-1" {
			return rvReply{Err: &provider.Error{Kind: provider.ErrServer, Status: 503, Message: "No endpoints are currently available"}}
		}
		return rvReply{Text: "ok"}
	})
	r.sw.StartManager()
	t1, err := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "build the API", Role: "backend", Files: []string{"api/**"}})
	if err != nil {
		t.Fatal(err)
	}
	r.sw.spawnMu.Lock()
	if _, err := r.sw.ensurePool("backend"); err != nil {
		t.Fatal(err)
	}
	r.sw.spawnMu.Unlock()
	r.sw.get("be-2").model.ID = "other/model" // the second member runs on another model
	if _, err := r.sw.Spawn(SpawnReq{Role: "backend", Agent: "be-1", TaskID: t1.ID, Solo: true, By: "mgr"}); err != nil {
		t.Fatal(err)
	}
	rvWait(t, "be-1's run to fail", func() bool { _, out := r.sw.inOutage(r.sw.get("be-1")); return r.idle("be-1") && out })
	if p := pendingOf(r, "be-1"); p != nil {
		t.Fatal("be-1 is restarted on the model that failed while be-2 runs on another")
	}
	if tk, _ := r.sw.Board.Snapshot().Task(t1.ID); tk.Status != StatusTodo || tk.Owner != "" {
		t.Fatalf("%s = %s/%s after be-1's run ended on an outage, want back on the board", t1.ID, tk.Status, tk.Owner)
	}
	// It is the manager's to assign: the repair does not start be-1 on it again, now or once outageSteer has passed.
	r.sw.repairTick(clock.Now())
	if tk, _ := r.sw.Board.Snapshot().Task(t1.ID); tk.Status != StatusTodo || tk.Owner != "" {
		t.Fatalf("%s = %s/%s after a repair tick, want left for the manager", t1.ID, tk.Status, tk.Owner)
	}
	r.sw.repairTick(clock.Now().Add(outageSteer + time.Minute))
	if tk, _ := r.sw.Board.Snapshot().Task(t1.ID); tk.Status != StatusTodo || tk.Owner != "" {
		t.Fatalf("%s = %s/%s after a repair tick past outageSteer, want left for the manager", t1.ID, tk.Status, tk.Owner)
	}

	out := r.callTool(context.Background(), "spawn", "mgr", "manager", map[string]any{"agent": "be-1", "task": t1.ID})
	if !out.IsError || !strings.Contains(out.Text, "be-1's last run ended") || !strings.Contains(out.Text, "spawn agent=be-2") {
		t.Fatalf("the spawn naming be-1 = %q, want refused with be-2 named", out.Text)
	}
	if tk, _ := r.sw.Board.Snapshot().Task(t1.ID); tk.Status != StatusTodo || tk.Owner != "" {
		t.Fatalf("%s = %s/%s after the refused spawn", t1.ID, tk.Status, tk.Owner)
	}
	if res, err := r.sw.spawn(SpawnReq{Role: "backend", TaskID: t1.ID, Solo: true, By: "mgr"}); err != nil || res.Agent != "be-2" {
		t.Fatalf("a spawn by role started %q (%v), want be-2", res.Agent, err)
	}

	// Past outageSteer the worker is the manager's to choose again.
	clock.Advance(outageSteer)
	if _, out := r.sw.inOutage(r.sw.get("be-1")); out {
		t.Fatal("be-1 is still steered away from after outageSteer")
	}
}
