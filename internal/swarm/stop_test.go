package swarm

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
)

// harnessMailTo returns the texts of the harness's mail to an agent, in order.
func harnessMailTo(r *rvRig, to string) []string {
	var out []string
	for _, e := range r.log.OfType(events.TypeMailSend) {
		var m struct{ From, To, Text string }
		_ = json.Unmarshal(e.Data, &m)
		if m.From == harnessSender && m.To == to {
			out = append(out, m.Text)
		}
	}
	return out
}

// stopEvents counts the swarm.stall events with action stop.
func stopEvents(r *rvRig) int {
	n := 0
	for _, e := range stallEvents(r) {
		if e.Action == "stop" {
			n++
		}
	}
	return n
}

// The person stops a worker whose run never ends: the run is cancelled, its task goes back to todo with the attempt counted, the
// harness does not restart the worker on it, and the manager is told. The manager, the harness's own agents, an unknown id and a
// worker that is not running are refused.
func TestStopAgentStopsAWorkerThatNeverFinishes(t *testing.T) {
	r := newRVRig(t, stallCfg(), gatedWorkers(nil)) // every request of a worker blocks until the test ends
	r.sw.StartManager()
	id, task := spawnWorker(t, r, "paginate users", 1)

	if _, err := r.sw.StopAgent("mgr"); err == nil || !strings.Contains(err.Error(), "manager") {
		t.Fatalf("stopping the manager: %v", err)
	}
	if _, err := r.sw.StopAgent("be-99"); err == nil {
		t.Fatal("stopped an agent that does not exist")
	}
	tasks, err := r.sw.StopAgent(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0] != task.ID {
		t.Fatalf("tasks = %v, want [%s]", tasks, task.ID)
	}
	rvWait(t, id+" to stop", func() bool { return r.idle(id) })
	got, _ := r.sw.Board.Snapshot().Task(task.ID)
	if got.Status != StatusTodo || got.Attempts != 1 {
		t.Fatalf("task after the stop: status %s, attempts %d", got.Status, got.Attempts)
	}
	m := r.sw.get(id)
	m.mu.Lock()
	again := m.restart
	m.mu.Unlock()
	if again != nil {
		t.Fatal("the harness scheduled a restart of a worker the person stopped")
	}
	told := strings.Join(harnessMailTo(r, "mgr"), "\n")
	if !strings.Contains(told, "stopped by the person") || !strings.Contains(told, task.ID) {
		t.Fatalf("the manager was not told: %q", told)
	}
	if _, err := r.sw.StopAgent(id); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("stopping an idle worker: %v", err)
	}
	if n := len(r.log.OfType("agent.stop")); n != 1 {
		t.Fatalf("%d agent.stop events", n)
	}
}

// A worker that holds one assignment past TaskTimeout is a task_overrun finding whatever it does meanwhile: it is told, the manager
// gets the diagnosis ten minutes later, and when the finding still holds StallStop after that the harness stops the worker itself.
func TestATaskThatRunsPastItsBoundIsStoppedByTheHarness(t *testing.T) {
	clock := newFakeClock()
	cfg := stallCfg()
	cfg.StallTurns = 100 // the turn-based kinds stay quiet: only the clock decides here
	cfg.TaskTimeout, cfg.StallStop = 30*time.Minute, 20*time.Minute
	r := newRVRigWith(t, cfg, gatedWorkers(nil), func(d *Deps) { d.Now = clock.Now })
	r.sw.StartManager()
	id, task := spawnWorker(t, r, "build the importer", 1)

	clock.Advance(29 * time.Minute)
	r.sw.sweepStalls()
	if _, ok := active(r, StallTaskOverrun); ok {
		t.Fatal("a task within its bound was found overrun")
	}
	clock.Advance(2 * time.Minute)
	r.sw.sweepStalls()
	f, ok := active(r, StallTaskOverrun)
	if !ok || f.Task != task.ID || f.Agent != id || f.Notify != id {
		t.Fatalf("finding = %+v, %v; stalls %+v", f, ok, r.sw.Stalls())
	}
	if !nudged(r, id) {
		t.Fatal("the worker was not told")
	}
	clock.Advance(stallEscalateAfter + time.Second)
	r.sw.sweepStalls()
	d := diagnoses(r)
	if len(d) != 1 || !strings.Contains(d[0], "(task_overrun)") || !strings.Contains(d[0], "the harness stops "+id) {
		t.Fatalf("diagnoses = %q", d)
	}
	if stopEvents(r) != 0 || r.idle(id) {
		t.Fatal("the worker was stopped when the finding was escalated")
	}
	clock.Advance(cfg.StallStop - 2*time.Second)
	r.sw.sweepStalls()
	if stopEvents(r) != 0 {
		t.Fatal("the worker was stopped before StallStop had passed")
	}
	clock.Advance(2 * time.Second)
	r.sw.sweepStalls()
	r.sw.sweepStalls() // once: the finding stops its worker one time
	if n := stopEvents(r); n != 1 {
		t.Fatalf("%d stop events", n)
	}
	rvWait(t, id+" to stop", func() bool { return r.idle(id) })
	got, _ := r.sw.Board.Snapshot().Task(task.ID)
	if got.Status != StatusTodo || got.Attempts != 1 {
		t.Fatalf("task after the stop: status %s, attempts %d", got.Status, got.Attempts)
	}
	r.sw.sweepStalls()
	if _, ok := active(r, StallTaskOverrun); ok {
		t.Fatal("the finding outlived the run it was about")
	}
	told := strings.Join(harnessMailTo(r, "mgr"), "\n")
	if !strings.Contains(told, "stopped by the harness: task_overrun on "+task.ID) {
		t.Fatalf("the manager was not told why: %q", told)
	}
}

// With StallStop negative the harness escalates but never stops a worker, and with TaskTimeout negative there is no task_overrun.
func TestStallStopAndTaskTimeoutCanBeTurnedOff(t *testing.T) {
	clock := newFakeClock()
	cfg := stallCfg()
	cfg.StallTurns = 100
	cfg.TaskTimeout, cfg.StallStop = time.Minute, -1
	r := newRVRigWith(t, cfg, gatedWorkers(nil), func(d *Deps) { d.Now = clock.Now })
	r.sw.StartManager()
	id, _ := spawnWorker(t, r, "build the importer", 1)
	clock.Advance(2 * time.Minute)
	r.sw.sweepStalls()
	clock.Advance(stallEscalateAfter + time.Second)
	r.sw.sweepStalls()
	clock.Advance(10 * time.Hour)
	r.sw.sweepStalls()
	if stopEvents(r) != 0 || r.idle(id) {
		t.Fatal("the harness stopped a worker with StallStop off")
	}
	if d := diagnoses(r); len(d) != 1 || strings.Contains(d[0], "the harness stops") {
		t.Fatalf("diagnoses = %q", d)
	}
	r.sw.cfg.TaskTimeout = -1
	r.sw.sweepStalls()
	if _, ok := active(r, StallTaskOverrun); ok {
		t.Fatal("task_overrun held with TaskTimeout off")
	}
}
