package swarm

import (
	"context"
	"errors"
	"fmt"
	"path"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/cost"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/kv"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/plan"
	"github.com/anemos-labs/sleipnir/internal/provider"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

// Config sizes and tunes a swarm.
type Config struct {
	SessionID string
	// MaxAgents bounds registered agents (running or idle).
	MaxAgents int
	// MaxWriters bounds agents whose roles may modify files that are running at
	// once, including a worker reused for new work (spawn agent=...). Evidence from
	// multi-agent coding studies is that peer writers on one codebase collide
	// far more than they help; readers (review, research, test runs) scale.
	MaxWriters int
	// RPM/MaxConcurrent feed the shared request governor.
	RPM           int
	MaxConcurrent int
	Hot           HotConfig
	Router        RouterConfig
	LeaseTTL      time.Duration
	// AffinityShards spreads agents over provider engines (see agent.Config).
	AffinityShards int
	// BudgetUSD caps total spend across the swarm, retired agents included (0 =
	// unlimited). When it is spent every running worker is stopped and no request
	// is admitted.
	BudgetUSD float64
	// AgentBudgetUSD caps each worker.
	AgentBudgetUSD float64
	// VerifyCmd, when set, is run in an agent's workdir before its task may leave
	// "doing", and again when the manager accepts it: the harness, not the model,
	// decides whether work is finished.
	VerifyCmd string
	// Verify runs a command; injected so the swarm does not depend on the shell
	// package. It returns combined output and exit code.
	Verify func(ctx context.Context, dir, cmd string) (string, int, error)
	// IdleRetire retires idle workers after this long (0 = never).
	IdleRetire time.Duration

	// Board bounds what agents can put on the board: tasks, pending notes, alerts.
	Board BoardLimits
	// MaxAttempts is how many times workers may stop without finishing a task
	// before the task is failed instead of requeued (default 3).
	MaxAttempts int
	// VerifyTimeout bounds scope discovery, queueing, and one verifier run
	// together (default 15 minutes); MaxVerifies
	// bounds how many run at once (default 2).
	VerifyTimeout time.Duration
	MaxVerifies   int
	// StuckAfter is how long a running worker may show no sign of life before the
	// watchdog raises an alert; at twice that it is cancelled and its task requeued
	// (default 10 minutes, negative disables). StuckGrace is how long a cancelled
	// worker gets to return before the harness stops waiting for it (default 30s).
	StuckAfter time.Duration
	StuckGrace time.Duration
	// ShutdownGrace bounds how long Shutdown waits for agents to stop (default 10s).
	ShutdownGrace time.Duration
	// SuperviseEvery is the housekeeping interval (default 1s).
	SuperviseEvery time.Duration
	// InboxSoftCap is how many messages may wait in one agent's inbox before further
	// mail is coalesced into a digest (default 12).
	InboxSoftCap int
	// MaxMailWakes bounds how many times peer mail may wake one idle worker for its
	// current task (default 40; negative: no bound). Each wake is a whole run, so two
	// workers answering each other would otherwise keep each other running for as
	// long as the swarm budget lasts. Mail past the bound is still delivered and waits
	// in the inbox; the manager is told once, and the count starts again when the
	// worker is given a new task. The manager's own mail and the harness's never count.
	MaxMailWakes int

	// HoldManager makes a final answer of the manager wait for the board: while
	// workers are running or tasks are unreviewed, the manager's Stop is vetoed with a
	// short reason (at most agent's per-run bound of times), so a batch run does not end
	// with its workers cut off. Interactive sessions leave it off (see hold.go).
	HoldManager bool
	// WakeManager starts a manager run, with a short harness-written note, when the
	// manager is idle and a worker finishes, fails or submits, or mail arrives for it:
	// what an interactive session needs, since workers outlive a turn there (see
	// wake.go). WakeQuiet is how long a burst of such events must be quiet before the
	// run starts, WakeMax the longest a burst may postpone it, MaxWakes the bound on
	// automatic runs between two human inputs (defaults 1.5s, 10s, 8).
	WakeManager bool
	WakeQuiet   time.Duration
	WakeMax     time.Duration
	MaxWakes    int

	// Mailman routes worker mail through a mailman agent instead of delivering each
	// message at once (mailman.go). The router still validates and rate-limits every
	// message; the mailman only decides how bursts are worded. The manager's and the
	// harness's own mail is never routed through it. MailmanQuiet is how long a burst
	// of parcels must be quiet before the mailman is asked (default 1.5s),
	// MailmanMax the longest a burst may postpone it (6s), MailmanBound the longest any
	// parcel waits before the harness delivers it directly (30s), MailmanBatch how
	// many parcels one request of the mailman covers (24), MailmanMaxPending how many
	// the harness holds at all before delivering directly (200), and MailmanDigestChars
	// the longest digest (700).
	Mailman            bool
	MailmanQuiet       time.Duration
	MailmanMax         time.Duration
	MailmanBound       time.Duration
	MailmanBatch       int
	MailmanMaxPending  int
	MailmanDigestChars int
}

// DefaultConfig returns sane limits for a laptop-sized swarm.
func DefaultConfig() Config {
	return Config{
		MaxAgents: 24, MaxWriters: 4, RPM: 500, MaxConcurrent: 24,
		Hot: DefaultHotConfig(), Router: DefaultRouterConfig(), LeaseTTL: 10 * time.Minute,
		IdleRetire: 15 * time.Minute,
		Board:      DefaultBoardLimits(), MaxAttempts: 3, VerifyTimeout: 15 * time.Minute, MaxVerifies: 2,
		StuckAfter: 10 * time.Minute, StuckGrace: 30 * time.Second, ShutdownGrace: 10 * time.Second,
		SuperviseEvery: time.Second, InboxSoftCap: 12, MaxMailWakes: 40,
		WakeQuiet: 1500 * time.Millisecond, WakeMax: 10 * time.Second, MaxWakes: 8,
		MailmanQuiet: 1500 * time.Millisecond, MailmanMax: 6 * time.Second, MailmanBound: 30 * time.Second,
		MailmanBatch: 24, MailmanMaxPending: 200, MailmanDigestChars: 700,
	}
}

// Deps are the session services agents are built from.
type Deps struct {
	Provider provider.Provider
	Model    cost.Model
	// Compactor and CompactorModel are agent.Config's, for every agent.
	Compactor      provider.Provider
	CompactorModel cost.Model
	// Plans holds each agent's plan (internal/plan); nil: no plan tool.
	Plans *plan.Store
	// OutagePatience is agent.Config.OutagePatience for every agent of the swarm.
	OutagePatience time.Duration
	Registry       *tools.Registry
	// ToolSpecs is the frozen tool list (must include the swarm tools).
	ToolSpecs []core.ToolSpec
	Const     *kv.Layer
	Shared    *kv.Layer

	Events  events.Emitter
	Blobs   events.Blobs
	Archive *kv.Archive
	Files   *tools.FileState
	Perm    perm.Requester
	Snap    tools.Snapshotter
	Handles *tools.Handles

	Workdir string
	Root    string
	Params  core.Params
	Planner kv.Planner
	// KVPolicy tunes breakpoint placement for every agent (kv.Policy); zero is
	// kv.DefaultPolicy, as for a solo agent.
	KVPolicy kv.Policy
	Est      core.Estimator
	Limits   tools.Limits
	Now      func() time.Time

	// NewSink builds a UI sink for an agent (optional).
	NewSink func(agentID string) agent.Sink

	// RoleModels overrides the model (and endpoint) a role runs on. RL uses it to
	// train one role against fixed others, for example the manager on the policy
	// under training and the workers on a stronger reference model.
	RoleModels map[string]RoleModel
	// CaptureTokens asks endpoints for token ids and logprobs on every call.
	CaptureTokens bool
	// OnWrite is told after every successful write (the checkpoint store uses it
	// to tell an agent's last write from a human's later edit).
	OnWrite func(agent, path string)
	// Hooks runs user-configured commands around every agent's tool calls and
	// stops (nil: none).
	Hooks agent.Hooks
	// Isolation, when set, gives every writer a git worktree of its own and
	// integrates finished work through a verifying merge queue (isolate.go). Nil is
	// swarm.isolation = "none": one shared tree, guarded by leases.
	Isolation *Isolation
}

// RoleModel is a per-role model override.
type RoleModel struct {
	Provider provider.Provider
	Model    cost.Model
}

// Swarm runs many agents over one repository.
type Swarm struct {
	cfg   Config
	deps  Deps
	roles Roles

	Board  *Board
	Router *Router
	Leases *Leases
	Gov    *Governor
	Gate   *WarmGate

	quietSince atomic.Int64 // unix nanoseconds when the board last had a quiet wait with nothing changed since; 0 when something changed

	spawnMu  sync.Mutex // serialises admission: spawn, reuse and the limits they enforce
	sharedMu sync.Mutex // serialises SetShared

	mu       sync.Mutex
	members  map[string]*member
	seq      map[string]int
	manager  string
	shared   *kv.Layer
	roleLay  map[string]*kv.Layer
	rootCtx  context.Context
	cancel   context.CancelFunc
	closed   bool
	spent    float64              // spend of agents that have left (the budget ledger)
	lastSeen map[string]*Snapshot // per agent: the board at the end of its last wait
	wg       sync.WaitGroup

	verifySem               chan struct{}
	verifyRan, verifyFailed atomic.Int32 // runs of the verify command, and those that failed (VerifyRuns)
	hseq                    atomic.Int64
	budgetOnce              atomic.Bool

	wk      waker                    // waking an idle manager (wake.go)
	mgrSeen atomic.Pointer[Snapshot] // the board as the manager's newest request showed it
	mail    *mailroom                // mailman mode (mailman.go); nil when it is off

	// Worktree isolation (isolate.go): the harness's record of which task assignments
	// reached the integration branch, how often each came back from the merge queue,
	// and the end-of-run state.
	merged     map[string]mergeRec
	bounces    map[string]int
	treeAgents map[string]bool // agents that ever had a tree of their own
	apply      applyState
	finishMu   sync.Mutex
	finished   *IntegrationReport
	left       string // what the manager's last run left unfinished (see Unfinished)
}

// New builds a swarm. Call Start before spawning.
func New(cfg Config, deps Deps, roles Roles) *Swarm {
	def := DefaultConfig()
	if cfg.MaxAgents == 0 {
		cfg.MaxAgents = def.MaxAgents
	}
	if cfg.MaxWriters == 0 {
		cfg.MaxWriters = def.MaxWriters
	}
	if cfg.LeaseTTL == 0 {
		cfg.LeaseTTL = def.LeaseTTL
	}
	if cfg.Hot == (HotConfig{}) {
		cfg.Hot = def.Hot
	}
	if cfg.Router == (RouterConfig{}) {
		cfg.Router = def.Router
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = def.MaxAttempts
	}
	if cfg.MaxMailWakes == 0 {
		cfg.MaxMailWakes = def.MaxMailWakes
	}
	if cfg.VerifyTimeout <= 0 {
		cfg.VerifyTimeout = def.VerifyTimeout
	}
	if cfg.MaxVerifies <= 0 {
		cfg.MaxVerifies = def.MaxVerifies
	}
	if cfg.StuckAfter == 0 {
		cfg.StuckAfter = def.StuckAfter
	}
	if cfg.StuckGrace <= 0 {
		cfg.StuckGrace = def.StuckGrace
	}
	if cfg.ShutdownGrace <= 0 {
		cfg.ShutdownGrace = def.ShutdownGrace
	}
	if cfg.SuperviseEvery <= 0 {
		cfg.SuperviseEvery = def.SuperviseEvery
	}
	if cfg.InboxSoftCap <= 0 {
		cfg.InboxSoftCap = def.InboxSoftCap
	}
	if cfg.WakeQuiet <= 0 {
		cfg.WakeQuiet = def.WakeQuiet
	}
	if cfg.WakeMax <= 0 {
		cfg.WakeMax = def.WakeMax
	}
	if cfg.MaxWakes <= 0 {
		cfg.MaxWakes = def.MaxWakes
	}
	if cfg.MailmanQuiet <= 0 {
		cfg.MailmanQuiet = def.MailmanQuiet
	}
	if cfg.MailmanMax <= 0 {
		cfg.MailmanMax = def.MailmanMax
	}
	if cfg.MailmanBound <= 0 {
		cfg.MailmanBound = def.MailmanBound
	}
	if cfg.MailmanBatch <= 0 {
		cfg.MailmanBatch = def.MailmanBatch
	}
	if cfg.MailmanMaxPending <= 0 {
		cfg.MailmanMaxPending = def.MailmanMaxPending
	}
	if cfg.MailmanDigestChars <= 0 {
		cfg.MailmanDigestChars = def.MailmanDigestChars
	}
	if deps.Events == nil {
		deps.Events = events.Discard{}
	}
	if deps.Est == nil {
		deps.Est = core.NewBytesEstimator()
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if roles == nil {
		roles = BuiltinRoles()
	}
	if cfg.Mailman {
		// The mailman is the harness's own role: added here (over any role of the same
		// name), in a copy, so the caller's table is not changed.
		with := make(Roles, len(roles)+1)
		for n, r := range roles {
			with[n] = r
		}
		with[MailmanRoleName] = MailmanRole()
		roles = with
	}
	s := &Swarm{cfg: cfg, deps: deps, roles: roles, members: map[string]*member{}, seq: map[string]int{},
		roleLay: map[string]*kv.Layer{}, lastSeen: map[string]*Snapshot{}, shared: deps.Shared,
		verifySem: make(chan struct{}, cfg.MaxVerifies), merged: map[string]mergeRec{}, bounces: map[string]int{}, treeAgents: map[string]bool{}}
	s.Board = NewBoard(deps.Events)
	s.Board.SetClock(deps.Now)
	s.Board.SetLimits(cfg.Board)
	s.Leases = NewLeases(cfg.LeaseTTL, s.Board)
	s.Leases.SetEmitter(deps.Events)
	s.Leases.SetRoots(s.roots()...)
	if s.isolated() {
		s.Leases.Isolate()
	}
	s.Gov = NewGovernor(GovernorConfig{RPM: cfg.RPM, MaxConcurrent: cfg.MaxConcurrent, Admit: s.budgetErr,
		OnEvent: func(action string, data map[string]any) {
			data["action"] = action
			s.emit(events.TypeGovernor, data)
		}})
	s.Gate = NewWarmGate(deps.Model.Cache.DefaultTTL(), 0)
	s.Router = NewRouter(cfg.Router, deps.Events, s.roster, s.ManagerID, nil)
	s.Router.SetDeliver(s.deliver)
	if cfg.Mailman {
		s.mail = newMailroom(s)
		s.Router.SetDivert(s.mail.divert)
	}
	for name, r := range roles {
		s.roleLay[name] = r.Layer()
	}
	return s
}

// Start binds the swarm to a context; cancelling it stops every agent. It is safe
// to call on every turn of a session: while the swarm is running it does nothing,
// and after its context has ended (a turn that was cancelled) it starts again on
// the new one. After Shutdown it does nothing.
func (s *Swarm) Start(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || (s.rootCtx != nil && s.rootCtx.Err() == nil) {
		return
	}
	s.rootCtx, s.cancel = context.WithCancel(ctx)
	rc := s.rootCtx
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.supervise(rc)
	}()
}

// Shutdown cancels all agents and waits for them, but not forever: an agent stuck
// in a tool that ignores its context is left behind after ShutdownGrace. It is safe
// to call twice; after it nothing new starts.
func (s *Swarm) Shutdown() {
	s.mu.Lock()
	s.closed = true
	cancel := s.cancel
	s.mu.Unlock()
	s.wk.stop() // no wake after this
	if s.mail != nil {
		s.mail.stop() // and no batch for the mailman
	}
	if cancel != nil {
		cancel()
	}
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	t := time.NewTimer(s.cfg.ShutdownGrace)
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
		s.emit("swarm.shutdown", map[string]any{"waited": s.cfg.ShutdownGrace.String(), "note": "agents still running were left behind"})
	}
	// An idle agent may still have a compaction job in flight, and nothing else
	// cancels it: close every agent (concurrently, each bounded by agent.CloseGrace).
	var closing sync.WaitGroup
	for _, id := range s.roster() {
		if m := s.get(id); m != nil {
			closing.Add(1)
			go func() { defer closing.Done(); _ = m.a.Close() }()
		}
	}
	closing.Wait()
}

// roster lists the agents mail can be addressed to: everyone but the harness's own
// service agents (the mailman is nobody's correspondent).
func (s *Swarm) roster() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.members))
	for id, m := range s.members {
		if !m.service {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// Roles returns the role table.
func (s *Swarm) Roles() Roles { return s.roles }

// MaxAgents is the most agents the swarm registers, the manager included.
func (s *Swarm) MaxAgents() int { return s.cfg.MaxAgents }

// MaxWriters is how many agents that may modify files can run at once.
func (s *Swarm) MaxWriters() int { return s.cfg.MaxWriters }

// ManagerID returns the manager's agent id.
func (s *Swarm) ManagerID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.manager
}

// currentShared reads the current shared prompt-layer pointer under the swarm lock.
func (s *Swarm) currentShared() *kv.Layer {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.shared
}

// StartManager creates the manager agent, or returns the one that exists: a session
// that runs several turns keeps one manager and its thread.
func (s *Swarm) StartManager() (*agent.Agent, error) {
	s.spawnMu.Lock()
	defer s.spawnMu.Unlock()
	if m := s.get(s.ManagerID()); m != nil {
		return m.a, nil
	}
	r, ok := s.roles["manager"]
	if !ok {
		return nil, errors.New("no manager role defined")
	}
	id := r.Short
	if id == "" {
		id = "mgr"
	}
	var notes *kv.Layer
	if s.isolated() {
		notes = kv.NewLayer("notes:"+id, kv.KindNotes, 1, []kv.Segment{{Key: "isolation", Text: managerIsolationCard, Vol: kv.VolFrozen}})
	}
	m, err := s.newMember(id, r, notes, NewEvidence(), nil)
	if err != nil {
		return nil, err
	}
	s.register(m, s.currentShared())
	s.mu.Lock()
	s.manager = id
	s.mu.Unlock()
	m.setState(s, "running", "")
	s.emit(events.TypeAgentSpawn, map[string]any{"id": id, "role": "manager", "model": s.modelFor("manager").ID})
	return m.a, nil
}

// StartIdleManager is StartManager for a manager that has nothing to do yet: a resumed team's is back with its conversation and waits for the
// next goal, so it is done (as after any turn), not running. RunManager marks it running again when the goal comes.
func (s *Swarm) StartIdleManager() (*agent.Agent, error) {
	a, err := s.StartManager()
	if err != nil {
		return nil, err
	}
	if m := s.get(a.ID()); m != nil {
		m.setState(s, "done", "")
	}
	return a, nil
}

// RunManager runs the manager on a goal and returns its final answer. If ctx ends
// while it runs, the workers are stopped too (their tasks go back to todo). A panic
// in the manager's run is returned as an error.
//
// With Config.HoldManager the manager is sent back to work, a bounded number of
// times, when it answers while the board holds unfinished work; if it still stops
// with work left, the answer is followed by a harness line naming what was left
// (running workers, tasks in review, ...), which is also shown as a notice.
func (s *Swarm) RunManager(ctx context.Context, goal string) (*agent.Result, error) {
	a, err := s.StartManager()
	if err != nil {
		return nil, err
	}
	m := s.get(a.ID())
	if m == nil {
		return nil, errors.New("the manager is gone")
	}
	if !s.reserveManager(m) && !s.supersedeWake(m) {
		return nil, errors.New("the manager is already running")
	}
	s.HumanInput()
	return s.runManager(ctx, m, goal, "")
}

// runManager runs the reserved manager. mail, when set, is a message the harness
// framed (a wake note) that is queued for the manager's first step; goal is the
// person's input, or "" for a run the harness started.
func (s *Swarm) runManager(ctx context.Context, m *member, goal, mail string) (res *agent.Result, err error) {
	epoch := s.wk.epochNow()
	stop := context.AfterFunc(ctx, func() {
		s.wk.hold(epoch) // first: stopping the workers is an event the manager would be woken for
		s.stopWorkers("interrupted", false)
	})
	defer stop()
	// A run the harness started (a wake, which comes with a note as mail) can be ended by a person's goal without stopping the workers: it has a
	// context of its own.
	runCtx := ctx
	if mail != "" {
		var end context.CancelFunc
		runCtx, end = context.WithCancel(ctx)
		defer end()
		s.wk.setRun(end)
		defer s.wk.setRun(nil)
	}
	m.setState(s, "running", "planning")
	if mail != "" {
		m.a.Send(mail)
	}
	func() {
		defer func() {
			if r := recover(); r != nil {
				st := debug.Stack()
				res, err = nil, &panicError{val: r, stack: st}
				s.emitAs(m.id, "agent.panic", map[string]any{"id": m.id, "panic": fmt.Sprint(r), "stack": string(st)})
			}
		}()
		res, err = m.a.Run(runCtx, goal)
	}()
	s.releaseManager(m)
	state := "done"
	switch {
	case errors.Is(err, context.Canceled):
		state = "idle" // the person interrupted it: it did not fail, and the next goal starts it again
	case err != nil:
		state = "failed"
	}
	m.setState(s, state, "")
	s.applyMerged(m) // isolated runs: the verified, merged work reaches the person's checkout now
	if err == nil {
		s.afterManagerRun(m, res)
	}
	return res, err
}

// afterManagerRun is what the swarm does when the manager's run has returned cleanly.
// A held manager that stopped anyway (the veto bound was reached) reports what it left
// behind; in an interactive session, anything the manager has not seen wakes it again.
func (s *Swarm) afterManagerRun(m *member, res *agent.Result) {
	if s.cfg.HoldManager && res != nil {
		u := s.unfinishedWork()
		txt := ""
		if !u.empty() {
			txt = u.summary(holdListCap)
		}
		s.mu.Lock()
		s.left = txt // each run says what it left, so a clean run after an unclean one says nothing
		s.mu.Unlock()
		if txt != "" {
			res.Text = strings.TrimRight(res.Text, "\n")
			if res.Text != "" {
				res.Text += "\n\n"
			}
			res.Text += "[harness] Unfinished when the manager stopped: " + txt + "."
			s.emitAs(m.id, events.TypeSwarmUnfinished, map[string]any{"unfinished": txt})
			s.managerNotice(m, "warn", "the manager stopped with unfinished work: "+txt)
		}
	}
	if s.cfg.WakeManager && s.wakeNote(m) != "" {
		s.managerEvent()
	}
}

// Unfinished says what the manager's last run left undone: the running workers, the submissions waiting for a verdict and the
// tasks nobody finished, as the final answer's harness note lists them. It is empty for a run that settled its board, and for
// a swarm that is not held (an interactive session, where workers outlive a turn).
func (s *Swarm) Unfinished() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.left
}

// Manager returns the manager agent, if started.
func (s *Swarm) Manager() *agent.Agent {
	if m := s.get(s.ManagerID()); m != nil {
		return m.a
	}
	return nil
}

// guardWithAfter forwards to the lease guard and additionally reports writes.
type guardWithAfter struct {
	tools.Guard
	after func(agent, path string)
}

// AfterWrite notifies the wrapped guard before invoking an optional additional write callback.
func (g guardWithAfter) AfterWrite(agent, path string) {
	g.Guard.AfterWrite(agent, path)
	if g.after != nil {
		g.after(agent, path)
	}
}

// modelFor is the model a role runs on.
func (s *Swarm) modelFor(role string) cost.Model {
	if rm, ok := s.deps.RoleModels[role]; ok && rm.Provider != nil {
		return rm.Model
	}
	return s.deps.Model
}

// spawnableRoles returns configured roles excluding the manager and service roles.
func (s *Swarm) spawnableRoles() []string {
	var out []string
	for _, n := range s.roles.Names() {
		if n != "manager" && !s.isService(n) {
			out = append(out, n)
		}
	}
	return out
}

// isService reports whether a role is one of the harness's own (the mailman, while
// mailman mode is on): the harness starts it, and it is nobody's teammate or
// correspondent. When the mode is off a role of that name is whatever the project made
// it.
func (s *Swarm) isService(role string) bool { return s.mail != nil && role == MailmanRoleName }

// taskCard renders the assignment text. In notes (pin=true) it carries the full
// card; the kickoff message is the short form. Everything from the task is made
// single-line (the description keeps its line breaks) and defused: the task text comes
// from a board that models fill in, and it ends up in a worker's notes (and, for the
// kickoff, in a task turn; never in the user's instructions).
func taskCard(t Task, agentID string, pin bool) string {
	title := cleanText(t.Title, maxTitleRunes)
	if pin {
		var sb strings.Builder
		fmt.Fprintf(&sb, "You are %s. Your assignment is task %s: %s\n", agentID, t.ID, title)
		if d := cleanBlock(t.Desc, maxDescRunes); d != "" {
			sb.WriteString(d + "\n")
		}
		if len(t.Files) > 0 {
			files := make([]string, 0, len(t.Files))
			for _, f := range t.Files {
				files = append(files, cleanText(f, maxScopeLen))
			}
			sb.WriteString("Scope: " + strings.Join(files, ", ") + " (edit only inside your scope: writes outside it are rejected; ask the manager to widen it)\n")
		}
		if len(t.Deps) > 0 {
			sb.WriteString("Depends on: " + cleanText(strings.Join(t.Deps, ", "), 120) + "\n")
		}
		sb.WriteString("When finished, call task done with a one-line result. The harness verifies your work before accepting it.")
		return sb.String()
	}
	return fmt.Sprintf("Begin task %s: %s. Your assignment and scope are in <my-notes>.", t.ID, title)
}

// onPromote receives facts compactors want promoted into shared context.
func (s *Swarm) onPromote(from string, ps []kv.Promotion) {
	role := ""
	if m := s.get(from); m != nil {
		role = m.role
	}
	for _, p := range ps {
		_, _ = s.Board.AddNote(from, p.Scope, role, p.Text)
	}
}

// roleRequester tightens permissions for read-only roles. It runs in front of
// the session's permission engine, so a reviewer can never write even if the
// mode would allow it.
type roleRequester struct {
	inner perm.Requester
	role  Role
	// denyWrites, when set, makes the agent read-only whatever its role says, and is
	// what it is told: read-only roles, and the manager of an isolated run.
	denyWrites string
	// strictShell applies the fallback allowlist to shell commands even when the
	// permission engine is there to enforce the role's profile: a second, stricter
	// opinion for an agent whose profile the swarm cannot vouch for.
	strictShell bool
	// only, when set, is the whole list of tools the agent may call: the mailman may
	// call mail, and nothing else, whatever the mode or the rules say.
	only map[string]bool
}

// isolatedManagerMsg is what the manager of an isolated run is told when it tries to
// change a file.
const isolatedManagerMsg = "in an isolated run the manager does not edit files: spawn a worker for the change (the harness verifies and merges its work)"

// readOnlyShellHint is added to a shell command's refusal: "the manager does not edit files" alone told a
// manager that had only chained two reads with && (a real one, on its first action) that reading was
// forbidden.
const readOnlyShellHint = "; the shell here takes one plain read-only command at a time (ls, cat, grep, find, git status/diff/log, go test), with no pipes, && or redirects"

func (r roleRequester) Check(ctx context.Context, req perm.Request) perm.Decision {
	req.Role = r.role.Name
	if r.only != nil && !r.only[req.Tool] {
		return perm.Decision{Allow: false, Reason: r.denyWrites}
	}
	if r.denyWrites != "" {
		_, engine := r.inner.(*perm.Engine)
		switch {
		case req.Tool == "bash" && engine && !r.strictShell:
			// The permission engine parses shell syntax and enforces the role's
			// (plan) profile itself; a prefix allowlist here would only be a weaker,
			// bypassable second opinion (and would deny the checks the profile allows).
		case req.Tool == "bash":
			// No engine (tests, embedding), or an agent the swarm holds to a stricter shell:
			// the conservative allowlist.
			if !readOnlyCommand(req.Command) {
				return perm.Decision{Allow: false, Reason: r.denyWrites + readOnlyShellHint}
			}
		case req.Writes:
			return perm.Decision{Allow: false, Reason: r.denyWrites}
		}
	}
	return r.inner.Check(ctx, req)
}

// readOnlyCommand is the fallback allowlist for read-only roles' shell use when no
// permission engine is installed. It accepts one plain command from a short list
// of inspection programs: no control operators, redirections or substitutions, no
// flag that makes the program write or run another, and no path that leaves the
// project (absolute paths, "~", "..").
func readOnlyCommand(cmd string) bool {
	c := strings.TrimSpace(cmd)
	if c == "" || strings.ContainsAny(c, ";&|<>`$(){}\n\r\\") {
		return false
	}
	cs := splitShell(c)
	if len(cs) != 1 || cs[0].op != "" || len(cs[0].words) == 0 {
		return false
	}
	w := cs[0].words
	prog := w[0]
	args := w[1:]
	for _, a := range args {
		if pathEscapes(a) {
			return false
		}
	}
	has := func(deny ...string) bool {
		for _, a := range args {
			for _, d := range deny {
				if a == d || strings.HasPrefix(a, d+"=") {
					return true
				}
			}
		}
		return false
	}
	switch prog {
	case "ls", "cat", "head", "tail", "wc", "pwd", "echo", "grep", "diff", "stat", "file", "tree":
		return true
	case "rg":
		return !has("--pre", "--pre-glob", "--hostname-bin", "-z", "--search-zip")
	case "find":
		return !has("-delete", "-exec", "-execdir", "-ok", "-okdir", "-fprint", "-fprint0", "-fprintf", "-fls")
	case "git":
		if len(args) == 0 {
			return false
		}
		switch args[0] {
		case "status", "diff", "log", "show", "blame", "ls-files", "rev-parse", "grep", "shortlog", "describe":
			return !has("--output", "-o", "--ext-diff", "--textconv", "--no-index", "--open-files-in-pager", "-O", "--exec")
		}
		return false
	case "go":
		if len(args) == 0 {
			return false
		}
		switch args[0] {
		case "test", "vet", "list", "version":
			return !has("-exec", "-toolexec", "-vettool", "-o", "-coverprofile", "-cpuprofile", "-memprofile", "-blockprofile", "-mutexprofile", "-trace", "-outputdir", "-overlay", "-modfile", "-pkgdir", "-fuzz", "-fuzztime")
		}
		return false
	case "npm", "pnpm", "yarn":
		return len(args) == 1 && args[0] == "test"
	case "pytest":
		return !has("--basetemp", "-p", "--junitxml", "--junit-xml", "--cov-report", "--resultlog")
	case "cargo":
		return len(args) > 0 && (args[0] == "test" || args[0] == "check") && !has("--target-dir", "--manifest-path", "--config")
	case "make":
		return len(args) == 1 && (args[0] == "test" || args[0] == "check")
	}
	return false
}

// pathEscapes reports whether a command argument names a place outside the
// project: an absolute path, a home-relative path, or a ".." component. Flags and
// revision syntax such as HEAD~1 or main..topic are not paths.
func pathEscapes(a string) bool {
	if strings.HasPrefix(a, "-") {
		if i := strings.IndexByte(a, '='); i > 0 { // --flag=/etc/passwd
			a = a[i+1:]
		} else {
			return false
		}
	}
	if a == "" {
		return false
	}
	if strings.HasPrefix(a, "/") || strings.HasPrefix(a, "~") {
		return true
	}
	for _, seg := range strings.Split(path.Clean(strings.ReplaceAll(a, `\`, "/")), "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}

// SetShared installs new shared context for every agent at its next turn
// boundary. Editing the shared layer flushes every agent's cache, so this is an
// epoch: callers batch promotions and do it rarely (see Epoch). Two overlapping
// calls are serialised, so every agent ends on the same, newest epoch.
func (s *Swarm) SetShared(l *kv.Layer, reason string) {
	s.sharedMu.Lock()
	defer s.sharedMu.Unlock()
	s.mu.Lock()
	s.shared = l
	ids := make([]*member, 0, len(s.members))
	for _, m := range s.members {
		ids = append(ids, m)
	}
	s.mu.Unlock()
	for _, m := range ids {
		m.a.SyncShared(l, s.roleLay[m.role], reason)
	}
	s.emit(events.TypeLayerCommit, map[string]any{"scope": "shared-epoch", "reason": reason, "hash": l.Hash().Short()})
}

// SetToolset installs the session's registry and frozen tool list. It exists
// because the swarm's own tools must be registered before the list is frozen,
// and those tools need the swarm: build the swarm, register Tools(), freeze the
// specs, then call SetToolset before Start.
func (s *Swarm) SetToolset(reg *tools.Registry, specs []core.ToolSpec) {
	s.deps.Registry, s.deps.ToolSpecs = reg, specs
}
