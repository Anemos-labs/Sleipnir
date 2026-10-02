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
	"math"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/cost"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/kv"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/provider"
	"github.com/anemos-labs/sleipnir/internal/tools"
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
// board and mailbox. It is called once per request in kv.HotInline mode and once
// per user turn otherwise, and must be cheap. Where the text lands (after the
// last cache marker, as a persisted notice, or as a turn-scoped system message)
// is decided per route, see kv.ResolveHot.
type HotSource func(agent string) []core.Block

// Gate holds back requests over a cold shared prefix until one of them has
// started, so a fan-out reads the prefix from cache instead of writing it once
// per agent. Enter returns a function to call when the response starts or the
// request fails; it must be called exactly once.
type Gate interface {
	Enter(ctx context.Context, key string) (func(ok bool), error)
}

// PriorityGate is a Gate that knows who is asking. A request that outranks the one
// priming a cold prefix must not wait for it (the governor would admit it first), so
// the agent passes its priority (Prio*) when the gate can use it. Gates that do not
// implement it are called through Enter.
type PriorityGate interface {
	Gate
	EnterPrio(ctx context.Context, key string, prio int) (func(ok bool), error)
}

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

// A Resetter is a Sink that can take back what it showed of a response that is being retried. When a request fails part-way
// through a stream and is sent again, the text of the failed attempt has already been given to the sink; a sink that can do
// something about it (end the line, drop the partial text, mark the start of the new attempt) implements this, and is called
// when the new attempt begins. One that does not gets the new attempt's text after the old one's, as it always has.
type Resetter interface {
	Reset(agent string)
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
	// Compactor, when set, writes the compaction patches in place of Provider (a cheaper model
	// for the summaries). It pays full price for the thread it reads, since it shares no cache with
	// the agent, so it is used only while the thread fits CompactorModel's window; past that the
	// agent's own model compacts.
	Compactor      provider.Provider
	CompactorModel cost.Model
	Tools          *tools.Registry
	// ToolSpecs is the frozen, sorted tool list every agent in the session sends.
	ToolSpecs []core.ToolSpec

	// Layers. Const/Shared/Role are shared values; Notes and Spine start empty.
	Const  *kv.Layer
	Shared *kv.Layer
	RoleL  *kv.Layer
	Notes  *kv.Layer

	Params core.Params
	Hot    HotSource
	// VerifyHint is the project's test command ("go test ./...", from the survey): with it, an answer given after code was changed and before any
	// test ran is sent back once to run it. Empty: no such check.
	VerifyHint string
	// PlanOpen says how many steps of the agent's plan are not done (nil: there is no plan tool). A run that would end with some is sent back once.
	PlanOpen func(agent string) int
	// HotMode requests a hot-tail mechanism. The zero value (kv.HotInline) lets
	// the harness choose for the route: persist-on-change on models that enforce
	// preserved thinking, turn-scoped where the provider supports it, inline
	// otherwise (kv.ResolveHot).
	HotMode kv.HotMode
	// HotMinRequests is the fewest requests between two persisted hot notices
	// (kv.HotPersist only; default 3). A busy board changes on almost every step;
	// each notice stays in the thread until compaction folds it.
	HotMinRequests int
	// HotKey maps the hot blocks to the string whose change makes a new persisted
	// notice necessary. The default hashes the text with the board version stamp
	// and context counter removed.
	HotKey func([]core.Block) string

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
	Gate    Gate
	Sink    Sink
	// Hooks runs user-configured commands around tool calls and before the agent
	// stops (nil: none).
	Hooks Hooks

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
	// CaptureTokens asks the endpoint for token ids and logprobs on every call
	// (RL rollouts against a self-hosted policy). Ignored by endpoints that
	// cannot provide them.
	CaptureTokens bool
	// Compaction toggles background compaction (on by default via NewAgent).
	NoCompaction bool
	// OnPromote receives the facts a compactor proposes for the shared layers, after
	// the harness has vetted them (kv.Apply: few, short, worded as facts, marked
	// Unverified). Each Text starts with UnverifiedPrefix: a proposal is what a model
	// wrote after reading whatever was in its thread, and nothing has checked it.
	OnPromote func(agent string, p []kv.Promotion)

	Est core.Estimator
	Now func() time.Time

	MaxSteps  int     // per Run; default 200
	BudgetUSD float64 // 0: unlimited
	Priority  int

	// OutagePatience is how long, in all, the agent keeps repeating a request that fails because the endpoint is down or
	// overloaded (an HTTP status of 500 or more, or 429): the waits between its attempts, the first six included, add up to
	// at most this. Every failure gets six attempts; this is what lets such a failure get more. The endpoint answered, so it is
	// there and the request is not wrong: waiting is what a person would do, and a worker that gives up after forty seconds
	// takes its task with it. Zero (the default) is the six attempts only. A failure with no status (nothing answered: a
	// misspelt URL, a refused connection) never gets it, so a misconfiguration fails as fast as ever.
	OutagePatience time.Duration

	// MaxToolCallsPerTurn is how many of the tool calls in one model turn are run
	// (default DefaultMaxToolCalls). The rest are answered with an error that tells
	// the model to issue fewer, so every call has its result and the thread stays valid
	// but one turn cannot fan out into hundreds of executions. Negative: no cap.
	MaxToolCallsPerTurn int
	// MaxTurnResultChars is how much result text one turn's tool calls may put into
	// the next request (default DefaultMaxTurnResultChars). Results are kept whole, in
	// call order, while they fit; the rest are cut to a short excerpt (a few hundred to
	// 1,500 characters, so nothing vanishes and the total stays within about 1.5 times
	// this), and the full text is stored behind a recall handle named in the result.
	// Negative: no cap.
	MaxTurnResultChars int

	// ToolTimeout bounds one tool call (default DefaultToolTimeout). Its context ends
	// when the time is up; a tool that honours it returns, and the model is told the call
	// ran too long. It is a backstop for a tool that would otherwise hold the agent for
	// ever (a third-party server, a hung mount), well above what the built-in tools
	// allow themselves (the shell tool at most 10 minutes, the swarm's wait the same).
	// Negative: no deadline.
	ToolTimeout time.Duration

	// NoMailReopen makes Run return, as it used to, when the model answers without tool
	// calls even though mail arrived while it was answering; the mail then stays in the
	// inbox (PendingInbox). By default Run reads it first, in the same run (at most
	// maxMailRounds times), so that a message sent to a working agent is never
	// stranded. Set it for a caller that owns a run's scope and starts the next run
	// itself when mail is waiting: a swarm worker's run settles only the assignments it
	// started with, so mail that changes one (a reject) has to be read by the run that
	// then owns it.
	NoMailReopen bool
}

// The per-turn tool budgets (S24) unless the Config says otherwise. The call cap is
// generous on purpose: a manager that creates a task and spawns a worker for each of 50
// agents asks for about a hundred calls in one turn, and refusing those would break the
// fan-out the swarm exists for. What it stops is a runaway turn of hundreds of executions.
// Bytes are what fill the context, so the result budget is the tighter one: 120,000
// characters is about 30,000 tokens, half of what one turn may add before the planner's
// hard limit.
const (
	DefaultMaxToolCalls       = 128
	DefaultMaxTurnResultChars = 120_000
	// DefaultToolTimeout is the longest one tool call may take (Config.ToolTimeout).
	DefaultToolTimeout = 30 * time.Minute
)

// ErrClosed is returned by Run after Close.
var ErrClosed = errors.New("agent closed")

// CloseGrace bounds how long Close waits for a compaction job that ignores its
// cancelled context (a provider that does not honour it). Tests shrink it.
var CloseGrace = 5 * time.Second

// Result summarises one Run.
type Result struct {
	Text        string
	Steps       int
	Usage       core.Usage
	CostUSD     float64
	Stop        core.StopReason
	Compactions int
}

// Budget is the agent's dollar budget (0: none), and SetBudget changes it for the runs that follow: the chat's /budget. A value that is
// not a number, or negative, is refused for the reason New refuses it.
func (a *Agent) Budget() float64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg.BudgetUSD
}

// SetBudget sets the budget; 0 removes it.
func (a *Agent) SetBudget(usd float64) error {
	if math.IsNaN(usd) || math.IsInf(usd, 0) || usd < 0 {
		return fmt.Errorf("the budget must be zero (none) or a positive amount, got %v", usd)
	}
	a.mu.Lock()
	a.cfg.BudgetUSD = usd
	a.mu.Unlock()
	return nil
}

// ErrBudget is returned when an agent exhausts its dollar budget.
var ErrBudget = errors.New("agent budget exhausted")

// ErrOutputLimit is returned when the model's responses were cut off by the output limit (max_tokens) more than
// maxCutoffRounds times in a row, each time with no tool call: nothing it wrote was an answer, and the run says so instead of
// handing on the last cut-off text as if it were one.
var ErrOutputLimit = errors.New("the response was cut off by the output limit")

// Agent is one worker. Run is not reentrant; Send may be called from anywhere.
type Agent struct {
	cfg Config
	est core.Estimator

	mu     sync.Mutex
	stack  kv.Stack
	thread *kv.Thread
	guard  kv.Guard
	epoch  uint64 // rebase counter fed to the guard (thread epoch is added to it)
	inbox  []inboxMsg
	reqN   int
	forkN  int
	// man is the manifest state the next main request's prompt is delta-encoded
	// against (see core.BuildManifest).
	man     core.ManifestState
	usage   core.Usage
	costUSD float64

	// Cache state (see warm.go).
	lastStart    time.Time
	mainReqs     int  // completed main-thread requests
	reportsCache bool // the provider has reported cache reads or writes at least once
	lastMiss     bool // the previous request read far less than the guard expected
	lastEstAll   int  // estimated and reported size of the previous main prompt,
	lastTotalIn  int  // for scaling the guard's estimates to provider tokens
	lastReqEpoch uint64
	lastReqLen   int // thread length at the previous main request
	rollRef      core.BlockRef
	rollEpoch    uint64
	rollValid    bool
	anomStreak   int
	rep          repeatGuard // the run's failed calls (see repeat.go); used by run only
	tests        testGuard   // a failing test run followed by edits to tests only (see testguard.go); used by run only

	// Hot tail persistence (kv.HotPersist).
	hotFP  string // fingerprint of the newest persisted notice ("" when none)
	hotAge int    // main requests since it was written
	// enforcing is set once the route has rejected a replayed thinking block: it
	// binds signatures whatever the model table says, so the hot view must stop
	// rewriting earlier bytes (see caps).
	enforcing atomic.Bool

	stepInRun   int
	lastMaskReq int
	lastSnap    core.Hash // the blob of the newest snapshot written

	comp compactionState

	// life is the agent's lifetime: compaction jobs run on it, and Close ends it.
	// jobs counts the jobs in flight so that Close (and a swarm's Shutdown) can wait
	// for them instead of leaving them to write into a closed log.
	life       context.Context
	lifeCancel context.CancelFunc
	jobs       sync.WaitGroup
	closed     bool
}

// inboxMsg is one queued message. Steering typed by a human is preserved through
// compaction as an instruction; mail from other agents is data and is not.
type inboxMsg struct {
	text  string
	steer bool
}

// New builds an agent with an empty thread.
func New(cfg Config) (*Agent, error) {
	if cfg.Provider == nil || cfg.Tools == nil {
		return nil, errors.New("agent: provider and tools are required")
	}
	if b := cfg.BudgetUSD; math.IsNaN(b) || math.IsInf(b, 0) || b < 0 {
		// A budget that cannot be compared (NaN) or that reads as "none" (negative) would
		// switch the breaker off without a word.
		return nil, fmt.Errorf("agent: the budget must be zero (none) or a positive amount, got %v", b)
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
		cfg.Perm = perm.DenyAll{} // fail closed: see perm.DenyAll
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
	cfg.ApplyPolicy = cfg.ApplyPolicy.WithDefaults()
	if cfg.KVPolicy == (kv.Policy{}) {
		cfg.KVPolicy = kv.DefaultPolicy()
	}
	if cfg.AffinityShards == 0 {
		cfg.AffinityShards = 1
	}
	if cfg.Params.MaxTokens == 0 {
		cfg.Params.MaxTokens = 8192
	}
	if cfg.HotMinRequests == 0 {
		cfg.HotMinRequests = 3
	}
	if cfg.HotKey == nil {
		cfg.HotKey = defaultHotKey
	}
	if cfg.MaxToolCallsPerTurn == 0 {
		cfg.MaxToolCallsPerTurn = DefaultMaxToolCalls
	}
	if cfg.MaxTurnResultChars == 0 {
		cfg.MaxTurnResultChars = DefaultMaxTurnResultChars
	}
	if cfg.ToolTimeout == 0 {
		cfg.ToolTimeout = DefaultToolTimeout
	}
	th := kv.NewThread()
	th.SetClock(cfg.Now)
	a := &Agent{cfg: cfg, est: cfg.Est, thread: th, lastMaskReq: -1 << 20}
	a.life, a.lifeCancel = context.WithCancel(context.Background())
	a.stack = kv.Stack{
		Agent: cfg.ID, Role: cfg.Role, Model: cfg.Model.ID,
		Tools: cfg.ToolSpecs,
		Const: cfg.Const, Shared: cfg.Shared, RoleL: cfg.RoleL, Notes: cfg.Notes,
	}
	return a, nil
}

// ID returns the agent id.
func (a *Agent) ID() string { return a.cfg.ID }

// mailPrefix starts every message the swarm's router delivers. Anything else
// sent to an agent comes from the human driving it.
const mailPrefix = "[mail"

// Send queues text for the next turn boundary: steering from a human, a
// delivered mailbox message, a harness notice. It never blocks the loop. Text
// that carries the router's "[mail" prefix is mail from another agent (data, not
// instructions); anything else is human steering and survives compaction
// verbatim in the instructions notes. Use Steer to say so explicitly.
func (a *Agent) Send(text string) { a.enqueue(text, !strings.HasPrefix(text, mailPrefix)) }

// Steer queues human steering: text typed by the person driving this agent. It
// rides with the next tool results (or as its own turn) and, unlike mail, is
// copied into the instructions notes when its turn is compacted.
func (a *Agent) Steer(text string) { a.enqueue(text, true) }

func (a *Agent) enqueue(text string, steer bool) {
	a.mu.Lock()
	a.inbox = append(a.inbox, inboxMsg{text: text, steer: steer})
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

// applyPolicy is the configured apply policy with the provider's capabilities.
func (a *Agent) applyPolicy() kv.ApplyPolicy {
	pol := a.cfg.ApplyPolicy
	pol.Caps = a.caps(a.cfg.Provider.Profile())
	return pol
}

// caps derives the renderer's view of the route: the provider's profile plus
// the hot-tail mechanism this model needs.
func (a *Agent) caps(prof provider.Profile) kv.Caps {
	c := prof.KVCaps()
	// A route that has once rejected a replayed thinking block (a gateway, an
	// unflagged model) binds signatures like a preserved-thinking model does, so
	// it is treated as one from then on rather than rejected on every request.
	c.HotMode = kv.ResolveHot(a.cfg.HotMode, c, a.cfg.Model.PreservedThinking || a.enforcing.Load())
	return c
}

// stripTurn removes thinking from a turn when the policy says a rebase voids it.
func (a *Agent) stripTurn(t core.Turn) core.Turn {
	if !a.cfg.ApplyPolicy.StripThinking {
		return t
	}
	out, _ := kv.StripThinkingTurn(t)
	return out
}

// SyncShared installs new shared or role layers at a turn boundary. It is a
// declared rebase: everything after the shared layers is re-prefilled once, and
// thinking blocks bound to the old prefix are voided, so they are stripped from
// the thread in the same critical section that swaps the layers. A request that
// snapshots the stack therefore sees either the old layers with the old thinking
// or the new layers without it, never a mix; a response that was already in
// flight is stripped when it is appended (see pushResponse).
func (a *Agent) SyncShared(shared, role *kv.Layer, reason string) {
	a.mu.Lock()
	changed := false
	if shared != nil && shared.Hash() != a.stack.Shared.Hash() {
		a.stack.Shared, changed = shared, true
	}
	if role != nil && role.Hash() != a.stack.RoleL.Hash() {
		a.stack.RoleL, changed = role, true
	}
	stripped := false
	if changed {
		a.epoch++
		if a.cfg.Provider.Profile().ReplayThinking && a.cfg.ApplyPolicy.StripThinking {
			stripped = a.thread.Rewrite(func(t core.Turn) (core.Turn, bool) { return kv.StripThinkingTurn(t) })
		}
	}
	sharedHash, roleHash := a.stack.Shared.Hash().Short(), a.stack.RoleL.Hash().Short()
	a.mu.Unlock()
	if changed {
		a.emit(events.TypeLayerCommit, map[string]any{
			"scope": "shared-sync", "reason": reason, "shared": sharedHash, "role": roleHash, "thinking_stripped": stripped,
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

// pushResponse appends the model's turn. epoch is the rebase counter the request
// was rendered at: if a declared rebase (a shared-layer epoch on another
// goroutine) landed while the request was in flight, the response's thinking is
// bound to a prefix that no longer exists and is stripped before it enters the
// thread. The check and the append share a.mu with SyncShared, so the turn is
// either stripped here or by that strip, never left stale.
func (a *Agent) pushResponse(resp *provider.Response, epoch uint64) core.Turn {
	t := withUsage(resp)
	a.mu.Lock()
	if a.epoch+a.thread.Snapshot().Epoch != epoch {
		t = a.stripTurn(t)
	}
	t = a.thread.Append(t)
	a.mu.Unlock()
	_ = a.cfg.Archive.Put(a.cfg.ID, t)
	a.emit(events.TypeTurnAppend, t)
	return t
}

func (a *Agent) emit(typ string, data any) {
	if _, err := a.cfg.Events.Emit(a.cfg.ID, typ, data); err != nil {
		a.cfg.Sink.Notice(a.cfg.ID, "warn", fmt.Sprintf("event log write failed: %v", err))
	}
}

// Run feeds input, which a person typed, to the agent and loops until it produces a
// final answer with no tool calls, is cancelled, or hits a limit. Compaction pins the
// input into the agent's notes as the user's own words.
func (a *Agent) Run(ctx context.Context, input string) (*Result, error) {
	var blocks []core.Block
	if input != "" {
		blocks = []core.Block{core.Text(input)}
	}
	return a.run(ctx, core.OriginUser, blocks)
}

// RunTask is Run for work the harness hands to the agent, not something a person
// typed: a swarm's kickoff or a reused worker's next assignment. brief is what the
// model reads first. assignment, when it is not empty, is the task itself; it is
// shown to the model after the brief, and when the turn is folded away the
// "assignment" section of the agent's notes takes it over, so the notes keep
// describing the task the agent has now. Neither text is ever pinned as the user's
// instructions (core.OriginTask, kv.Task). With both empty the run continues from the
// thread as it stands.
func (a *Agent) RunTask(ctx context.Context, brief, assignment string) (*Result, error) {
	var blocks []core.Block
	if brief != "" {
		blocks = append(blocks, core.Text(brief))
	}
	if assignment != "" {
		blocks = append(blocks, kv.Task(assignment))
	}
	return a.run(ctx, core.OriginTask, blocks)
}

// run is the loop behind Run and RunTask: it adds the input turn, if there is one, with
// the origin that says who it speaks for.
func (a *Agent) run(ctx context.Context, origin core.Origin, input []core.Block) (*Result, error) {
	res := &Result{}
	if a.life.Err() != nil {
		return res, ErrClosed
	}
	defer a.saveSnapshot() // however Run ends: a resumed session continues from here
	// Interrupting this run interrupts the compaction job that is in flight (it is
	// working for a run that will not continue), while a run that ends normally leaves
	// its job to finish: an interactive agent's Run returns after every answer.
	stop := context.AfterFunc(ctx, a.cancelCompaction)
	defer stop()
	vetoes := 0       // Stop hooks that sent the agent back to work in this run
	mailRounds := 0   // times a finished answer was reopened because mail arrived meanwhile
	cutoffs := 0      // responses in a row that the output limit cut off
	planNudges := 0   // times a finished answer was sent back because the plan still had open steps
	verifyNudges := 0 // times a finished answer was sent back because code was changed and no test ran since
	if len(input) > 0 {
		a.pushUser(origin, input)
		a.emit(events.TypeUserInput, inputEvent(origin, input))
	}
	a.rep.reset()
	a.tests.reset()
	phase := "between" // what a cancellation interrupted, for agent.cancel
	defer func() {
		if err := ctx.Err(); err != nil {
			cause := "canceled"
			if errors.Is(err, context.DeadlineExceeded) {
				cause = "deadline"
			}
			a.emit(events.TypeAgentCancel, map[string]any{"phase": phase, "cause": cause, "steps": res.Steps})
		}
	}()
	for step := 0; step < a.cfg.MaxSteps; step++ {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if a.life.Err() != nil {
			return res, ErrClosed
		}
		if b := a.Budget(); b > 0 {
			// Written so that a cost that is not a number stops the run instead of
			// slipping under the limit (a comparison with NaN is false either way).
			if _, c := a.Usage(); !(c < b) {
				return res, ErrBudget
			}
		}
		a.mu.Lock()
		a.stepInRun = step
		a.mu.Unlock()
		a.boundary(ctx)
		a.drainInbox()

		phase = "model"
		resp, epoch, err := a.request(ctx)
		if err != nil {
			return res, err
		}
		phase = "between"
		res.Steps++
		res.Usage = res.Usage.Add(resp.Usage)
		res.Stop = resp.Stop
		turn := a.pushResponse(resp, epoch)
		calls := turn.ToolCalls()
		if len(calls) == 0 && resp.Stop == core.StopMaxTokens {
			// A response that ended at the output limit, with no call to run, is not an answer: the model was cut off, and what
			// it wrote is the start of something. Taking it for the end of the run recorded a degenerate 16,000-token
			// generation as the answer, and "done". It is asked to carry on, a bounded number of times in a row; then the run
			// says what happened. (A call cut off in the middle has its own answer: the arguments are marked invalid.)
			res.Text = kv.AnswerText(turn)
			cutoffs++
			if cutoffs > maxCutoffRounds {
				return res, fmt.Errorf("%w: %d tokens, %d responses in a row", ErrOutputLimit, a.cfg.Params.MaxTokens, cutoffs)
			}
			a.cfg.Sink.Notice(a.cfg.ID, "warn", fmt.Sprintf("the response reached the output limit (%d tokens): asking the model to carry on (%d of %d)",
				a.cfg.Params.MaxTokens, cutoffs, maxCutoffRounds))
			a.pushUser(core.OriginSystem, []core.Block{core.Text(cutoffNudge)})
			continue
		}
		cutoffs = 0
		if len(calls) == 0 {
			res.Text = kv.AnswerText(turn) // what the model said, not its reasoning
			// Mail or steering that arrived while this answer was being produced would
			// be stranded: the run is ending and nothing else reads the inbox. Read it
			// now. The thread ends with an assistant turn, so the next step turns it
			// into the user turn it should have been. Bounded, so a peer that keeps
			// writing cannot keep a finished agent working for ever; whatever is left
			// stays in the inbox for the caller to see (PendingInbox). A caller that
			// starts the next run itself can ask for that (NoMailReopen).
			if !a.cfg.NoMailReopen && a.PendingInbox() > 0 && mailRounds < maxMailRounds {
				mailRounds++
				continue
			}
			// A Stop hook may send the agent back to work (run the tests, fix the
			// lint) a bounded number of times.
			if h := a.cfg.Hooks; h != nil && vetoes < maxStopVetoes {
				if o := h.BeforeStop(ctx, a.cfg.ID, a.cfg.Role, res.Text, vetoes > 0); o.Veto {
					vetoes++
					// A nudge from the harness, not something the user typed: it must
					// not be pinned into the instructions notes as the user's word.
					a.pushUser(core.OriginSystem, []core.Block{core.Text("[stop hook] " + o.Reason)})
					continue
				}
			}
			// A plan with steps still open is not a finished task. The model is asked once to finish them or to change the plan (a plan it
			// no longer means is its to change); asked again, it would only learn to ignore the note.
			if open := a.planOpen(); open > 0 && planNudges < maxPlanNudges {
				planNudges++
				note := fmt.Sprintf("[harness] Your plan still has %d open step(s) (see it above). Do them, or send the plan again with the steps that are done marked done and the ones you no longer mean removed, then give your answer.", open)
				a.emit(events.TypeAgentStuck, map[string]any{"phase": "plan", "note": note})
				a.pushUser(core.OriginSystem, []core.Block{core.Text(note)})
				continue
			}
			// Code changed, no test run since: the answer would claim a result nobody looked at. Once, with the project's own command.
			if a.cfg.VerifyHint != "" && a.tests.unverified() && verifyNudges < maxVerifyNudges {
				verifyNudges++
				note := fmt.Sprintf("[harness] You changed code and have not run the tests since. Run `%s` and fix what fails, or say plainly why you cannot, before you give your answer.", a.cfg.VerifyHint)
				a.emit(events.TypeAgentStuck, map[string]any{"phase": "verify", "note": note})
				a.pushUser(core.OriginSystem, []core.Block{core.Text(note)})
				continue
			}
			u, c := a.Usage()
			_, res.CostUSD = u, c
			res.Compactions = a.comp.count
			return res, nil
		}
		phase = "tools"
		results, exitFailed := a.runTools(ctx, calls)
		phase = "between"
		blocks := results
		if extra := a.takeInbox(); len(extra) > 0 {
			blocks = append(blocks, extra...)
		}
		note, stuck := a.rep.observeExits(a.cfg.ID, calls, results, exitFailed)
		if tn := a.tests.observe(calls, exitFailed); tn != "" && note == "" {
			note = tn
		}
		if note != "" && stuck == nil {
			blocks = append(blocks, core.Text(note))
			a.emit(events.TypeAgentStuck, map[string]any{"phase": "nudge", "note": note})
		}
		a.pushUser(core.OriginTool, blocks)
		if stuck != nil {
			// The results are in the thread, so it stays valid; the run ends here.
			a.emit(events.TypeAgentStuck, map[string]any{"phase": "stop", "error": stuck.Error()})
			return res, stuck
		}
	}
	return res, fmt.Errorf("agent %s: step limit %d reached", a.cfg.ID, a.cfg.MaxSteps)
}

// inputEvent is the payload of the user.input event for an input turn: what the model
// reads (text), and, for a task the harness handed over, who it speaks for (origin) and
// the assignment it carries. A person's input keeps the plain {"text": …} shape.
func inputEvent(origin core.Origin, blocks []core.Block) map[string]any {
	var text, card []string
	for _, b := range blocks {
		switch {
		case kv.IsTask(b):
			card = append(card, b.Text)
		case b.Kind == core.BlockText:
			text = append(text, b.Text)
		}
	}
	m := map[string]any{"text": strings.Join(text, "\n")}
	if origin != core.OriginUser {
		m["origin"] = string(origin)
	}
	if len(card) > 0 {
		m["assignment"] = strings.Join(card, "\n")
	}
	return m
}

// maxVerifyNudges is how many times one Run sends a finished answer back to run the tests.
const maxVerifyNudges = 1

// maxPlanNudges is how many times one Run sends a finished answer back for the open steps of its plan.
const maxPlanNudges = 1

// planOpen is how many steps of the agent's plan are open (0 with no plan, or when the session has no plan tool).
func (a *Agent) planOpen() int {
	if a.cfg.PlanOpen == nil {
		return 0
	}
	return a.cfg.PlanOpen(a.cfg.ID)
}

// maxMailRounds is how many times one Run reopens a finished answer to read mail that
// arrived while it was being written.
const maxMailRounds = 4

// maxCutoffRounds is how many times in a row one Run asks the model to carry on after a response that the output limit cut off.
// A model that is cut off again and again is not going to finish by being asked, and every round is a full-length generation.
const maxCutoffRounds = 2

// cutoffNudge is what the model reads after such a response. It cannot see its own token count, so the harness says what
// happened; "do it with a tool call" is the way out of the commonest cause, a long description of what it is about to do.
const cutoffNudge = "[harness] Your last response was cut off at the output limit before it was complete. Continue from where it stopped. " +
	"If you were about to run a command or change a file, do it now with a tool call instead of writing it out."

// Close ends the agent's background work: it cancels the compaction job in flight,
// waits for it (up to CloseGrace, so a provider that ignores its context cannot wedge
// a shutdown) and releases the agent's index in the archive. It is safe to call more
// than once and from any goroutine; Run returns ErrClosed afterwards. A swarm calls it
// when an agent is retired.
func (a *Agent) Close() error {
	a.mu.Lock()
	first := !a.closed
	a.closed = true
	a.mu.Unlock()
	a.lifeCancel()
	done := make(chan struct{})
	go func() {
		a.jobs.Wait()
		close(done)
	}()
	var err error
	t := time.NewTimer(CloseGrace)
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
		err = fmt.Errorf("agent %s: a compaction job did not stop within %s", a.cfg.ID, CloseGrace)
	}
	if first {
		a.cfg.Archive.Release(a.cfg.ID)
	}
	return err
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

// drainInbox turns queued messages into a user turn when the thread currently
// ends with an assistant turn (so roles alternate).
func (a *Agent) drainInbox() {
	snap := a.thread.Snapshot()
	last := len(snap.Turns) - 1
	for last >= 0 && snap.Turns[last].Role == core.RoleSystem {
		last-- // a turn-scoped board view behind the user turn is part of that turn
	}
	if last >= 0 && snap.Turns[last].Role == core.RoleUser {
		return // pending input will ride along with the next tool-results turn
	}
	if blocks := a.takeInbox(); len(blocks) > 0 {
		origin := core.OriginMail
		steerOnly := true
		for _, b := range blocks {
			if !kv.IsSteer(b) {
				steerOnly = false
			}
		}
		if steerOnly {
			origin = core.OriginUser
		}
		a.pushUser(origin, blocks)
	}
}

// takeInbox returns the queued messages as blocks: at most one steering block
// and one mail block per drain. Every block is a position for the provider's
// cache lookback (20 positions), so a burst of mail must not become a burst of
// blocks; steering keeps its own block because it is preserved differently.
func (a *Agent) takeInbox() []core.Block {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.inbox) == 0 {
		return nil
	}
	var steer, mail []string
	for _, m := range a.inbox {
		if m.steer {
			steer = append(steer, m.text)
		} else {
			mail = append(mail, m.text)
		}
	}
	a.inbox = nil
	var blocks []core.Block
	if len(steer) > 0 {
		blocks = append(blocks, kv.Steer(strings.Join(steer, "\n\n")))
	}
	if len(mail) > 0 {
		blocks = append(blocks, core.Text(strings.Join(mail, "\n")))
	}
	return blocks
}

// PendingInbox reports how many queued messages await the next turn boundary.
func (a *Agent) PendingInbox() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.inbox)
}

// ---- hot tail delivery -----------------------------------------------------------

// hotVolatile matches the stamps that change on every board write without any
// visible content changing: the board version and the context counter.
var hotVolatile = regexp.MustCompile(`board="v\d+"| · ctx \d+k`)

func defaultHotKey(blocks []core.Block) string {
	var sb strings.Builder
	for _, b := range blocks {
		sb.WriteString(hotVolatile.ReplaceAllString(b.Text, ""))
		sb.WriteByte(0)
	}
	return string(core.HashString(sb.String()))
}

// pushUser appends a user turn and delivers the hot tail with it where the route
// persists it: as a frozen notice block in the same turn (HotPersist, only when
// it changed and not more often than HotMinRequests), or as a turn-scoped system
// message right behind it (HotTurnScoped). Nothing here rewrites earlier bytes.
func (a *Agent) pushUser(origin core.Origin, blocks []core.Block) core.Turn {
	mode := a.caps(a.cfg.Provider.Profile()).HotMode
	var hot []core.Block
	if a.cfg.Hot != nil && mode != kv.HotInline {
		for _, h := range a.hotBlocks() {
			if strings.TrimSpace(h.Text) != "" {
				hot = append(hot, core.Text(h.Text))
			}
		}
	}
	if mode == kv.HotPersist && len(hot) > 0 {
		fp := a.cfg.HotKey(hot)
		a.mu.Lock()
		due := a.hotFP == "" || (fp != a.hotFP && a.hotAge >= a.cfg.HotMinRequests)
		if due {
			a.hotFP, a.hotAge = fp, 0
		}
		a.mu.Unlock()
		if due {
			for _, h := range hot {
				blocks = append(blocks, kv.Notice(h.Text))
			}
		}
	}
	t := a.push(core.Turn{Role: core.RoleUser, Origin: origin, Blocks: blocks})
	if mode == kv.HotTurnScoped && len(hot) > 0 {
		a.push(core.Turn{Role: core.RoleSystem, Origin: core.OriginSystem, Blocks: hot})
	}
	return t
}

// hotBlocks asks the HotSource for the board view and guards it: whatever the source
// did to sanitise the text of other agents, the view the model receives is one
// <live> frame with one opening and one closing tag, so nothing that got into its
// interior can close it or open another structural frame. A well-formed view comes
// back byte for byte as it was produced.
func (a *Agent) hotBlocks() []core.Block {
	if a.cfg.Hot == nil {
		return nil
	}
	in := a.cfg.Hot(a.cfg.ID)
	out := make([]core.Block, len(in))
	for i, b := range in {
		if b.Kind == core.BlockText {
			b.Text = kv.GuardFrame(b.Text, "live")
		}
		out[i] = b
	}
	return out
}

// resyncHotLocked re-derives which hot notice the thread ends with after a
// rebase rewrote it (compaction keeps only the newest notice, and folds it away
// when the retained region has none). Callers hold a.mu.
func (a *Agent) resyncHotLocked() {
	turns := a.thread.Snapshot().Turns
	for i := len(turns) - 1; i >= 0; i-- {
		var notices []core.Block
		for _, b := range turns[i].Blocks {
			if kv.IsNotice(b) {
				notices = append(notices, b)
			}
		}
		if len(notices) > 0 {
			a.hotFP = a.cfg.HotKey(notices)
			return
		}
	}
	a.hotFP = ""
}
