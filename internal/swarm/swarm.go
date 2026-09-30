package swarm

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
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
	// MaxWriters bounds agents whose roles may modify files. Evidence from
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
	// BudgetUSD caps total spend across the swarm (0 = unlimited).
	BudgetUSD float64
	// AgentBudgetUSD caps each worker.
	AgentBudgetUSD float64
	// VerifyCmd, when set, is run in an agent's workdir before its task may leave
	// "doing": the harness, not the model, decides whether work is finished.
	VerifyCmd string
	// Verify runs a command; injected so the swarm does not depend on the shell
	// package. It returns combined output and exit code.
	Verify func(ctx context.Context, dir, cmd string) (string, int, error)
	// IdleRetire retires idle workers after this long (0 = never).
	IdleRetire time.Duration
}

// DefaultConfig returns sane limits for a laptop-sized swarm.
func DefaultConfig() Config {
	return Config{
		MaxAgents: 24, MaxWriters: 4, RPM: 500, MaxConcurrent: 24,
		Hot: DefaultHotConfig(), Router: DefaultRouterConfig(), LeaseTTL: 10 * time.Minute,
		IdleRetire: 15 * time.Minute,
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
}

// member is one registered agent.
type member struct {
	id, role string
	a        *agent.Agent
	ev       *Evidence
	task     string

	mu       sync.Mutex
	running  bool
	state    string
	line     string
	lastPush time.Time
	cancel   context.CancelFunc
	idleAt   time.Time
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

	mu       sync.Mutex
	members  map[string]*member
	seq      map[string]int
	manager  string
	shared   *kv.Layer
	roleLay  map[string]*kv.Layer
	rootCtx  context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	lastSeen map[string]uint64 // per agent: board version at its last wait
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
		roleLay: map[string]*kv.Layer{}, lastSeen: map[string]uint64{}, shared: deps.Shared}
	s.Board = NewBoard(deps.Events)
	s.Leases = NewLeases(cfg.LeaseTTL, s.Board)
	s.Gov = NewGovernor(GovernorConfig{RPM: cfg.RPM, MaxConcurrent: cfg.MaxConcurrent})
	s.Gate = NewWarmGate(deps.Model.Cache.DefaultTTL(), 0)
	s.Router = NewRouter(cfg.Router, deps.Events, s.roster, func() string { return s.manager }, s.deliver)
	for name, r := range roles {
		s.roleLay[name] = r.Layer()
	}
	return s
}

// Start binds the swarm to a context; cancelling it stops every agent.
func (s *Swarm) Start(ctx context.Context) {
	s.rootCtx, s.cancel = context.WithCancel(ctx)
	if s.cfg.IdleRetire > 0 {
		s.wg.Add(1)
		go s.janitor()
	}
}

// Shutdown cancels all agents and waits for them.
func (s *Swarm) Shutdown() {
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
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

// TotalCost sums spend across agents.
func (s *Swarm) TotalCost() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var t float64
	for _, m := range s.members {
		_, c := m.a.Usage()
		t += c
	}
	return t
}

// buildAgent constructs an agent for a member.
func (s *Swarm) buildAgent(id string, r Role, notes *kv.Layer, ev *Evidence) (*agent.Agent, error) {
	d := s.deps
	isMgr := r.Name == "manager"
	requester := perm.Requester(d.Perm)
	if requester == nil {
		requester = perm.AllowAll{}
	}
	requester = roleRequester{inner: requester, role: r}
	sink := agent.Sink(agent.NopSink{})
	if d.NewSink != nil {
		sink = d.NewSink(id)
	}
	m := &member{id: id, role: r.Name, ev: ev, state: "idle"}
	cfg := agent.Config{
		ID: id, Role: r.Name, Model: d.Model, Provider: d.Provider, Tools: d.Registry, ToolSpecs: d.ToolSpecs,
		Const: d.Const, Shared: s.currentShared(), RoleL: s.roleLay[r.Name], Notes: notes,
		Params: d.Params,
		Hot: func(agentID string) []core.Block {
			snap := s.Board.Snapshot()
			txt := RenderHot(snap, agentID, r.Name, isMgr, s.cfg.Hot, d.Est)
			return []core.Block{core.Text(txt)}
		},
		Events: d.Events, Blobs: d.Blobs, Archive: d.Archive, Files: d.Files, Guard: s.Leases, Snap: d.Snap,
		Handles: d.Handles, Perm: requester, Limiter: s.Gov, Gate: s.Gate,
		Sink:    &memberSink{Sink: sink, s: s, m: m, ev: ev},
		Workdir: d.Workdir, Root: d.Root, Limits: d.Limits,
		Planner: d.Planner, SessionID: s.cfg.SessionID, AffinityShards: s.cfg.AffinityShards,
		OnPromote: s.onPromote, Est: d.Est, Now: d.Now, MaxSteps: r.MaxSteps, Priority: r.Priority,
		BudgetUSD: s.cfg.AgentBudgetUSD,
	}
	a, err := agent.New(cfg)
	if err != nil {
		return nil, err
	}
	m.a = a
	s.mu.Lock()
	s.members[id] = m
	s.mu.Unlock()
	return a, nil
}

func (s *Swarm) currentShared() *kv.Layer {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.shared
}

func (s *Swarm) get(id string) *member {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.members[id]
}

// StartManager creates the manager agent.
func (s *Swarm) StartManager() (*agent.Agent, error) {
	r, ok := s.roles["manager"]
	if !ok {
		return nil, errors.New("no manager role defined")
	}
	id := r.Short
	if id == "" {
		id = "mgr"
	}
	ev := NewEvidence()
	a, err := s.buildAgent(id, r, nil, ev)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.manager = id
	s.mu.Unlock()
	s.Board.SetAgent(AgentInfo{ID: id, Role: "manager", State: "running"})
	return a, nil
}

// RunManager runs the manager on a goal and returns its final answer.
func (s *Swarm) RunManager(ctx context.Context, goal string) (*agent.Result, error) {
	a, err := s.StartManager()
	if err != nil {
		return nil, err
	}
	m := s.get(a.ID())
	m.setState(s, "running", "planning")
	res, err := a.Run(ctx, goal)
	state := "done"
	if err != nil {
		state = "failed"
	}
	m.setState(s, state, "")
	return res, err
}

// Manager returns the manager agent, if started.
func (s *Swarm) Manager() *agent.Agent {
	if m := s.get(s.ManagerID()); m != nil {
		return m.a
	}
	return nil
}

// SpawnReq describes a worker to start.
type SpawnReq struct {
	Role string
	// TaskID assigns an existing task; otherwise Title creates one.
	TaskID string
	Title  string
	Brief  string
	Files  []string
	// Agent reuses an idle worker (its context and cache are already warm).
	Agent string
	By    string
}

// Spawn starts (or reuses) a worker for a task and returns its id.
func (s *Swarm) Spawn(req SpawnReq) (string, error) {
	if s.rootCtx == nil {
		return "", errors.New("swarm not started")
	}
	if s.cfg.BudgetUSD > 0 && s.TotalCost() >= s.cfg.BudgetUSD {
		return "", fmt.Errorf("swarm budget of $%.2f is exhausted", s.cfg.BudgetUSD)
	}
	var task Task
	snap := s.Board.Snapshot()
	switch {
	case req.TaskID != "":
		t, ok := snap.Task(req.TaskID)
		if !ok {
			return "", fmt.Errorf("no task %s", req.TaskID)
		}
		if t.Status == StatusDone {
			return "", fmt.Errorf("%s is already done", req.TaskID)
		}
		if t.Owner != "" && t.Owner != req.Agent {
			return "", fmt.Errorf("%s is already owned by %s", req.TaskID, t.Owner)
		}
		task = t
	case strings.TrimSpace(req.Title) != "":
		t, err := s.Board.CreateTask(req.By, TaskSpec{Title: req.Title, Desc: req.Brief, Role: req.Role, Files: req.Files})
		if err != nil {
			return "", err
		}
		task = t
	default:
		return "", errors.New("spawn needs a task id or a title")
	}
	if len(req.Files) > 0 {
		task.Files = req.Files
	}
	if c := s.scopeConflict(task); c != "" {
		return "", errors.New(c)
	}

	// Reuse an idle worker.
	if req.Agent != "" {
		m := s.get(req.Agent)
		if m == nil {
			return "", fmt.Errorf("no agent %q", req.Agent)
		}
		m.mu.Lock()
		busy := m.running
		m.mu.Unlock()
		if busy {
			return "", fmt.Errorf("%s is still working; wait for it or spawn a new worker", req.Agent)
		}
		if err := s.Board.Assign(req.By, m.id, task.ID); err != nil {
			return "", err
		}
		m.task = task.ID
		s.startRun(m, taskCard(task, m.id, false))
		return m.id, nil
	}

	role, ok := s.roles[req.Role]
	if !ok || req.Role == "manager" {
		return "", fmt.Errorf("unknown role %q (roles: %s)", req.Role, strings.Join(s.spawnableRoles(), ", "))
	}
	s.mu.Lock()
	total := len(s.members)
	writers := 0
	for _, m := range s.members {
		if r := s.roles[m.role]; !r.ReadOnly && m.role != "manager" && m.isActive() {
			writers++
		}
	}
	s.mu.Unlock()
	if total >= s.cfg.MaxAgents {
		return "", fmt.Errorf("agent limit reached (%d); reuse an idle worker with spawn agent=… or wait", s.cfg.MaxAgents)
	}
	if !role.ReadOnly && writers >= s.cfg.MaxWriters {
		return "", fmt.Errorf("%d writers are already active (limit %d): concurrent writers collide on shared code. Wait for one to finish, reuse an idle worker, or use a read-only role (reviewer, scout) for this", writers, s.cfg.MaxWriters)
	}

	s.mu.Lock()
	s.seq[role.Name]++
	id := fmt.Sprintf("%s-%d", role.Short, s.seq[role.Name])
	s.mu.Unlock()

	ev := NewEvidence()
	notes := kv.NewLayer("notes:"+id, kv.KindNotes, 1, []kv.Segment{{Key: "assignment", Text: taskCard(task, id, true), Vol: kv.VolFrozen}})
	if _, err := s.buildAgent(id, role, notes, ev); err != nil {
		return "", err
	}
	m := s.get(id)
	m.task = task.ID
	if err := s.Board.Assign(req.By, id, task.ID); err != nil {
		return "", err
	}
	s.Board.SetAgent(AgentInfo{ID: id, Role: role.Name, State: "running", Task: task.ID})
	s.emit(events.TypeAgentSpawn, map[string]any{"id": id, "role": role.Name, "task": task.ID, "by": req.By})
	s.startRun(m, taskCard(task, id, false))
	return id, nil
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

func (m *member) isActive() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running || m.state == "running"
}

// taskCard renders the assignment text. In notes (pin=true) it carries the full
// card; the kickoff message is the short form.
func taskCard(t Task, agentID string, pin bool) string {
	var sb strings.Builder
	if pin {
		fmt.Fprintf(&sb, "You are %s. Your assignment is task %s: %s\n", agentID, t.ID, t.Title)
		if t.Desc != "" {
			sb.WriteString(t.Desc + "\n")
		}
		if len(t.Files) > 0 {
			sb.WriteString("Scope: " + strings.Join(t.Files, ", ") + " (edit only inside your scope; ask the manager to widen it)\n")
		}
		if len(t.Deps) > 0 {
			sb.WriteString("Depends on: " + strings.Join(t.Deps, ", ") + "\n")
		}
		sb.WriteString("When finished, call task done with a one-line result. The harness verifies your work before accepting it.")
		return sb.String()
	}
	return fmt.Sprintf("Begin task %s: %s. Your assignment and scope are in <my-notes>.", t.ID, t.Title)
}

// scopeConflict reports overlap between a task's scope and other active tasks.
func (s *Swarm) scopeConflict(t Task) string {
	if len(t.Files) == 0 {
		return ""
	}
	snap := s.Board.Snapshot()
	for _, o := range snap.Tasks {
		if o.ID == t.ID || o.Status != StatusDoing || len(o.Files) == 0 {
			continue
		}
		for _, a := range t.Files {
			for _, b := range o.Files {
				if scopesOverlap(a, b) {
					return fmt.Sprintf("scope %q overlaps %s (%s, held by %s). Give the two tasks disjoint areas, or make %s depend on %s", a, o.ID, b, o.Owner, t.ID, o.ID)
				}
			}
		}
	}
	return ""
}

// scopesOverlap reports whether two path patterns can match a common file. It is
// deliberately conservative: prefix relationships between directory patterns
// count as overlap.
func scopesOverlap(a, b string) bool {
	pa, pb := globPrefix(a), globPrefix(b)
	return strings.HasPrefix(pa, pb) || strings.HasPrefix(pb, pa)
}

func globPrefix(p string) string {
	p = strings.TrimPrefix(strings.TrimSpace(p), "./")
	if i := strings.IndexAny(p, "*?[{"); i >= 0 {
		p = p[:i]
	}
	return p
}

// startRun runs the agent in the background.
func (s *Swarm) startRun(m *member, input string) {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return
	}
	m.running = true
	ctx, cancel := context.WithCancel(s.rootCtx)
	m.cancel = cancel
	m.mu.Unlock()
	m.setState(s, "running", "starting")
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer cancel()
		res, err := m.a.Run(ctx, input)
		s.finishRun(m, res, err)
	}()
}

// finishRun records a worker's completion. The harness closes the loop even if
// the model forgot: a task still "doing" moves to review with the evidence and
// the model's final message attached.
func (s *Swarm) finishRun(m *member, res *agent.Result, err error) {
	m.mu.Lock()
	m.running = false
	m.idleAt = s.deps.Now()
	m.mu.Unlock()
	state := "idle"
	if err != nil && !errors.Is(err, context.Canceled) {
		state = "failed"
	}
	snap := s.Board.Snapshot()
	if t, ok := snap.Task(m.task); ok && t.Owner == m.id && t.Status == StatusDoing {
		summary := ""
		if res != nil {
			summary = strings.TrimSpace(res.Text)
		}
		if err != nil {
			_ = s.Board.Finish(m.id, t.ID, StatusFailed, fmt.Sprintf("agent stopped: %v", err))
		} else {
			_ = s.Board.Finish(m.id, t.ID, StatusReview, oneLine(summary, 120)+" ["+m.ev.Summary()+"]")
		}
	}
	s.Leases.ReleaseAll(m.id)
	m.setState(s, state, "")
	s.emit(events.TypeAgentEnd, map[string]any{"id": m.id, "state": state, "evidence": m.ev.Summary()})
}

// deliver hands mail to the recipient and wakes it if idle.
func (s *Swarm) deliver(msg Message) {
	m := s.get(msg.To)
	if m == nil {
		return
	}
	m.a.Send(msg.Format())
	m.mu.Lock()
	idle := !m.running
	m.mu.Unlock()
	if idle && msg.To != s.ManagerID() {
		s.startRun(m, "")
	}
}

// Retire removes an idle agent.
func (s *Swarm) Retire(id string) error {
	m := s.get(id)
	if m == nil {
		return fmt.Errorf("no agent %q", id)
	}
	m.mu.Lock()
	busy := m.running
	m.mu.Unlock()
	if busy {
		return fmt.Errorf("%s is running", id)
	}
	s.mu.Lock()
	delete(s.members, id)
	s.mu.Unlock()
	s.Board.RemoveAgent(id)
	s.Leases.ReleaseAll(id)
	return nil
}

func (s *Swarm) janitor() {
	defer s.wg.Done()
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-s.rootCtx.Done():
			return
		case <-t.C:
			now := s.deps.Now()
			for _, id := range s.roster() {
				m := s.get(id)
				if m == nil || id == s.ManagerID() {
					continue
				}
				m.mu.Lock()
				stale := !m.running && !m.idleAt.IsZero() && now.Sub(m.idleAt) > s.cfg.IdleRetire
				m.mu.Unlock()
				if stale {
					_ = s.Retire(id)
				}
			}
		}
	}
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

func (s *Swarm) emit(typ string, data any) {
	_, _ = s.deps.Events.Emit("swarm", typ, data)
}

// setState publishes an agent's harness-derived status, throttled so a busy
// swarm does not turn every tool call into a board version.
func (m *member) setState(s *Swarm, state, line string) {
	m.mu.Lock()
	changed := m.state != state || m.line != line
	now := s.deps.Now()
	if !changed || (state == m.state && now.Sub(m.lastPush) < 750*time.Millisecond) {
		m.mu.Unlock()
		return
	}
	m.state, m.line, m.lastPush = state, line, now
	task := m.task
	m.mu.Unlock()
	_, cost := m.a.Usage()
	s.Board.SetAgent(AgentInfo{ID: m.id, Role: m.role, State: state, Task: task, Line: line, CostUSD: cost})
}

// memberSink derives agent status and evidence from what an agent actually does.
type memberSink struct {
	agent.Sink
	s  *Swarm
	m  *member
	ev *Evidence
}

func (k *memberSink) ToolStart(a string, call core.Block) {
	k.Sink.ToolStart(a, call)
	k.m.setState(k.s, "running", activity(call))
}

func (k *memberSink) ToolEnd(a string, call core.Block, res *tools.Result, took time.Duration) {
	k.Sink.ToolEnd(a, call, res, took)
	k.ev.Observe(call, res, k.s.deps.Now())
	if call.ToolName == "wait" {
		k.m.setState(k.s, "running", "reviewing results")
	}
}

// roleRequester tightens permissions for read-only roles. It runs in front of
// the session's permission engine, so a reviewer can never write even if the
// mode would allow it.
type roleRequester struct {
	inner perm.Requester
	role  Role
}

func (r roleRequester) Check(ctx context.Context, req perm.Request) perm.Decision {
	req.Role = r.role.Name
	if r.role.ReadOnly && (req.Writes || (req.Tool == "bash" && !readOnlyCommand(req.Command))) {
		return perm.Decision{Allow: false, Reason: fmt.Sprintf("the %s role is read-only: report findings instead of changing files", r.role.Name)}
	}
	return r.inner.Check(ctx, req)
}

// readOnlyCommand is a conservative allowlist for read-only roles' shell use.
func readOnlyCommand(cmd string) bool {
	c := strings.TrimSpace(cmd)
	if strings.ContainsAny(c, ";&|><`$") && !strings.HasPrefix(c, "git ") {
		return false
	}
	for _, p := range []string{"ls", "cat ", "head ", "tail ", "wc ", "grep ", "rg ", "find ", "git status", "git diff", "git log", "git show", "git blame", "go test", "go vet", "go build", "npm test", "pytest", "cargo test", "make test", "pwd", "echo "} {
		if strings.HasPrefix(c, p) || c == strings.TrimSpace(p) {
			return true
		}
	}
	return false
}

// SetShared installs new shared context for every agent at its next turn
// boundary. Editing the shared layer flushes every agent's cache, so this is an
// epoch: callers batch promotions and do it rarely (see Epoch).
func (s *Swarm) SetShared(l *kv.Layer, reason string) {
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
