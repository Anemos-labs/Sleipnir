// Package agent runs one model-driven worker: render its layered prompt, call
// the provider, execute the tools it asks for, and keep its context healthy.
//
// The loop is deliberately boring. Everything interesting about cost lives in
// the layers it renders (package kv) and in the decisions it takes at turn
// boundaries: commit a ready compaction patch, start a new one, pick up a
// shared-layer epoch. Between boundaries a thread is strictly append-only,
// which is what keeps the provider's prefix cache hot.
package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/cost"
	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/kv"
	"github.com/reee344/sleipnir/internal/perm"
	"github.com/reee344/sleipnir/internal/provider"
	"github.com/reee344/sleipnir/internal/tools"
)

// Priority orders requests when the governor must choose: lower runs first.
const (
	PrioInteractive = 0 // a human is waiting, or the manager is
	PrioWorker      = 1
	PrioBackground  = 2 // compactors, curators, keep-alives
)

// Release reports a request's outcome back to the limiter.
type Release func(u *core.Usage, err error)

// Limiter gates outbound requests (rate limits, concurrency, priorities).
type Limiter interface {
	Acquire(ctx context.Context, prio int) (Release, error)
}

// NopLimiter admits everything.
type NopLimiter struct{}

// Acquire implements Limiter.
func (NopLimiter) Acquire(context.Context, int) (Release, error) {
	return func(*core.Usage, error) {}, nil
}

// HotSource renders the always-fresh tail for an agent: its view of the task
// board and mailbox. It is called once per request and must be cheap.
type HotSource func(agent string) []core.Block

// Sink receives live progress for UIs. All methods must be safe for concurrent
// use and must not block.
type Sink interface {
	Text(agent, delta string)
	Thinking(agent, delta string)
	ToolStart(agent string, call core.Block)
	ToolEnd(agent string, call core.Block, res *tools.Result, took time.Duration)
	Response(agent string, r *provider.Response, hitRatio float64)
	Notice(agent, level, msg string)
}

// NopSink ignores everything.
type NopSink struct{}

func (NopSink) Text(string, string)                                      {}
func (NopSink) Thinking(string, string)                                  {}
func (NopSink) ToolStart(string, core.Block)                             {}
func (NopSink) ToolEnd(string, core.Block, *tools.Result, time.Duration) {}
func (NopSink) Response(string, *provider.Response, float64)             {}
func (NopSink) Notice(string, string, string)                            {}

// Config assembles an agent. Fields marked shared point at session-wide
// services; the rest are per agent.
type Config struct {
	ID    string
	Role  string
	Model cost.Model

	Provider provider.Provider
	Tools    *tools.Registry
	// ToolSpecs is the frozen, sorted tool list every agent in the session sends.
	ToolSpecs []core.ToolSpec

	// Layers. Const/Shared/Role are shared values; Notes and Spine start empty.
	Const  *kv.Layer
	Shared *kv.Layer
	RoleL  *kv.Layer
	Notes  *kv.Layer

	Params core.Params
	Hot    HotSource

	// Session services (shared).
	Events  events.Emitter
	Blobs   events.Blobs
	Archive *kv.Archive
	Files   *tools.FileState
	Guard   tools.Guard
	Snap    tools.Snapshotter
	Handles *tools.Handles
	Perm    perm.Requester
	Limiter Limiter
	Sink    Sink

	Workdir string
	Root    string
	Limits  tools.Limits

	// Cache engine tuning.
	Planner     kv.Planner
	ApplyPolicy kv.ApplyPolicy
	KVPolicy    kv.Policy
	// SessionID and AffinityShards shape the provider routing key.
	SessionID      string
	AffinityShards int
	// Compaction toggles background compaction (on by default via NewAgent).
	NoCompaction bool
	// OnPromote receives facts the compactor proposes for shared layers.
	OnPromote func(agent string, p []kv.Promotion)

	Est core.Estimator
	Now func() time.Time

	MaxSteps  int     // per Run; default 200
	BudgetUSD float64 // 0: unlimited
	Priority  int
}

// Result summarises one Run.
type Result struct {
	Text        string
	Steps       int
	Usage       core.Usage
	CostUSD     float64
	Stop        core.StopReason
	Compactions int
}

// ErrBudget is returned when an agent exhausts its dollar budget.
var ErrBudget = errors.New("agent budget exhausted")

// Agent is one worker. Run is not reentrant; Send may be called from anywhere.
type Agent struct {
	cfg Config
	est core.Estimator

	mu      sync.Mutex
	stack   kv.Stack
	thread  *kv.Thread
	guard   kv.Guard
	epoch   uint64 // rebase counter fed to the guard
	strip   bool   // strip thinking on the next render (declared rebase)
	inbox   []string
	reqN    int
	usage   core.Usage
	costUSD float64

	lastStart time.Time
	lastHit   float64
	haveHit   bool
	mainReqs  int // completed main-thread requests

	comp compactionState
}

// New builds an agent with an empty thread.
func New(cfg Config) (*Agent, error) {
	if cfg.Provider == nil || cfg.Tools == nil {
		return nil, errors.New("agent: provider and tools are required")
	}
	if cfg.Est == nil {
		cfg.Est = core.NewBytesEstimator()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Events == nil {
		cfg.Events = events.Discard{}
	}
	if cfg.Limiter == nil {
		cfg.Limiter = NopLimiter{}
	}
	if cfg.Sink == nil {
		cfg.Sink = NopSink{}
	}
	if cfg.Perm == nil {
		cfg.Perm = perm.AllowAll{}
	}
	if cfg.Guard == nil {
		cfg.Guard = tools.NoGuard{}
	}
	if cfg.Files == nil {
		cfg.Files = tools.NewFileState()
	}
	if cfg.Handles == nil {
		cfg.Handles = tools.NewHandles()
	}
	if cfg.Archive == nil {
		if cfg.Blobs == nil {
			cfg.Blobs = events.NewMemBlobs()
		}
		cfg.Archive = kv.NewArchive(cfg.Blobs)
	}
	if cfg.Blobs == nil {
		cfg.Blobs = events.NewMemBlobs()
	}
	if cfg.MaxSteps == 0 {
		cfg.MaxSteps = 200
	}
	if cfg.Limits == (tools.Limits{}) {
		cfg.Limits = tools.DefaultLimits()
	}
	if cfg.ApplyPolicy == (kv.ApplyPolicy{}) {
		cfg.ApplyPolicy = kv.DefaultApplyPolicy()
	}
	if cfg.KVPolicy == (kv.Policy{}) {
		cfg.KVPolicy = kv.DefaultPolicy()
	}
	if cfg.AffinityShards == 0 {
		cfg.AffinityShards = 1
	}
	if cfg.Params.MaxTokens == 0 {
		cfg.Params.MaxTokens = 8192
	}
	th := kv.NewThread()
	th.SetClock(cfg.Now)
	a := &Agent{cfg: cfg, est: cfg.Est, thread: th}
	a.stack = kv.Stack{
		Agent: cfg.ID, Role: cfg.Role, Model: cfg.Model.ID,
		Tools: cfg.ToolSpecs,
		Const: cfg.Const, Shared: cfg.Shared, RoleL: cfg.RoleL, Notes: cfg.Notes,
	}
	return a, nil
}

// ID returns the agent id.
func (a *Agent) ID() string { return a.cfg.ID }

// Send queues text for the next turn boundary: steering from a human, a
// delivered mailbox message, a harness notice. It never blocks the loop.
func (a *Agent) Send(text string) {
	a.mu.Lock()
	a.inbox = append(a.inbox, text)
	a.mu.Unlock()
}

// Usage returns cumulative usage and cost.
func (a *Agent) Usage() (core.Usage, float64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.usage, a.costUSD
}

// Thread exposes the live thread (read-only use: snapshots).
func (a *Agent) Thread() *kv.Thread { return a.thread }

// Stack returns a consistent snapshot of the prompt state.
func (a *Agent) Stack() kv.Stack {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.stack
	s.Thread = a.thread.Snapshot()
	return s
}

// SyncShared installs new shared or role layers at a turn boundary. It is a
// declared rebase: everything after the shared layers is re-prefilled once.
func (a *Agent) SyncShared(shared, role *kv.Layer, reason string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	changed := false
	if shared != nil && shared.Hash() != a.stack.Shared.Hash() {
		a.stack.Shared, changed = shared, true
	}
	if role != nil && role.Hash() != a.stack.RoleL.Hash() {
		a.stack.RoleL, changed = role, true
	}
	if changed {
		a.epoch++
		a.strip = true
		a.emit(events.TypeLayerCommit, map[string]any{
			"scope": "shared-sync", "reason": reason,
			"shared": a.stack.Shared.Hash().Short(), "role": a.stack.RoleL.Hash().Short(),
		})
	}
}

// push appends a turn to the thread and records it in the archive and log.
func (a *Agent) push(t core.Turn) core.Turn {
	t = a.thread.Append(t)
	_ = a.cfg.Archive.Put(a.cfg.ID, t)
	a.emit(events.TypeTurnAppend, t)
	return t
}

func (a *Agent) emit(typ string, data any) {
	if _, err := a.cfg.Events.Emit(a.cfg.ID, typ, data); err != nil {
		a.cfg.Sink.Notice(a.cfg.ID, "warn", fmt.Sprintf("event log write failed: %v", err))
	}
}

// Run feeds input to the agent and loops until it produces a final answer with
// no tool calls, is cancelled, or hits a limit.
func (a *Agent) Run(ctx context.Context, input string) (*Result, error) {
	res := &Result{}
	if input != "" {
		a.push(core.Turn{Role: core.RoleUser, Origin: core.OriginUser, Blocks: []core.Block{core.Text(input)}})
		a.emit(events.TypeUserInput, map[string]any{"text": input})
	}
	for step := 0; step < a.cfg.MaxSteps; step++ {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if a.cfg.BudgetUSD > 0 {
			if _, c := a.Usage(); c >= a.cfg.BudgetUSD {
				return res, ErrBudget
			}
		}
		a.boundary(ctx)
		a.drainInbox()

		resp, err := a.request(ctx)
		if err != nil {
			return res, err
		}
		res.Steps++
		res.Usage = res.Usage.Add(resp.Usage)
		res.Stop = resp.Stop
		turn := a.push(withUsage(resp))
		calls := turn.ToolCalls()
		if len(calls) == 0 {
			res.Text = turn.PlainText()
			u, c := a.Usage()
			_, res.CostUSD = u, c
			res.Compactions = a.comp.count
			return res, nil
		}
		results := a.runTools(ctx, calls)
		blocks := results
		if extra := a.takeInbox(); len(extra) > 0 {
			blocks = append(blocks, extra...)
		}
		a.push(core.Turn{Role: core.RoleUser, Origin: core.OriginTool, Blocks: blocks})
	}
	return res, fmt.Errorf("agent %s: step limit %d reached", a.cfg.ID, a.cfg.MaxSteps)
}

func withUsage(r *provider.Response) core.Turn {
	t := r.Turn
	u := r.Usage
	t.Usage = &u
	if t.Model == "" {
		t.Model = r.Model
	}
	return t
}

// drainInbox turns queued steering into a user turn when the thread currently
// ends with an assistant turn (so roles alternate).
func (a *Agent) drainInbox() {
	snap := a.thread.Snapshot()
	if len(snap.Turns) > 0 && snap.Turns[len(snap.Turns)-1].Role == core.RoleUser {
		return // pending input will ride along with the next tool-results turn
	}
	if blocks := a.takeInbox(); len(blocks) > 0 {
		a.push(core.Turn{Role: core.RoleUser, Origin: core.OriginMail, Blocks: blocks})
	}
}

func (a *Agent) takeInbox() []core.Block {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.inbox) == 0 {
		return nil
	}
	blocks := make([]core.Block, 0, len(a.inbox))
	for _, s := range a.inbox {
		blocks = append(blocks, core.Text(s))
	}
	a.inbox = nil
	return blocks
}
