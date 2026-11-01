package swarm

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

// The person stops the team: every running worker stops without an attempt counted, every open task is canceled, nothing is
// restarted, nothing new starts and the stop guard lets the manager answer. When the person writes again the board holds nothing
// the guard could send the manager back to, and work can start again.
func TestStopAllStopsTheWholeTeam(t *testing.T) {
	cfg := stallCfg()
	cfg.HoldManager, cfg.MaxWriters = true, 4
	r := newRVRig(t, cfg, gatedWorkers(nil)) // every request of a worker blocks until its run is cancelled
	r.sw.StartManager()
	a, ta := spawnWorker(t, r, "paginate users", 1)
	b, tb := spawnWorker(t, r, "rate limit the API", 1)
	todo, err := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "write the changelog"})
	if err != nil {
		t.Fatal(err)
	}
	m := r.sw.get(r.sw.ManagerID())
	h := &holdHooks{s: r.sw, m: m}
	if o := h.BeforeStop(context.Background(), "mgr", "manager", "done", false); !o.Veto {
		t.Fatal("setup: a manager that answers with work left is sent back to it")
	}

	rep := r.sw.StopAll("stop all your agents")
	if strings.Join(rep.Workers, ",") != a+","+b {
		t.Fatalf("stopped workers = %v, want %s and %s", rep.Workers, a, b)
	}
	if len(rep.Canceled) != 3 {
		t.Fatalf("canceled = %v, want %s, %s and %s", rep.Canceled, ta.ID, tb.ID, todo.ID)
	}
	if !strings.HasPrefix(rep.Line(), "team stopped: stopped 2 workers") || !strings.Contains(rep.Line(), "canceled 3 open tasks") {
		t.Fatalf("line = %q", rep.Line())
	}
	rvWait(t, "the workers to stop", func() bool { return r.idle(a) && r.idle(b) })
	for _, task := range r.sw.Board.Snapshot().Tasks {
		if task.Status != StatusFailed || task.Closure.Kind() != CloseCanceled || task.Attempts != 0 {
			t.Fatalf("%s after the stop: %s (%s), %d attempts", task.ID, task.Status, task.Closure.Kind(), task.Attempts)
		}
	}
	for _, id := range []string{a, b} {
		w := r.sw.get(id)
		w.mu.Lock()
		again := w.restart
		w.mu.Unlock()
		if again != nil {
			t.Fatalf("the harness means to restart %s after the person stopped the team", id)
		}
	}

	if !r.sw.Stopped() {
		t.Fatal("the team does not say it is stopped")
	}
	if o := h.BeforeStop(context.Background(), "mgr", "manager", "stopped", false); o.Veto {
		t.Fatalf("the stop guard sent the manager back after the person stopped the team: %s", o.Reason)
	}
	if _, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "one more", By: "mgr"}); !errors.Is(err, ErrStopped) {
		t.Fatalf("a spawn while stopped: %v", err)
	}
	if _, _, ok := r.sw.reserve(r.sw.get(a)); ok {
		t.Fatal("an idle worker could be started (by mail, a restart or a docs pass) while the team is stopped")
	}
	call := &tools.Call{Input: json.RawMessage(`{"action":"create","title":"more"}`), Env: &tools.Env{Agent: "mgr", Role: "manager", Perm: perm.AllowAll{}}}
	if res, _ := (&taskTool{s: r.sw}).Run(context.Background(), call); res == nil || !res.IsError {
		t.Fatalf("task create while stopped: %+v", res)
	}
	var stopped bool
	for _, e := range r.log.OfType(events.TypeSwarmStop) {
		var p struct{ Phase string }
		_ = json.Unmarshal(e.Data, &p)
		stopped = stopped || p.Phase == "stopped"
	}
	if !stopped {
		t.Fatal("no swarm.stop event")
	}

	if !r.sw.LiftStop() || r.sw.Stopped() {
		t.Fatal("the stop was not lifted")
	}
	if u := r.sw.unfinishedWork(); !u.empty() {
		t.Fatalf("after the stop the board still holds unfinished work: %s", u.summary(holdListCap))
	}
	if o := h.BeforeStop(context.Background(), "mgr", "manager", "done", false); o.Veto {
		t.Fatalf("the manager was sent back to work the person canceled: %s", o.Reason)
	}
	if _, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "new work", By: "mgr"}); err != nil {
		t.Fatalf("a spawn after the person wrote again: %v", err)
	}
}

// Canceled work is settled in a persistent team too: the stop guard counts a failed task as work to repair there, but not one the
// person's stop canceled.
func TestCanceledTasksAreNotUnfinishedInAPersistentTeam(t *testing.T) {
	cfg := stallCfg()
	cfg.PersistentPools = true
	r := newRVRig(t, cfg, gatedWorkers(nil))
	r.sw.StartManager()
	task, _ := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "port the importer"})
	r.sw.StopAll("")
	r.sw.LiftStop()
	if u := r.sw.unfinishedWork(); !u.empty() {
		t.Fatalf("%s canceled by the stop counts as unfinished: %s", task.ID, u.summary(holdListCap))
	}
}

// The stop ends the manager's running turn, as an interrupt does.
func TestStopAllEndsTheManagersTurn(t *testing.T) {
	r := newRVRig(t, stallCfg(), gatedWorkers(nil)) // the manager's request blocks as well
	done := make(chan error, 1)
	go func() {
		_, err := r.sw.RunManager(context.Background(), "build the API")
		done <- err
	}()
	rvWait(t, "the manager's request", func() bool { return r.prov.callsFor("mgr") >= 1 })
	if rep := r.sw.StopAll(""); !rep.Turn {
		t.Fatal("the report says the manager's turn was not ended")
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("the manager's turn ended with %v, want it cancelled", err)
	}
	// The person's next goal lifts the stop.
	go func() {
		_, err := r.sw.RunManager(context.Background(), "now only fix the README")
		done <- err
	}()
	rvWait(t, "the stop to be lifted", func() bool { return !r.sw.Stopped() })
	r.sw.StopAll("")
	<-done
}
