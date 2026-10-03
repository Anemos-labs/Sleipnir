package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/cost"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/kv"
	"github.com/anemos-labs/sleipnir/internal/provider"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/provider/openaichat"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

// fakeTool is a configurable tool for loop tests.
type fakeTool struct {
	name     string
	readOnly bool
	run      func(in json.RawMessage) *tools.Result
	// runCtx, when set, is used instead of run and gets the call's context and the call.
	runCtx func(ctx context.Context, c *tools.Call) *tools.Result
}

func (f fakeTool) Spec() core.ToolSpec {
	return core.ToolSpec{Name: f.name, Description: "fake " + f.name, InputSchema: json.RawMessage(`{"type":"object"}`), ReadOnly: f.readOnly}
}

func (f fakeTool) Run(ctx context.Context, c *tools.Call) (*tools.Result, error) {
	if f.runCtx != nil {
		return f.runCtx(ctx, c), nil
	}
	return f.run(c.Input), nil
}

type rig struct {
	t      *testing.T
	srv    *mock.Server
	agent  *agent.Agent
	log    *events.MemLog
	tools  *tools.Registry
	client *openaichat.Client
}

type rigOpts struct {
	mock      mock.Config
	planner   kv.Planner
	noCompact bool
	model     *cost.Model
	tools     []fakeTool
	steps     int
	snapSteps bool
	budget    float64
	blobs     events.Blobs
	capture   bool // ask the (mock) endpoint for token ids and logprobs
	hooks     agent.Hooks
	notes     *kv.Layer  // the agent's notes at the start (a swarm worker's assignment)
	sink      agent.Sink // what the person is told; nil means nothing

	verifyHint     string            // the project's test command
	planOpen       func(string) int  // how many steps of the agent's plan are open
	compactor      provider.Provider // a model of its own for the compaction summaries
	compactorModel cost.Model
	effort         *provider.EffortSetting
}

func newRig(t *testing.T, opts rigOpts, r mock.Responder) *rig {
	t.Helper()
	agent.RetryBase = time.Millisecond
	srv := mock.New(opts.mock, r)
	ts := srv.Start()
	t.Cleanup(ts.Close)
	prof := openaichat.DefaultProfile("mock", ts.URL)
	prof.CaptureTokens = opts.capture
	client := openaichat.New(openaichat.Config{Name: "mock", BaseURL: ts.URL, Profile: &prof, Options: openaichat.Options{SessionHeader: true, CacheKeyBody: true, ReasoningEffortField: "reasoning_effort"}})

	reg := tools.NewRegistry()
	fts := opts.tools
	if len(fts) == 0 {
		fts = []fakeTool{
			{name: "echo", readOnly: true, run: func(in json.RawMessage) *tools.Result { return &tools.Result{Text: "echo:" + string(in)} }},
			{name: "big", run: func(json.RawMessage) *tools.Result {
				return &tools.Result{Text: strings.Repeat("build output line with details\n", 120)}
			}},
		}
	}
	for _, f := range fts {
		reg.Register(f)
	}
	specs, err := reg.Specs()
	if err != nil {
		t.Fatal(err)
	}
	model := cost.Defaults().All()[0]
	if m, ok := cost.Defaults().Lookup("mock-1"); ok {
		model = m
	}
	model.Cache = cost.OpenAICacheModel()
	model.Cache.MinPrefixTokens = 64
	model.Price = cost.Price{InputPerM: 4, OutputPerM: 20, CacheReadPerM: 1, CacheWrite5mPerM: 4, CacheWrite1hPerM: 4}
	if opts.model != nil {
		model = *opts.model
	}
	log := events.NewMemLog()
	a, err := agent.New(agent.Config{
		SnapshotEachStep: opts.snapSteps,
		ID:               "be-1", Role: "backend", Model: model, Provider: client, Tools: reg, ToolSpecs: specs,
		Const:  kv.NewLayer("const", kv.KindConst, 1, []kv.Segment{{Text: strings.Repeat("You are Sleipnir, a careful coding agent. ", 120)}}),
		Shared: kv.NewLayer("shared", kv.KindShared, 1, []kv.Segment{{Key: "project", Text: strings.Repeat("The repo is a Go service with a users API. ", 150), Vol: kv.VolEpoch}}),
		Params: core.Params{MaxTokens: 512}, Effort: opts.effort,
		Events: log, Planner: opts.planner, NoCompaction: opts.noCompact, Notes: opts.notes,
		SessionID: "testsession", MaxSteps: opts.steps, BudgetUSD: opts.budget,
		Now: time.Now, Blobs: opts.blobs, CaptureTokens: opts.capture, Hooks: opts.hooks,
		Compactor: opts.compactor, CompactorModel: opts.compactorModel, PlanOpen: opts.planOpen, VerifyHint: opts.verifyHint, Sink: opts.sink,
	})
	if err != nil {
		t.Fatal(err)
	}
	// A compaction still running when its test ends (a second turn can start one the test never waits for) reads RetryBase while
	// the next test's rig writes it: -race fails a test that did nothing wrong, and only when the machine is busy.
	t.Cleanup(func() { _ = a.Close() })
	return &rig{t: t, srv: srv, agent: a, log: log, tools: reg, client: client}
}

func isCompactor(c *mock.Call) bool { return strings.Contains(c.LastUser(), "<compactor-task>") }

// scriptedWork makes the model call `big` for n turns then finish; compactor
// forks get compactorReply.
func scriptedWork(n int, compactorReply func(c *mock.Call) string) mock.Responder {
	var mu sync.Mutex
	step := 0
	return func(c *mock.Call) mock.Reply {
		if isCompactor(c) {
			return mock.Reply{Text: compactorReply(c)}
		}
		mu.Lock()
		step++
		s := step
		mu.Unlock()
		if s <= n {
			return mock.Reply{Text: fmt.Sprintf("working on step %d", s), ToolCalls: []mock.ToolCall{{ID: fmt.Sprintf("call_%d", s), Name: "big", Args: fmt.Sprintf(`{"step":%d}`, s)}}}
		}
		return mock.Reply{Text: "all done"}
	}
}

func TestSingleAgentToolLoop(t *testing.T) {
	r := newRig(t, rigOpts{noCompact: true}, func(c *mock.Call) mock.Reply {
		if len(c.Messages) < 4 {
			return mock.Reply{Text: "let me check", ToolCalls: []mock.ToolCall{{ID: "call_a", Name: "echo", Args: `{"q":"hi"}`}}}
		}
		return mock.Reply{Text: "the tool said hi"}
	})
	res, err := r.agent.Run(context.Background(), "say hi via the echo tool")
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "the tool said hi" || res.Steps != 2 {
		t.Fatalf("result = %+v", res)
	}
	snap := r.agent.Thread().Snapshot()
	if err := kv.Validate(snap.Turns); err != nil {
		t.Fatal(err)
	}
	if len(snap.Turns) != 4 {
		t.Fatalf("thread has %d turns, want user/assistant/tool-results/assistant", len(snap.Turns))
	}
	tr := snap.Turns[2].Blocks[0]
	if tr.Kind != core.BlockToolResult || tr.PlainText() != `echo:{"q":"hi"}` {
		t.Fatalf("tool result = %+v", tr)
	}
	if n := len(r.log.OfType(events.TypeModelRequest)); n != 2 {
		t.Fatalf("model.request events = %d", n)
	}
	if n := len(r.log.OfType(events.TypeTurnAppend)); n != 4 {
		t.Fatalf("turn.append events = %d", n)
	}
	if n := len(r.log.OfType(events.TypeToolResult)); n != 1 {
		t.Fatalf("tool.result events = %d", n)
	}
	_, usd := r.agent.Usage()
	if usd <= 0 {
		t.Fatal("cost must be tracked")
	}
}

func TestSteadyStateHitRatioAndNoDrift(t *testing.T) {
	r := newRig(t, rigOpts{noCompact: true, mock: mock.Config{Engine: mock.EngineConfig{BlockTokens: 16, MinCacheTokens: 64}}}, scriptedWork(14, nil))
	if _, err := r.agent.Run(context.Background(), "build it"); err != nil {
		t.Fatal(err)
	}
	st := r.srv.Stats()
	if len(st) != 15 {
		t.Fatalf("requests = %d", len(st))
	}
	for i := 2; i < len(st); i++ {
		if st[i].HitRatio() < 0.80 {
			t.Fatalf("request %d hit ratio %.2f (cached %d / %d)", i+1, st[i].HitRatio(), st[i].Cached, st[i].PromptTokens)
		}
	}
	if n := len(r.log.OfType(events.TypeCacheAnomaly)); n != 0 {
		t.Fatalf("append-only loop produced %d cache anomalies: %s", n, r.log.OfType(events.TypeCacheAnomaly)[0].Data)
	}
	// All requests share one routing key so a marketplace router pins them.
	keys := map[string]bool{}
	for _, s := range st {
		keys[s.Session] = true
	}
	if len(keys) != 1 {
		t.Fatalf("expected one affinity key, got %v", keys)
	}
}

func compactorPatch(keepFrom int) func(c *mock.Call) string {
	return func(c *mock.Call) string {
		return fmt.Sprintf("```json\n"+`{"keep_from":"t%d","spine":[{"turns":"t1-t%d","line":"Built the project repeatedly with go build; output noisy but green"}],`+
			`"notes":[{"op":"add","key":"facts","text":"- build command output is noisy but always green"}],"promote":[{"scope":"shared","key":"conventions","text":"build: go build ./..."}]}`+"\n```", keepFrom, keepFrom-1)
	}
}

func TestBackgroundCompactionShrinksPromptAndCacheRecovers(t *testing.T) {
	pl := kv.DefaultPlanner()
	pl.SoftThreadTokens = 6500
	pl.MinThreadTokens = 2000
	r := newRig(t, rigOpts{planner: pl, mock: mock.Config{Engine: mock.EngineConfig{BlockTokens: 16, MinCacheTokens: 64}}},
		scriptedWork(22, func(c *mock.Call) string {
			// Fork sees the whole thread; keep the last ~4 exchanges (turn ids grow by 2 per step).
			n := 0
			for _, m := range c.Messages {
				if m.Role == "assistant" {
					n++
				}
			}
			keep := 2*n - 6
			if keep < 3 {
				keep = 3
			}
			return compactorPatch(keep)(c)
		}))
	// Let compaction jobs land between steps.
	res, err := r.agent.Run(context.Background(), "please build the whole thing")
	if err != nil {
		t.Fatal(err)
	}
	// Give any in-flight compactor a moment, then take another step to commit.
	time.Sleep(50 * time.Millisecond)
	if res.Compactions == 0 && len(r.log.OfType(events.TypeCompactCommit)) == 0 {
		// The last job may have landed after the final boundary; drive one more turn.
		if _, err := r.agent.Run(context.Background(), "and once more"); err != nil {
			t.Fatal(err)
		}
	}
	commits := r.log.OfType(events.TypeCompactCommit)
	if len(commits) == 0 {
		for _, e := range r.log.OfType(events.TypeCompactReject) {
			t.Logf("reject: %s", e.Data)
		}
		for _, e := range r.log.OfType(events.TypeCompactPatch) {
			t.Logf("patch: %s", e.Data)
		}
		t.Fatalf("expected at least one compaction commit; plan events: %d, rejects: %d", len(r.log.OfType(events.TypeCompactPlan)), len(r.log.OfType(events.TypeCompactReject)))
	}
	var c struct {
		RemovedTokens  int  `json:"removed_tokens"`
		RetainedTokens int  `json:"retained_tokens"`
		SpineAdded     int  `json:"spine_added"`
		Fallback       bool `json:"fallback"`
	}
	json.Unmarshal(commits[0].Data, &c)
	if c.Fallback || c.RemovedTokens < 2000 || c.SpineAdded > c.RemovedTokens/5 {
		t.Fatalf("commit should fold a lot into a tiny spine using the model's patch: %+v", c)
	}
	// The spine and notes must now be part of the rendered prompt.
	stk := r.agent.Stack()
	if !strings.Contains(stk.Spine.Text(), "Built the project repeatedly") {
		t.Fatalf("spine not installed:\n%s", stk.Spine.Text())
	}
	if seg, ok := stk.Notes.Segment("instructions"); !ok || !strings.Contains(seg.Text, "build the whole thing") {
		t.Fatalf("user instruction must be preserved verbatim in notes: %+v", stk.Notes)
	}
	if n := len(r.log.OfType(events.TypeCacheAnomaly)); n != 0 {
		t.Fatalf("a declared rebase must not register as drift/anomaly, got %d: %s", n, r.log.OfType(events.TypeCacheAnomaly)[0].Data)
	}
	// The cache pays once at each rebase and then recovers: requests that do not
	// directly follow a commit hit as usual, and even the one right after a commit
	// keeps the deep prefix (tools, constitution, shared pin) warm.
	type sample struct {
		hit         float64
		afterCommit bool
	}
	var samples []sample
	after := false
	for _, e := range r.log.All() {
		switch e.Type {
		case events.TypeCompactCommit:
			after = true
		case events.TypeModelResponse:
			var m struct {
				Hit  float64 `json:"hit_ratio"`
				Side bool    `json:"side"`
			}
			json.Unmarshal(e.Data, &m)
			if m.Side {
				continue
			}
			samples = append(samples, sample{m.Hit, after})
			after = false
		}
	}
	var post, steady int
	for i, sm := range samples {
		switch {
		case sm.afterCommit:
			post++
			if sm.hit < 0.2 {
				t.Fatalf("request %d right after a commit lost the deep prefix: hit %.2f", i, sm.hit)
			}
		case i >= 2:
			steady++
			if sm.hit < 0.6 {
				t.Fatalf("steady request %d hit ratio %.2f", i, sm.hit)
			}
		}
	}
	if post == 0 || steady == 0 {
		t.Fatalf("test did not exercise both regimes: post=%d steady=%d", post, steady)
	}
	// The thread must stay structurally valid.
	if err := kv.Validate(r.agent.Thread().Snapshot().Turns); err != nil {
		t.Fatal(err)
	}
}

func TestCompactorGarbageFallsBackToMechanical(t *testing.T) {
	pl := kv.DefaultPlanner()
	pl.SoftThreadTokens = 2500
	pl.MinThreadTokens = 800
	r := newRig(t, rigOpts{planner: pl}, scriptedWork(18, func(*mock.Call) string { return "I'm sorry, I can't produce JSON today." }))
	if _, err := r.agent.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	r.agent.Run(context.Background(), "next")
	commits := r.log.OfType(events.TypeCompactCommit)
	if len(commits) == 0 {
		t.Fatal("a failed model patch must still result in a mechanical compaction")
	}
	var c struct {
		Fallback bool `json:"fallback"`
	}
	json.Unmarshal(commits[0].Data, &c)
	if !c.Fallback {
		t.Fatalf("commit should be flagged fallback: %s", commits[0].Data)
	}
	if len(r.log.OfType(events.TypeCompactReject)) == 0 {
		t.Fatal("the bad model reply should be logged as a rejection")
	}
}

func TestRetriesRateLimitThenSucceeds(t *testing.T) {
	var n atomic.Int32
	r := newRig(t, rigOpts{noCompact: true}, func(c *mock.Call) mock.Reply {
		if n.Add(1) <= 2 {
			return mock.Reply{Fault: &mock.Fault{Status: 429, Message: "slow down"}}
		}
		return mock.Reply{Text: "finally"}
	})
	res, err := r.agent.Run(context.Background(), "hi")
	if err != nil || res.Text != "finally" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if got := len(r.log.OfType(events.TypeModelError)); got != 2 {
		t.Fatalf("retry events = %d, want 2", got)
	}
}

func TestAuthErrorIsNotRetried(t *testing.T) {
	r := newRig(t, rigOpts{noCompact: true}, func(*mock.Call) mock.Reply { return mock.Reply{Fault: &mock.Fault{Status: 401, Message: "bad key"}} })
	_, err := r.agent.Run(context.Background(), "hi")
	if err == nil || !strings.Contains(err.Error(), "auth") {
		t.Fatalf("err = %v", err)
	}
	if got := len(r.srv.Stats()); got != 1 {
		t.Fatalf("auth failures must not be retried, saw %d requests", got)
	}
}

func TestReadOnlyToolsRunConcurrentlyResultsStayOrdered(t *testing.T) {
	var running, peak atomic.Int32
	slow := func(name string) fakeTool {
		return fakeTool{name: name, readOnly: true, run: func(json.RawMessage) *tools.Result {
			cur := running.Add(1)
			for {
				p := peak.Load()
				if cur <= p || peak.CompareAndSwap(p, cur) {
					break
				}
			}
			time.Sleep(60 * time.Millisecond)
			running.Add(-1)
			return &tools.Result{Text: "result-of-" + name}
		}}
	}
	r := newRig(t, rigOpts{noCompact: true, tools: []fakeTool{slow("a"), slow("b"), slow("c")}}, func(c *mock.Call) mock.Reply {
		if len(c.Messages) < 4 {
			return mock.Reply{ToolCalls: []mock.ToolCall{
				{ID: "c1", Name: "c", Args: `{}`}, {ID: "c2", Name: "a", Args: `{}`}, {ID: "c3", Name: "b", Args: `{}`},
			}}
		}
		return mock.Reply{Text: "ok"}
	})
	if _, err := r.agent.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if peak.Load() < 3 {
		t.Fatalf("read-only calls should overlap, peak concurrency %d", peak.Load())
	}
	results := r.agent.Thread().Snapshot().Turns[2].Blocks
	want := []string{"result-of-c", "result-of-a", "result-of-b"}
	for i, w := range want {
		if results[i].ToolID != fmt.Sprintf("c%d", i+1) || results[i].PlainText() != w {
			t.Fatalf("result %d = %+v, want %s in call order", i, results[i], w)
		}
	}
}

func TestBadToolInputsBecomeModelVisibleErrors(t *testing.T) {
	r := newRig(t, rigOpts{noCompact: true}, func(c *mock.Call) mock.Reply {
		if len(c.Messages) < 4 {
			return mock.Reply{ToolCalls: []mock.ToolCall{
				{ID: "u1", Name: "no_such_tool", Args: `{}`},
				{ID: "u2", Name: "echo", Args: `{"broken`},
			}, Finish: "length"}
		}
		return mock.Reply{Text: "recovered"}
	})
	res, err := r.agent.Run(context.Background(), "go")
	if err != nil || res.Text != "recovered" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	blocks := r.agent.Thread().Snapshot().Turns[2].Blocks
	if !blocks[0].IsError || !strings.Contains(blocks[0].PlainText(), "unknown tool") {
		t.Fatalf("unknown tool: %+v", blocks[0])
	}
	if !blocks[1].IsError || !strings.Contains(blocks[1].PlainText(), "not valid JSON") {
		t.Fatalf("truncated args: %+v", blocks[1])
	}
}

func TestSteeringRidesWithNextToolResults(t *testing.T) {
	var sawSteer atomic.Bool
	r := newRig(t, rigOpts{noCompact: true}, func(c *mock.Call) mock.Reply {
		for _, m := range c.Messages {
			if strings.Contains(m.Content, "use cursor pagination") {
				sawSteer.Store(true)
			}
		}
		if len(c.Messages) < 4 {
			return mock.Reply{ToolCalls: []mock.ToolCall{{ID: "s1", Name: "echo", Args: `{}`}}}
		}
		return mock.Reply{Text: "ok"}
	})
	r.agent.Send("[from user] use cursor pagination")
	if _, err := r.agent.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if !sawSteer.Load() {
		t.Fatal("queued steering never reached the model")
	}
	if err := kv.Validate(r.agent.Thread().Snapshot().Turns); err != nil {
		t.Fatal(err)
	}
}

func TestCancellationStopsTheLoop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := newRig(t, rigOpts{noCompact: true}, func(c *mock.Call) mock.Reply {
		cancel()
		return mock.Reply{ToolCalls: []mock.ToolCall{{ID: "x", Name: "echo", Args: `{}`}}}
	})
	_, err := r.agent.Run(ctx, "go")
	if err == nil {
		t.Fatal("expected cancellation error")
	}
}

func TestBudgetStopsTheAgent(t *testing.T) {
	// Prices are inflated so a handful of requests exceeds a $1 budget.
	m := cost.Model{ID: "mock-1", ContextTokens: 1_000_000, Price: cost.Price{InputPerM: 40000, OutputPerM: 200000, CacheReadPerM: 10000, CacheWrite5mPerM: 40000}, Cache: cost.OpenAICacheModel()}
	r := newRig(t, rigOpts{noCompact: true, model: &m, budget: 1.0}, scriptedWork(50, nil))
	_, err := r.agent.Run(context.Background(), "go")
	if err != agent.ErrBudget {
		t.Fatalf("err = %v, want ErrBudget", err)
	}
	if got := len(r.srv.Stats()); got >= 50 {
		t.Fatalf("budget did not stop the loop early (%d requests)", got)
	}
}
