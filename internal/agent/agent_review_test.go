package agent_test

// Adversarial review tests for the agent loop's failure handling
// (docs/reviews/swarm-concurrency.md): inbox draining at Run exit, background
// compactor lifetime, and the time-to-first-byte bound on a model request. All of
// them are ordinary regression tests now (docs/reviews/tranche2-a.md and
// tranche2-b.md).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/cost"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/kv"
	"github.com/anemos-labs/sleipnir/internal/provider"
	"github.com/anemos-labs/sleipnir/internal/provider/openaichat"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

// arvProvider is an in-process provider whose replies the test scripts.
type arvProvider struct {
	fn func(ctx context.Context, req *provider.Request) (core.Turn, core.Usage)
	n  atomic.Int64
}

func (p *arvProvider) Profile() provider.Profile {
	return provider.Profile{Name: "arv", Dialect: "openai-chat", Cache: cost.OpenAICacheModel()}
}

func (p *arvProvider) Do(ctx context.Context, req *provider.Request, on func(provider.Event)) (*provider.Response, error) {
	if on != nil {
		on(provider.Event{Kind: provider.EvStart})
	}
	turn, u := p.fn(ctx, req)
	if err := ctx.Err(); err != nil {
		return nil, &provider.Error{Kind: provider.ErrNetwork, Message: "request cancelled", Err: err}
	}
	stop := core.StopEnd
	if len(turn.ToolCalls()) > 0 {
		stop = core.StopToolUse
	}
	turn.Role = core.RoleAssistant
	return &provider.Response{ID: fmt.Sprintf("arv-%d", p.n.Add(1)), Model: "arv-1", Turn: turn, Usage: u, Stop: stop}, nil
}

// arvIsCompactor recognises the compactor fork by its instruction block (the
// request label format is an implementation detail that has already changed once).
func arvIsCompactor(req *provider.Request) bool {
	msgs := req.Prompt.Messages
	if len(msgs) == 0 {
		return false
	}
	for _, b := range msgs[len(msgs)-1].Blocks {
		if strings.Contains(b.Text, "<compactor-task>") {
			return true
		}
	}
	return false
}

func arvText(s string) core.Turn { return core.Turn{Blocks: []core.Block{core.Text(s)}} }

func arvTool(id, name string, args any) core.Turn {
	b, _ := json.Marshal(args)
	return core.Turn{Blocks: []core.Block{core.ToolUse(id, name, b)}}
}

type arvRigOpts struct {
	planner  kv.Planner
	maxSteps int
}

func newARV(t *testing.T, p provider.Provider, o arvRigOpts) (*agent.Agent, *events.MemLog) {
	t.Helper()
	agent.RetryBase = time.Millisecond
	reg := tools.NewRegistry()
	reg.Register(arvFake{name: "big", run: func() *tools.Result {
		return &tools.Result{Text: strings.Repeat("build output line with details and a few more words\n", 120)}
	}})
	specs, err := reg.Specs()
	if err != nil {
		t.Fatal(err)
	}
	model := cost.Model{ID: "arv-1", ContextTokens: 1_000_000, Cache: cost.OpenAICacheModel(),
		Price: cost.Price{InputPerM: 4, OutputPerM: 20, CacheReadPerM: 1, CacheWrite5mPerM: 4, CacheWrite1hPerM: 4}}
	log := events.NewMemLog()
	a, err := agent.New(agent.Config{
		ID: "be-1", Role: "backend", Model: model, Provider: p, Tools: reg, ToolSpecs: specs,
		Const:  kv.NewLayer("const", kv.KindConst, 1, []kv.Segment{{Text: strings.Repeat("You are a careful coding agent. ", 40)}}),
		Shared: kv.NewLayer("shared", kv.KindShared, 1, []kv.Segment{{Key: "project", Text: strings.Repeat("The repo is a Go service. ", 60), Vol: kv.VolEpoch}}),
		Params: core.Params{MaxTokens: 512}, Events: log, Planner: o.planner, SessionID: "review", MaxSteps: o.maxSteps,
	})
	if err != nil {
		t.Fatal(err)
	}
	return a, log
}

type arvFake struct {
	name string
	run  func() *tools.Result
}

func (f arvFake) Spec() core.ToolSpec {
	return core.ToolSpec{Name: f.name, Description: "fake " + f.name, InputSchema: json.RawMessage(`{"type":"object"}`)}
}
func (f arvFake) Run(context.Context, *tools.Call) (*tools.Result, error) { return f.run(), nil }

func arvBlock(ctx context.Context, ch <-chan struct{}) {
	select {
	case <-ch:
	case <-ctx.Done():
	}
}

// Run drains the inbox only before a request and behind tool results. When the
// model's answer has no tool calls it returns immediately, so anything Send()
// delivered while that request was in flight is left in the inbox with nothing to
// wake the agent (swarm.deliver saw the agent as running and did not restart it).
func TestConc_RunDoesNotReturnWithMailInTheInbox(t *testing.T) {
	gate := make(chan struct{})
	inflight := make(chan struct{}, 1)
	a, _ := newARV(t, &arvProvider{fn: func(ctx context.Context, req *provider.Request) (core.Turn, core.Usage) {
		select {
		case inflight <- struct{}{}:
		default:
		}
		arvBlock(ctx, gate)
		return arvText("all done"), core.Usage{}
	}}, arvRigOpts{})
	done := make(chan error, 1)
	go func() { _, err := a.Run(context.Background(), "do the task"); done <- err }()
	<-inflight
	a.Send("[mail m1 contract from mgr] POST /users now returns 201")
	close(gate)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if n := a.PendingInbox(); n != 0 {
		t.Fatalf("Run returned its final answer with %d undelivered message(s) in the inbox; nothing re-checks it", n)
	}
}

// The compactor job is deliberately detached from the caller's context
// (context.WithoutCancel + 3 minute timeout) and is not tracked by anything the
// swarm can wait on: after the run is cancelled (Shutdown) it keeps calling the
// provider, spending money and writing events into a log that is being closed.
func TestConc_CompactorJobEndsWithACancelledRun(t *testing.T) {
	compGate := make(chan struct{})
	compStarted := make(chan struct{}, 1)
	var compCtxErrAfterCancel atomic.Value
	compCtxErrAfterCancel.Store("unset")
	mainGate := make(chan struct{})
	var steps atomic.Int32
	warm := core.Usage{InputTokens: 50, CacheReadTokens: 2000} // reads as a warm prefix so the planner forks
	prov := &arvProvider{}
	prov.fn = func(ctx context.Context, req *provider.Request) (core.Turn, core.Usage) {
		if arvIsCompactor(req) {
			compStarted <- struct{}{}
			arvBlock(context.Background(), compGate) // the job's own context is what we are probing
			// The run's context cancels the job from an AfterFunc, which runs on a goroutine of its own: reading the context at once
			// found it live now and then on a slow runner (the Windows job). The wait is for the cancellation, and a job that outlives
			// the run still ends it, as a bound.
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
			}
			compCtxErrAfterCancel.Store(fmt.Sprint(ctx.Err()))
			return arvText("not a patch"), warm
		}
		n := int(steps.Add(1))
		if n <= 6 {
			return arvTool(fmt.Sprintf("c%d", n), "big", map[string]any{"n": n}), warm
		}
		arvBlock(ctx, mainGate) // the agent is mid-request when the swarm shuts down
		return arvText("done"), warm
	}
	pl := kv.DefaultPlanner()
	pl.SoftThreadTokens = 1500
	pl.MinThreadTokens = 500
	a, log := newARV(t, prov, arvRigOpts{planner: pl})
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { _, err := a.Run(ctx, "build everything"); runDone <- err }()
	select {
	case <-compStarted:
	case <-time.After(10 * time.Second):
		cancel()
		for _, e := range log.OfType(events.TypeCompactPlan) {
			t.Logf("plan: %s", e.Data)
		}
		t.Fatalf("setup: no compactor call was started (steps taken: %d)", steps.Load())
	}
	cancel() // swarm shutdown / user interrupt
	select {
	case <-runDone:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not stop")
	}
	close(compGate)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && compCtxErrAfterCancel.Load().(string) == "unset" {
		time.Sleep(5 * time.Millisecond)
	}
	if got := compCtxErrAfterCancel.Load().(string); got == "<nil>" {
		t.Fatal("the compactor request's context was still live after the agent's run was cancelled: the job outlives Shutdown by up to 3 minutes and nothing can wait for it")
	}
}

// C-08 (fixed): there used to be no time-to-first-byte deadline on a model request.
// The stream-idle watchdog was armed only after response headers arrived, so a
// server that accepts the request and goes silent held the agent (its governor
// slot, and the warm gate if it is the primer) until the caller's context ended.
// The adapters now arm the watchdog when the request is sent: a silent server ends
// the attempt with provider.ErrTimeout after FirstByteTimeout (StreamIdleTimeout
// when only that is set), and a request that gets no response twice is not retried
// again (provider.MaxSilentAttempts), so the run below ends after two attempts of
// 200ms each, not six.
func TestConc_HungRequestIsBoundedByTheFirstByteDeadline(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // accept, never answer
	}))
	defer srv.Close()
	defer close(release)
	client := openaichat.New(openaichat.Config{Name: "hang", BaseURL: srv.URL, StreamIdleTimeout: 200 * time.Millisecond})
	a, _ := newARV(t, client, arvRigOpts{})
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	start := time.Now()
	_, err := a.Run(ctx, "hello")
	if d := time.Since(start); d > 4*time.Second { // two attempts of 200ms; only the caller's 8 s deadline would take longer
		t.Fatalf("a silent server held the request for %v (error: %v); StreamIdleTimeout=200ms never applied because it is armed after the headers; only the caller's deadline ended it", d.Round(time.Millisecond), err)
	}
}
