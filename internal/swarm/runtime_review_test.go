package swarm

// Adversarial review tests for the swarm runtime (Spawn/startRun/finishRun/
// deliver/Retire, the coordination tools, failure handling). Written for
// docs/reviews/swarm-concurrency.md. Every TestConc_* repro asserts the CORRECT
// behaviour, so it FAILS while its finding is open, and is skipped unless
// SLEIPNIR_REVIEW is set (same switch as the security review). The interleavings
// are forced with locks and channels rather than left to scheduler luck.
//
//	SLEIPNIR_REVIEW=1 go test -race -count=1 -run 'TestConc_' ./internal/swarm

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/agent"
	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/tools"
)

func rvWorkers(r *rvRig) (writers, total int) {
	r.sw.mu.Lock()
	defer r.sw.mu.Unlock()
	for _, m := range r.sw.members {
		total++
		if role := r.sw.roles[m.role]; !role.ReadOnly && m.role != "manager" && m.isActive() {
			writers++
		}
	}
	return
}

// A message that arrives while the worker's final (tool-less) request is in flight
// is queued in the agent's inbox, but Run returns without draining it and deliver
// saw running==true so it never restarted the worker. The mail is never processed.
func TestConc_MailArrivingDuringFinalAnswerIsStranded(t *testing.T) {
	concGate(t)
	gate := make(chan struct{})
	inflight := make(chan struct{}, 1)
	r := newRVRig(t, Config{MaxWriters: 4}, func(ctx context.Context, c *rvCall) rvReply {
		if c.Role == "backend" && c.Assistants == 0 {
			select {
			case inflight <- struct{}{}:
			default:
			}
			rvBlock(ctx, gate) // the model is writing its final answer
			return rvReply{Text: "done, nothing to report"}
		}
		return rvReply{Text: "ack"}
	})
	if _, err := r.sw.StartManager(); err != nil {
		t.Fatal(err)
	}
	id, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "work", By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	<-inflight
	if _, err := r.sw.Router.Send("mgr", id, "contract", "POST /users now returns 201"); err != nil {
		t.Fatal(err)
	}
	close(gate)
	rvWait(t, "worker to go idle", func() bool { return r.idle(id) })
	time.Sleep(300 * time.Millisecond) // any restart would have happened by now
	m := r.sw.get(id)
	if pending := m.a.PendingInbox(); pending != 0 || !r.prov.sawEver("[mail m1") {
		t.Fatalf("mail sent during the worker's final request was never processed: inbox=%d, model saw it=%v, model calls for %s=%d (worker is idle and nothing will wake it)",
			pending, r.prov.sawEver("[mail m1"), id, r.prov.callsFor(id))
	}
}

// task reject = Assign(owner) + mail. When the owner is still producing its final
// summary after `task done` (the tool result tells it to do exactly that), the
// mail is stranded (see above) and finishRun then sees the freshly re-assigned
// task in "doing" and moves it straight back to "review": the rejection is
// silently undone and the worker never hears the feedback.
func TestConc_RejectDuringFinalAnswerIsSilentlyUndone(t *testing.T) {
	concGate(t)
	gate := make(chan struct{})
	inflight := make(chan struct{}, 1)
	r := newRVRig(t, Config{MaxWriters: 4}, func(ctx context.Context, c *rvCall) rvReply {
		if c.Role != "backend" {
			return rvReply{Text: "ok"}
		}
		switch {
		case c.Assistants == 0:
			return rvReply{Tools: []rvToolCall{{"task", map[string]any{"action": "done", "id": "T1", "text": "implemented"}}}}
		case c.Assistants == 1:
			select {
			case inflight <- struct{}{}:
			default:
			}
			rvBlock(ctx, gate)
			return rvReply{Text: "summary of my work"}
		}
		return rvReply{Text: "ack"}
	})
	r.sw.StartManager()
	id, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "work", By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	<-inflight // T1 is in review; the worker is writing its final paragraph
	if tk, _ := r.sw.Board.Snapshot().Task("T1"); tk.Status != StatusReview {
		t.Fatalf("setup: T1 = %s", tk.Status)
	}
	res := r.callTool(context.Background(), "task", "mgr", "manager", map[string]any{"action": "reject", "id": "T1", "text": "fix the failing test"})
	if res.IsError {
		t.Fatalf("reject: %s", res.Text)
	}
	close(gate)
	rvWait(t, "worker to go idle", func() bool { return r.idle(id) })
	time.Sleep(300 * time.Millisecond)
	tk, _ := r.sw.Board.Snapshot().Task("T1")
	heard := r.prov.sawEver("fix the failing test")
	if tk.Status == StatusReview && !heard {
		t.Fatalf("the manager's rejection was silently undone: T1 is back in %q with the worker's old summary %q, and the worker never saw the feedback (heard=%v)", tk.Status, tk.Result, heard)
	}
}

// startRun silently ignores its input when the agent is already running, but
// Spawn's reuse path has already Assign()ed the task by then. If a mail-triggered
// run wins the race the task is owned by an agent that never received the card,
// and finishRun promptly moves it to "review" on the strength of a run that did
// something else entirely. The interleaving is forced by freezing the board.
func TestConc_ReuseAssignmentIsDroppedWhenMailRunStartsFirst(t *testing.T) {
	concGate(t)
	r := newRVRig(t, Config{MaxWriters: 4}, func(ctx context.Context, c *rvCall) rvReply { return rvReply{Text: "ok"} })
	r.sw.StartManager()
	id, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "first", By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	rvWait(t, "worker idle", func() bool { return r.idle(id) })
	t2, _ := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "second real task"})

	b := r.sw.Board
	b.mu.Lock() // freeze the board: every mutation stalls
	var spawnErr error
	spawned := make(chan struct{})
	go func() {
		_, spawnErr = r.sw.Spawn(SpawnReq{Agent: id, TaskID: t2.ID, By: "mgr"})
		close(spawned)
	}()
	time.Sleep(60 * time.Millisecond) // Spawn passed its busy check, now stalled in Board.Assign
	mailed := make(chan struct{})
	go func() {
		_, _ = r.sw.Router.Send("mgr", id, "info", "fyi: heads up")
		close(mailed)
	}()
	time.Sleep(60 * time.Millisecond) // deliver set running=true, now stalled in setState
	b.mu.Unlock()
	<-spawned
	<-mailed
	rvWait(t, "worker idle again", func() bool { return r.idle(id) && r.prov.callsFor(id) >= 2 })
	time.Sleep(100 * time.Millisecond)
	if spawnErr != nil {
		t.Fatalf("setup: Spawn reported failure: %v", spawnErr)
	}
	tk, _ := b.Snapshot().Task(t2.ID)
	if tk.Owner == id && !r.prov.sawEver("Begin task "+t2.ID) {
		t.Fatalf("Spawn returned success and the board says %s is %s/%s (result %q), but %s never received the assignment card: startRun dropped it because a mail-triggered run was already live",
			t2.ID, tk.Owner, tk.Status, tk.Result, id)
	}
}

// The writer cap is enforced only on the "new worker" path. Reusing an idle
// writer (spawn agent=…) skips it, so the cap can be exceeded deterministically.
func TestConc_WriterCapBypassedByReuse(t *testing.T) {
	concGate(t)
	gate := make(chan struct{})
	r := newRVRig(t, Config{MaxWriters: 1}, func(ctx context.Context, c *rvCall) rvReply {
		if c.Agent == "be-2" || (c.Agent == "be-1" && c.Assistants >= 1) {
			rvBlock(ctx, gate)
		}
		return rvReply{Text: "ok"}
	})
	t.Cleanup(func() { close(gate) })
	r.sw.StartManager()
	id1, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "A", By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	rvWait(t, "first writer idle", func() bool { return r.idle(id1) })
	id2, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "B", By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	rvWait(t, "second writer running", func() bool { return r.running(id2) })
	if _, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "C", By: "mgr"}); err == nil {
		t.Fatal("setup: a third writer must be refused while one is active")
	}
	// Reuse the idle writer: no cap check on this path.
	_, err = r.sw.Spawn(SpawnReq{Agent: id1, Title: "D", By: "mgr"})
	writers, _ := rvWorkers(r)
	if err == nil && writers > 1 {
		t.Fatalf("MaxWriters=1 but %d writers are running: the reuse path in Spawn skips the writer cap", writers)
	}
}

// The cap checks read the roster under s.mu, release it, and only much later
// (buildAgent) insert the new member; a fresh member is not "active" until
// startRun. Concurrent Spawn calls therefore all pass the check.
func TestConc_ConcurrentSpawnExceedsWriterAndAgentCaps(t *testing.T) {
	concGate(t)
	spawnMany := func(t *testing.T, cfg Config, role string) (ok int, writers, total int) {
		gate := make(chan struct{})
		// NewSink runs inside buildAgent: after the cap checks, before the member is
		// registered. Holding every caller there (up to 300ms) lines them all up in
		// exactly the window the race needs, without depending on scheduler luck.
		var arrived atomic.Int32
		r := newRVRigWith(t, cfg, func(ctx context.Context, c *rvCall) rvReply {
			rvBlock(ctx, gate)
			return rvReply{Text: "ok"}
		}, func(d *Deps) {
			d.NewSink = func(string) agent.Sink {
				arrived.Add(1)
				for deadline := time.Now().Add(300 * time.Millisecond); arrived.Load() < 24 && time.Now().Before(deadline); {
					time.Sleep(time.Millisecond)
				}
				return agent.NopSink{}
			}
		})
		t.Cleanup(func() { close(gate) })
		r.sw.StartManager()
		var wg sync.WaitGroup
		var n atomic.Int32
		arrived.Store(-1) // the manager's own NewSink call above has already happened; ignore it
		arrived.Store(0)
		for i := 0; i < 24; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				if _, err := r.sw.Spawn(SpawnReq{Role: role, Title: fmt.Sprintf("job %d", i), By: "mgr"}); err == nil {
					n.Add(1)
				}
			}(i)
		}
		wg.Wait()
		w, tot := rvWorkers(r)
		return int(n.Load()), w, tot
	}
	t.Run("writers", func(t *testing.T) {
		ok, writers, _ := spawnMany(t, Config{MaxWriters: 2, MaxAgents: 40}, "backend")
		if ok > 2 || writers > 2 {
			t.Fatalf("MaxWriters=2 but %d Spawn calls succeeded and %d writers are running", ok, writers)
		}
	})
	t.Run("agents", func(t *testing.T) {
		ok, _, total := spawnMany(t, Config{MaxWriters: 40, MaxAgents: 4}, "reviewer") // manager + 3
		if total > 4 {
			t.Fatalf("MaxAgents=4 but %d agents are registered (%d Spawn calls succeeded)", total, ok)
		}
	})
}

// Assign() is unconditional; Spawn checks the owner on a stale snapshot. Two
// spawns for one task that overlap (an API caller alongside the manager, or a UI
// command) both succeed, both workers are told the task is theirs, and the board
// remembers only the last one. The overlap is forced by freezing the board.
func TestConc_TwoWorkersOwnTheSameTask(t *testing.T) {
	concGate(t)
	gate := make(chan struct{})
	r := newRVRig(t, Config{MaxAgents: 40}, func(ctx context.Context, c *rvCall) rvReply {
		rvBlock(ctx, gate)
		return rvReply{Text: "ok"}
	})
	t.Cleanup(func() { close(gate) })
	r.sw.StartManager()
	task, _ := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "shared work"})
	r.sw.Board.mu.Lock() // both Spawns read Owner=="" and stall in Assign
	var wg sync.WaitGroup
	var ok atomic.Int32
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := r.sw.Spawn(SpawnReq{Role: "reviewer", TaskID: task.ID, By: "mgr"}); err == nil {
				ok.Add(1)
			}
		}()
	}
	time.Sleep(100 * time.Millisecond)
	r.sw.Board.mu.Unlock()
	wg.Wait()
	if n := ok.Load(); n != 1 {
		tk, _ := r.sw.Board.Snapshot().Task(task.ID)
		t.Fatalf("%d workers were started on %s and were each told it is theirs; the board owner is only %s", n, task.ID, tk.Owner)
	}
}

// Retire checks "not running", releases the lock, then deletes the member. A run
// that starts in that window keeps executing as an agent the swarm no longer
// knows: mail to it is refused, TotalCost ignores it, the janitor never sees it,
// and when it finishes setState upserts it back onto the board roster forever.
func TestConc_RetireRacingStartRunLeavesRunningGhost(t *testing.T) {
	concGate(t)
	gate := make(chan struct{})
	r := newRVRig(t, Config{MaxWriters: 4}, func(ctx context.Context, c *rvCall) rvReply {
		if c.Assistants >= 1 {
			rvBlock(ctx, gate)
		}
		return rvReply{Text: "ok"}
	})
	t.Cleanup(func() {
		select {
		case <-gate:
		default:
			close(gate)
		}
	})
	r.sw.StartManager()
	id, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "first", By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	rvWait(t, "worker idle", func() bool { return r.idle(id) })
	m := r.sw.get(id)

	m.mu.Lock() // stall Retire at its busy check
	retired := make(chan error, 1)
	go func() { retired <- r.sw.Retire(id) }()
	time.Sleep(60 * time.Millisecond)
	r.sw.mu.Lock() // Retire will pass the check, then stall on the roster lock
	m.mu.Unlock()
	time.Sleep(60 * time.Millisecond)
	r.sw.startRun(m, "second job") // a run starts inside Retire's window
	rvWait(t, "second run to reach the model", func() bool { return r.prov.callsFor(id) >= 2 })
	r.sw.mu.Unlock()
	err = <-retired

	m.mu.Lock()
	live := m.running
	m.mu.Unlock()
	failed := false
	if err == nil && live {
		failed = true
		t.Errorf("Retire returned nil for %s although its run is live (Retire is documented to remove an idle agent)", id)
	}
	if _, sendErr := r.sw.Router.Send("mgr", id, "info", "are you there?"); sendErr != nil && live {
		t.Errorf("a running agent cannot be mailed any more: %v", sendErr)
		failed = true
	}
	close(gate)
	rvWait(t, "ghost to finish", func() bool { m.mu.Lock(); defer m.mu.Unlock(); return !m.running })
	time.Sleep(60 * time.Millisecond)
	if _, onBoard := r.sw.Board.Snapshot().Agent(id); onBoard && r.sw.get(id) == nil {
		t.Errorf("%s is off the swarm roster but back on the board roster (a permanent ghost in every agent's hot view; Retire can no longer remove it)", id)
		failed = true
	}
	if failed {
		t.FailNow()
	}
}

// TotalCost sums live members only, so retiring an agent (by hand, or the
// janitor after IdleRetire) erases its spend and re-opens a spent BudgetUSD.
func TestConc_RetireForgetsSpendAndReopensTheBudget(t *testing.T) {
	concGate(t)
	r := newRVRig(t, Config{MaxWriters: 4, BudgetUSD: 3}, func(ctx context.Context, c *rvCall) rvReply {
		return rvReply{Text: "ok", Usage: core.Usage{InputTokens: 1_000_000}} // $4 at 4$/M
	})
	r.sw.StartManager()
	id, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "A", By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	rvWait(t, "worker idle", func() bool { return r.idle(id) })
	if _, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "B", By: "mgr"}); err == nil {
		t.Fatalf("setup: budget $3 is spent ($%.2f) yet Spawn was allowed", r.sw.TotalCost())
	}
	before := r.sw.TotalCost()
	if err := r.sw.Retire(id); err != nil {
		t.Fatal(err)
	}
	if after := r.sw.TotalCost(); after < before {
		_, spawnErr := r.sw.Spawn(SpawnReq{Role: "backend", Title: "C", By: "mgr"})
		t.Fatalf("TotalCost fell from $%.2f to $%.2f when the agent was retired; Spawn after retirement: err=%v (the swarm budget is now open again)", before, after, spawnErr)
	}
}

// claim overwrites member.task, and finishRun only settles member.task. Any task
// the worker claimed earlier stays "doing" with a dead owner; Spawn and claim both
// refuse a task that has an owner, so nothing can ever pick it up again.
func TestConc_ClaimOrphansTheAssignedTaskForever(t *testing.T) {
	concGate(t)
	r := newRVRig(t, Config{MaxWriters: 4}, func(ctx context.Context, c *rvCall) rvReply {
		if c.Role == "backend" && c.Assistants == 0 {
			return rvReply{Tools: []rvToolCall{{"task", map[string]any{"action": "claim", "id": "T2"}}}}
		}
		return rvReply{Text: "finished T2"}
	})
	r.sw.StartManager()
	t1, _ := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "assigned task"})
	t2, _ := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "extra task the worker grabs"})
	id, err := r.sw.Spawn(SpawnReq{Role: "backend", TaskID: t1.ID, By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	rvWait(t, "worker idle", func() bool { return r.idle(id) })
	s := r.sw.Board.Snapshot()
	a, _ := s.Task(t1.ID)
	b, _ := s.Task(t2.ID)
	t.Logf("after the run: %s=%s/%s  %s=%s/%s", t1.ID, a.Status, a.Owner, t2.ID, b.Status, b.Owner)
	if err := r.sw.Retire(id); err != nil {
		t.Fatal(err)
	}
	if a.Status == StatusDoing {
		_, spawnErr := r.sw.Spawn(SpawnReq{Role: "backend", TaskID: t1.ID, By: "mgr"})
		claimErr := r.sw.Board.Claim("be-9", t1.ID)
		if spawnErr != nil && claimErr != nil {
			t.Fatalf("%s is stuck in %q owned by retired %s: finishRun only settles the last claimed task; Spawn says %q and claim says %q", t1.ID, a.Status, a.Owner, spawnErr, claimErr)
		}
	}
}

// reject = Assign then Send. If the mail is refused (dedupe, per-pair rate limit,
// recipient retired) the task has already flipped back to "doing" with an idle
// owner who will never hear about it.
func TestConc_FailedRejectLeavesTaskDoingWithNoFeedback(t *testing.T) {
	concGate(t)
	r := newRVRig(t, Config{MaxWriters: 4}, func(ctx context.Context, c *rvCall) rvReply { return rvReply{Text: "ok"} })
	r.sw.StartManager()
	id, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "work", By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	rvWait(t, "T1 in review", func() bool {
		tk, _ := r.sw.Board.Snapshot().Task("T1")
		return tk.Status == StatusReview && r.idle(id)
	})
	reject := func() *tools.Result {
		return r.callTool(context.Background(), "task", "mgr", "manager", map[string]any{"action": "reject", "id": "T1", "text": "fix it"})
	}
	if res := reject(); res.IsError {
		t.Fatalf("first reject: %s", res.Text)
	}
	rvWait(t, "worker to handle the rejection and settle", func() bool {
		tk, _ := r.sw.Board.Snapshot().Task("T1")
		return tk.Status == StatusReview && r.idle(id) && r.prov.callsFor(id) >= 2
	})
	res := reject() // identical text inside the dedupe window: the mail is refused
	if !res.IsError {
		t.Fatalf("setup: expected the router to refuse the duplicate (%s)", res.Text)
	}
	if tk, _ := r.sw.Board.Snapshot().Task("T1"); tk.Status != StatusReview {
		t.Errorf("reject failed (%q) but T1 was already flipped to %q/%s: the task now waits on an idle worker that was never told", res.Text, tk.Status, tk.Owner)
	}
	// Owner retired: Assign succeeds, Send cannot.
	if err := r.sw.Retire(id); err != nil {
		t.Fatal(err)
	}
	r.sw.Board.Finish("manager", "T1", StatusReview, "back in review")
	res = r.callTool(context.Background(), "task", "mgr", "manager", map[string]any{"action": "reject", "id": "T1", "text": "fix it properly"})
	if tk, _ := r.sw.Board.Snapshot().Task("T1"); res.IsError && tk.Status == StatusDoing {
		t.Errorf("reject to a retired owner failed (%q) yet T1 is now %q, owned by nobody who exists", res.Text, tk.Status)
	}
	if t.Failed() {
		t.FailNow()
	}
}

// A refused Spawn(title) has already created the task.
func TestConc_RefusedSpawnLeavesOrphanTasksOnTheBoard(t *testing.T) {
	concGate(t)
	r := newRVRig(t, Config{MaxWriters: 1}, func(ctx context.Context, c *rvCall) rvReply { return rvReply{Text: "ok"} })
	r.sw.StartManager()
	for i := 0; i < 3; i++ {
		if _, err := r.sw.Spawn(SpawnReq{Role: "nonesuch", Title: "will be refused", By: "mgr"}); err == nil {
			t.Fatal("setup: unknown role must be refused")
		}
	}
	if n := len(r.sw.Board.Snapshot().Tasks); n != 0 {
		t.Fatalf("three refused spawns left %d orphan todo tasks on the board (every retry by the manager creates another)", n)
	}
}

// finishRun's first critical section marks the member idle, but the "idle" board
// publication happens at its very end. If the manager reuses the worker inside
// that window, the old run's late setState(idle) overwrites the new run's status:
// the board says idle for an agent that is running (and `m.task` is written with
// no lock while finishRun reads it, so -race reports it too).
func TestConc_ReuseDuringFinishRunPublishesIdleForARunningAgent(t *testing.T) {
	concGate(t)
	gate := make(chan struct{})
	r := newRVRig(t, Config{MaxWriters: 4}, func(ctx context.Context, c *rvCall) rvReply {
		if c.Assistants >= 1 {
			rvBlock(ctx, gate)
		}
		return rvReply{Text: "ok"}
	})
	t.Cleanup(func() {
		select {
		case <-gate:
		default:
			close(gate)
		}
	})
	r.sw.StartManager()
	r.sw.Leases.mu.Lock() // finishRun will stall in ReleaseAll, after it flipped running=false
	id, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "first", By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	m := r.sw.get(id)
	rvWait(t, "finishRun to reach ReleaseAll", func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return !m.running && !m.idleAt.IsZero()
	})
	if _, err := r.sw.Spawn(SpawnReq{Agent: id, Title: "second", By: "mgr"}); err != nil {
		r.sw.Leases.mu.Unlock()
		t.Fatalf("reuse: %v", err)
	}
	rvWait(t, "second run to reach the model", func() bool { return r.prov.callsFor(id) >= 2 })
	r.sw.Leases.mu.Unlock() // finishRun resumes and publishes "idle"
	time.Sleep(150 * time.Millisecond)
	a, _ := r.sw.Board.Snapshot().Agent(id)
	if !r.running(id) {
		t.Fatal("setup: second run is not live")
	}
	if a.State != "running" {
		t.Fatalf("%s is running its second task but the board (and every agent's hot view, and wait's report) says %q", id, a.State)
	}
}

// Nothing bounds a worker's Run: a panic in the provider adapter, the renderer or
// a Sink (runOne only recovers around the tool call itself) kills the process,
// taking the manager, every other worker and the in-memory board with it.
func TestConc_WorkerPanicKillsWholeProcess(t *testing.T) {
	concGate(t)
	if testing.Short() {
		t.Skip("spawns a subprocess")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestConc_PanicChild$", "-test.v", "-test.count=1", "-test.timeout=60s")
	cmd.Env = append(os.Environ(), "SLEIPNIR_REVIEW_PANIC_CHILD=1")
	out, err := cmd.CombinedOutput()
	if !strings.Contains(string(out), "SURVIVED") {
		head := string(out)
		if len(head) > 600 {
			head = head[:600]
		}
		t.Fatalf("one worker panicked and the whole process died (exit: %v); startRun has no recover:\n%s", err, head)
	}
}

// TestConc_PanicChild is the subprocess body of the test above.
func TestConc_PanicChild(t *testing.T) {
	if os.Getenv("SLEIPNIR_REVIEW_PANIC_CHILD") != "1" {
		t.Skip("subprocess only")
	}
	r := newRVRig(t, Config{MaxWriters: 4}, func(ctx context.Context, c *rvCall) rvReply {
		if c.Role == "backend" {
			panic("adapter bug: nil pointer in worker request")
		}
		return rvReply{Text: "ok"}
	})
	r.sw.StartManager()
	if _, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "doomed", By: "mgr"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	tk, _ := r.sw.Board.Snapshot().Task("T1")
	fmt.Printf("SURVIVED task=%s\n", tk.Status)
}

// The verifier's context has no deadline: a hung `go test` stalls the worker (and
// its task, and the manager's wait) until the whole swarm is shut down.
func TestConc_VerifierRunsWithoutAnyHarnessDeadline(t *testing.T) {
	concGate(t)
	var hasDeadline atomic.Bool
	called := make(chan struct{}, 4)
	cfg := Config{MaxWriters: 4, VerifyCmd: "go test ./...", Verify: func(ctx context.Context, dir, cmd string) (string, int, error) {
		_, ok := ctx.Deadline()
		hasDeadline.Store(ok)
		called <- struct{}{}
		return "ok", 0, nil
	}}
	r := newRVRig(t, cfg, func(ctx context.Context, c *rvCall) rvReply {
		if c.Role == "backend" && c.Assistants == 0 {
			return rvReply{Tools: []rvToolCall{{"task", map[string]any{"action": "done", "id": "T1", "text": "x"}}}}
		}
		return rvReply{Text: "summary"}
	})
	r.sw.StartManager()
	if _, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "work", By: "mgr"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-called:
	case <-time.After(5 * time.Second):
		t.Fatal("verifier never ran")
	}
	if !hasDeadline.Load() {
		t.Fatal("Verify runs with a context that has no deadline: a hung verification blocks the worker, its task, wait(until=…) and Shutdown indefinitely")
	}
}

// Cancelling the manager (its Run context) does not touch the workers it started:
// they belong to the swarm's root context. The user's Ctrl-C ends the manager's
// turn while the workers keep spending and editing.
func TestConc_CancellingTheManagerLeavesWorkersRunning(t *testing.T) {
	concGate(t)
	gate := make(chan struct{})
	r := newRVRig(t, Config{MaxWriters: 4}, func(ctx context.Context, c *rvCall) rvReply {
		if c.Role == "manager" {
			if c.Assistants == 0 {
				return rvReply{Tools: []rvToolCall{{"spawn", map[string]any{"role": "backend", "task": "Build the feature"}}}}
			}
			return rvReply{Tools: []rvToolCall{{"wait", map[string]any{"timeout_sec": 30}}}}
		}
		rvBlock(ctx, gate) // long-running worker request
		return rvReply{Text: "worker done"}
	})
	t.Cleanup(func() { close(gate) })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := r.sw.RunManager(ctx, "build it"); done <- err }()
	rvWait(t, "worker to be running", func() bool { return r.running("be-1") })
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("manager should report the cancellation")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("manager did not stop")
	}
	time.Sleep(200 * time.Millisecond)
	if r.running("be-1") {
		t.Fatalf("manager cancelled and %d worker(s) are still running with nobody to report to (mail for the manager just piles up in its inbox)", 1)
	}
}

// There is no closed state: after Shutdown, Spawn happily builds agents, marks the
// tasks doing and starts goroutines (untracked once Shutdown's Wait has returned)
// that die on the cancelled context and mark the task failed.
func TestConc_SpawnAfterShutdownIsNotRefused(t *testing.T) {
	concGate(t)
	r := newRVRig(t, Config{MaxWriters: 4}, func(ctx context.Context, c *rvCall) rvReply { return rvReply{Text: "ok"} })
	r.sw.StartManager()
	r.sw.Shutdown()
	id, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "late work", By: "mgr"})
	if err == nil {
		time.Sleep(100 * time.Millisecond)
		tk, _ := r.sw.Board.Snapshot().Task("T1")
		t.Fatalf("Spawn after Shutdown succeeded (%s) and the task ended up %q (%s)", id, tk.Status, tk.Result)
	}
}

// Spawn registers the new member (visible to the roster, mail and the agent cap)
// BEFORE it Assigns the task. If Assign then fails (here: the manager accepted the
// task in the meantime), the error path returns without unregistering: an idle
// member that never ran, that the manager was never told about, that counts toward
// MaxAgents, and that the janitor can never retire (it requires idleAt != 0).
func TestConc_FailedSpawnLeaksARegisteredAgent(t *testing.T) {
	concGate(t)
	r := newRVRig(t, Config{MaxWriters: 4, MaxAgents: 3}, func(ctx context.Context, c *rvCall) rvReply { return rvReply{Text: "ok"} })
	r.sw.StartManager()
	tk, _ := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "work"})
	b := r.sw.Board
	b.mu.Lock() // freeze the board: Spawn will stall in Assign, after buildAgent registered the member
	var spawnErr error
	done := make(chan struct{})
	go func() {
		_, spawnErr = r.sw.Spawn(SpawnReq{Role: "backend", TaskID: tk.ID, By: "mgr"})
		close(done)
	}()
	time.Sleep(80 * time.Millisecond)
	cur := b.snap.Load() // the manager accepts the task concurrently
	cp := &Snapshot{Version: cur.Version + 1, Tasks: append([]Task(nil), cur.Tasks...), Agents: cur.Agents}
	cp.Tasks[0].Status = StatusDone
	b.snap.Store(cp)
	b.mu.Unlock()
	<-done
	if spawnErr == nil {
		t.Fatal("setup: Spawn should have failed in Assign")
	}
	r.sw.mu.Lock()
	var leaked []string
	for id, m := range r.sw.members {
		if id == "mgr" {
			continue
		}
		m.mu.Lock()
		if !m.running && m.idleAt.IsZero() {
			leaked = append(leaked, id)
		}
		m.mu.Unlock()
	}
	r.sw.mu.Unlock()
	if len(leaked) > 0 {
		t.Fatalf("Spawn failed (%v) but left %v registered: never started, unknown to the board, counted against MaxAgents=3, invisible to the janitor", spawnErr, leaked)
	}
}

// buildAgent publishes the member (roster, mail routing, MaxAgents) before Spawn has
// finished initialising it. A peer's mail in that window starts the worker with an
// empty kickoff; Spawn's own startRun then finds it running and its "Begin task"
// input is dropped, so the worker's first user turn is somebody's FYI. (Also an
// unsynchronised write of member.task, which -race reports.)
func TestConc_MailToAHalfBuiltWorkerReplacesItsKickoff(t *testing.T) {
	concGate(t)
	r := newRVRig(t, Config{MaxWriters: 4}, func(ctx context.Context, c *rvCall) rvReply { return rvReply{Text: "ok"} })
	r.sw.StartManager()
	tk, _ := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "the real work"})
	b := r.sw.Board
	b.mu.Lock() // Spawn stalls in Assign, after the member is registered
	spawned := make(chan struct{})
	go func() {
		_, _ = r.sw.Spawn(SpawnReq{Role: "backend", TaskID: tk.ID, By: "mgr"})
		close(spawned)
	}()
	time.Sleep(80 * time.Millisecond)
	mailed := make(chan error, 1)
	go func() { _, err := r.sw.Router.Send("mgr", "be-1", "info", "fyi: the API moved"); mailed <- err }()
	time.Sleep(80 * time.Millisecond)
	b.mu.Unlock()
	<-spawned
	if err := <-mailed; err != nil {
		t.Fatalf("setup: the half-built worker was not routable: %v", err)
	}
	rvWait(t, "worker idle", func() bool { return r.idle("be-1") && r.prov.callsFor("be-1") >= 1 })
	if !r.prov.sawEver("Begin task " + tk.ID) {
		t.Fatalf("be-1 owns %s but its first user turn was the peer's mail; the kickoff was dropped (startRun found it already running)", tk.ID)
	}
}

// Spawn registers the member in buildAgent and then looks it up again with
// s.get(id) and dereferences the result. Retire (exported; any UI or CLI may call
// it) removes any registered member that is not running, including one that Spawn
// has just built but not started, so the lookup returns nil and Spawn panics on
// `m.task = ...`: the process dies. (The janitor cannot trigger it: it skips
// members whose idleAt is zero.)
func TestConc_RetireDuringSpawnCrashesTheProcess(t *testing.T) {
	concGate(t)
	if testing.Short() {
		t.Skip("spawns a subprocess")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestConc_RetireSpawnChild$", "-test.v", "-test.count=1", "-test.timeout=60s")
	cmd.Env = append(os.Environ(), "SLEIPNIR_REVIEW_RETIRE_CHILD=1")
	out, err := cmd.CombinedOutput()
	if !strings.Contains(string(out), "SURVIVED") {
		txt := string(out)
		if i := strings.Index(txt, "panic:"); i >= 0 {
			txt = txt[i:]
		}
		if len(txt) > 700 {
			txt = txt[:700]
		}
		t.Fatalf("a concurrent Retire made Spawn dereference a nil member and killed the process (exit: %v):\n%s", err, txt)
	}
}

// TestConc_RetireSpawnChild is the subprocess body of the test above.
func TestConc_RetireSpawnChild(t *testing.T) {
	if os.Getenv("SLEIPNIR_REVIEW_RETIRE_CHILD") != "1" {
		t.Skip("subprocess only")
	}
	r := newRVRig(t, Config{MaxAgents: 1 << 20, MaxWriters: 1 << 20}, func(ctx context.Context, c *rvCall) rvReply { return rvReply{Text: "ok"} })
	r.sw.StartManager()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for g := 0; g < 3; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				for _, id := range r.sw.roster() {
					if id != "mgr" {
						_ = r.sw.Retire(id)
					}
				}
			}
		}()
	}
	deadline := time.Now().Add(8 * time.Second)
	var sp sync.WaitGroup
	for g := 0; g < 4; g++ {
		sp.Add(1)
		go func(g int) {
			defer sp.Done()
			for i := 0; time.Now().Before(deadline); i++ {
				_, _ = r.sw.Spawn(SpawnReq{Role: "reviewer", Title: fmt.Sprintf("job %d-%d", g, i), By: "mgr"})
			}
		}(g)
	}
	sp.Wait()
	close(stop)
	wg.Wait()
	fmt.Println("SURVIVED")
}

// Config.BudgetUSD is consulted in exactly one place: at Spawn. Agents that are
// already running are never stopped by it (AgentBudgetUSD is per agent and is 0 by
// default), so one long-running worker overshoots the swarm budget without limit.
func TestConc_SwarmBudgetDoesNotStopRunningAgents(t *testing.T) {
	concGate(t)
	r := newRVRig(t, Config{MaxWriters: 4, BudgetUSD: 3}, func(ctx context.Context, c *rvCall) rvReply {
		u := core.Usage{InputTokens: 1_000_000} // $4 per request
		if c.Role == "backend" && c.Assistants < 6 {
			return rvReply{Tools: []rvToolCall{{"read", map[string]any{"path": "a.go"}}}, Usage: u}
		}
		return rvReply{Text: "done", Usage: u}
	})
	r.sw.StartManager()
	id, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "endless work", By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	rvWait(t, "worker idle", func() bool { return r.idle(id) })
	if spent := r.sw.TotalCost(); spent > 3+4 { // budget plus at most one request of slack
		t.Fatalf("BudgetUSD is $3 but the worker ran on to $%.0f: nothing stops an agent that is already running", spent)
	}
}

// The production form of the dual-ownership race: a worker self-claims a task
// (task claim -> Board.Claim, which refuses if someone owns it) while the manager
// spawns a worker for the same task (Spawn checks the owner on a snapshot, then
// Assign overwrites unconditionally). Both are told success; the board keeps the
// spawned worker, and the claimer keeps working on a task it no longer owns.
func TestConc_SelfClaimRacingSpawnGivesTwoAgentsTheSameTask(t *testing.T) {
	concGate(t)
	gate := make(chan struct{})
	r := newRVRig(t, Config{MaxWriters: 4}, func(ctx context.Context, c *rvCall) rvReply {
		rvBlock(ctx, gate)
		return rvReply{Text: "ok"}
	})
	t.Cleanup(func() { close(gate) })
	r.sw.StartManager()
	tk, _ := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "contested task"})
	b := r.sw.Board
	b.mu.Lock() // freeze the board so both actions are in flight together
	claimed := make(chan *tools.Result, 1)
	go func() {
		claimed <- r.callTool(context.Background(), "task", "be-9", "backend", map[string]any{"action": "claim", "id": tk.ID})
	}()
	time.Sleep(60 * time.Millisecond) // the claim is queued on the board lock first
	spawned := make(chan error, 1)
	go func() { _, err := r.sw.Spawn(SpawnReq{Role: "backend", TaskID: tk.ID, By: "mgr"}); spawned <- err }()
	time.Sleep(60 * time.Millisecond) // Spawn read Owner=="" and is queued behind the claim
	b.mu.Unlock()
	claim := <-claimed
	spawnErr := <-spawned
	cur, _ := b.Snapshot().Task(tk.ID)
	if !claim.IsError && spawnErr == nil {
		t.Fatalf("be-9 was told %q and the manager's Spawn succeeded, but the board says %s is owned by %s: two agents are working on it and one of them has been silently disowned", strings.TrimSpace(claim.Text), tk.ID, cur.Owner)
	}
}

// The scope-overlap refusal is a check on a snapshot followed by a separate board
// mutation (claim, spawn, and scope widening all do it), so two actions in flight
// together both pass: two tasks with overlapping scopes end up "doing" at once,
// which sequentially is exactly what the harness refuses ("overlap refused").
func TestConc_ConcurrentClaimsBypassTheScopeOverlapCheck(t *testing.T) {
	concGate(t)
	r := newRVRig(t, Config{MaxWriters: 4}, func(ctx context.Context, c *rvCall) rvReply { return rvReply{Text: "ok"} })
	r.sw.StartManager()
	t1, _ := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "handlers", Files: []string{"api/**"}})
	t2, _ := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "handler tests", Files: []string{"api/handlers/**"}})
	// Sequentially the second claim is refused:
	if res := r.callTool(context.Background(), "task", "be-8", "backend", map[string]any{"action": "claim", "id": t1.ID}); res.IsError {
		t.Fatalf("setup: %s", res.Text)
	}
	if res := r.callTool(context.Background(), "task", "be-9", "backend", map[string]any{"action": "claim", "id": t2.ID}); !res.IsError {
		t.Fatal("setup: a sequential overlapping claim must be refused")
	}
	// Fresh tasks, same scopes, claims in flight together:
	t3, _ := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "handlers v2", Files: []string{"web/**"}})
	t4, _ := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "components", Files: []string{"web/components/**"}})
	b := r.sw.Board
	b.mu.Lock()
	results := make(chan *tools.Result, 2)
	for i, id := range []string{t3.ID, t4.ID} {
		go func(i int, id string) {
			results <- r.callTool(context.Background(), "task", fmt.Sprintf("fe-%d", i), "frontend", map[string]any{"action": "claim", "id": id})
		}(i, id)
	}
	time.Sleep(100 * time.Millisecond)
	b.mu.Unlock()
	a, c := <-results, <-results
	if !a.IsError && !c.IsError {
		t.Fatalf("both claims succeeded (%q, %q): tasks %s (web/**) and %s (web/components/**) are now doing at the same time", strings.TrimSpace(a.Text), strings.TrimSpace(c.Text), t3.ID, t4.ID)
	}
}
