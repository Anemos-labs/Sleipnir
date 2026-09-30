package swarm

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/provider"
)

func TestBoardLifecycleAndOwnership(t *testing.T) {
	b := NewBoard(nil)
	t1, err := b.CreateTask("mgr", TaskSpec{Title: "Paginate /users", Role: "backend"})
	if err != nil || t1.ID != "T1" {
		t.Fatalf("create: %v %+v", err, t1)
	}
	t2, _ := b.CreateTask("mgr", TaskSpec{Title: "Client pagination", Deps: []string{"T1"}})
	if _, err := b.CreateTask("mgr", TaskSpec{Title: "x", Deps: []string{"T99"}}); err == nil {
		t.Fatal("unknown dependency must be rejected")
	}
	if err := b.Claim("fe-1", t2.ID); err == nil || !strings.Contains(err.Error(), "waits for T1") {
		t.Fatalf("claim before deps: %v", err)
	}
	if err := b.Claim("be-1", "T1"); err != nil {
		t.Fatal(err)
	}
	if err := b.Claim("be-2", "T1"); err == nil || !strings.Contains(err.Error(), "already owned by be-1") {
		t.Fatalf("double claim: %v", err)
	}
	if err := b.Update("be-2", "T1", "hax"); err == nil {
		t.Fatal("only the owner may update")
	}
	if err := b.Update("be-1", "T1", "added limit param"); err != nil {
		t.Fatal(err)
	}
	if err := b.Finish("be-1", "T1", StatusReview, "cursor pagination merged"); err != nil {
		t.Fatal(err)
	}
	if err := b.Finish("mgr", "T1", StatusDone, ""); err != nil {
		t.Fatal(err)
	}
	if err := b.Claim("fe-1", t2.ID); err != nil {
		t.Fatalf("deps are done now: %v", err)
	}
	s := b.Snapshot()
	got, _ := s.Task("T1")
	if got.Status != StatusDone || got.Result != "cursor pagination merged" || got.Line != "" {
		t.Fatalf("T1 = %+v", got)
	}
}

func TestBoardSnapshotsAreImmutableAndConcurrentSafe(t *testing.T) {
	b := NewBoard(nil)
	before := b.Snapshot()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			task, err := b.CreateTask(fmt.Sprintf("a%d", i), TaskSpec{Title: fmt.Sprintf("task %d", i)})
			if err != nil {
				t.Error(err)
				return
			}
			b.SetAgent(AgentInfo{ID: fmt.Sprintf("a%d", i), Role: "backend", State: "running", Task: task.ID})
			_ = b.Claim(fmt.Sprintf("a%d", i), task.ID)
			_ = b.Snapshot() // lock-free read
		}(i)
	}
	wg.Wait()
	if len(before.Tasks) != 0 {
		t.Fatal("an old snapshot must never change")
	}
	s := b.Snapshot()
	if len(s.Tasks) != 20 || len(s.Agents) != 20 || s.Version < 60 {
		t.Fatalf("tasks=%d agents=%d version=%d", len(s.Tasks), len(s.Agents), s.Version)
	}
	ids := map[string]bool{}
	for _, tk := range s.Tasks {
		if ids[tk.ID] {
			t.Fatalf("duplicate task id %s", tk.ID)
		}
		ids[tk.ID] = true
	}
}

func TestBoardWaitWakesOnChange(t *testing.T) {
	b := NewBoard(nil)
	v := b.Snapshot().Version
	done := make(chan *Snapshot, 1)
	go func() { done <- b.Wait(v, 5*time.Second) }()
	time.Sleep(20 * time.Millisecond)
	b.CreateTask("m", TaskSpec{Title: "x"})
	select {
	case s := <-done:
		if s.Version <= v {
			t.Fatal("woke without change")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Wait did not wake")
	}
	if s := b.Wait(b.Snapshot().Version, 30*time.Millisecond); s == nil {
		t.Fatal("timeout must still return a snapshot")
	}
}

func TestNotesDedupeAndTake(t *testing.T) {
	b := NewBoard(nil)
	a, _ := b.AddNote("be-1", "shared", "", "tests: make test-unit is the fast one")
	c, _ := b.AddNote("fe-2", "shared", "", "tests: make test-unit is the fast one")
	if a != c || len(b.Snapshot().Notes) != 1 {
		t.Fatal("identical notes must merge")
	}
	if _, err := b.AddNote("x", "galaxy", "", "t"); err == nil {
		t.Fatal("bad scope")
	}
	if got := b.TakeNotes(); len(got) != 0 || len(b.Snapshot().Notes) != 1 {
		t.Fatalf("TakeNotes with no ids must take nothing: %+v", got)
	}
	got := b.TakeAllNotes()
	if len(got) != 1 || len(b.Snapshot().Notes) != 0 {
		t.Fatalf("take: %+v", got)
	}
}

func newRouter(t *testing.T) (*Router, *[]Message) {
	var got []Message
	r := NewRouter(DefaultRouterConfig(), nil,
		func() []string { return []string{"mgr", "be-1", "fe-1"} },
		func() string { return "mgr" },
		func(m Message) { got = append(got, m) })
	return r, &got
}

func TestRouterRules(t *testing.T) {
	r, got := newRouter(t)
	if _, err := r.Send("be-1", "all", "", "hello"); err == nil || !strings.Contains(err.Error(), "no broadcast") {
		t.Fatalf("broadcast: %v", err)
	}
	if _, err := r.Send("be-1", "ghost", "", "hi"); err == nil || !strings.Contains(err.Error(), "agents: mgr, be-1, fe-1") {
		t.Fatalf("unknown recipient should list agents: %v", err)
	}
	if _, err := r.Send("be-1", "be-1", "", "hi"); err == nil {
		t.Fatal("self mail")
	}
	m, err := r.Send("be-1", "manager", "blocker", "blocked on schema")
	if err != nil || m.To != "mgr" || m.ID != "m1" {
		t.Fatalf("manager alias: %v %+v", err, m)
	}
	if _, err := r.Send("be-1", "mgr", "blocker", "blocked on schema"); err == nil || !strings.Contains(err.Error(), "already sent") {
		t.Fatalf("dedupe: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := r.Send("be-1", "fe-1", "", fmt.Sprintf("fact %d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.Send("be-1", "fe-1", "", "fact 4"); err == nil || !strings.Contains(err.Error(), "several times") {
		t.Fatalf("pair limit: %v", err)
	}
	if _, err := r.Send("be-1", "fe-1", "", strings.Repeat("x", 601)); err == nil {
		t.Fatal("length cap")
	}
	if _, err := r.Send("be-1", "mgr", "shout", "hi"); err == nil {
		t.Fatal("unknown kind must be rejected")
	}
	if len(*got) != 4 {
		t.Fatalf("delivered %d", len(*got))
	}
	if f := (*got)[0].Format(); f != "[mail m1 blocker from be-1] blocked on schema" {
		t.Fatalf("format = %q", f)
	}
}

func TestLeases(t *testing.T) {
	b := NewBoard(nil)
	l := NewLeases(time.Minute, b)
	now := time.Now()
	l.now = func() time.Time { return now }
	if err := l.BeforeWrite("be-1", "/repo/a.go"); err != nil {
		t.Fatal(err)
	}
	err := l.BeforeWrite("be-2", "/repo/a.go")
	if err == nil || !strings.Contains(err.Error(), "being edited by be-1") {
		t.Fatalf("conflict: %v", err)
	}
	if len(b.Snapshot().Alerts) != 1 {
		t.Fatal("conflict should raise an alert for the manager")
	}
	if err := l.BeforeWrite("be-1", "/repo/a.go"); err != nil {
		t.Fatal("holder may write again")
	}
	now = now.Add(2 * time.Minute) // lease expires
	if err := l.BeforeWrite("be-2", "/repo/a.go"); err != nil {
		t.Fatalf("expired lease must be stealable: %v", err)
	}
	l.AfterWrite("be-2", "/repo/a.go")
	if h, ok := l.Holder("/repo/a.go"); !ok || h != "be-2" {
		t.Fatal("holder")
	}
	if got := l.ReleaseAll("be-2"); len(got) != 1 {
		t.Fatalf("release: %v", got)
	}
	if err := l.BeforeWrite("be-3", "/repo/a.go"); err != nil {
		t.Fatal(err)
	}
}

func TestGovernorPacesAndPrioritises(t *testing.T) {
	g := NewGovernor(GovernorConfig{RPM: 6000, Burst: 1}) // 100/s
	start := time.Now()
	for i := 0; i < 11; i++ {
		rel, err := g.Acquire(context.Background(), 1)
		if err != nil {
			t.Fatal(err)
		}
		rel(nil, nil)
	}
	if took := time.Since(start); took < 80*time.Millisecond {
		t.Fatalf("11 requests at 100/s with burst 1 took only %v", took)
	}

	// Priority order under contention.
	g2 := NewGovernor(GovernorConfig{MaxConcurrent: 1})
	hold, _ := g2.Acquire(context.Background(), 1)
	var mu sync.Mutex
	var order []int
	var wg sync.WaitGroup
	for _, prio := range []int{2, 1, 0} {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			rel, err := g2.Acquire(context.Background(), p)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			order = append(order, p)
			mu.Unlock()
			rel(nil, nil)
		}(prio)
		time.Sleep(10 * time.Millisecond) // enqueue in a known order: 2, 1, 0
	}
	hold(nil, nil)
	wg.Wait()
	if fmt.Sprint(order) != "[0 1 2]" {
		t.Fatalf("admission order %v, want [0 1 2]", order)
	}
}

func TestGovernorHonoursRetryAfterAndBacksOff(t *testing.T) {
	g := NewGovernor(GovernorConfig{RPM: 6000, Burst: 50})
	rel, _ := g.Acquire(context.Background(), 1)
	rel(nil, &provider.Error{Kind: provider.ErrRateLimit, RetryAfter: 120 * time.Millisecond})
	if !g.Stats().Paused {
		t.Fatal("must pause after a 429")
	}
	start := time.Now()
	rel2, err := g.Acquire(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	rel2(nil, nil)
	if took := time.Since(start); took < 100*time.Millisecond {
		t.Fatalf("admitted after only %v despite Retry-After", took)
	}
	if g.Stats().Rate >= 6000 {
		t.Fatalf("effective rate should have been cut, got %.0f/min", g.Stats().Rate)
	}
}

func TestGovernorCancelledWaiterFreesSlot(t *testing.T) {
	g := NewGovernor(GovernorConfig{MaxConcurrent: 1})
	hold, _ := g.Acquire(context.Background(), 1)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := g.Acquire(ctx, 1); err == nil {
		t.Fatal("expected timeout")
	}
	hold(nil, nil)
	rel, err := g.Acquire(context.Background(), 1)
	if err != nil {
		t.Fatalf("slot leaked by the cancelled waiter: %v", err)
	}
	rel(nil, nil)
	if g.Stats().InFlight != 0 || g.Stats().Queued != 0 {
		t.Fatalf("stats: %+v", g.Stats())
	}
}

func TestWarmGateElectsOnePrimer(t *testing.T) {
	g := NewWarmGate(time.Minute, 5*time.Second)
	var primers, released atomic.Int32
	var wg sync.WaitGroup
	var startedAt atomic.Int64
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start := time.Now()
			started, err := g.Enter(context.Background(), "prefix")
			if err != nil {
				t.Error(err)
				return
			}
			if time.Since(start) < 20*time.Millisecond {
				// Returned immediately: this is the primer.
				primers.Add(1)
				time.Sleep(80 * time.Millisecond) // prefill
				startedAt.Store(time.Now().UnixNano())
				started(true)
				return
			}
			released.Add(1)
			if time.Now().UnixNano() < startedAt.Load() {
				t.Error("follower released before the primer started")
			}
			started(true)
		}()
		time.Sleep(3 * time.Millisecond)
	}
	wg.Wait()
	if primers.Load() != 1 || released.Load() != 7 {
		t.Fatalf("primers=%d followers=%d", primers.Load(), released.Load())
	}
	// Now warm: nobody waits.
	start := time.Now()
	s, _ := g.Enter(context.Background(), "prefix")
	s(true)
	if time.Since(start) > 10*time.Millisecond || !g.Warm("prefix") {
		t.Fatal("warm prefix must pass straight through")
	}
}

func TestWarmGatePrimerFailurePromotesAWaiter(t *testing.T) {
	g := NewWarmGate(time.Minute, 5*time.Second)
	first, _ := g.Enter(context.Background(), "k")
	got := make(chan struct{})
	go func() {
		s, err := g.Enter(context.Background(), "k")
		if err == nil {
			s(true)
		}
		close(got)
	}()
	time.Sleep(30 * time.Millisecond)
	first(false) // primer died before starting
	select {
	case <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("waiter stuck behind a failed primer")
	}
	if !g.Warm("k") {
		t.Fatal("the promoted waiter should have warmed the key")
	}
}

func hotSnapshot() *Snapshot {
	b := NewBoard(nil)
	b.CreateTask("mgr", TaskSpec{Title: "Paginate GET /users", Role: "backend"})
	b.CreateTask("mgr", TaskSpec{Title: "Users table UI", Deps: []string{"T1"}})
	b.CreateTask("mgr", TaskSpec{Title: "Write migration for sessions"})
	b.Assign("mgr", "be-2", "T1")
	b.Assign("mgr", "be-1", "T3")
	b.Update("be-2", "T1", "added limit/offset, writing tests")
	for _, a := range []AgentInfo{
		{ID: "mgr", Role: "manager", State: "waiting"},
		{ID: "be-1", Role: "backend", State: "running", Task: "T3", Line: "goose up"},
		{ID: "be-2", Role: "backend", State: "running", Task: "T1", CtxTokens: 21400},
		{ID: "fe-1", Role: "frontend", State: "idle"},
	} {
		b.SetAgent(a)
	}
	b.AddNote("be-1", "shared", "", "run `make test-unit` for the fast suite")
	b.RaiseAlert("lease", "fe-1 wanted users.go (held by be-2)")
	return b.Snapshot()
}

func TestHotIsDeterministicPersonalisedAndBounded(t *testing.T) {
	est := core.NewBytesEstimator().WithRatio(4)
	s := hotSnapshot()
	a := RenderHot(s, "be-2", "backend", false, DefaultHotConfig(), est)
	b := RenderHot(s, "be-2", "backend", false, DefaultHotConfig(), est)
	if a != b {
		t.Fatal("hot rendering must be deterministic")
	}
	for _, want := range []string{
		`<live board="v`, "you: be-2 (backend) · T1 · ctx 21k", "! fe-1 wanted users.go",
		`T1 doing be-2 "Paginate GET /users" — added limit/offset`, `T2 todo "Users table UI" (needs T1)`,
		"be-1 backend running T3", "make test-unit", "</live>",
	} {
		if !strings.Contains(a, want) {
			t.Fatalf("hot view missing %q:\n%s", want, a)
		}
	}
	if strings.Contains(a, "Write migration") {
		t.Fatalf("unrelated task leaked into a worker's view:\n%s", a)
	}
	// The manager sees every open task.
	m := RenderHot(s, "mgr", "manager", true, DefaultHotConfig(), est)
	if !strings.Contains(m, "Write migration for sessions") {
		t.Fatalf("manager view should list all open work:\n%s", m)
	}
	// A tiny budget drops low-priority lines but keeps identity and alerts.
	tiny := RenderHot(s, "be-2", "backend", false, HotConfig{MaxTokens: 60}, est)
	if !strings.Contains(tiny, "you: be-2") || !strings.Contains(tiny, "! fe-1") {
		t.Fatalf("essentials dropped:\n%s", tiny)
	}
	if !strings.Contains(tiny, "more lines omitted") || est.Tokens(tiny) >= est.Tokens(a) {
		t.Fatalf("budget not enforced:\n%s", tiny)
	}
}

func TestHotStaysBoundedWithFiftyAgents(t *testing.T) {
	b := NewBoard(nil)
	for i := 0; i < 50; i++ {
		id := fmt.Sprintf("w-%02d", i)
		task, _ := b.CreateTask("mgr", TaskSpec{Title: fmt.Sprintf("Task number %d with a moderately long title", i)})
		b.Assign("mgr", id, task.ID)
		b.SetAgent(AgentInfo{ID: id, Role: "backend", State: "running", Task: task.ID, Line: "doing some work on the thing"})
	}
	est := core.NewBytesEstimator().WithRatio(4)
	v := RenderHot(b.Snapshot(), "w-07", "backend", false, DefaultHotConfig(), est)
	if tok := est.Tokens(v); tok > 950 {
		t.Fatalf("worker hot view is %d tokens with 50 agents; budget is 900", tok)
	}
	if !strings.Contains(v, "you: w-07") {
		t.Fatal("identity missing")
	}
}
