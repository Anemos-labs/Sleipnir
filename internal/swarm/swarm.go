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

	"github.com/reee344/sleipnir/internal/agent"
	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/cost"
	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/kv"
	"github.com/reee344/sleipnir/internal/perm"
	"github.com/reee344/sleipnir/internal/provider"
	"github.com/reee344/sleipnir/internal/tools"
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
	// VerifyTimeout bounds one verifier run (default 15 minutes); MaxVerifies
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
}

// DefaultConfig returns sane limits for a laptop-sized swarm.
func DefaultConfig() Config {
	return Config{
		MaxAgents: 24, MaxWriters: 4, RPM: 500, MaxConcurrent: 24,
		Hot: DefaultHotConfig(), Router: DefaultRouterConfig(), LeaseTTL: 10 * time.Minute,
		IdleRetire: 15 * time.Minute,
		Board:      DefaultBoardLimits(), MaxAttempts: 3, VerifyTimeout: 15 * time.Minute, MaxVerifies: 2,
		StuckAfter: 10 * time.Minute, StuckGrace: 30 * time.Second, ShutdownGrace: 10 * time.Second,
		SuperviseEvery: time.Second, InboxSoftCap: 12,
		WakeQuiet: 1500 * time.Millisecond, WakeMax: 10 * time.Second, MaxWakes: 8,
	}
}

// Deps are the session services agents are built from.
type Deps struct {
	Provider provider.Provider
	Model    cost.Model
	Registry *tools.Registry
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
	Est     core.Estimator
	Limits  tools.Limits
	Now     func() time.Time

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

	verifySem  chan struct{}
	hseq       atomic.Int64
	budgetOnce atomic.Bool

	wk      waker                    // waking an idle manager (wake.go)
	mgrSeen atomic.Pointer[Snapshot] // the board as the manager's newest request showed it

	// Worktree isolation (isolate.go): the harness's record of which task assignments
	// reached the integration branch, how often each came back from the merge queue,
	// and the end-of-run state.
	merged     map[string]mergeRec
	bounces    map[string]int
	treeAgents map[string]bool // agents that ever had a tree of their own
	apply      applyState
	finishMu   sync.Mutex
	finished   *IntegrationReport
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
}

func (s *Swarm) roster() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.members))
	for id := range s.members {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Roles returns the role table.
func (s *Swarm) Roles() Roles { return s.roles }

// ManagerID returns the manager's agent id.
func (s *Swarm) ManagerID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.manager
}

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
	m, err := s.newMember(id, r, nil, NewEvidence(), nil)
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
	if !s.reserveManager(m) {
		return nil, errors.New("the manager is already running")
	}
	s.HumanInput()
	return s.runManager(ctx, m, goal, "")
}

// runManager runs the reserved manager. mail, when set, is a message the harness
// framed (a wake note) that is queued for the manager's first step; goal is the
// person's input, or "" for a run the harness started.
func (s *Swarm) runManager(ctx context.Context, m *member, goal, mail string) (res *agent.Result, err error) {
	stop := context.AfterFunc(ctx, func() { s.stopWorkers("interrupted", false) })
	defer stop()
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
		res, err = m.a.Run(ctx, goal)
	}()
	s.releaseManager(m)
	state := "done"
	if err != nil {
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
		if u := s.unfinishedWork(); !u.empty() {
			txt := u.summary(holdListCap)
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

func (s *Swarm) spawnableRoles() []string {
	var out []string
	for _, n := range s.roles.Names() {
		if n != "manager" {
			out = append(out, n)
		}
	}
	return out
}

// taskCard renders the assignment text. In notes (pin=true) it carries the full
// card; the kickoff message is the short form. Everything from the task is made
// single-line (the description keeps its line breaks) and defused: the kickoff is a
// user-origin turn that compaction may later preserve as an instruction.
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
}

// isolatedManagerMsg is what the manager of an isolated run is told when it tries to
// change a file.
const isolatedManagerMsg = "in an isolated run the manager does not edit files: spawn a worker for the change (the harness verifies and merges its work)"

func (r roleRequester) Check(ctx context.Context, req perm.Request) perm.Decision {
	req.Role = r.role.Name
	if r.denyWrites != "" {
		_, engine := r.inner.(*perm.Engine)
		switch {
		case req.Tool == "bash" && engine && !r.strictShell:
			// The permission engine parses shell syntax and enforces the role's
			// (plan) profile itself; a prefix allowlist here would only be a weaker,
			// bypassable second opinion (and would deny the checks the profile allows).
		case req.Tool == "bash":
			// No engine (tests, embedding): fall back to the conservative allowlist.
			if !readOnlyCommand(req.Command) {
				return perm.Decision{Allow: false, Reason: r.denyWrites}
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
