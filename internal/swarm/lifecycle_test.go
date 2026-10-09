package swarm

// Regression tests for the swarm runtime's lifecycle and failure handling
// (docs/SWARM-PROTOCOL.md, findings C-01 to C-05, C-10, C-14): mail that
// arrives while a worker finishes, ownership and caps under concurrency, member
// state transitions, containment of panics, budget and shutdown. Interleavings are
// forced with locks and channels rather than left to scheduler luck.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/tools"
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

// looseRouter removes the mail rate limits so tests can send freely.
func looseRouter() RouterConfig {
	return RouterConfig{MaxPerMinute: 1 << 30, MaxPerPairPerMin: 1 << 30, MaxChars: 600, DedupeWindow: time.Nanosecond}
}

// A message that arrives while the worker's final (tool-less) request is in flight
// is queued in the agent's inbox, but Run returns without draining it. The swarm
// notices when the worker goes idle and starts it again, so the mail is processed.
func TestMailArrivingDuringFinalAnswerIsProcessed(t *testing.T) {
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
	rvWait(t, "the worker to see the mail", func() bool { return r.prov.sawEver("[mail m1") })
	rvWait(t, "worker to go idle", func() bool { return r.idle(id) })
	if pending := r.sw.get(id).a.PendingInbox(); pending != 0 {
		t.Fatalf("%d messages still queued for an idle worker", pending)
	}
}

// A manager's reject while the worker is still writing the final summary of the run
// that produced the reviewed result must not be undone by that run ending: the task
// stays with the worker (the run does not own the new assignment) and the worker
// hears the feedback.
func TestRejectDuringFinalAnswerIsNotUndone(t *testing.T) {
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
		return rvReply{Text: "fixed it"}
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
	if tk, _ := r.sw.Board.Snapshot().Task("T1"); tk.Status != StatusDoing || tk.Owner != id {
		t.Fatalf("after reject T1 = %s/%s, want doing/%s", tk.Status, tk.Owner, id)
	}
	close(gate)
	rvWait(t, "the worker to hear the feedback", func() bool { return r.prov.sawEver("fix the failing test") })
	rvWait(t, "T1 to come back to review with the new summary", func() bool {
		tk, _ := r.sw.Board.Snapshot().Task("T1")
		return tk.Status == StatusReview && tk.Result == "fixed it" && r.idle(id)
	})
}

// A reused worker gets its new task card and the mail that arrived while it was
// being reassigned: neither replaces the other (a run that started for the mail
// used to make Spawn's card vanish).
func TestReuseCardAndMailArriveTogether(t *testing.T) {
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
	time.Sleep(60 * time.Millisecond) // Spawn reserved the worker and stalled in Board.assignTask
	mailed := make(chan error, 1)
	go func() { _, err := r.sw.Router.Send("mgr", id, "info", "fyi: heads up"); mailed <- err }()
	if err := <-mailed; err != nil {
		b.mu.Unlock()
		t.Fatalf("mail to a worker being reassigned was refused: %v", err)
	}
	b.mu.Unlock()
	<-spawned
	if spawnErr != nil {
		t.Fatalf("Spawn: %v", spawnErr)
	}
	rvWait(t, "the card and the mail to reach the model", func() bool {
		return r.prov.sawEver("Your assignment is task "+t2.ID+":") && r.prov.sawEver("[mail m1")
	})
	rvWait(t, "worker idle", func() bool { return r.idle(id) })
	if tk, _ := b.Snapshot().Task(t2.ID); tk.Owner != id || tk.Status != StatusReview {
		t.Fatalf("%s = %s/%s", t2.ID, tk.Owner, tk.Status)
	}
}

// The writer cap counts reuse: giving an idle writer new work while the limit of
// running writers is reached is refused, and nothing is left behind.
func TestWriterCapAppliesToReuse(t *testing.T) {
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
		t.Fatal("a third writer must be refused while one is active")
	}
	tasks := len(r.sw.Board.Snapshot().Tasks)
	_, err = r.sw.Spawn(SpawnReq{Agent: id1, Title: "D", By: "mgr"})
	if err == nil || !strings.Contains(err.Error(), "writers are already active") {
		t.Fatalf("reusing an idle writer at the cap must be refused, got %v", err)
	}
	if w, _ := rvWorkers(r); w != 1 {
		t.Fatalf("MaxWriters=1 but %d writers are running", w)
	}
	if !r.idle(id1) {
		t.Fatal("the refused reuse left the idle worker reserved")
	}
	if n := len(r.sw.Board.Snapshot().Tasks); n != tasks {
		t.Fatalf("the refused reuse created %d task(s)", n-tasks)
	}
}

// The caps are enforced under one lock: concurrent spawns cannot exceed them.
func TestConcurrentSpawnRespectsWriterAndAgentCaps(t *testing.T) {
	spawnMany := func(t *testing.T, cfg Config, role string) (ok int, writers, total int) {
		gate := make(chan struct{})
		r := newRVRig(t, cfg, func(ctx context.Context, c *rvCall) rvReply {
			rvBlock(ctx, gate)
			return rvReply{Text: "ok"}
		})
		t.Cleanup(func() { close(gate) })
		r.sw.StartManager()
		var wg sync.WaitGroup
		var n atomic.Int32
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
		ok, writers, _ := spawnMany(t, Config{MaxWriters: 2, MaxWorkers: 40}, "backend")
		if ok != 2 || writers != 2 {
			t.Fatalf("MaxWriters=2 but %d Spawn calls succeeded and %d writers are running", ok, writers)
		}
	})
	t.Run("agents", func(t *testing.T) {
		ok, _, total := spawnMany(t, Config{MaxWriters: 40, MaxWorkers: 3}, "reviewer")
		if ok != 3 || total != 4 {
			t.Fatalf("MaxWorkers=3 but %d Spawn calls succeeded and %d agents (the manager and its workers) are registered", ok, total)
		}
	})
}

// A task has one owner: of two spawns for it, one wins and the other is refused.
func TestTwoWorkersCannotOwnTheSameTask(t *testing.T) {
	gate := make(chan struct{})
	r := newRVRig(t, Config{MaxWorkers: 40}, func(ctx context.Context, c *rvCall) rvReply {
		rvBlock(ctx, gate)
		return rvReply{Text: "ok"}
	})
	t.Cleanup(func() { close(gate) })
	r.sw.StartManager()
	task, _ := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "shared work"})
	var wg sync.WaitGroup
	var ok atomic.Int32
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := r.sw.Spawn(SpawnReq{Role: "reviewer", TaskID: task.ID, By: "mgr"}); err == nil {
				ok.Add(1)
			}
		}()
	}
	wg.Wait()
	if n := ok.Load(); n != 1 {
		tk, _ := r.sw.Board.Snapshot().Task(task.ID)
		t.Fatalf("%d workers were started on %s; the board owner is %s", n, task.ID, tk.Owner)
	}
	if _, total := rvWorkers(r); total != 2 {
		t.Fatalf("%d agents registered, want the manager and one worker", total)
	}
}

// Self-claim and spawn compete for one task through the same compare-and-set on the
// board: exactly one of them wins, every time.
func TestSelfClaimRacingSpawnHasOneWinner(t *testing.T) {
	r := newRVRig(t, Config{MaxWorkers: 200, MaxWriters: 200}, func(ctx context.Context, c *rvCall) rvReply { return rvReply{Text: "ok"} })
	r.sw.StartManager()
	for i := 0; i < 30; i++ {
		tk, _ := r.sw.Board.CreateTask("mgr", TaskSpec{Title: fmt.Sprintf("contested %d", i)})
		var claim *tools.Result
		var spawnErr error
		var spawnID string
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			claim = r.callTool(context.Background(), "task", "be-9", "backend", map[string]any{"action": "claim", "id": tk.ID})
		}()
		go func() {
			defer wg.Done()
			spawnID, spawnErr = r.sw.Spawn(SpawnReq{Role: "reviewer", TaskID: tk.ID, By: "mgr"})
		}()
		wg.Wait()
		cur, _ := r.sw.Board.Snapshot().Task(tk.ID)
		switch {
		case !claim.IsError && spawnErr == nil:
			t.Fatalf("iteration %d: be-9 was told %q and Spawn started %s, but the board owner is %s", i, strings.TrimSpace(claim.Text), spawnID, cur.Owner)
		case claim.IsError && spawnErr != nil:
			t.Fatalf("iteration %d: nobody got the task (%q, %v)", i, claim.Text, spawnErr)
		case !claim.IsError && cur.Owner != "be-9", spawnErr == nil && cur.Owner != spawnID:
			t.Fatalf("iteration %d: the winner is not the owner: %s", i, cur.Owner)
		}
	}
}

// Overlapping scopes are refused inside the board's critical section, so claims that
// are in flight together cannot both pass.
func TestConcurrentClaimsRespectScopeOverlap(t *testing.T) {
	r := newRVRig(t, Config{MaxWriters: 20}, func(ctx context.Context, c *rvCall) rvReply { return rvReply{Text: "ok"} })
	r.sw.StartManager()
	var ids []string
	for i := 0; i < 8; i++ {
		tk, _ := r.sw.Board.CreateTask("mgr", TaskSpec{Title: fmt.Sprintf("web work %d", i), Files: []string{fmt.Sprintf("web/%s**", strings.Repeat("x/", i%2))}})
		ids = append(ids, tk.ID)
	}
	results := make(chan *tools.Result, len(ids))
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			results <- r.callTool(context.Background(), "task", fmt.Sprintf("fe-%d", i), "frontend", map[string]any{"action": "claim", "id": id})
		}(i, id)
	}
	wg.Wait()
	close(results)
	won := 0
	for res := range results {
		if !res.IsError {
			won++
		}
	}
	if won != 1 {
		t.Fatalf("%d overlapping claims succeeded (web/** and web/x/** collide)", won)
	}
}

// Retire and mail-triggered runs race: whichever wins, the roster and the board
// agree afterwards (no running ghost that is off the roster but back on the board).
func TestRetireRacingWakeLeavesNoGhost(t *testing.T) {
	r := newRVRig(t, Config{MaxWorkers: 100, MaxWriters: 100, Router: looseRouter()}, func(ctx context.Context, c *rvCall) rvReply {
		time.Sleep(200 * time.Microsecond)
		return rvReply{Text: "ok"}
	})
	r.sw.StartManager()
	var ids []string
	for i := 0; i < 8; i++ {
		id, err := r.sw.Spawn(SpawnReq{Role: "reviewer", Title: fmt.Sprintf("j%d", i), By: "mgr"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	for _, id := range ids {
		id := id
		rvWait(t, "idle", func() bool { return r.idle(id) })
	}
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(2)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				_ = r.sw.Retire(ids[(g+i)%len(ids)])
			}
		}(g)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				_, _ = r.sw.Router.Send("mgr", ids[(g*3+i)%len(ids)], "info", fmt.Sprintf("ping %d.%d", g, i))
			}
		}(g)
	}
	wg.Wait()
	rvWait(t, "every run to finish", func() bool {
		for _, id := range r.sw.roster() {
			if r.running(id) {
				return false
			}
		}
		return true
	})
	time.Sleep(100 * time.Millisecond)
	snap := r.sw.Board.Snapshot()
	members := map[string]bool{}
	for _, id := range r.sw.roster() {
		members[id] = true
	}
	for _, a := range snap.Agents {
		if !members[a.ID] {
			t.Errorf("%s is on the board but not in the swarm (a ghost)", a.ID)
		}
	}
	for id := range members {
		if _, ok := snap.Agent(id); !ok {
			t.Errorf("%s is in the swarm but not on the board", id)
		}
	}
}

// Retiring an agent folds its spend into the swarm's ledger: the budget does not
// reopen.
func TestRetireKeepsSpendInTheBudget(t *testing.T) {
	r := newRVRig(t, Config{MaxWriters: 4, BudgetUSD: 3}, func(ctx context.Context, c *rvCall) rvReply {
		return rvReply{Text: "ok", Usage: core.Usage{InputTokens: 1_000_000}} // $4 at 4$/M
	})
	r.sw.StartManager()
	id, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "A", By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	rvWait(t, "worker idle", func() bool { return r.idle(id) })
	if _, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "B", By: "mgr"}); err == nil || !strings.Contains(err.Error(), "budget") {
		t.Fatalf("budget $3 is spent ($%.2f) yet Spawn was allowed: %v", r.sw.TotalCost(), err)
	}
	before := r.sw.TotalCost()
	if err := r.sw.Retire(id); err != nil {
		t.Fatal(err)
	}
	if after := r.sw.TotalCost(); after < before {
		t.Fatalf("TotalCost fell from $%.2f to $%.2f when the agent was retired", before, after)
	}
	if _, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "C", By: "mgr"}); err == nil {
		t.Fatal("the swarm budget reopened after a retirement")
	}
}

// The swarm budget stops agents that are already running: no request is admitted
// once it is spent, their tasks return to todo without counting against them, and
// the manager is told once.
func TestSwarmBudgetStopsRunningAgents(t *testing.T) {
	r := newRVRig(t, Config{MaxWriters: 4, BudgetUSD: 3}, func(ctx context.Context, c *rvCall) rvReply {
		u := core.Usage{InputTokens: 1_000_000} // $4 per request
		if c.Role == "backend" {
			return rvReply{Tools: []rvToolCall{{"read", map[string]any{"path": "a.go"}}}, Usage: u}
		}
		return rvReply{Text: "done", Usage: u}
	})
	r.sw.StartManager()
	id, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "endless work", By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	rvWait(t, "worker to stop", func() bool { return r.idle(id) })
	if spent := r.sw.TotalCost(); spent > 3+4 { // budget plus at most one request of slack
		t.Fatalf("BudgetUSD is $3 but the worker ran on to $%.0f", spent)
	}
	tk, _ := r.sw.Board.Snapshot().Task("T1")
	if tk.Status != StatusTodo || tk.Attempts != 0 {
		t.Fatalf("T1 = %s attempts=%d, want todo with no attempt counted (the swarm ran out of money, the worker did nothing wrong)", tk.Status, tk.Attempts)
	}
	rvWait(t, "the manager to be told", func() bool {
		for _, e := range r.log.OfType(events.TypeMailSend) {
			if strings.Contains(string(e.Data), "budget") {
				return true
			}
		}
		return false
	})
}

// A task the worker claimed during its run is settled with the one it was spawned
// on; none is left "doing" under an idle owner.
func TestClaimedTasksAreSettledWhenTheRunEnds(t *testing.T) {
	r := newRVRig(t, Config{MaxWriters: 4}, func(ctx context.Context, c *rvCall) rvReply {
		if c.Role == "backend" && c.Assistants == 0 {
			return rvReply{Tools: []rvToolCall{{"task", map[string]any{"action": "claim", "id": "T2"}}}}
		}
		return rvReply{Text: "finished both"}
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
	for _, tid := range []string{t1.ID, t2.ID} {
		if tk, _ := s.Task(tid); tk.Status != StatusReview {
			t.Fatalf("%s = %s/%s after the run, want review", tid, tk.Status, tk.Owner)
		}
	}
}

// A reject always reaches the worker (the harness delivers it, the router's limits do
// not apply), and when the worker is gone the task returns to the pool instead of
// waiting on an owner who does not exist.
func TestRejectReachesTheWorkerOrPoolsTheTask(t *testing.T) {
	r := newRVRig(t, Config{MaxWriters: 4}, func(ctx context.Context, c *rvCall) rvReply { return rvReply{Text: "ok"} })
	r.sw.StartManager()
	id, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "work", By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	settled := func(calls int) func() bool {
		return func() bool {
			tk, _ := r.sw.Board.Snapshot().Task("T1")
			return tk.Status == StatusReview && r.idle(id) && r.prov.callsFor(id) >= calls
		}
	}
	rvWait(t, "T1 in review", settled(1))
	reject := func(text string) *tools.Result {
		return r.callTool(context.Background(), "task", "mgr", "manager", map[string]any{"action": "reject", "id": "T1", "text": text})
	}
	for i, calls := range []int{2, 3} { // the same text twice: no dedupe on the harness's path
		if res := reject("fix it"); res.IsError {
			t.Fatalf("reject #%d: %s", i+1, res.Text)
		}
		rvWait(t, "the worker to handle the rejection and settle", settled(calls))
	}
	if err := r.sw.Retire(id); err != nil {
		t.Fatal(err)
	}
	res := reject("fix it properly")
	if res.IsError {
		t.Fatalf("reject to a retired owner: %s", res.Text)
	}
	if tk, _ := r.sw.Board.Snapshot().Task("T1"); tk.Status != StatusTodo || tk.Owner != "" {
		t.Fatalf("after rejecting work whose worker is gone T1 = %s/%s, want todo with no owner", tk.Status, tk.Owner)
	}
}

// A refused Spawn creates nothing: no task, no agent, no id consumed.
func TestRefusedSpawnLeavesNothingBehind(t *testing.T) {
	r := newRVRig(t, Config{MaxWriters: 1, MaxWorkers: 2}, func(ctx context.Context, c *rvCall) rvReply {
		time.Sleep(300 * time.Millisecond) // keep the first worker busy
		return rvReply{Text: "ok"}
	})
	r.sw.StartManager()
	for i := 0; i < 3; i++ {
		if _, err := r.sw.Spawn(SpawnReq{Role: "nonesuch", Title: "will be refused", By: "mgr"}); err == nil {
			t.Fatal("unknown role must be refused")
		}
	}
	if n := len(r.sw.Board.Snapshot().Tasks); n != 0 {
		t.Fatalf("three refused spawns left %d orphan todo tasks on the board", n)
	}
	id, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "API", Files: []string{"api/**"}, By: "mgr"})
	if err != nil || id != "be-1" {
		t.Fatalf("first real spawn: %q %v", id, err)
	}
	if _, err := r.sw.Spawn(SpawnReq{Role: "reviewer", Title: "overlapping", Files: []string{"api/x/**"}, By: "mgr"}); err != nil {
		t.Fatalf("a reader may share a writer's area: %v", err)
	}
	if _, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "overlap", Files: []string{"api/v2/**"}, By: "mgr"}); err == nil {
		t.Fatal("a second writer on the same area must be refused (by scope or by the writer cap)")
	}
	tasks := r.sw.Board.Snapshot().Tasks
	if len(tasks) != 2 {
		t.Fatalf("board has %d tasks, want the 2 that were started", len(tasks))
	}
	if _, err := r.sw.Spawn(SpawnReq{Role: "reviewer", Title: "one more", By: "mgr"}); err == nil || !strings.Contains(err.Error(), "worker limit") {
		t.Fatalf("MaxWorkers=2 must refuse the third worker: %v", err)
	}
	if n := len(r.sw.Board.Snapshot().Tasks); n != 2 {
		t.Fatalf("the refused spawn created a task (%d tasks)", n)
	}
}

// Reusing a worker while its previous run is still finishing is refused: the old
// run cannot overwrite the new run's status with a late "idle".
func TestReuseDuringFinishRunIsRefusedNotOverwritten(t *testing.T) {
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
	r.sw.Leases.mu.Lock() // finishRun stalls in ReleaseAll
	id, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "first", By: "mgr"})
	if err != nil {
		r.sw.Leases.mu.Unlock()
		t.Fatal(err)
	}
	rvWait(t, "the first task to be settled", func() bool { tk, _ := r.sw.Board.Snapshot().Task("T1"); return tk.Status == StatusReview })
	if _, err := r.sw.Spawn(SpawnReq{Agent: id, Title: "second", By: "mgr"}); err == nil || !strings.Contains(err.Error(), "still working") {
		r.sw.Leases.mu.Unlock()
		t.Fatalf("reuse of a worker that is still finishing must be refused, got %v", err)
	}
	r.sw.Leases.mu.Unlock()
	rvWait(t, "worker idle", func() bool { return r.idle(id) })
	if _, err := r.sw.Spawn(SpawnReq{Agent: id, Title: "second", By: "mgr"}); err != nil {
		t.Fatalf("reuse once idle: %v", err)
	}
	rvWait(t, "second run to reach the model", func() bool { return r.prov.callsFor(id) >= 2 })
	time.Sleep(100 * time.Millisecond)
	if a, _ := r.sw.Board.Snapshot().Agent(id); a.State != "running" || !r.running(id) {
		t.Fatalf("%s is running its second task but the board says %q", id, a.State)
	}
}

// A panic anywhere on a worker's run path (here the provider adapter) is contained:
// the process survives, the worker is marked failed, its task goes back to todo and
// the manager is told in one line.
func TestWorkerPanicIsContained(t *testing.T) {
	r := newRVRig(t, Config{MaxWriters: 4}, func(ctx context.Context, c *rvCall) rvReply {
		if c.Role == "backend" {
			panic("adapter bug: nil pointer in worker request")
		}
		return rvReply{Text: "ok"}
	})
	r.sw.StartManager()
	id, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "doomed", By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	rvWait(t, "the crashed worker to settle and notify the manager", func() bool {
		a, _ := r.sw.Board.Snapshot().Agent(id)
		// A worker becomes available before its failure notice is published.
		return a.State == "failed" && r.idle(id) && mailSent(r, "crashed") > 0
	})
	tk, _ := r.sw.Board.Snapshot().Task("T1")
	if tk.Status != StatusTodo || tk.Owner != "" || tk.Attempts != 1 {
		t.Fatalf("T1 = %s/%s attempts=%d after the crash, want todo, unowned, 1 attempt", tk.Status, tk.Owner, tk.Attempts)
	}
	if h := r.sw.Leases.HeldBy(id); len(h) != 0 {
		t.Fatalf("leases still held: %v", h)
	}
	var notices int
	for _, e := range r.log.OfType(events.TypeMailSend) {
		if strings.Contains(string(e.Data), "returned to todo") && strings.Contains(string(e.Data), "crashed") {
			notices++
		}
	}
	if notices != 1 {
		t.Fatalf("the manager got %d one-line notices about the crash, want 1", notices)
	}
	if len(r.log.OfType("agent.panic")) == 0 {
		t.Fatal("the panic was not logged")
	}
	// The pool works: the task can be given to another worker.
	if _, err := r.sw.Spawn(SpawnReq{Role: "reviewer", TaskID: "T1", By: "mgr"}); err != nil {
		t.Fatalf("respawn on the requeued task: %v", err)
	}
}

// After MaxAttempts stops without finishing, the task is failed instead of requeued.
func TestTaskFailsAfterMaxAttempts(t *testing.T) {
	r := newRVRig(t, Config{MaxWriters: 4, MaxAttempts: 2}, func(ctx context.Context, c *rvCall) rvReply {
		if c.Role == "backend" {
			panic("boom")
		}
		return rvReply{Text: "ok"}
	})
	r.sw.StartManager()
	tk, _ := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "cursed"})
	for i := 1; i <= 2; i++ {
		id, err := r.sw.Spawn(SpawnReq{Role: "backend", TaskID: tk.ID, By: "mgr"})
		if err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
		rvWait(t, "the worker to crash", func() bool { a, _ := r.sw.Board.Snapshot().Agent(id); return a.State == "failed" && r.idle(id) })
	}
	got, _ := r.sw.Board.Snapshot().Task(tk.ID)
	if got.Status != StatusFailed || got.Attempts != 2 {
		t.Fatalf("%s = %s attempts=%d, want failed after 2 attempts", tk.ID, got.Status, got.Attempts)
	}
	if _, err := r.sw.Spawn(SpawnReq{Role: "backend", TaskID: tk.ID, By: "mgr"}); err == nil {
		t.Fatal("a failed task must not be spawned again without reopen")
	}
	res := r.callTool(context.Background(), "task", "mgr", "manager", map[string]any{"action": "reopen", "id": tk.ID})
	if res.IsError {
		t.Fatalf("reopen: %s", res.Text)
	}
	if again, _ := r.sw.Board.Snapshot().Task(tk.ID); again.Status != StatusTodo || again.Attempts != 0 {
		t.Fatalf("reopened task = %s attempts=%d", again.Status, again.Attempts)
	}
}

type panicSink struct{ agent.NopSink }

func (panicSink) ToolStart(string, core.Block) { panic("ui bug in ToolStart") }

// A panic in the UI sink, which runs on the agent's goroutines, does not take the
// worker (or the process) down.
func TestSinkPanicIsContained(t *testing.T) {
	r := newRVRigWith(t, Config{MaxWriters: 4}, func(ctx context.Context, c *rvCall) rvReply {
		if c.Role == "backend" && c.Assistants == 0 {
			return rvReply{Tools: []rvToolCall{{"read", map[string]any{"path": "a.go"}}}}
		}
		return rvReply{Text: "done"}
	}, func(d *Deps) { d.NewSink = func(string) agent.Sink { return panicSink{} } })
	r.sw.StartManager()
	if _, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "work", By: "mgr"}); err != nil {
		t.Fatal(err)
	}
	rvWait(t, "T1 to reach review despite the sink", func() bool { tk, _ := r.sw.Board.Snapshot().Task("T1"); return tk.Status == StatusReview })
}

// What the verify gate did is counted, so that a run can say it ran: how many times the command was run and how many of them failed.
func TestTheVerifyGateCountsItsRuns(t *testing.T) {
	var n atomic.Int32
	cfg := Config{MaxWriters: 4, VerifyCmd: "go test ./...", Verify: func(ctx context.Context, dir, cmd string) (string, int, error) {
		if n.Add(1) == 1 {
			return "FAIL", 1, nil
		}
		return "ok", 0, nil
	}}
	r := newRVRig(t, cfg, func(ctx context.Context, c *rvCall) rvReply {
		if c.Role == "backend" && c.Assistants < 2 {
			return rvReply{Tools: []rvToolCall{{"task", map[string]any{"action": "done", "id": "T1", "text": "x"}}}}
		}
		return rvReply{Text: "summary"}
	})
	r.sw.StartManager()
	id, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "work", By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	rvWait(t, "worker idle", func() bool { return r.idle(id) })
	if ran, failed := r.sw.VerifyRuns(); ran != 2 || failed != 1 {
		t.Fatalf("VerifyRuns() = %d ran, %d failed; want 2 and 1", ran, failed)
	}
}

// The verifier runs under a harness deadline: a verifier that hangs (and ignores its
// context) neither blocks done past the deadline nor fails the task: an infrastructure
// problem is reported as such.
func TestVerifierHasADeadlineAndAnInfraErrorNeverFailsATask(t *testing.T) {
	var hasDeadline atomic.Bool
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	cfg := Config{MaxWriters: 4, VerifyTimeout: 150 * time.Millisecond, VerifyCmd: "go test ./...", Verify: func(ctx context.Context, dir, cmd string) (string, int, error) {
		_, ok := ctx.Deadline()
		hasDeadline.Store(ok)
		<-block // ignores ctx
		return "ok", 0, nil
	}}
	var sawInfra atomic.Bool
	r := newRVRig(t, cfg, func(ctx context.Context, c *rvCall) rvReply {
		if c.Role == "backend" && c.Assistants == 0 {
			return rvReply{Tools: []rvToolCall{{"task", map[string]any{"action": "done", "id": "T1", "text": "x"}}}}
		}
		if c.Sees("verification could not run") {
			sawInfra.Store(true)
		}
		return rvReply{Text: "summary"}
	})
	r.sw.StartManager()
	id, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "work", By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	rvWait(t, "worker idle", func() bool { return r.idle(id) })
	if !hasDeadline.Load() {
		t.Fatal("Verify ran with a context that has no deadline")
	}
	if !sawInfra.Load() {
		t.Fatal("the worker was not told that verification could not run")
	}
	tk, _ := r.sw.Board.Snapshot().Task("T1")
	if tk.Status != StatusReview || !strings.Contains(tk.Evidence, "could not run") {
		t.Fatalf("T1 = %s (evidence %q): an infrastructure error must not fail the task, and must be reported", tk.Status, tk.Evidence)
	}
}

// Cancelling the manager's run stops its workers too, and their tasks return to todo.
func TestCancellingTheManagerStopsItsWorkers(t *testing.T) {
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
	rvWait(t, "the worker to stop", func() bool { return r.idle("be-1") })
	tk, _ := r.sw.Board.Snapshot().Task("T1")
	if tk.Status != StatusTodo || tk.Attempts != 0 {
		t.Fatalf("T1 = %s attempts=%d after the interrupt, want todo (nobody failed)", tk.Status, tk.Attempts)
	}
}

// After Shutdown nothing starts: Spawn is refused, Start does not revive the swarm.
func TestSpawnAfterShutdownIsRefused(t *testing.T) {
	r := newRVRig(t, Config{MaxWriters: 4}, func(ctx context.Context, c *rvCall) rvReply { return rvReply{Text: "ok"} })
	r.sw.StartManager()
	r.sw.Shutdown()
	r.sw.Start(context.Background())
	if id, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "late work", By: "mgr"}); err == nil {
		t.Fatalf("Spawn after Shutdown succeeded (%s)", id)
	}
	if n := len(r.sw.Board.Snapshot().Tasks); n != 0 {
		t.Fatalf("the refused spawn created %d task(s)", n)
	}
	r.sw.Shutdown() // twice is harmless
}

// Start is safe to call on every turn: while the swarm runs it is a no-op, and once
// its context has ended it starts again on the new one.
func TestStartIsIdempotentAndRevivesAfterItsContextEnds(t *testing.T) {
	r := newRVRig(t, Config{MaxWriters: 4}, func(ctx context.Context, c *rvCall) rvReply { return rvReply{Text: "ok"} })
	r.sw.Start(context.Background())
	r.sw.Start(context.Background())
	first := r.sw.rootCtx
	r.sw.mu.Lock()
	cancel := r.sw.cancel
	r.sw.mu.Unlock()
	cancel()
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	r.sw.Start(ctx)
	if r.sw.rootCtx == first || r.sw.rootCtx.Err() != nil {
		t.Fatal("Start did not revive the swarm on the new context")
	}
	r.sw.StartManager()
	if _, err := r.sw.Spawn(SpawnReq{Role: "reviewer", Title: "after the revival", By: "mgr"}); err != nil {
		t.Fatal(err)
	}
}

// The manager persists across turns: StartManager returns the same agent, so a second
// turn continues its thread.
func TestManagerPersistsAcrossTurns(t *testing.T) {
	r := newRVRig(t, Config{}, func(ctx context.Context, c *rvCall) rvReply { return rvReply{Text: "ok"} })
	a1, err := r.sw.StartManager()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.sw.RunManager(context.Background(), "turn one"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.sw.RunManager(context.Background(), "turn two"); err != nil {
		t.Fatal(err)
	}
	if a2 := r.sw.Manager(); a2 != a1 {
		t.Fatal("the second turn built a new manager (its thread is lost)")
	}
	if turns := len(a1.Thread().Snapshot().Turns); turns < 4 {
		t.Fatalf("manager thread has %d turns after two runs", turns)
	}
}

// A worker that has not been registered yet cannot be mailed: the roster only lists
// members that are fully built and assigned, so a peer's mail cannot replace its
// kickoff. Nothing is registered when the assignment fails either.
func TestHalfBuiltWorkerIsNotRoutable(t *testing.T) {
	r := newRVRig(t, Config{MaxWriters: 4, MaxWorkers: 2}, func(ctx context.Context, c *rvCall) rvReply { return rvReply{Text: "ok"} })
	r.sw.StartManager()
	tk, _ := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "the real work"})
	b := r.sw.Board
	b.mu.Lock() // Spawn stalls in Board.assignTask, before anything is registered
	spawned := make(chan struct{})
	go func() {
		_, _ = r.sw.Spawn(SpawnReq{Role: "backend", TaskID: tk.ID, By: "mgr"})
		close(spawned)
	}()
	time.Sleep(80 * time.Millisecond)
	_, mailErr := r.sw.Router.Send("mgr", "be-1", "info", "fyi: the API moved")
	b.mu.Unlock()
	<-spawned
	if mailErr == nil {
		t.Fatal("mail to a worker that is not registered yet was accepted")
	}
	rvWait(t, "worker idle", func() bool { return r.idle("be-1") && r.prov.callsFor("be-1") >= 1 })
	if !r.prov.sawEver("Begin task " + tk.ID) {
		t.Fatalf("be-1's first user turn was not its kickoff")
	}

	// A failed assignment registers nothing.
	tk2, _ := b.CreateTask("mgr", TaskSpec{Title: "already done"})
	_ = b.Claim("x-1", tk2.ID)
	_ = b.Submit("x-1", tk2.ID, "r", "e")
	_ = b.Accept("mgr", tk2.ID, "")
	before := r.sw.roster()
	if _, err := r.sw.Spawn(SpawnReq{Role: "backend", TaskID: tk2.ID, By: "mgr"}); err == nil {
		t.Fatal("a done task must not be spawned on")
	}
	if after := r.sw.roster(); len(after) != len(before) {
		t.Fatalf("a failed Spawn changed the roster: %v -> %v", before, after)
	}
}

// Spawn and Retire hammering each other must never panic (a retired member used to
// be dereferenced as nil) or leave a ghost behind.
func TestRetireAndSpawnRaceDoesNotCrash(t *testing.T) {
	r := newRVRig(t, Config{MaxWorkers: 1 << 20, MaxWriters: 1 << 20}, func(ctx context.Context, c *rvCall) rvReply { return rvReply{Text: "ok"} })
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
	deadline := time.Now().Add(1500 * time.Millisecond)
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
	rvWait(t, "every run to finish", func() bool {
		for _, id := range r.sw.roster() {
			if r.running(id) {
				return false
			}
		}
		return true
	})
	time.Sleep(50 * time.Millisecond)
	members := map[string]bool{}
	for _, id := range r.sw.roster() {
		members[id] = true
	}
	for _, a := range r.sw.Board.Snapshot().Agents {
		if !members[a.ID] {
			t.Errorf("ghost %s on the board", a.ID)
		}
	}
}

// Shutdown is bounded: a tool that ignores its context cannot hold it past
// ShutdownGrace.
func TestShutdownIsBoundedByAToolThatIgnoresItsContext(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	t.Cleanup(func() { close(release) })
	r := newRVRig(t, Config{MaxWriters: 4, ShutdownGrace: 300 * time.Millisecond}, func(ctx context.Context, c *rvCall) rvReply {
		if c.Role == "backend" && c.Assistants == 0 {
			return rvReply{Tools: []rvToolCall{{"stuck", map[string]any{}}}}
		}
		return rvReply{Text: "summary"}
	})
	r.sw.deps.Registry.Register(rvFakeTool{name: "stuck", run: func(ctx context.Context, c *tools.Call) *tools.Result {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release // ignores ctx
		return &tools.Result{Text: "finally"}
	}})
	r.sw.StartManager()
	if _, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "work", By: "mgr"}); err != nil {
		t.Fatal(err)
	}
	<-entered
	done := make(chan struct{})
	start := time.Now()
	go func() { r.sw.Shutdown(); close(done) }()
	select {
	case <-done:
		if d := time.Since(start); d < 250*time.Millisecond {
			t.Fatalf("Shutdown returned after %v without waiting for the running tool", d)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Shutdown is still blocked 3s after cancellation: it waits for every agent goroutine with no deadline")
	}
}
