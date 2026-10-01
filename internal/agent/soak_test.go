package agent_test

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/agent"
	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/cost"
	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/kv"
	"github.com/reee344/sleipnir/internal/provider"
	"github.com/reee344/sleipnir/internal/tools"
)

// A session that runs for hours is thousands of requests, and what the harness keeps of each one is what decides whether it can. The agent
// here makes a tool call a step for a thousand steps (the nightly run makes twenty thousand, SLEIPNIR_SOAK_STEPS), every result a few
// kilobytes, the thread folded as it grows (the compactor's patches are not valid, so the harness makes its own), and what is measured is
// what is alive after a collection a quarter, half and three quarters of the way through. The thread is folded, so it is bounded; what
// the run keeps besides is the archive's index (kv.Archive: a few hundred bytes for each turn it ever had, the turns themselves are in the
// blob store, which is on disk in a session and here: a store held in memory would be the test's own growth, it is what made the first
// version of this test look like a leak of 17 KB a step). The events go nowhere for the same reason.

func TestALongRunKeepsABoundedFootprint(t *testing.T) {
	if testing.Short() {
		t.Skip("two thousand steps")
	}
	agent.RetryBase = time.Millisecond
	steps := 1000
	if v := os.Getenv("SLEIPNIR_SOAK_STEPS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 100 {
			t.Fatalf("SLEIPNIR_SOAK_STEPS=%q is not a number of steps (100 at the least)", v)
		}
		steps = n
	}
	var n atomic.Int32
	var live [4]uint64
	var goroutines [4]int
	var thread [4]int
	var a *agent.Agent
	est := core.NewBytesEstimator()
	sample := func(i int) {
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		live[i] = m.HeapAlloc
		goroutines[i] = runtime.NumGoroutine()
		thread[i] = a.Stack().Thread.Tokens(est)
	}
	warm := core.Usage{InputTokens: 50, CacheReadTokens: 2000}
	prov := &arvProvider{}
	prov.fn = func(ctx context.Context, req *provider.Request) (core.Turn, core.Usage) {
		if arvIsCompactor(req) {
			return arvText("not a patch"), warm
		}
		k := int(n.Add(1))
		switch k {
		case steps / 4:
			sample(0)
		case steps / 2:
			sample(1)
		case 3 * steps / 4:
			sample(2)
		}
		if k <= steps {
			return arvTool(fmt.Sprintf("c%d", k), "big", map[string]any{"n": k}), warm
		}
		return arvText("done"), warm
	}
	reg := tools.NewRegistry()
	reg.Register(arvFake{name: "big", run: func() *tools.Result {
		return &tools.Result{Text: strings.Repeat("build output line with details and a few more words\n", 80)}
	}})
	specs, err := reg.Specs()
	if err != nil {
		t.Fatal(err)
	}
	model := cost.Model{ID: "arv-1", ContextTokens: 1_000_000, Cache: cost.OpenAICacheModel(),
		Price: cost.Price{InputPerM: 4, OutputPerM: 20, CacheReadPerM: 1, CacheWrite5mPerM: 4, CacheWrite1hPerM: 4}}
	pl := kv.DefaultPlanner()
	pl.SoftThreadTokens = 12000
	pl.MinThreadTokens = 3000
	blobs, err := events.NewDirBlobs(t.TempDir()) // on disk, as in a session: a store held in memory would be the test's own growth
	if err != nil {
		t.Fatal(err)
	}
	a, err = agent.New(agent.Config{
		ID: "be-1", Role: "backend", Model: model, Provider: prov, Tools: reg, ToolSpecs: specs,
		Const:  kv.NewLayer("const", kv.KindConst, 1, []kv.Segment{{Text: strings.Repeat("You are a careful coding agent. ", 40)}}),
		Shared: kv.NewLayer("shared", kv.KindShared, 1, []kv.Segment{{Key: "project", Text: strings.Repeat("The repo is a Go service. ", 60), Vol: kv.VolEpoch}}),
		Params: core.Params{MaxTokens: 512}, Events: events.Discard{}, Planner: pl, SessionID: "soak", MaxSteps: steps + 20,
		Blobs: blobs, Handles: tools.NewHandles(),
	})
	if err != nil {
		t.Fatal(err)
	}
	before := runtime.NumGoroutine()
	res, err := a.Run(context.Background(), "build everything, over and over")
	if err != nil || res == nil {
		t.Fatalf("the run: %v", err)
	}
	t.Logf("after a collection at a quarter, a half and three quarters of %d steps: live heap %.1f, %.1f, %.1f MB; thread %v tokens (folded from %d); goroutines %v, %d before",
		steps, float64(live[0])/1e6, float64(live[1])/1e6, float64(live[2])/1e6, thread[:3], pl.SoftThreadTokens, goroutines[:3], before)
	if live[0] == 0 || live[2] == 0 {
		t.Fatal("the samples were not taken")
	}
	// A quarter to three quarters is half the steps, two turns each. The index is a few hundred bytes a turn, a kilobyte a step; the
	// bound is twice that. What a request leaves behind is four times it or more (the tool output alone is four kilobytes).
	const perStep = 2 << 10
	between := int64(3*steps/4 - steps/4)
	if grew := int64(live[2]) - int64(live[0]); grew > between*perStep {
		t.Errorf("the live heap grew by %.1f MB between a quarter and three quarters of the run (%.1f to %.1f MB), %d bytes a step, more than the %d of the archive's index: something else is kept for every step",
			float64(grew)/1e6, float64(live[0])/1e6, float64(live[2])/1e6, grew/between, perStep)
	}
	// and the folding kept the thread where the planner puts it: never past the point at which it commits whatever it costs
	for i, tk := range thread[:3] {
		if tk > pl.HardThreadTokens {
			t.Errorf("at sample %d the thread was %d tokens, past the %d at which the planner commits a fold whatever it costs: the folding does not keep up", i, tk, pl.HardThreadTokens)
		}
	}
	if goroutines[2] > goroutines[0]+4 {
		t.Errorf("the goroutines went from %d to %d during the run: something is started for every step and left running", goroutines[0], goroutines[2])
	}
	// and what ran for the run is gone when it ends
	deadline := time.Now().Add(10 * time.Second)
	for runtime.NumGoroutine() > before+2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if g := runtime.NumGoroutine(); g > before+2 {
		t.Errorf("%d goroutines when the run ended, %d before it began", g, before)
	}
}
