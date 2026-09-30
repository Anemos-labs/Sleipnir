package swarm

// Adversarial review tests for the swarm's shared state: board, wait, router,
// leases, governor, warm gate, hot view (docs/reviews/swarm-concurrency.md).
// TestConc_* repros assert the CORRECT behaviour and fail while the finding is
// open; they are skipped unless SLEIPNIR_REVIEW is set. TestConcSound_* tests are
// ungated regression checks for behaviour the review found sound.
//
//	SLEIPNIR_REVIEW=1 go test -race -count=1 -run 'TestConc_' ./internal/swarm
//	go test -race -count=1 -run 'TestConcSound_' ./internal/swarm

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/agent"
	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/provider"
	"github.com/reee344/sleipnir/internal/tools"
)

// ---- Board.Changed / Board.Wait / wait tool ----------------------------------

// Wait loads the snapshot and only then asks for the wake channel. A mutation that
// lands in between has already closed the old channel, so Wait sleeps on the NEW
// one until the deadline. Forced here by freezing the lock Changed() takes, then
// publishing exactly what mutate() publishes (store, close, replace).
func TestConc_BoardWaitLosesAWakeupThatLandsBetweenSnapshotAndChanged(t *testing.T) {
	concGate(t)
	b := NewBoard(nil)
	v := b.Snapshot().Version
	b.mu.Lock()
	got := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		b.Wait(v, 800*time.Millisecond)
		got <- time.Since(start)
	}()
	time.Sleep(80 * time.Millisecond) // Wait has read the old snapshot and is parked in Changed()
	b.snap.Store(&Snapshot{Version: v + 1})
	close(b.wake)
	b.wake = make(chan struct{})
	b.mu.Unlock()
	if d := <-got; d >= 700*time.Millisecond {
		t.Fatalf("Wait(after=%d) slept %v although the board moved to v%d while it was arming its wake-up (lost wake-up: fetch Changed() BEFORE reading the snapshot)", v, d.Round(time.Millisecond), v+1)
	}
}

// The same defect with no test-side locking: one mutation racing each Wait.
func TestConc_BoardWaitLostWakeupStress(t *testing.T) {
	concGate(t)
	b := NewBoard(nil)
	for i := 0; i < 4000; i++ {
		v := b.Snapshot().Version
		done := make(chan time.Duration, 1)
		go func() {
			s := time.Now()
			b.Wait(v, 250*time.Millisecond)
			done <- time.Since(s)
		}()
		b.CreateTask("m", TaskSpec{Title: "x"})
		if d := <-done; d >= 200*time.Millisecond {
			t.Fatalf("iteration %d: Wait slept %v with the change already published (lost wake-up)", i, d.Round(time.Millisecond))
		}
	}
}

// wait computes its digest against a snapshot taken when the tool is CALLED, so a
// change that happens between two consecutive waits (while the model is thinking)
// is invisible to the second one. Swarm.lastSeen exists for this and is never used.
func TestConc_WaitToolMissesChangesBetweenWaits(t *testing.T) {
	concGate(t)
	r := newRVRig(t, Config{MaxWriters: 4}, func(ctx context.Context, c *rvCall) rvReply { return rvReply{Text: "ok"} })
	r.sw.StartManager()
	b := r.sw.Board
	b.CreateTask("mgr", TaskSpec{Title: "long task"})
	b.Assign("mgr", "be-1", "T1")

	first := make(chan string, 1)
	go func() {
		first <- r.callTool(context.Background(), "wait", "mgr", "manager", map[string]any{"timeout_sec": 5}).Text
	}()
	time.Sleep(50 * time.Millisecond)
	b.CreateTask("mgr", TaskSpec{Title: "unrelated"}) // wakes the first wait
	<-first
	// While the manager's model is thinking about that result, the worker finishes.
	b.Finish("be-1", "T1", StatusReview, "implemented")

	start := time.Now()
	res := r.callTool(context.Background(), "wait", "mgr", "manager", map[string]any{"timeout_sec": 3}).Text
	if d := time.Since(start); d >= 2500*time.Millisecond || strings.Contains(res, "no task changes") {
		t.Fatalf("T1 moved to review between the two waits; the second wait slept %v and reported %q", d.Round(time.Millisecond), strings.ReplaceAll(strings.TrimSpace(res), "\n", " | "))
	}
}

// allSettled skips unknown ids, so a typo'd `until` returns at once with "all
// awaited tasks settled".
func TestConc_WaitUntilUnknownTaskReturnsImmediately(t *testing.T) {
	concGate(t)
	r := newRVRig(t, Config{}, func(ctx context.Context, c *rvCall) rvReply { return rvReply{Text: "ok"} })
	r.sw.StartManager()
	r.sw.Board.CreateTask("mgr", TaskSpec{Title: "real"})
	start := time.Now()
	res := r.callTool(context.Background(), "wait", "mgr", "manager", map[string]any{"until": []string{"T42"}, "timeout_sec": 3})
	if d := time.Since(start); d < time.Second && !res.IsError {
		t.Fatalf("wait(until=[T42]) returned after %v with %q although T42 does not exist and nothing settled", d.Round(time.Millisecond), strings.ReplaceAll(strings.TrimSpace(res.Text), "\n", " | "))
	}
}

type rvStallEmitter struct {
	gate    chan struct{}
	entered chan struct{}
}

func (s *rvStallEmitter) Emit(string, string, any, ...events.Opt) (uint64, error) {
	select {
	case s.entered <- struct{}{}:
	default:
	}
	<-s.gate
	return 0, nil
}

// Board.mutate calls the event log while holding the board mutex, and Changed()
// takes that mutex when the wait tool builds its select: a stalled log (slow disk)
// freezes every waiter, which then cannot even observe its own cancellation.
func TestConc_WaitCannotBeInterruptedWhileTheEventLogStalls(t *testing.T) {
	concGate(t)
	em := &rvStallEmitter{gate: make(chan struct{}), entered: make(chan struct{}, 1)}
	s := &Swarm{Board: NewBoard(em), members: map[string]*member{}}
	t.Cleanup(func() { close(em.gate) })
	go s.Board.CreateTask("w", TaskSpec{Title: "x"}) // parks inside Emit with the board lock held
	<-em.entered

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		env := (&tools.Env{Agent: "mgr", Role: "manager"}).Defaults()
		(&waitTool{s}).Run(ctx, &tools.Call{Input: []byte(`{"timeout_sec":30}`), Env: env})
		close(done)
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(700 * time.Millisecond):
		t.Fatal("wait ignored ctx cancellation for >700ms: Changed() blocks on the board mutex, which mutate() holds across the event-log write")
	}
}

// mutate() bumps the version, emits an event and wakes every waiter even when fn
// changed nothing (duplicate alert, unknown agent, duplicate note).
func TestConc_NoopMutationsBumpVersionAndWakeWaiters(t *testing.T) {
	concGate(t)
	log := events.NewMemLog()
	b := NewBoard(log)
	b.RaiseAlert("lease", "be-2 wanted a.go (held by be-1)")
	b.AddNote("be-1", "shared", "", "use make test")
	v := b.Snapshot().Version
	ch := b.Changed()
	before := len(log.OfType(events.TypeBoardOp))
	for i := 0; i < 200; i++ {
		b.RaiseAlert("lease", "be-2 wanted a.go (held by be-1)") // an agent retrying a locked file
	}
	b.AddNote("fe-1", "shared", "", "use make test")
	b.RemoveAgent("nobody")
	b.ClearAlerts("nothing-of-this-kind")
	woke := false
	select {
	case <-ch:
		woke = true
	default:
	}
	if after := b.Snapshot().Version; after != v || woke {
		t.Fatalf("203 no-op mutations: version v%d -> v%d, waiters woken=%v, %d board.op events logged", v, after, woke, len(log.OfType(events.TypeBoardOp))-before)
	}
}

// CreateTask keeps the caller's Deps/Files slices inside an "immutable" snapshot.
func TestConc_BoardSnapshotAliasesCallerSlices(t *testing.T) {
	concGate(t)
	b := NewBoard(nil)
	b.CreateTask("m", TaskSpec{Title: "a"})
	deps := []string{"T1"}
	files := []string{"api/**"}
	tk, _ := b.CreateTask("m", TaskSpec{Title: "b", Deps: deps, Files: files})
	snap := b.Snapshot()
	deps[0], files[0] = "T99", "web/**"
	got, _ := snap.Task(tk.ID)
	if got.Deps[0] != "T1" || got.Files[0] != "api/**" {
		t.Fatalf("a published snapshot changed under a reader after the caller reused its slices: deps=%v files=%v", got.Deps, got.Files)
	}
}

// TakeNotes(ids...) with an empty computed list means "everything".
func TestConc_TakeNotesWithNoIDsTakesEverything(t *testing.T) {
	concGate(t)
	b := NewBoard(nil)
	for i := 0; i < 3; i++ {
		b.AddNote("w", "shared", "", fmt.Sprintf("fact %d", i))
	}
	var promoted []int // the curator decided to promote nothing this round
	if taken := b.TakeNotes(promoted...); len(taken) != 0 {
		t.Fatalf("TakeNotes() with an empty id list removed %d unpromoted notes", len(taken))
	}
}

// Shared-scope dedupe keys on the author's role too, so the same convention noted
// by two roles is stored (and rendered to every agent) twice.
func TestConc_SharedNoteDedupeIsRoleSensitive(t *testing.T) {
	concGate(t)
	b := NewBoard(nil)
	b.AddNote("be-1", "shared", "backend", "tests: make test-unit")
	b.AddNote("fe-1", "shared", "frontend", "tests: make test-unit")
	if n := len(b.Snapshot().Notes); n != 1 {
		t.Fatalf("identical shared notes from two roles were stored %d times", n)
	}
}

// Notes have no cap and no rate limit; the hot renderer copies, sorts, formats and
// then re-renders and re-tokenises the whole text once per dropped line, for every
// request of every agent. One misbehaving agent can degrade the whole swarm.
func TestConc_NotesAreUnboundedAndRenderHotIsQuadratic(t *testing.T) {
	concGate(t)
	b := NewBoard(nil)
	est := core.NewBytesEstimator().WithRatio(4)
	var last time.Duration
	added := 0
	for _, target := range []int{250, 500, 1000, 2000} {
		for ; added < target; added++ {
			if _, err := b.AddNote("w-1", "shared", "", fmt.Sprintf("unique fact number %d about the repository layout and its build", added)); err != nil {
				t.Fatal(err)
			}
		}
		start := time.Now()
		RenderHot(b.Snapshot(), "w-2", "backend", false, DefaultHotConfig(), est)
		last = time.Since(start)
		t.Logf("%4d notes: one worker RenderHot = %v", target, last.Round(time.Millisecond))
	}
	if n := len(b.Snapshot().Notes); n > 200 || last > 100*time.Millisecond {
		t.Fatalf("board holds %d notes (no cap or rate limit on the note tool); RenderHot for ONE agent took %v, and every agent pays that on every request", n, last.Round(time.Millisecond))
	}
}

// Tasks are never pruned and failed tasks count as "open" forever, so the
// manager's hot view (and the O(lines^2) budget loop) grows with session age.
func TestConc_ManagerHotViewCostGrowsWithFailedTasks(t *testing.T) {
	concGate(t)
	b := NewBoard(nil)
	est := core.NewBytesEstimator().WithRatio(4)
	var last time.Duration
	made := 0
	for _, target := range []int{100, 250, 500, 1000} {
		for ; made < target; made++ {
			tk, _ := b.CreateTask("mgr", TaskSpec{Title: fmt.Sprintf("attempt %d at the flaky migration", made)})
			b.Assign("mgr", "be-1", tk.ID)
			b.Finish("be-1", tk.ID, StatusFailed, "agent stopped: context canceled")
		}
		start := time.Now()
		RenderHot(b.Snapshot(), "mgr", "manager", true, DefaultHotConfig(), est)
		last = time.Since(start)
		t.Logf("%4d failed tasks: manager RenderHot = %v", target, last.Round(time.Millisecond))
	}
	if last > 40*time.Millisecond {
		t.Fatalf("manager RenderHot took %v with 1000 dead (failed) tasks that still count as open (it runs on every manager request)", last.Round(time.Millisecond))
	}
}

// Alerts are described as short-lived but nothing ever clears them (no production
// caller of ClearAlerts). A lease alert stays in every agent's hot view, at
// priority 1 (never dropped), long after the holder released the file.
func TestConc_LeaseAlertsOutliveTheConflict(t *testing.T) {
	concGate(t)
	b := NewBoard(nil)
	l := NewLeases(time.Minute, b)
	if err := l.BeforeWrite("be-1", "/repo/a.go"); err != nil {
		t.Fatal(err)
	}
	if err := l.BeforeWrite("fe-1", "/repo/a.go"); err == nil {
		t.Fatal("setup: conflict expected")
	}
	l.ReleaseAll("be-1") // be-1 finished; the file is free again
	est := core.NewBytesEstimator().WithRatio(4)
	hot := RenderHot(b.Snapshot(), "be-2", "backend", false, DefaultHotConfig(), est)
	if len(b.Snapshot().Alerts) != 0 || strings.Contains(hot, "held by be-1") {
		t.Fatalf("the lease conflict is over but every agent still sees:\n%s", hot)
	}
}

// Board has no transition table: an owner can push its own accepted task out of
// "done", and the harness-owned "done" can be overwritten by the model-facing verbs.
func TestConc_BoardAllowsDoneToRegress(t *testing.T) {
	concGate(t)
	b := NewBoard(nil)
	b.CreateTask("mgr", TaskSpec{Title: "x"})
	if err := b.Claim("be-1", "T1"); err != nil {
		t.Fatal(err)
	}
	b.Finish("manager", "T1", StatusDone, "accepted")
	var bad []string
	if err := b.Resume("be-1", "T1"); err == nil {
		bad = append(bad, "resume: done -> doing")
	}
	b.Finish("manager", "T1", StatusDone, "accepted again")
	if err := b.Finish("be-1", "T1", StatusReview, "worker re-submits"); err == nil {
		bad = append(bad, "finish(review) after done")
	}
	b.Finish("manager", "T1", StatusDone, "accepted a third time")
	if err := b.Block("be-1", "T1", "changed my mind"); err == nil {
		bad = append(bad, "block after done")
	}
	if len(bad) > 0 {
		tk, _ := b.Snapshot().Task("T1")
		t.Fatalf("done is not terminal: %s (T1 ends %s)", strings.Join(bad, ", "), tk.Status)
	}
}

// Dependencies are enforced only by Board.Claim. Spawn -> Assign ignores them.
func TestConc_SpawnIgnoresUnmetDependencies(t *testing.T) {
	concGate(t)
	gate := make(chan struct{})
	r := newRVRig(t, Config{MaxWriters: 4}, func(ctx context.Context, c *rvCall) rvReply {
		rvBlock(ctx, gate)
		return rvReply{Text: "ok"}
	})
	t.Cleanup(func() { close(gate) })
	r.sw.StartManager()
	t1, _ := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "schema migration"})
	t2, _ := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "code that needs the migration", Deps: []string{t1.ID}})
	if err := r.sw.Board.Claim("be-9", t2.ID); err == nil {
		t.Fatal("setup: claim must refuse a task with unmet deps")
	}
	if id, err := r.sw.Spawn(SpawnReq{Role: "backend", TaskID: t2.ID, By: "mgr"}); err == nil {
		t.Fatalf("Spawn started %s on %s although its dependency %s is still todo (only self-claim enforces deps)", id, t2.ID, t1.ID)
	}
}

// ---- Router ----------------------------------------------------------------------

// r.recent (keyed by sender, recipient and the full text) is never pruned, and the
// per-sender/pair maps keep a key for every agent that ever spoke.
func TestConc_RouterMapsGrowForever(t *testing.T) {
	concGate(t)
	now := time.Now()
	r := NewRouter(DefaultRouterConfig(), nil, func() []string { return []string{"mgr", "w-0"} }, func() string { return "mgr" }, func(Message) {})
	r.now = func() time.Time { return now }
	const n = 5000
	for i := 0; i < n; i++ {
		now = now.Add(10 * time.Minute) // everything before is long outside every window
		if _, err := r.Send(fmt.Sprintf("w-%d", i), "mgr", "info", fmt.Sprintf("status update number %d with a reasonably long body text", i)); err != nil {
			t.Fatal(err)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.recent) > 100 || len(r.sender) > 100 || len(r.pair) > 100 {
		t.Fatalf("after %d messages spread over %d hours: recent=%d sender=%d pair=%d entries retained (dedupe window is 5 minutes)", n, n/6, len(r.recent), len(r.sender), len(r.pair))
	}
}

// Send validates the recipient against the roster and delivers later; a Retire in
// between makes deliver() a silent no-op, yet Send returns success and the log
// records mail.deliver.
func TestConc_RouterReportsSuccessForMailThatWasDropped(t *testing.T) {
	concGate(t)
	r := newRVRig(t, Config{MaxWriters: 4}, func(ctx context.Context, c *rvCall) rvReply { return rvReply{Text: "ok"} })
	r.sw.StartManager()
	id, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "work", By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	rvWait(t, "worker idle", func() bool { return r.idle(id) })
	router := NewRouter(DefaultRouterConfig(), r.log,
		func() []string { ids := r.sw.roster(); _ = r.sw.Retire(id); return ids }, // retire lands right after the roster check
		func() string { return "mgr" }, r.sw.deliver)
	_, sendErr := router.Send("mgr", id, "request", "please rebase before you finish")
	if sendErr == nil && r.sw.get(id) == nil {
		t.Fatalf("Send reported success and logged %d mail.deliver event(s), but %s was retired first and the message vanished", len(r.log.OfType(events.TypeMailDeliver)), id)
	}
}

// Nothing caps a recipient's inbox: every agent may send 3 mails a minute to the
// manager, and when the manager is not inside Run they all wait for the next turn,
// which then swallows them as one giant user turn.
func TestConc_ManagerInboxIsUnbounded(t *testing.T) {
	concGate(t)
	r := newRVRig(t, Config{MaxAgents: 100}, func(ctx context.Context, c *rvCall) rvReply { return rvReply{Text: "ok"} })
	r.sw.StartManager()
	now := time.Now()
	r.sw.Router.now = func() time.Time { return now }
	for minute := 0; minute < 10; minute++ {
		now = now.Add(time.Minute)
		for w := 0; w < 60; w++ {
			for k := 0; k < 3; k++ {
				r.sw.Router.Send(fmt.Sprintf("w-%d", w), "manager", "info", fmt.Sprintf("progress %d.%d from worker %d: still going", minute, k, w))
			}
		}
	}
	if n := r.sw.Manager().PendingInbox(); n > 200 {
		t.Fatalf("%d mails are queued for a manager that is not running; nothing caps, coalesces or digests them (the next turn receives all %d as one user turn)", n, n)
	}
}

// ---- Governor ------------------------------------------------------------------

// The multiplicative decrease runs once per failed request, not once per 429
// episode: N requests that were in flight together all come back 429 and the
// rate falls to its floor, then needs ~360 successes to recover.
func TestConc_GovernorConcurrent429sCollapseTheRate(t *testing.T) {
	concGate(t)
	g := NewGovernor(GovernorConfig{RPM: 6000, Burst: 50})
	var rels []agent.Release
	for i := 0; i < 20; i++ {
		rel, err := g.Acquire(context.Background(), 1)
		if err != nil {
			t.Fatal(err)
		}
		rels = append(rels, rel)
	}
	for _, rel := range rels {
		rel(nil, &provider.Error{Kind: provider.ErrRateLimit, RetryAfter: 10 * time.Millisecond})
	}
	if rate := g.Stats().Rate; rate < 3000 {
		t.Fatalf("one burst of 20 simultaneous 429s (a single rate-limit episode) cut the budget from 6000 to %.0f req/min (floor 10%%)", rate)
	}
}

// Cancellations mid-dispatch, timer re-arming, 429 pauses and priorities all at
// once: the concurrency cap must hold, nothing may leak, everything must drain.
func TestConcSound_GovernorStress(t *testing.T) {
	before := runtime.NumGoroutine()
	g := NewGovernor(GovernorConfig{RPM: 600000, Burst: 5, MaxConcurrent: 4})
	var cur, peak, admitted, cancelled atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 300; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if i%3 == 0 {
				time.AfterFunc(time.Duration(rand.Intn(4000))*time.Microsecond, cancel)
			}
			rel, err := g.Acquire(ctx, i%3)
			if err != nil {
				cancelled.Add(1)
				return
			}
			admitted.Add(1)
			n := cur.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			time.Sleep(time.Duration(rand.Intn(300)) * time.Microsecond)
			cur.Add(-1)
			var rerr error
			if i%23 == 0 {
				rerr = &provider.Error{Kind: provider.ErrRateLimit, RetryAfter: time.Millisecond}
			}
			rel(nil, rerr)
		}(i)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatalf("governor wedged: %+v admitted=%d cancelled=%d", g.Stats(), admitted.Load(), cancelled.Load())
	}
	if peak.Load() > 4 {
		t.Fatalf("MaxConcurrent=4 exceeded: peak %d", peak.Load())
	}
	if st := g.Stats(); st.InFlight != 0 || st.Queued != 0 {
		t.Fatalf("leaked state after drain: %+v", st)
	}
	time.Sleep(50 * time.Millisecond)
	if after := runtime.NumGoroutine(); after > before+5 {
		t.Fatalf("goroutines leaked: %d -> %d", before, after)
	}
	t.Logf("admitted=%d cancelled=%d peak=%d", admitted.Load(), cancelled.Load(), peak.Load())
}

// Strict priority with no aging: as long as higher-priority work keeps arriving, a
// background request (compactor) is never admitted.
func TestConc_GovernorBackgroundStarvesUnderSteadyWorkerLoad(t *testing.T) {
	concGate(t)
	g := NewGovernor(GovernorConfig{MaxConcurrent: 1})
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ { // four workers hammering the single slot
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				rel, err := g.Acquire(context.Background(), agent.PrioWorker)
				if err != nil {
					return
				}
				time.Sleep(time.Millisecond)
				rel(nil, nil)
			}
		}()
	}
	time.Sleep(20 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	rel, err := g.Acquire(ctx, agent.PrioBackground)
	close(stop)
	if err == nil {
		rel(nil, nil)
	}
	wg.Wait()
	if err != nil {
		t.Fatalf("a background (compaction) request was starved for 400ms of steady worker load: %v", err)
	}
}

// ---- WarmGate --------------------------------------------------------------------

// Regression check. This was a finding (C-18) against the first WarmGate: if a
// primer never reported (panic between Enter and the finisher, a hung request),
// priming stayed true forever and EVERY later request on that prefix waited the
// whole maxWait. The gate was rewritten while this review was under way (after
// maxWait it releases a co-primer, then two, then four; a co-primer's first byte
// warms the level), and this now passes: later arrivals are not taxed.
func TestConcSound_WarmGateEscalatesPastAStuckPrimer(t *testing.T) {
	const maxWait = 150 * time.Millisecond
	g := NewWarmGate(time.Minute, maxWait)
	if _, err := g.Enter(context.Background(), "k"); err != nil { // the primer; it never calls back
		t.Fatal(err)
	}
	var waits []time.Duration
	for i := 0; i < 3; i++ {
		start := time.Now()
		fin, err := g.Enter(context.Background(), "k")
		if err != nil {
			t.Fatal(err)
		}
		waits = append(waits, time.Since(start))
		fin(true)
	}
	if waits[2] > maxWait/2 {
		t.Fatalf("after a primer vanished, followers still each wait the whole maxWait (%v, %v, %v); production maxWait is 45s", waits[0].Round(time.Millisecond), waits[1].Round(time.Millisecond), waits[2].Round(time.Millisecond))
	}
}

// Followers cancelling, primers failing and succeeding at random: nothing may
// deadlock, double-close or leave the key primed.
func TestConcSound_WarmGateStress(t *testing.T) {
	g := NewWarmGate(20*time.Millisecond, 2*time.Second)
	var wg sync.WaitGroup
	var primers atomic.Int32
	for i := 0; i < 400; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if i%4 == 0 {
				time.AfterFunc(time.Duration(rand.Intn(3000))*time.Microsecond, cancel)
			}
			fin, err := g.Enter(ctx, fmt.Sprintf("k%d", i%3))
			if err != nil {
				return
			}
			primers.Add(1)
			time.Sleep(time.Duration(rand.Intn(500)) * time.Microsecond)
			ok := i%5 != 0
			fin(ok)
			fin(!ok) // callers "must call exactly once"; a second call must be harmless
		}(i)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("warm gate deadlocked")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for k, ks := range g.keys {
		if ks.priming {
			t.Fatalf("key %s left in priming state", k)
		}
	}
}

// ---- 50 agents ---------------------------------------------------------------------

// Board, router, leases, hot rendering, wait wake-ups and notes hammered by 50
// goroutines under -race. Invariants: unique ids, monotonic versions, no lost
// mail, at most one live holder per file.
func TestConcSound_FiftyAgentsHammerSharedState(t *testing.T) {
	const agents = 50
	log := events.NewMemLog()
	b := NewBoard(log)
	l := NewLeases(time.Hour, b)
	ids := make([]string, agents+1)
	ids[0] = "mgr"
	for i := 0; i < agents; i++ {
		ids[i+1] = fmt.Sprintf("w-%02d", i)
	}
	var mu sync.Mutex
	inbox := map[string][]Message{}
	var delivered atomic.Int64
	r := NewRouter(RouterConfig{MaxPerMinute: 1 << 30, MaxPerPairPerMin: 1 << 30, MaxChars: 600, DedupeWindow: time.Nanosecond},
		log, func() []string { return ids }, func() string { return "mgr" },
		func(m Message) { delivered.Add(1); mu.Lock(); inbox[m.To] = append(inbox[m.To], m); mu.Unlock() })
	est := core.NewBytesEstimator().WithRatio(4)
	paths := []string{"/r/a.go", "/r/b.go", "/r/c.go", "/r/d.go", "/r/e.go", "/r/f.go", "/r/g.go", "/r/h.go"}
	owner := make([]atomic.Value, len(paths)) // test-side view of who holds each path
	for i := range owner {
		owner[i].Store("")
	}
	stop := make(chan struct{})
	var violations atomic.Int32
	var accepted atomic.Int64
	var wg sync.WaitGroup
	for a := 0; a < agents; a++ {
		wg.Add(1)
		go func(a int) {
			defer wg.Done()
			me := ids[a+1]
			rng := rand.New(rand.NewSource(int64(a)))
			var lastSeen uint64
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				switch rng.Intn(9) {
				case 0:
					tk, err := b.CreateTask(me, TaskSpec{Title: fmt.Sprintf("task by %s #%d", me, i)})
					if err == nil {
						_ = b.Claim(me, tk.ID)
					}
				case 1:
					s := b.Snapshot()
					if len(s.Tasks) > 0 {
						tk := s.Tasks[rng.Intn(len(s.Tasks))]
						_ = b.Claim(me, tk.ID)
						_ = b.Update(me, tk.ID, "working")
						_ = b.Block(me, tk.ID, "x")
						_ = b.Resume(me, tk.ID)
						_ = b.Finish(me, tk.ID, StatusReview, "done")
					}
				case 2:
					b.SetAgent(AgentInfo{ID: me, Role: "backend", State: "running", Line: fmt.Sprintf("step %d", i)})
				case 3:
					to := ids[rng.Intn(len(ids))]
					if to != me {
						if _, err := r.Send(me, to, "info", fmt.Sprintf("hello %d from %s", i, me)); err == nil {
							accepted.Add(1)
						}
					}
				case 4:
					_, _ = b.AddNote(me, "shared", "backend", fmt.Sprintf("note %s %d", me, i%7))
					b.RaiseAlert("lease", fmt.Sprintf("alert %d", i%5))
				case 5:
					p := rng.Intn(len(paths))
					if err := l.BeforeWrite(me, paths[p]); err == nil {
						if cur := owner[p].Load().(string); cur != "" && cur != me {
							violations.Add(1)
						}
						owner[p].Store(me)
						l.AfterWrite(me, paths[p])
					}
				case 6:
					for p := range paths {
						if owner[p].Load().(string) == me {
							owner[p].Store("")
						}
					}
					l.ReleaseAll(me)
				case 7:
					s := b.Snapshot()
					if s.Version < lastSeen {
						violations.Add(1) // versions must never go backwards for one reader
					}
					lastSeen = s.Version
					_ = RenderHot(s, me, "backend", false, DefaultHotConfig(), est)
				case 8:
					ch := b.Changed()
					select {
					case <-ch:
					case <-time.After(time.Millisecond):
					}
				}
			}
		}(a)
	}
	time.Sleep(400 * time.Millisecond)
	close(stop)
	wg.Wait()

	s := b.Snapshot()
	seen := map[string]bool{}
	for _, tk := range s.Tasks {
		if seen[tk.ID] {
			t.Fatalf("duplicate task id %s", tk.ID)
		}
		seen[tk.ID] = true
	}
	if delivered.Load() != accepted.Load() {
		t.Fatalf("mail accepted=%d delivered=%d", accepted.Load(), delivered.Load())
	}
	ids2 := map[string]bool{}
	mu.Lock()
	for _, msgs := range inbox {
		for _, m := range msgs {
			if ids2[m.ID] {
				t.Fatalf("message id %s delivered twice", m.ID)
			}
			ids2[m.ID] = true
		}
	}
	mu.Unlock()
	if violations.Load() != 0 {
		t.Fatalf("%d lease mutual-exclusion violations", violations.Load())
	}
	// Every mutation published exactly one version.
	if ops := len(log.OfType(events.TypeBoardOp)); uint64(ops) != s.Version {
		t.Fatalf("version v%d but %d board.op events", s.Version, ops)
	}
	t.Logf("tasks=%d agents=%d notes=%d mail=%d version=v%d", len(s.Tasks), len(s.Agents), len(s.Notes), accepted.Load(), s.Version)
}

// "The log is the truth ... crash recovery, resume, replay": the board, mail, leases
// and governor are supposed to be derivable from it. board.op records only the verb
// and the new version, never the operands (which task, which agent, which status,
// which text), agent.state / lease / governor / mail.route / mail.ack are declared
// but never emitted, and nothing anywhere replays the log. After a crash the board
// (owners, statuses, results, notes, alerts) is simply gone.
func TestConc_BoardStateCannotBeRebuiltFromTheEventLog(t *testing.T) {
	concGate(t)
	log := events.NewMemLog()
	b := NewBoard(log)
	tk, _ := b.CreateTask("mgr", TaskSpec{Title: "paginate /users", Deps: nil})
	b.Assign("mgr", "be-1", tk.ID)
	b.Update("be-1", tk.ID, "wrote the handler")
	b.Finish("be-1", tk.ID, StatusReview, "cursor pagination [edited 2; last test passed]")
	b.AddNote("be-1", "shared", "", "tests: make test-unit")
	b.RaiseAlert("lease", "fe-1 wanted users.go (held by be-1)")
	l := NewLeases(time.Minute, b)
	l.BeforeWrite("be-1", "/repo/users.go")
	ops := log.OfType(events.TypeBoardOp)
	if len(ops) == 0 {
		t.Fatal("no board.op events")
	}
	blind := 0
	for _, e := range ops {
		var m map[string]any
		_ = json.Unmarshal(e.Data, &m)
		if len(m) <= 2 { // {op, version} only
			blind++
		}
	}
	emitted := map[string]bool{}
	for _, e := range log.All() {
		emitted[e.Type] = true
	}
	if blind > 0 || !emitted[events.TypeLease] {
		t.Fatalf("%d of %d board.op events carry no operands (no task id, owner, status, text), and lease grants/conflicts are not logged (event types seen: %v): the board cannot be reconstructed from the log", blind, len(ops), emitted)
	}
}

// diffSnapshots reports alerts by slicing b.Alerts[len(a.Alerts):], which assumes
// the list only grows. RaiseAlert keeps the LAST 8, so once the list is full (and
// nothing ever clears it) a new alert pushes an old one out, the length stays 8 and
// wait reports no alert at all.
func TestConc_WaitDigestMissesAnAlertOnceTheAlertListIsFull(t *testing.T) {
	concGate(t)
	b := NewBoard(nil)
	for i := 0; i < 8; i++ {
		b.RaiseAlert("lease", fmt.Sprintf("be-%d wanted a.go (held by be-9)", i))
	}
	base := b.Snapshot()
	b.RaiseAlert("lease", "fe-1 wanted users.go (held by be-2)")
	digest := diffSnapshots(base, b.Snapshot())
	found := false
	for _, d := range digest {
		if strings.Contains(d, "fe-1 wanted users.go") {
			found = true
		}
	}
	if !found {
		t.Fatalf("a new lease-conflict alert was raised but the wait digest is %q", digest)
	}
}
