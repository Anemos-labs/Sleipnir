package swarm

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// settle takes a task through review to done for owner.
func settle(t *testing.T, b *Board, owner, title string) Task {
	t.Helper()
	task, err := b.CreateTask("mgr", TaskSpec{Title: title, Role: "backend"})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Assign("mgr", owner, task.ID); err != nil {
		t.Fatal(err)
	}
	if err := b.Finish(owner, task.ID, StatusReview, "ready"); err != nil {
		t.Fatal(err)
	}
	if err := b.Accept("mgr", task.ID, "ok"); err != nil {
		t.Fatal(err)
	}
	got, _ := b.Snapshot().Task(task.ID)
	return got
}

// A manager's wait ends when no worker is running and every task is settled: no task can change state then. In a 12-worker run the
// manager waited the whole ten minutes after the last worker went idle with all 130 tasks merged, and the timeout's message was the
// first it heard of it.
func TestManagerCannotWaitOnASettledBoard(t *testing.T) {
	r := newRVRig(t, Config{}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "ok"} })
	if _, err := r.sw.StartManager(); err != nil {
		t.Fatal(err)
	}
	if why := r.sw.stalledWaitReason("mgr"); why != "" {
		t.Fatalf("an empty board must allow waiting for future work: %s", why)
	}
	settle(t, r.sw.Board, "be-1", "paginate users")
	why := r.sw.stalledWaitReason("mgr")
	if !strings.Contains(why, "Cannot wait") || !strings.Contains(why, "every task is settled") || !strings.Contains(why, "spawn") {
		t.Fatalf("a settled board with nobody running: %q", why)
	}
	if why := r.sw.stalledWaitReason("be-1"); why != "" {
		t.Fatalf("worker waits must remain available for manager mail: %s", why)
	}
	res := r.callTool(context.Background(), "wait", "mgr", "manager", map[string]any{"timeout_sec": 600})
	if !res.IsError || !strings.Contains(res.Text, "every task is settled") {
		t.Fatalf("the wait slept on a settled board: %+v", res)
	}
}

// The wait that began while a worker ran ends when the worker goes idle on a settled board, not when its timeout passes.
func TestManagerWaitEndsWhenTheLastWorkerGoesIdleOnASettledBoard(t *testing.T) {
	r := idleCapacityRigWith(t, 0, false) // be-1 runs with no task; be-2, be-3 and fe-1 are idle
	settle(t, r.sw.Board, "be-2", "paginate users")
	if why := r.sw.stalledWaitReason("mgr"); why != "" {
		t.Fatalf("a running worker was mistaken for a stall: %s", why)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	done := make(chan string, 1)
	go func() {
		res := r.callTool(ctx, "wait", "mgr", "manager", map[string]any{"timeout_sec": 600})
		done <- res.Text
	}()
	select {
	case got := <-done:
		t.Fatalf("the wait ended while be-1 ran: %s", got)
	case <-time.After(1500 * time.Millisecond):
	}
	m := r.sw.get("be-1")
	m.mu.Lock()
	m.life = lifeIdle
	m.mu.Unlock()
	select {
	case got := <-done:
		if !strings.Contains(got, "every task is settled") {
			t.Fatalf("the wait ended without saying why: %s", got)
		}
	case <-ctx.Done():
		t.Fatal("the wait went on after the last worker went idle on a settled board")
	}
}

// A task the manager has just accepted is news the docs keeper collects on its next tick: until then the board looks settled, and
// the wait for the docs pass is allowed.
func TestManagerMayWaitForTheDocsPassOfAnAcceptedTask(t *testing.T) {
	r := newRVRig(t, Config{AutoDocs: true, MaxWorkers: 2, Team: []string{"backend", DocsRole}}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "ok"} })
	if _, err := r.sw.StartManager(); err != nil {
		t.Fatal(err)
	}
	r.sw.docsTick(r.sw.deps.Now()) // the keeper has looked at the board once
	settle(t, r.sw.Board, "be-1", "paginate users")
	if why := r.sw.stalledWaitReason("mgr"); why != "" {
		t.Fatalf("the docs pass the accept starts was not waited for: %s", why)
	}
}

// A model's list of one task id often comes as the id itself: until and deps take a string, one id or several.
func TestWaitAndTaskTakeTaskIDsAsAString(t *testing.T) {
	r := newRVRig(t, Config{}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "ok"} })
	if _, err := r.sw.StartManager(); err != nil {
		t.Fatal(err)
	}
	a := settle(t, r.sw.Board, "be-1", "first")
	b := settle(t, r.sw.Board, "be-2", "second")
	for _, until := range []any{a.ID, a.ID + ", " + b.ID, []string{a.ID, b.ID}} {
		res := r.callTool(context.Background(), "wait", "mgr", "manager", map[string]any{"until": until})
		if !strings.Contains(res.Text, "all awaited tasks settled") {
			t.Fatalf("until %#v: %+v", until, res)
		}
	}
	res := r.callTool(context.Background(), "wait", "mgr", "manager", map[string]any{"until": 3})
	if !res.IsError || !strings.Contains(res.Text, "invalid arguments") {
		t.Fatalf("a number for until: %+v", res)
	}
	res = r.callTool(context.Background(), "task", "mgr", "manager", map[string]any{"action": "create", "title": "third", "role": "backend", "deps": a.ID + " " + b.ID})
	if res.IsError {
		t.Fatalf("deps as a string: %+v", res)
	}
	sn := r.sw.Board.Snapshot()
	third := sn.Tasks[len(sn.Tasks)-1]
	if strings.Join(third.Deps, ",") != a.ID+","+b.ID {
		t.Fatalf("deps %v", third.Deps)
	}
}

// The free workers are the idle ones that hold no open task; the line names them for the manager.
func TestFreeCapacityNamesIdleWorkersWithoutOpenTasks(t *testing.T) {
	r := idleCapacityRig(t, time.Minute) // be-1 runs on T1; be-2, be-3 and fe-1 are idle
	got := r.sw.FreeCapacity()
	if !strings.Contains(got, "3 of its 4 workers are free (be-2, be-3, fe-1)") || !strings.Contains(got, "spawn") {
		t.Fatalf("free capacity %q", got)
	}
	task, err := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "reserved"})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.sw.Board.Assign("mgr", "fe-1", task.ID); err != nil {
		t.Fatal(err)
	}
	if got := r.sw.FreeCapacity(); !strings.Contains(got, "2 of its 4 workers are free (be-2, be-3)") {
		t.Fatalf("a worker holding a task counted as free: %q", got)
	}
}

// After a resume, a worker whose task is settled is plain idle; only one that holds an unfinished task waits for it to restart.
func TestRestoredWorkersSayWaitingOnlyForAnUnfinishedTask(t *testing.T) {
	previous := NewBoard(nil)
	done := settle(t, previous, "be-2", "merged work")
	open, err := previous.CreateTask("mgr", TaskSpec{Title: "interrupted work", Role: "backend"})
	if err != nil {
		t.Fatal(err)
	}
	if err := previous.Assign("mgr", "be-1", open.ID); err != nil {
		t.Fatal(err)
	}
	r := newRVRig(t, Config{Team: []string{"backend", "backend"}, MaxWorkers: 2}, func(ctx context.Context, _ *rvCall) rvReply { <-ctx.Done(); return rvReply{Text: "stopped"} })
	workers := []RecoveredWorker{{ID: "be-1", Role: "backend", Task: open.ID}, {ID: "be-2", Role: "backend", Task: done.ID}}
	if err := r.sw.RestoreTeam(context.Background(), previous.Snapshot(), workers, map[string]string{"be-1": "backend", "be-2": "backend"}, 0); err != nil {
		t.Fatal(err)
	}
	line := func(id string) string {
		m := r.sw.get(id)
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.line
	}
	if got := line("be-1"); got != "restored; waiting for "+open.ID+" to restart" {
		t.Errorf("be-1, holding %s: %q", open.ID, got)
	}
	if got := line("be-2"); got != "" {
		t.Errorf("be-2, whose task is merged: %q", got)
	}
}

// While a worker sits free, the manager's wait for several tasks returns as soon as one of them newly reaches review: its verdict
// is due then, not once the longest of the others settles. A live run slept ten minutes beside two reviews while one task ran on.
// With every worker busy the wait goes on for all of them.
func TestManagersWaitReturnsWhenAnAwaitedTaskReachesReview(t *testing.T) {
	for _, free := range []bool{true, false} {
		t.Run(fmt.Sprintf("free worker %v", free), func(t *testing.T) {
			release := make(chan struct{})
			r := newRVRig(t, Config{MaxWorkers: 2, Team: []string{"backend", "backend"}}, func(ctx context.Context, c *rvCall) rvReply {
				if c.Role == "backend" {
					select { // the worker on the long task keeps running
					case <-release:
					case <-ctx.Done():
					}
				}
				return rvReply{Text: "ok"}
			})
			defer close(release)
			if _, err := r.sw.StartManager(); err != nil {
				t.Fatal(err)
			}
			if res := r.callTool(context.Background(), "spawn", "mgr", "manager", map[string]any{"role": "backend", "task": "the long task", "solo": true}); res.IsError {
				t.Fatal(res.Text)
			}
			plan := agreementTask(t, r.sw.Board, "Shared contract")
			if err := r.sw.Board.Assign("mgr", "sc-1", plan.ID); err != nil {
				t.Fatal(err)
			}
			long := r.sw.Board.Snapshot().Tasks[0].ID
			if free {
				r.sw.Board.SetAgent(AgentInfo{ID: "be-2", Role: "backend", State: "idle"}) // a worker with nothing to do
			}
			done := make(chan string, 1)
			go func() {
				res := r.callTool(context.Background(), "wait", "mgr", "manager", map[string]any{"until": []string{long, plan.ID}, "timeout_sec": 60})
				done <- res.Text
			}()
			time.Sleep(200 * time.Millisecond)
			select {
			case got := <-done:
				t.Fatalf("the wait returned before anything reached review: %s", got)
			default:
			}
			if err := r.sw.Board.submitAgreementAt("sc-1", plan.ID, 0, "proposal", "", "Use the shared interface."); err != nil {
				t.Fatal(err)
			}
			wait := 10 * time.Second
			if !free {
				wait = time.Second
			}
			select {
			case got := <-done:
				if !free {
					t.Fatalf("the wait returned with every worker busy: %s", got)
				}
				if !strings.Contains(got, plan.ID+" reached review while be-2 is free") {
					t.Fatalf("the wait did not say what reached review: %s", got)
				}
			case <-time.After(wait):
				if free {
					t.Fatal("the wait slept on beside a task in review and a free worker")
				}
			}
		})
	}
}
