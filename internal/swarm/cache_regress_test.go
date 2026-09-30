package swarm

// Regression tests for the prompt-cache economics review (R12, R15): the warm
// gate's keys, window and stuck-primer behaviour, and the brief a reused worker
// receives.

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/agent"
	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/cost"
	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/kv"
	"github.com/reee344/sleipnir/internal/perm"
	"github.com/reee344/sleipnir/internal/provider/mock"
	"github.com/reee344/sleipnir/internal/provider/openaichat"
	"github.com/reee344/sleipnir/internal/tools"
)

// The provider measures an entry's lifetime from the START of the request that
// wrote or last read it. The gate presumes a prefix warm from that moment, less a
// safety margin, and never past the entry's end however long the first byte took.
func TestCacheEcon_GateWarmWindowEndsBeforeTheProviderEntry(t *testing.T) {
	g := NewWarmGate(5*time.Minute, time.Second)
	clock := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	g.now = func() time.Time { return clock }
	start := clock

	started, err := g.Enter(context.Background(), "k") // the primer starts at t0
	if err != nil {
		t.Fatal(err)
	}
	clock = start.Add(45 * time.Second) // a long cold prefill: first byte after 45s
	started(true)

	at := func(d time.Duration) bool { clock = start.Add(d); return g.Warm("k") }
	if !at(4 * time.Minute) {
		t.Fatal("warm well inside the entry's lifetime")
	}
	if at(4*time.Minute + 45*time.Second) {
		t.Fatal("the safety margin (20s) before the provider's 5-minute mark must already be cold")
	}
	if at(5*time.Minute + 20*time.Second) {
		t.Fatal("warm 20s after the provider entry expired (the window used to run from the first byte)")
	}

	// A request over a warm prefix reads it, which refreshes the entry: the window
	// restarts at that request's start.
	clock = start.Add(4 * time.Minute)
	again, err := g.Enter(context.Background(), "k")
	if err != nil {
		t.Fatal(err)
	}
	clock = start.Add(4*time.Minute + 2*time.Second)
	again(true)
	if !at(8 * time.Minute) {
		t.Fatal("a refreshing read extends the window")
	}
	if at(9 * time.Minute) {
		t.Fatal("...but only by one lifetime from the refreshing request's start")
	}
}

// A slow cold prefill (or a hung connection) must neither hold followers forever
// nor release them all at once onto a cold prefix: after maxWait ONE follower is
// released as a co-primer; the first byte of any primer then warms the level for
// everyone still waiting.
func TestCacheEcon_GateStaggersReleaseWhenThePrimerIsStuck(t *testing.T) {
	const followers = 7
	g := NewWarmGate(5*time.Minute, 80*time.Millisecond)
	started, err := g.Enter(context.Background(), "k")
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Now()
	var mu sync.Mutex
	var releasedAt []time.Duration
	var wg sync.WaitGroup
	primerByteSeen := make(chan struct{})
	for i := 0; i < followers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			done, err := g.Enter(context.Background(), "k")
			if err != nil {
				return
			}
			mu.Lock()
			releasedAt = append(releasedAt, time.Since(t0))
			mu.Unlock()
			// A released co-primer is as slow as the primer: its own first byte only
			// arrives with the primer's.
			<-primerByteSeen
			done(true)
		}()
	}
	// The primer's first byte arrives after the first escalation tick (80ms) and
	// before the second (160ms).
	time.Sleep(115 * time.Millisecond)
	primerByte := time.Since(t0)
	started(true)
	close(primerByteSeen)
	wg.Wait()
	early := 0
	for _, d := range releasedAt {
		if d < primerByte-5*time.Millisecond {
			early++
		}
	}
	t.Logf("%d of %d followers were released before the primer's first byte (at %v); release times %v", early, followers, primerByte.Round(time.Millisecond), releasedAt)
	if early != 1 {
		t.Fatalf("exactly one co-primer may be released while the primer is stuck (the old gate released all %d at maxWait), got %d", followers, early)
	}
}

// A hung primer that never calls back must not tax every later request: the
// first co-primer to succeed warms the key.
func TestCacheEcon_GateRecoversFromAPrimerThatNeverReports(t *testing.T) {
	const maxWait = 100 * time.Millisecond
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
	if waits[0] < maxWait/2 || waits[1] > maxWait/4 || waits[2] > maxWait/4 {
		t.Fatalf("only the first follower pays the wait: %v", waits)
	}
}

// Keys are nested levels joined by "|": the shared prefix, then the role prefix
// inside it. Agents of a second role are not released onto a role prefix nobody
// has written yet.
func TestCacheEcon_GateElectsOnePrimerPerLevel(t *testing.T) {
	g := NewWarmGate(time.Minute, 5*time.Second)
	ctx := context.Background()
	var order []string
	var mu sync.Mutex
	note := func(s string) { mu.Lock(); order = append(order, s); mu.Unlock() }

	x1, _ := g.Enter(ctx, "A|X") // primer of A and of A|X
	var wg sync.WaitGroup
	enter := func(name, key string, hold time.Duration) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fin, err := g.Enter(ctx, key)
			if err != nil {
				t.Error(err)
				return
			}
			note("in:" + name)
			time.Sleep(hold)
			note("byte:" + name)
			fin(true)
		}()
	}
	enter("X2", "A|X", 0)
	time.Sleep(10 * time.Millisecond)
	// Two agents of a second role: one becomes the primer of A|Y once A is warm
	// (whichever gets there first), the other must wait for ITS first byte.
	enter("Y1", "A|Y", 60*time.Millisecond)
	enter("Y2", "A|Y", 60*time.Millisecond)
	time.Sleep(30 * time.Millisecond)
	if g.Warm("A") || g.Warm("A|X") {
		t.Fatal("nothing is warm before the primer's first byte")
	}
	note("byte:X1")
	x1(true)
	wg.Wait()
	joined := strings.Join(order, " ")
	t.Logf("order: %s", joined)
	idx := func(s string) int { return strings.Index(joined, s) }
	if !(idx("byte:X1") < idx("in:X2") && idx("byte:X1") < idx("in:Y1") && idx("byte:X1") < idx("in:Y2")) {
		t.Fatalf("followers wait for the primer's first byte: %s", joined)
	}
	firstY, secondY := "Y1", "Y2"
	if idx("in:Y2") < idx("in:Y1") {
		firstY, secondY = "Y2", "Y1"
	}
	if !(idx("byte:"+firstY) < idx("in:"+secondY)) {
		t.Fatalf("a second-role follower waits for ITS role's primer, not just the shared one: %s", joined)
	}
	if !g.Warm("A|X") || !g.Warm("A|Y") || !g.Warm("A") {
		t.Fatal("all levels are warm at the end")
	}
	// A failed primer hands over at its level.
	f1, _ := g.Enter(ctx, "B|Z")
	got := make(chan struct{})
	go func() {
		f, err := g.Enter(ctx, "B|Z")
		if err == nil {
			f(true)
		}
		close(got)
	}()
	time.Sleep(20 * time.Millisecond)
	f1(false)
	select {
	case <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("waiter stuck behind a failed primer")
	}
	if !g.Warm("B|Z") {
		t.Fatal("the promoted waiter warms every level")
	}
}

// Nested keys, random failures and cancellations: nothing deadlocks and no level
// is left in the priming state.
func TestCacheEcon_GateNestedKeysStress(t *testing.T) {
	g := NewWarmGate(30*time.Millisecond, 200*time.Millisecond)
	var wg sync.WaitGroup
	var entered atomic.Int32
	for i := 0; i < 300; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if i%5 == 0 {
				time.AfterFunc(time.Duration(rand.Intn(2000))*time.Microsecond, cancel)
			}
			fin, err := g.Enter(ctx, fmt.Sprintf("s%d|r%d", i%3, i%4))
			if err != nil {
				return
			}
			entered.Add(1)
			time.Sleep(time.Duration(rand.Intn(400)) * time.Microsecond)
			fin(i%7 != 0)
			fin(i%7 == 0) // "exactly once": a second call must be harmless
		}(i)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("nested-key gate deadlocked")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for k, ks := range g.keys {
		if ks.priming {
			t.Fatalf("level %q left in the priming state", k)
		}
	}
	if entered.Load() == 0 {
		t.Fatal("nobody got through")
	}
}

// ---------------------------------------------------------------------------
// R15: a reused worker receives the new task's brief, scope and dependencies.
// ---------------------------------------------------------------------------

func TestCacheEcon_ReusedWorkerReceivesTheNewTasksBrief(t *testing.T) {
	agent.RetryBase = time.Millisecond
	var mu sync.Mutex
	var prompts []string
	srv := mock.New(mock.Config{Engine: mock.EngineConfig{BlockTokens: 16, MinCacheTokens: 64}}, func(c *mock.Call) mock.Reply {
		var sb strings.Builder
		for _, m := range c.Messages {
			sb.WriteString(m.Role + ": " + m.Content + "\n")
		}
		mu.Lock()
		prompts = append(prompts, sb.String())
		mu.Unlock()
		return mock.Reply{Text: "done"}
	})
	ts := srv.Start()
	t.Cleanup(ts.Close)
	client := openaichat.New(openaichat.Config{Name: "mock", BaseURL: ts.URL, Options: openaichat.Options{SessionHeader: true, CacheKeyBody: true}})
	model := cost.Model{ID: "mock-1", ContextTokens: 1_000_000, Cache: cost.OpenAICacheModel(),
		Price: cost.Price{InputPerM: 4, OutputPerM: 20, CacheReadPerM: 1, CacheWrite5mPerM: 4, CacheWrite1hPerM: 4}}
	log := events.NewMemLog()
	blobs := events.NewMemBlobs()
	deps := Deps{
		Provider: client, Model: model, Events: log, Blobs: blobs, Archive: kv.NewArchive(blobs),
		Const:   kv.NewLayer("const", kv.KindConst, 1, []kv.Segment{{Text: agent.Constitution(agent.ConstitutionOpts{Swarm: true})}}),
		Shared:  kv.NewLayer("shared", kv.KindShared, 1, []kv.Segment{{Key: "project", Text: strings.Repeat("The repo is a Go API. ", 100), Vol: kv.VolEpoch}}),
		Workdir: t.TempDir(), Root: t.TempDir(), Params: core.Params{MaxTokens: 256},
		Files: tools.NewFileState(), Handles: tools.NewHandles(),
		Perm: perm.AllowAll{}, // these tests are about the swarm, not the permission policy
	}
	sw := New(Config{}, deps, nil)
	reg := tools.NewRegistry()
	for _, tl := range sw.Tools() {
		reg.Register(tl)
	}
	specs, err := reg.Specs()
	if err != nil {
		t.Fatal(err)
	}
	sw.SetToolset(reg, specs)
	sw.Start(context.Background())
	t.Cleanup(sw.Shutdown)

	id, err := sw.Spawn(SpawnReq{Role: "backend", Title: "first task", Brief: "brief one", By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	waitIdle := func() {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if m := sw.get(id); m != nil && !m.isActive() {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatal("worker did not go idle")
	}
	time.Sleep(50 * time.Millisecond)
	waitIdle()
	mu.Lock()
	first := prompts[0]
	mu.Unlock()
	if strings.Contains(first, "New assignment") {
		t.Fatal("a fresh worker's kickoff stays short: its card is in <my-notes>")
	}

	t2, err := sw.Board.CreateTask("mgr", TaskSpec{Title: "second task", Desc: "MUST-USE-CURSOR-PAGINATION-BRIEF", Role: "backend", Files: []string{"pkg/users/"}})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	before := len(prompts)
	mu.Unlock()
	if _, err := sw.Spawn(SpawnReq{Agent: id, TaskID: t2.ID, By: "mgr"}); err != nil {
		t.Fatalf("reuse: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	waitIdle()
	mu.Lock()
	second := ""
	if len(prompts) > before {
		second = prompts[len(prompts)-1]
	}
	mu.Unlock()
	if second == "" {
		t.Fatal("no request was made for the reused worker")
	}
	// The scope is printed in its canonical form (with or without a trailing slash).
	for _, want := range []string{"MUST-USE-CURSOR-PAGINATION-BRIEF", "second task", "Scope: pkg/users", "New assignment", "replaces the assignment in <my-notes>"} {
		if !strings.Contains(second, want) {
			t.Fatalf("the reused worker's prompt is missing %q:\n%s", want, second[max(0, len(second)-900):])
		}
	}

	// Both are the harness's own task turns, never the person's word (compaction pins a
	// person's turns into the instructions the agent obeys): the kickoff carries no card, and
	// the reassignment carries its card as a task block, which replaces the notes' assignment
	// when the turn is folded.
	var tasks []core.Turn
	for _, tr := range sw.get(id).a.Thread().Snapshot().Turns {
		switch tr.Origin {
		case core.OriginUser:
			t.Fatalf("a swarm worker's thread holds a turn typed by a person: %+v", tr)
		case core.OriginTask:
			tasks = append(tasks, tr)
		}
	}
	if len(tasks) != 2 {
		t.Fatalf("want a kickoff and a reassignment, got %d task turns", len(tasks))
	}
	if len(tasks[0].Blocks) != 1 || kv.IsTask(tasks[0].Blocks[0]) {
		t.Fatalf("a fresh worker's kickoff must not carry a card (its notes hold it): %+v", tasks[0].Blocks)
	}
	if len(tasks[1].Blocks) != 2 || kv.IsTask(tasks[1].Blocks[0]) || !kv.IsTask(tasks[1].Blocks[1]) ||
		!strings.Contains(tasks[1].Blocks[1].Text, "MUST-USE-CURSOR-PAGINATION-BRIEF") || !strings.Contains(tasks[1].Blocks[0].Text, "replaces the assignment in <my-notes>") {
		t.Fatalf("the reassignment is a brief and a task block with the new card: %+v", tasks[1].Blocks)
	}
}
