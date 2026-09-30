package swarm

// Adversarial review tests (lens: prompt-cache economics and correctness).
// See docs/reviews/cache-economics.md. Convention: tests PASS while the defect
// is present; REVIEW_STRICT=1 inverts.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/agent"
	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/cost"
	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/kv"
	"github.com/reee344/sleipnir/internal/provider/mock"
	"github.com/reee344/sleipnir/internal/provider/openaichat"
	"github.com/reee344/sleipnir/internal/tools"
)

func cxBug(t *testing.T, present bool, format string, args ...any) {
	t.Helper()
	msg := fmt.Sprintf(format, args...)
	if os.Getenv("REVIEW_STRICT") != "" {
		if present {
			t.Fatalf("DEFECT PRESENT: %s", msg)
		}
		return
	}
	if !present {
		t.Fatalf("defect no longer reproduces (%s): invert or delete this review test", msg)
	}
	t.Logf("DEFECT CONFIRMED: %s", msg)
}

// The gate marks a prefix warm until first byte + ttl, but Anthropic measures
// the entry lifetime from the START of the request that wrote or read it. With a
// slow first byte the gate keeps waving a fan-out through as "warm" for
// TTFT seconds after the provider has dropped the entry (and it has no safety
// margin, unlike agent.isWarmLocked which subtracts ColdMargin).
func TestCacheEcon_GateWarmWindowOutlivesTheProviderEntry(t *testing.T) {
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

	clock = start.Add(5*time.Minute + 20*time.Second) // the provider entry (written/read at t0) expired 20s ago
	warm := g.Warm("k")
	t.Logf("gate at t0+5m20s: warm=%v (provider entry lifetime ended at t0+5m)", warm)
	cxBug(t, warm, "the gate presumes the prefix warm for a further TTFT seconds after the provider TTL, so a fan-out is released onto a cold prefix without a primer")
}

// Fan-out over a cold prefix stops being gated once the primer's first byte takes
// longer than maxWait (default 45s): every follower then proceeds in parallel and
// pays its own cold prefill, which is exactly what the gate exists to prevent.
func TestCacheEcon_GateStampedesWhenColdPrefillOutlastsMaxWait(t *testing.T) {
	g := NewWarmGate(5*time.Minute, 60*time.Millisecond)
	const followers = 7
	var mu sync.Mutex
	primerByte := time.Time{}
	var early int
	var wg sync.WaitGroup

	started, err := g.Enter(context.Background(), "k")
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Now()
	for i := 0; i < followers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			done, err := g.Enter(context.Background(), "k")
			if err != nil {
				return
			}
			mu.Lock()
			if primerByte.IsZero() { // released before the primer produced a byte
				early++
			}
			mu.Unlock()
			done(true)
		}()
	}
	time.Sleep(250 * time.Millisecond) // the primer's cold prefill is slow
	mu.Lock()
	primerByte = time.Now()
	mu.Unlock()
	started(true)
	wg.Wait()
	t.Logf("%d of %d followers were released %v before the primer's first byte", early, followers, primerByte.Sub(t0).Round(time.Millisecond)-60*time.Millisecond)
	cxBug(t, early == followers, "with maxWait < TTFT all %d followers run their own cold prefill in parallel (default maxWait is 45s; a 200k-token cold prefill on a loaded engine can exceed that)", early)
}

// ---------------------------------------------------------------------------
// R-RU1: a reused worker never receives the new task's description, scope or
// dependencies. Spawn(agent=...) sends only "Begin task T2: <title>. Your
// assignment and scope are in <my-notes>." while <my-notes> still holds the
// FIRST task's assignment (notes are built once, at first spawn).
// ---------------------------------------------------------------------------

func TestCacheEcon_ReusedWorkerNeverSeesTheNewTasksBrief(t *testing.T) {
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
	sawBrief := strings.Contains(second, "MUST-USE-CURSOR-PAGINATION-BRIEF")
	sawOld := strings.Contains(second, "Your assignment is task T1")
	t.Logf("reused worker's prompt: mentions new brief=%v, still shows the first assignment in <my-notes>=%v", sawBrief, sawOld)
	_ = json.Marshal
	cxBug(t, !sawBrief && sawOld, "the reused worker is told to read its assignment in <my-notes>, which still describes task T1; the new task's description and scope never reach it")
}
