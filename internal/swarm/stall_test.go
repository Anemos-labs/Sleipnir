package swarm

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
)

// Seeded-regression tests for the stall sweep: each named stall is seeded and must be
// found (once, with its nudge), must clear when its condition ends, and must not be
// found in the healthy version of the same situation. Sweeps are run by hand
// (SuperviseEvery is an hour), so what a sweep sees is what the test set up.

// stallCfg turns the turn-based kinds on at three turns and the supervisor off.
func stallCfg() Config { return Config{StallTurns: 3, SuperviseEvery: time.Hour, StuckAfter: -1} }

// stallEvent is a decoded swarm.stall event.
type stallEv struct{ Action, Kind, Task, Agent, Notify, Detail string }

// stallEvents returns the swarm.stall events of the log, in order.
func stallEvents(r *rvRig) []stallEv {
	var out []stallEv
	for _, e := range r.log.OfType(events.TypeSwarmStall) {
		var d stallEv
		_ = json.Unmarshal(e.Data, &d)
		out = append(out, d)
	}
	return out
}

// raised counts the raise events of a kind.
func raised(r *rvRig, kind StallKind) int {
	n := 0
	for _, e := range stallEvents(r) {
		if e.Action == "raise" && e.Kind == string(kind) {
			n++
		}
	}
	return n
}

// active reports whether a finding of the kind holds now.
func active(r *rvRig, kind StallKind) (Stall, bool) {
	for _, f := range r.sw.Stalls() {
		if f.Kind == kind {
			return f, true
		}
	}
	return Stall{}, false
}

// nudged reports whether the harness mailed the agent a stall check.
func nudged(r *rvRig, to string) bool {
	for _, e := range r.log.OfType(events.TypeMailSend) {
		var m struct{ From, To, Text string }
		_ = json.Unmarshal(e.Data, &m)
		if m.From == harnessSender && m.To == to && strings.HasPrefix(m.Text, "Stall check:") {
			return true
		}
	}
	return false
}

// gatedWorkers scripts workers: each request of an agent gets the next reply from its
// script; past the end of it the request blocks until the test ends.
func gatedWorkers(scripts map[string][]rvReply) func(ctx context.Context, c *rvCall) rvReply {
	return func(ctx context.Context, c *rvCall) rvReply {
		if s, ok := scripts[c.Agent]; ok && c.Assistants < len(s) {
			return s[c.Assistants]
		}
		<-ctx.Done()
		return rvReply{}
	}
}

func repeat(n int, r rvReply) []rvReply {
	out := make([]rvReply, n)
	for i := range out {
		out[i] = r
	}
	return out
}

var (
	readReply = rvReply{Tools: []rvToolCall{{Name: "read", Args: map[string]any{"path": "api/users.go"}}}}
	editReply = rvReply{Tools: []rvToolCall{{Name: "edit", Args: map[string]any{"path": "api/users.go"}}}}
)

// spawnWorker starts a backend worker on a new task and waits until it has made want
// requests (the last of which blocks).
func spawnWorker(t *testing.T, r *rvRig, title string, want int) (string, Task) {
	t.Helper()
	id, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: title, By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	rvWait(t, id+" to reach its blocked request", func() bool { return r.prov.callsFor(id) >= want })
	task, _ := r.sw.Board.Snapshot().Task(currentTask(r.sw.Board.Snapshot(), id))
	return id, task
}

func TestStallClaimedNoProgress(t *testing.T) {
	t.Run("seeded: a worker that only reads", func(t *testing.T) {
		r := newRVRig(t, stallCfg(), gatedWorkers(map[string][]rvReply{"be-1": repeat(3, readReply)}))
		r.sw.StartManager()
		id, task := spawnWorker(t, r, "paginate users", 4)
		r.sw.sweepStalls()
		r.sw.sweepStalls() // idempotent: the finding is raised once
		f, ok := active(r, StallClaimedNoProgress)
		if !ok || f.Task != task.ID || f.Agent != id || f.Notify != id {
			t.Fatalf("finding = %+v, %v; stalls %+v", f, ok, r.sw.Stalls())
		}
		if n := raised(r, StallClaimedNoProgress); n != 1 {
			t.Fatalf("raised %d times", n)
		}
		if !nudged(r, id) {
			t.Error("the worker was not told")
		}
		if r.sw.StallCounts()[StallClaimedNoProgress] != 1 {
			t.Errorf("counts = %v", r.sw.StallCounts())
		}
		// The condition ends when the worker stops running: the finding clears itself.
		r.sw.stopRun(r.sw.get(id), "test over", false)
		rvWait(t, "the worker to stop", func() bool { return r.idle(id) })
		r.sw.sweepStalls()
		if _, ok := active(r, StallClaimedNoProgress); ok {
			t.Fatal("the finding outlived its condition")
		}
		evs := stallEvents(r)
		if last := evs[len(evs)-1]; last.Action != "clear" || last.Kind != string(StallClaimedNoProgress) {
			t.Fatalf("events = %+v", evs)
		}
	})
	t.Run("healthy: a worker that edits", func(t *testing.T) {
		r := newRVRig(t, stallCfg(), gatedWorkers(map[string][]rvReply{"be-1": {readReply, editReply, readReply, editReply}}))
		r.sw.StartManager()
		spawnWorker(t, r, "paginate users", 5)
		r.sw.sweepStalls()
		if n := raised(r, StallClaimedNoProgress); n != 0 {
			t.Fatalf("a worker that makes progress was found stalled: %+v", stallEvents(r))
		}
	})
}

func TestStallManagerWaitingOnIdle(t *testing.T) {
	t.Run("seeded: the manager waits while nobody works", func(t *testing.T) {
		r := newRVRig(t, stallCfg(), gatedWorkers(nil))
		r.sw.StartManager()
		task, _ := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "paginate users"})
		ctx := context.Background()
		if res := r.callTool(ctx, "wait", "mgr", "manager", map[string]any{"until": []string{task.ID}}); !res.IsError {
			t.Fatalf("wait: %s", res.Text)
		}
		f, ok := active(r, StallManagerWaitingOnIdle)
		if !ok || f.Agent != "mgr" || !strings.Contains(f.Detail, task.ID) {
			t.Fatalf("finding = %+v, %v", f, ok)
		}
		r.callTool(ctx, "wait", "mgr", "manager", map[string]any{"until": []string{task.ID}}) // still the same stall
		r.sw.sweepStalls()
		if n := raised(r, StallManagerWaitingOnIdle); n != 1 {
			t.Fatalf("raised %d times", n)
		}
		// The manager acts: the condition ends.
		r.callTool(ctx, "task", "mgr", "manager", map[string]any{"action": "list"})
		r.sw.sweepStalls()
		if _, ok := active(r, StallManagerWaitingOnIdle); ok {
			t.Fatal("the finding outlived its condition")
		}
	})
	t.Run("healthy: the manager waits while a worker works", func(t *testing.T) {
		r := newRVRig(t, stallCfg(), gatedWorkers(nil))
		r.sw.StartManager()
		_, task := spawnWorker(t, r, "paginate users", 1)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		res := r.callTool(ctx, "wait", "mgr", "manager", map[string]any{"until": []string{task.ID}, "timeout_sec": 3})
		if res.IsError {
			t.Fatalf("wait: %s", res.Text)
		}
		r.sw.sweepStalls()
		if n := raised(r, StallManagerWaitingOnIdle); n != 0 {
			t.Fatalf("a manager waiting on a running worker was found stalled: %+v", stallEvents(r))
		}
	})
}

func TestStallOrphanedTask(t *testing.T) {
	t.Run("seeded: a task owned by an agent that is not on the team", func(t *testing.T) {
		r := newRVRig(t, stallCfg(), gatedWorkers(nil))
		r.sw.StartManager()
		task, _ := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "paginate users"})
		if err := r.sw.Board.Assign("mgr", "be-9", task.ID); err != nil {
			t.Fatal(err)
		}
		r.sw.sweepStalls()
		if _, ok := active(r, StallOrphanedTask); ok {
			t.Fatal("raised on the first sight: a spawn in progress looks the same")
		}
		r.sw.sweepStalls()
		f, ok := active(r, StallOrphanedTask)
		if !ok || f.Task != task.ID || f.Agent != "be-9" || f.Notify != "mgr" {
			t.Fatalf("finding = %+v, %v", f, ok)
		}
		if !nudged(r, "mgr") {
			t.Error("the manager was not told")
		}
		if err := r.sw.Board.Fail("mgr", task.ID, closeAs(CloseCanceled), "nobody owns it"); err != nil {
			t.Fatal(err)
		}
		r.sw.sweepStalls()
		if _, ok := active(r, StallOrphanedTask); ok {
			t.Fatal("the finding outlived its condition")
		}
	})
	t.Run("healthy: a task owned by a running worker", func(t *testing.T) {
		r := newRVRig(t, stallCfg(), gatedWorkers(nil))
		r.sw.StartManager()
		spawnWorker(t, r, "paginate users", 1)
		r.sw.sweepStalls()
		r.sw.sweepStalls()
		if n := raised(r, StallOrphanedTask); n != 0 {
			t.Fatalf("found an orphan: %+v", stallEvents(r))
		}
	})
}

func TestStallBlockedCycle(t *testing.T) {
	setup := func(t *testing.T) (*rvRig, []Task) {
		r := newRVRig(t, stallCfg(), gatedWorkers(nil))
		r.sw.StartManager()
		var tasks []Task
		for _, title := range []string{"api", "client", "docs"} {
			_, task := spawnWorker(t, r, title, 1)
			tasks = append(tasks, task)
		}
		return r, tasks
	}
	t.Run("seeded: tasks blocked on each other", func(t *testing.T) {
		r, ts := setup(t)
		b := r.sw.Board
		must := func(err error) {
			t.Helper()
			if err != nil {
				t.Fatal(err)
			}
		}
		// T3 waits for T2, T2 for T1, T1 for T2: one cycle (T1, T2) with a tail.
		must(b.BlockOn(ts[2].Owner, ts[2].ID, ts[1].ID, "needs the client"))
		must(b.BlockOn(ts[1].Owner, ts[1].ID, ts[0].ID, "needs the api"))
		must(b.BlockOn(ts[0].Owner, ts[0].ID, ts[1].ID, "needs the client"))
		r.sw.sweepStalls()
		r.sw.sweepStalls()
		var cycles []Stall
		for _, f := range r.sw.Stalls() {
			if f.Kind == StallBlockedCycle {
				cycles = append(cycles, f)
			}
		}
		if len(cycles) != 1 || cycles[0].Task != "T1" || cycles[0].Detail != "blocked on each other: T1 -> T2 -> T1" || cycles[0].Notify != "mgr" {
			t.Fatalf("cycles = %+v", cycles)
		}
		if raised(r, StallBlockedCycle) != 1 || !nudged(r, "mgr") {
			t.Fatalf("events %+v", stallEvents(r))
		}
		must(b.Unblock("mgr", ts[1].ID))
		r.sw.sweepStalls()
		if _, ok := active(r, StallBlockedCycle); ok {
			t.Fatal("the finding outlived its condition")
		}
	})
	t.Run("healthy: a chain that ends in work", func(t *testing.T) {
		r, ts := setup(t)
		if err := r.sw.Board.BlockOn(ts[0].Owner, ts[0].ID, ts[1].ID, "needs the client"); err != nil {
			t.Fatal(err)
		}
		if err := r.sw.Board.BlockOn(ts[2].Owner, ts[2].ID, ts[0].ID, "needs the api"); err != nil {
			t.Fatal(err)
		}
		r.sw.sweepStalls()
		if n := raised(r, StallBlockedCycle); n != 0 {
			t.Fatalf("found a cycle: %+v", stallEvents(r))
		}
	})
}

func TestStallReviewStarved(t *testing.T) {
	setup := func(t *testing.T) (*rvRig, *member, Task) {
		r := newRVRig(t, stallCfg(), gatedWorkers(nil))
		if _, err := r.sw.StartManager(); err != nil {
			t.Fatal(err)
		}
		b := r.sw.Board
		task, _ := b.CreateTask("mgr", TaskSpec{Title: "paginate users"})
		if err := b.Assign("mgr", "be-1", task.ID); err != nil {
			t.Fatal(err)
		}
		if err := b.Submit("be-1", task.ID, "paginated", "edited api/users.go"); err != nil {
			t.Fatal(err)
		}
		r.sw.sweepStalls() // the review clock starts
		return r, r.sw.get("mgr"), task
	}
	t.Run("seeded: the manager works on other things", func(t *testing.T) {
		r, mgr, task := setup(t)
		mgr.turns.Add(3) // three manager turns that do not look at the task
		r.callTool(context.Background(), "task", "mgr", "manager", map[string]any{"action": "list"})
		r.sw.sweepStalls()
		f, ok := active(r, StallReviewStarved)
		if !ok || f.Task != task.ID || f.Notify != "mgr" {
			t.Fatalf("finding = %+v, %v", f, ok)
		}
		if !nudged(r, "mgr") {
			t.Error("the manager was not told")
		}
		if err := r.sw.Board.Accept("mgr", task.ID, ""); err != nil {
			t.Fatal(err)
		}
		r.sw.sweepStalls()
		if _, ok := active(r, StallReviewStarved); ok {
			t.Fatal("the finding outlived its condition")
		}
	})
	t.Run("healthy: the manager reads the submission", func(t *testing.T) {
		r, mgr, task := setup(t)
		mgr.turns.Add(2)
		r.callTool(context.Background(), "task", "mgr", "manager", map[string]any{"action": "get", "id": task.ID})
		r.sw.sweepStalls()
		mgr.turns.Add(2)
		r.sw.sweepStalls()
		if n := raised(r, StallReviewStarved); n != 0 {
			t.Fatalf("a submission the manager is reading was found starved: %+v", stallEvents(r))
		}
	})
}

// A whole healthy run, swept continuously, names no stall at all.
func TestAHealthyRunHasNoStalls(t *testing.T) {
	var turn atomic.Int32
	cfg := stallCfg()
	cfg.SuperviseEvery = 2 * time.Millisecond
	r := newRVRig(t, cfg, func(ctx context.Context, c *rvCall) rvReply {
		switch c.Role {
		case "manager":
			switch turn.Add(1) {
			case 1:
				return rvReply{Tools: []rvToolCall{
					{Name: "task", Args: map[string]any{"action": "create", "title": "paginate users", "description": "cursor pagination", "files": []string{"api/**"}}},
					{Name: "spawn", Args: map[string]any{"role": "backend", "task": "T1"}},
				}}
			case 2:
				return rvReply{Tools: []rvToolCall{{Name: "wait", Args: map[string]any{"until": []string{"T1"}, "timeout_sec": 20}}}}
			case 3:
				return rvReply{Tools: []rvToolCall{{Name: "task", Args: map[string]any{"action": "accept", "id": "T1"}}}}
			}
			return rvReply{Text: "users are paginated"}
		case "backend":
			switch c.Assistants {
			case 0:
				return rvReply{Tools: []rvToolCall{{Name: "read", Args: map[string]any{"path": "api/users.go"}}, {Name: "edit", Args: map[string]any{"path": "api/users.go"}}}}
			case 1:
				return rvReply{Tools: []rvToolCall{{Name: "task", Args: map[string]any{"action": "done", "id": "T1", "text": "paginated"}}}}
			}
			return rvReply{Text: "T1 is paginated"}
		}
		return rvReply{Text: "ok"}
	})
	if _, err := r.sw.RunManager(context.Background(), "paginate the users list"); err != nil {
		t.Fatal(err)
	}
	if tk, _ := r.sw.Board.Snapshot().Task("T1"); tk.Status != StatusDone {
		t.Fatalf("T1 = %+v", tk)
	}
	r.sw.sweepStalls()
	if evs := stallEvents(r); len(evs) != 0 {
		t.Fatalf("a healthy run named stalls: %+v", evs)
	}
}
