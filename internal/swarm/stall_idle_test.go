package swarm

import (
	"context"
	"strings"
	"testing"
	"time"
)

// An idle-capacity rig: a configured team of four (be-1..3, fe-1) made idle, be-1 at work on T1, and the manager, which waits.
// idleFor sets how long the others have been idle. Members are set by hand: none of them runs a goroutine.
func idleCapacityRig(t *testing.T, idleFor time.Duration) *rvRig {
	return idleCapacityRigWith(t, idleFor, true)
}

// idleCapacityRigWith is idleCapacityRig whose T1 is given to be-1 only when withTask is set.
func idleCapacityRigWith(t *testing.T, idleFor time.Duration, withTask bool) *rvRig {
	t.Helper()
	cfg := stallCfg()
	cfg.PersistentPools, cfg.MaxWorkers, cfg.Team = true, 4, []string{"backend", "backend", "backend", "frontend"}
	r := newRVRig(t, cfg, gatedWorkers(nil))
	if _, err := r.sw.StartManager(); err != nil {
		t.Fatal(err)
	}
	r.sw.spawnMu.Lock()
	for _, role := range []string{"backend", "frontend"} {
		if _, err := r.sw.ensurePool(role); err != nil {
			r.sw.spawnMu.Unlock()
			t.Fatal(err)
		}
	}
	r.sw.spawnMu.Unlock()
	for _, id := range []string{"be-1", "be-2", "be-3", "fe-1"} {
		if m := r.sw.get(id); m.idleAt.IsZero() {
			t.Fatalf("%s was made idle with no time: a pool member that has not run would never count", id)
		}
	}
	if withTask {
		task, _ := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "paginate users"})
		if err := r.sw.Board.Assign("mgr", "be-1", task.ID); err != nil {
			t.Fatal(err)
		}
	}
	now := r.sw.deps.Now()
	for _, id := range []string{"be-1", "be-2", "be-3", "fe-1"} {
		m := r.sw.get(id)
		m.mu.Lock()
		if id == "be-1" {
			m.life = lifeRunning
		} else {
			m.idleAt = now.Add(-idleFor)
		}
		m.mu.Unlock()
	}
	t.Cleanup(func() { // be-1 runs no goroutine: let the shutdown find it idle
		m := r.sw.get("be-1")
		m.mu.Lock()
		m.life = lifeIdle
		m.mu.Unlock()
	})
	r.sw.noteCoordination("mgr", "wait", "")
	return r
}

// The manager waits on one worker while three of four have been idle for twenty minutes: it is told who is free, once, and the
// finding clears when it stops waiting. In a 12-worker run ten workers sat idle for forty minutes while the manager waited on the
// last two tasks, having written that much of the objective was still unbuilt.
func TestStallIdleCapacity(t *testing.T) {
	t.Run("seeded: most of the team idle while the manager waits", func(t *testing.T) {
		r := idleCapacityRig(t, 20*time.Minute)
		r.sw.sweepStalls()
		f, ok := active(r, StallIdleCapacity)
		if !ok || f.Notify != "mgr" || !strings.HasPrefix(f.Detail, "3 of 4 workers have been idle for 15 minutes or more (be-2, be-3, fe-1) while the manager waits for ") {
			t.Fatalf("finding = %+v, %v", f, ok)
		}
		if !nudged(r, "mgr") {
			t.Error("the manager was not told")
		}
		r.sw.sweepStalls()
		if n := raised(r, StallIdleCapacity); n != 1 {
			t.Errorf("raised %d times", n)
		}
		r.callTool(context.Background(), "task", "mgr", "manager", map[string]any{"action": "list"})
		r.sw.sweepStalls()
		if _, ok := active(r, StallIdleCapacity); ok {
			t.Fatal("the finding outlived the manager's wait")
		}
	})
	t.Run("healthy: nothing is open, a worker runs with no task", func(t *testing.T) {
		r := idleCapacityRigWith(t, 20*time.Minute, false)
		r.sw.sweepStalls()
		if n := raised(r, StallIdleCapacity); n != 0 {
			t.Fatalf("a settled board was found: %+v", stallEvents(r))
		}
	})
	t.Run("healthy: the team went idle a moment ago", func(t *testing.T) {
		r := idleCapacityRig(t, 5*time.Minute)
		r.sw.sweepStalls()
		if n := raised(r, StallIdleCapacity); n != 0 {
			t.Fatalf("a team idle for five minutes was found: %+v", stallEvents(r))
		}
	})
	t.Run("healthy: workers owning a task in review or reserved for them are not free", func(t *testing.T) {
		r := idleCapacityRig(t, 20*time.Minute)
		review, _ := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "work of be-2"})
		if err := r.sw.Board.Assign("mgr", "be-2", review.ID); err != nil {
			t.Fatal(err)
		}
		if err := r.sw.Board.Submit("be-2", review.ID, "done", "edited a.go"); err != nil {
			t.Fatal(err)
		}
		reserved, _ := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "work of be-3"})
		if _, err := r.sw.Board.assignTask(assignReq{by: "mgr", agent: "be-3", id: reserved.ID, reserve: true}); err != nil {
			t.Fatal(err)
		}
		r.sw.sweepStalls()
		if n := raised(r, StallIdleCapacity); n != 0 {
			t.Fatalf("workers owning open tasks counted as free: %+v", stallEvents(r))
		}
	})
	t.Run("healthy: workers with work in progress are not free", func(t *testing.T) {
		r := idleCapacityRig(t, 20*time.Minute)
		for _, id := range []string{"be-2", "be-3"} {
			task, _ := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "work of " + id})
			if err := r.sw.Board.Assign("mgr", id, task.ID); err != nil {
				t.Fatal(err)
			}
		}
		r.sw.sweepStalls()
		if n := raised(r, StallIdleCapacity); n != 0 {
			t.Fatalf("workers holding work in progress counted as free: %+v", stallEvents(r))
		}
	})
}
