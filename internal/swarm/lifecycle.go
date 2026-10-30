package swarm

import (
	"context"
	"errors"
	"fmt"
	"github.com/anemos-labs/sleipnir/internal/plan"
	"path/filepath"
	"runtime/debug"
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
	"github.com/anemos-labs/sleipnir/internal/provider"
	"github.com/anemos-labs/sleipnir/internal/tools"
	"github.com/anemos-labs/sleipnir/internal/workspace"
)

// A member goes idle -> running -> idle -> ... -> retired. Every transition is made
// under member.mu by one function (reserve/finishRun/Retire/abandon), so an agent
// is never both running and retired, a run is never started twice, and a run that
// was abandoned by the watchdog cannot overwrite the state of its successor.
type life int

const (
	lifeIdle life = iota
	lifeRunning
	lifeRetired
)

// statusThrottle is the shortest interval between two published line changes of one
// agent while its state does not change (a state change is always published at
// once; a throttled line is flushed when the interval ends).
const statusThrottle = 750 * time.Millisecond

// maxGateTries is the repair allowance after a failed gate. Explicit done calls
// and implicit stops share the task's verification counter.
const maxGateTries = 2

// runState is one run of a member's agent. Its tasks are the assignments (id and
// rev) it owns: at the end of the run only those are settled, and only if the
// board still shows the same rev, so a run that ends late cannot undo a newer
// assignment (a reject, a reuse).
type runState struct {
	id          uint64
	tasks       map[string]uint64
	cancel      context.CancelFunc
	started     time.Time
	mailSeq     uint64 // mail received before this run reserved the worker
	reason      string // why the harness stopped the run ("" = it was not asked to stop)
	count       bool   // whether that stop counts as a failed attempt at the task
	abortAt     time.Time
	quarantined bool
	// asked marks a run the watchdog stopped while it waited for the person to answer a question: a restart would ask again,
	// so the harness does not restart it on its own (restart.go).
	asked bool
}

// member is one registered agent.
type member struct {
	id, role string
	// slot is the team slot the member holds (Config.Team index; -1: none), model the
	// model it runs on.
	slot     int
	model    cost.Model
	a        *agent.Agent
	ev       *Evidence
	manager  bool
	readOnly bool
	// service marks an agent of the harness itself (the mailman): it is not a worker,
	// holds no task, and never counts as unfinished work.
	service bool
	// tree is the agent's git worktree in an isolated run (nil: it works in the
	// shared checkout); dir is the directory its tools work in either way.
	tree *workspace.Tree
	dir  string
	// sink is the session's sink for this agent (before the swarm wraps it): the
	// swarm's own notices to the person go through it.
	sink agent.Sink
	// notify is signalled (never blocking) whenever mail is queued for the member.
	notify chan struct{}

	mu        sync.Mutex
	life      life
	task      string // the assignment shown in its status
	recovered bool   // idle worker recovered with its task and private tree
	run       *runState
	runSeq    uint64
	state     string
	line      string
	lastPush  time.Time
	dirty     bool
	flushT    *time.Timer
	idleAt    time.Time
	// outage is when its last run ended on an outage of its model's endpoint (provider.Error.Outage); zero after a run that
	// ended any other way. The manager's assignments steer away from it for a while (steer.go).
	outage    time.Time
	gateTries int
	stuckWarn bool
	autoRuns  int
	mailSeq   uint64 // advances for each received message, including coalesced mail
	// mailWakes counts the runs peer mail started for the current task, and wakeLimited
	// that the bound on them (Config.MaxMailWakes) was reached and reported. wakeMu makes
	// "is the bound reached, wake, count" one step among the senders that do it at once.
	mailWakes   int
	wakeLimited bool
	wakeMu      sync.Mutex
	// asking counts the questions the agent has put to the person that are unanswered (an approval), and askWhat is the latest
	// (Swarm.Asking).
	asking  int
	askWhat string
	// inboxPeer says, for the messages sent to the agent's inbox (newest last), whether another
	// worker wrote each: see noteSent. The bound on mail wakes applies to those alone.
	inboxPeer []bool
	box       overflow
	emitted   string

	// retireOnIdle (under mu) retires the worker when its current run ends instead of
	// restarting it: it handed over its own task (handover.go). A new run clears it, so a worker that
	// stayed (it had unread mail) and was given work again is not retired at the end of that run.
	retireOnIdle bool

	// restart (under mu) is the automatic restart the harness has scheduled after a failed run of the worker (nil: none);
	// restarts counts the restarts in a row it has made of the worker on the work named by restartKey (restart.go).
	restart    *pendingRestart
	restartKey string
	restarts   int

	// progress is the unix time of the last sign of life (a model or tool event).
	progress atomic.Int64
	// turns counts the agent's model responses, and quietFrom is the count at its last
	// progress call or new assignment: the stall sweep's measure (stall.go).
	turns, quietFrom atomic.Int64
	// worked is one more than the turn count at the member's last progress call (0: none yet). Unlike quietFrom, a new
	// assignment does not set it: it says the member did something, for the excuse guard (excuseRefusal).
	worked atomic.Int64
	// pubMu serialises status publication: whoever publishes last reads the
	// newest state, so the board never keeps a stale one.
	pubMu sync.Mutex
}

// touch atomically records the swarm clock's current time as the member's latest progress.
func (m *member) touch(s *Swarm) { m.progress.Store(s.deps.Now().UnixNano()) }

// madeProgress restarts the stall sweep's count of turns without progress.
func (m *member) madeProgress() { m.quietFrom.Store(m.turns.Load()) }

// currentTask is the task the member last took (claimed, was given or resumed).
func (m *member) currentTask() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.task
}

// panicError is what a run that panicked returns to finishRun.
type panicError struct {
	val   any
	stack []byte
}

// Error formats the value recovered from a worker panic.
func (p *panicError) Error() string { return fmt.Sprintf("panic: %v", p.val) }

// emit writes a swarm-level event. An emitter that panics or fails is contained.
func (s *Swarm) emit(typ string, data any) {
	defer func() { _ = recover() }()
	_, _ = s.deps.Events.Emit("swarm", typ, data)
}

// emitAs emits an event for the specified agent while suppressing emitter errors and panics.
func (s *Swarm) emitAs(agentID, typ string, data any) {
	defer func() { _ = recover() }()
	_, _ = s.deps.Events.Emit(agentID, typ, data)
}

// nextHarnessID numbers the harness's own mail.
func (s *Swarm) nextHarnessID() string { return fmt.Sprintf("h%d", s.hseq.Add(1)) }

// newMember builds an agent and its member record; it registers nothing. tree is
// the git worktree of an isolated writer (nil: the shared checkout): the agent's
// tools then work there, with checkpoints of its own.
func (s *Swarm) newMember(id string, r Role, notes *kv.Layer, ev *Evidence, tree *workspace.Tree) (*member, error) {
	return s.newMemberAt(id, r, notes, ev, tree, -2)
}

// newMemberAt is newMember for a worker that takes team slot slot (a successor takes
// its predecessor's); -2 picks the first free slot of the role.
func (s *Swarm) newMemberAt(id string, r Role, notes *kv.Layer, ev *Evidence, tree *workspace.Tree, slot int) (*member, error) {
	d := s.deps
	isMgr := r.Name == "manager"
	requester := perm.Requester(d.Perm)
	if requester == nil {
		requester = perm.DenyAll{} // fail closed: see perm.DenyAll
	}
	rr := roleRequester{inner: requester, role: r, swarm: s, agent: id}
	isSvc := s.isService(r.Name)
	switch {
	case isSvc:
		// The mailman: one tool, mail. Whatever else it calls is refused, by the requester
		// (the file, shell and web tools ask it) and by the swarm tools themselves.
		rr.denyWrites = fmt.Sprintf("the %s only delivers mail: its one tool is mail", r.Name)
		rr.only = map[string]bool{"mail": true}
	case r.ReadOnly:
		rr.denyWrites = fmt.Sprintf("the %s role is read-only: it writes no file, $TMPDIR included; report findings instead of changing files", r.Name)

	}
	requester = rr
	sink := agent.Sink(agent.NopSink{})
	if d.NewSink != nil {
		sink = d.NewSink(id)
	}
	workdir, root, snap, after := d.Workdir, d.Root, d.Snap, d.OnWrite
	if tree != nil {
		workdir, root = tree.Path, tree.Path
		if iso := d.Isolation; iso != nil {
			if sub := iso.Subdir; sub != "" {
				// The session was started in a subdirectory: its writers work in the same
				// one of their trees (when the tree has it; an ignored directory is not there).
				if dir := filepath.Join(tree.Path, filepath.FromSlash(sub)); isDir(dir) {
					workdir = dir
				}
			}
			if iso.Checkpoints != nil {
				snap, after = iso.Checkpoints(id, tree.Path)
			}
		}
	}
	// A member is made idle, and has been since it was made (idle_capacity counts a pool member that has not run yet).
	m := &member{id: id, role: r.Name, ev: ev, state: "idle", manager: isMgr, readOnly: r.ReadOnly, service: isSvc, sink: sink, notify: make(chan struct{}, 1),
		tree: tree, dir: workdir, slot: -1, idleAt: s.deps.Now()}
	if ev != nil {
		ev.bind(func(path string) string { return s.editTask(id, path) }, func(task string) bool {
			t, ok := s.Board.Snapshot().Task(task)
			return ok && t.Owner == id && (t.Status == StatusDoing || t.Status == StatusBlocked || t.Status == StatusReview)
		})
	}
	m.box.init()
	if slot >= -1 {
		m.slot = slot
	} else if !isMgr && !isSvc {
		s.mu.Lock()
		m.slot = s.slotFor(r.Name)
		s.mu.Unlock()
	}
	if isMgr && tree == nil {
		previous := after
		after = func(id, path string) {
			if previous != nil {
				previous(id, path)
			}
			if rel, err := filepath.Rel(root, path); err == nil && filepath.IsLocal(rel) {
				path = filepath.ToSlash(rel)
			}
			s.review.mu.Lock()
			s.review.landLocked("manager changes", []string{path}, 0)
			s.review.mu.Unlock()
			s.noteManagerEdit(path)
		}
	}
	rm := s.modelFor(r.Name, m.slot)
	model, prov := rm.Model, rm.Provider
	m.model = model
	hooks := d.Hooks
	switch {
	case isMgr:
		hooks = &holdHooks{inner: d.Hooks, s: s, m: m}
	case isSvc:
		hooks = &serviceHooks{inner: d.Hooks, only: rr.only, role: r.Name}
	}
	params := d.Params
	if d.DefaultMaxTokens != nil {
		params.MaxTokens = d.DefaultMaxTokens(model)
	}
	cfg := agent.Config{
		SnapshotEachStep: s.cfg.PersistentPools || s.isolated() && s.deps.Isolation.Resumable,
		ID:               id, Role: r.Name, Model: model, Provider: prov, Compactor: d.Compactor, CompactorModel: d.CompactorModel, CompactorHealth: d.CompactorHealth, Tools: d.Registry, ToolSpecs: d.ToolSpecs,
		CaptureTokens: d.CaptureTokens, TrainingLogs: d.TrainingLogs,
		Const: d.Const, Shared: s.currentShared(), RoleL: s.roleLay[r.Name], Notes: notes,
		Params: params, Effort: d.Effort, OutagePatience: d.OutagePatience, WaitUnreachable: d.WaitUnreachable,
		Barrier: d.Barrier,
		Hot: func(agentID string) []core.Block {
			if isSvc {
				// The board is nothing to the mailman: it knows who it is, and that is all the
				// uncached tail it is billed for.
				return []core.Block{core.Text(fmt.Sprintf("you: %s (%s)", agentID, r.Name))}
			}
			snap := s.Board.Snapshot()
			if isMgr {
				s.noteManagerSeen(snap) // what the manager has looked at (see wake.go)
			}
			txt := RenderHot(snap, agentID, r.Name, isMgr, s.cfg.Hot, d.Est)
			// the team's occupancy (the manager's) and the auto-docs state (everyone's) go inside the frame too
			var extra []string
			if r.ReadOnly && s.isolated() {
				ref := s.integrationRef()
				extra = append(extra, "Isolation: Your read-only tools use the original project checkout, which can lag behind integrated work. Inspect merged work with git show "+ref+":<path>, git ls-tree or git diff of "+ref+". Resuming a task does not refresh this checkout; do not block waiting for it.")
			}
			if isMgr {
				if t := s.TeamState(); t != "" {
					extra = append(extra, t)
				}
				extra = append(extra, s.staffingLines(snap)...)
				if line := s.emptyBoardLine(snap); line != "" {
					extra = append(extra, line)
				}
				if last := s.reviewPending(); last != "" {
					extra = append(extra, s.reviewLine(last))
				}
			}
			if ds := s.DocsState(); ds != "" {
				extra = append(extra, ds)
			}
			if len(extra) > 0 {
				if head, ok := strings.CutSuffix(strings.TrimSpace(txt), "</live>"); ok {
					txt = head + strings.Join(extra, "\n") + "\n</live>"
				}
			}
			if d.Plans != nil {
				// the plan goes inside the board's own frame, before its closing tag, so the view stays one frame
				if lines := plan.Lines(d.Plans.Get(agentID)); lines != "" {
					if head, ok := strings.CutSuffix(strings.TrimSpace(txt), "</live>"); ok {
						txt = head + "your plan:\n" + lines + "</live>"
					}
				}
			}
			return []core.Block{core.Text(txt)}
		},
		Events: d.Events, Blobs: d.Blobs, Archive: d.Archive, Files: d.Files, Guard: guardWithAfter{s.Leases, after}, Snap: snap,
		Handles: d.Handles, Perm: requester, Limiter: s.Gov, Gate: s.Gate,
		Sink:    &memberSink{Sink: sink, s: s, m: m, ev: ev},
		Workdir: workdir, Root: root, Tmp: d.Tmp, Limits: d.Limits,
		Planner: d.Planner, KVPolicy: d.KVPolicy, SessionID: s.cfg.SessionID, AffinityShards: s.cfg.AffinityShards,
		BlockingCompaction: d.BlockingCompaction,
		OnPromote:          s.onPromote, Est: d.Est, Now: d.Now, MaxSteps: r.MaxSteps, Priority: r.Priority,
		ExtendSteps: !isMgr && !isSvc && !s.cfg.FixedSteps, WrapUp: s.wrapUp(),
		PlanOpen:  planOpen(d.Plans),
		BudgetUSD: s.cfg.AgentBudgetUSD, Hooks: hooks, NoMailReopen: !isMgr, // a worker's mail is read by its next run (afterIdle), which owns what the mail changes
	}
	a, err := agent.New(cfg)
	if err != nil {
		return nil, err
	}
	m.a = a
	m.touch(s)
	return m, nil
}

// register makes a member visible to the roster and the router. A worker that
// was built while a shared-context epoch was installed is brought up to date first,
// so it can never run on an epoch the swarm has moved past.
func (s *Swarm) register(m *member, builtShared *kv.Layer) {
	s.mu.Lock()
	s.members[m.id] = m
	cur := s.shared
	s.mu.Unlock()
	if cur != builtShared && cur != nil {
		m.a.SyncShared(cur, s.roleLay[m.role], "epoch (registration)")
	}
}

// arrive marks an agent ID as being added to the team and returns the function that ends the mark. A spawn or a handover
// assigns the task to the new agent before it registers it, and a worktree is made in between: however many sweeps fall in
// that interval, the task is not an orphan, so the stall sweep skips owners that are arriving.
func (s *Swarm) arrive(id string) func() {
	s.mu.Lock()
	if s.arriving == nil {
		s.arriving = map[string]bool{}
	}
	s.arriving[id] = true
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		delete(s.arriving, id)
		s.mu.Unlock()
	}
}

// get reads a member pointer under the swarm lock, returning nil for unknown IDs.
func (s *Swarm) get(id string) *member {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.members[id]
}

// roleOf is the role of a member, or "" for an id that is not one.
func (s *Swarm) roleOf(id string) string {
	if m := s.get(id); m != nil {
		return m.role
	}
	return ""
}

// activeWriters counts members that may write files and are running now.
func (s *Swarm) activeWriters() int {
	s.mu.Lock()
	ms := make([]*member, 0, len(s.members))
	for _, m := range s.members {
		ms = append(ms, m)
	}
	s.mu.Unlock()
	n := 0
	for _, m := range ms {
		if m.manager || m.readOnly {
			continue
		}
		m.mu.Lock()
		if m.life == lifeRunning {
			n++
		}
		m.mu.Unlock()
	}
	return n
}

// ---- status ----------------------------------------------------------------------

// setState records an agent's harness-derived status and publishes it. A change of
// state is published at once. A change of the line alone is published at most
// every statusThrottle, but never dropped: the newest line is flushed when the
// interval ends.
func (m *member) setState(s *Swarm, state, line string) { m.changeState(s, state, line, false) }

// setIdleState is setState for a member that is idle: one that a run has reserved since keeps the state its run publishes.
// The check and the change are one step under the member's lock, so a run that starts at the same moment cannot have its
// state overwritten by this one.
func (m *member) setIdleState(s *Swarm, state, line string) { m.changeState(s, state, line, true) }

// changeState is setState; with idleOnly it changes nothing unless the member is idle.
func (m *member) changeState(s *Swarm, state, line string, idleOnly bool) {
	line = cleanText(line, 70)
	m.mu.Lock()
	if m.life == lifeRetired || (m.state == state && m.line == line) || (idleOnly && m.life != lifeIdle) {
		m.mu.Unlock()
		return
	}
	now := s.deps.Now()
	if m.state == state && now.Sub(m.lastPush) < statusThrottle {
		m.line, m.dirty = line, true
		if m.flushT == nil {
			m.flushT = time.AfterFunc(statusThrottle-now.Sub(m.lastPush), func() { m.flushLine(s) })
		}
		m.mu.Unlock()
		return
	}
	m.state, m.line, m.lastPush, m.dirty = state, line, now, false
	m.mu.Unlock()
	m.publish(s)
}

// flushLine clears the pending flush timer and dirty flag, records the push time, and publishes
// outside the member lock only when progress changed.
func (m *member) flushLine(s *Swarm) {
	m.mu.Lock()
	m.flushT = nil
	dirty := m.dirty
	m.dirty = false
	m.lastPush = s.deps.Now()
	m.mu.Unlock()
	if dirty {
		m.publish(s)
	}
}

// stopTimers stops and clears a member's pending flush timer under its lock.
func (m *member) stopTimers() {
	m.mu.Lock()
	if m.flushT != nil {
		m.flushT.Stop()
		m.flushT = nil
	}
	m.mu.Unlock()
}

// publish writes the member's newest status to the board.
func (m *member) publish(s *Swarm) {
	defer func() { _ = recover() }()
	m.pubMu.Lock()
	defer m.pubMu.Unlock()
	m.mu.Lock()
	info := AgentInfo{ID: m.id, Role: m.role, State: m.state, Task: m.task, Line: m.line}
	retired := m.life == lifeRetired
	key := m.state + "|" + m.line
	first := m.emitted != key
	m.emitted = key
	m.mu.Unlock()
	if retired {
		return
	}
	_, info.CostUSD = m.a.Usage()
	info.CtxTokens = m.a.PromptTokens()
	if !m.service { // the harness's own agents are not on the board: nobody's teammate
		s.Board.SetAgent(info)
	}
	if first {
		s.emitAs(m.id, events.TypeAgentState, map[string]any{"id": m.id, "state": info.State, "line": info.Line, "task": info.Task})
	}
}

// memberSink derives agent status and evidence from what an agent actually does,
// and is the harness's liveness signal for the watchdog. It runs on the agent's
// own goroutines, so a panic in it (or in the UI sink it wraps) is contained here.
type memberSink struct {
	agent.Sink
	s  *Swarm
	m  *member
	ev *Evidence
}

// safely recovers sink panics and records them as sink.panic events instead of unwinding the
// worker.
func (k *memberSink) safely(fn func()) {
	defer func() {
		if r := recover(); r != nil {
			k.s.emit("sink.panic", map[string]any{"agent": k.m.id, "panic": fmt.Sprint(r)})
		}
	}()
	fn()
}

// Text records member progress before forwarding a text delta through the panic-safe sink wrapper.
func (k *memberSink) Text(a, d string) {
	k.m.touch(k.s)
	k.safely(func() { k.Sink.Text(a, d) })
}

// Thinking records member progress before forwarding a reasoning delta through the panic-safe sink
// wrapper.
func (k *memberSink) Thinking(a, d string) {
	k.m.touch(k.s)
	k.safely(func() { k.Sink.Thinking(a, d) })
}

// Response records member progress and counts the model turn before safely forwarding response
// metadata and cache usage.
func (k *memberSink) Response(a string, r *provider.Response, hit float64) {
	k.m.touch(k.s)
	k.m.turns.Add(1)
	k.safely(func() { k.Sink.Response(a, r, hit) })
}

// Reset passes a retry on to the sink the member wraps, if that sink cares (agent.Resetter).
func (k *memberSink) Reset(a string) {
	if r, ok := k.Sink.(agent.Resetter); ok {
		k.safely(func() { r.Reset(a) })
	}
}

// Notice is a sign of life too: what an agent tells the person while it waits for an endpoint that is down (every half minute, for
// as long as its patience lasts, which is bounded) is the worker doing what it should, and not a worker that has stopped.
func (k *memberSink) Notice(a, level, msg string) {
	k.m.touch(k.s)
	k.safely(func() { k.Sink.Notice(a, level, msg) })
}

// ToolStart records progress, notes the manager's coordination calls for the stall sweep,
// updates the member's running activity, and safely forwards the tool-start notification.
func (k *memberSink) ToolStart(a string, call core.Block) {
	k.m.touch(k.s)
	if k.m.manager {
		// A long sleep in the shell is noted as a wait by the manager's tool hook (holdHooks.BeforeTool), which runs after this
		// notice and sees the input the call runs with.
		k.safely(func() { k.s.noteCoordination(k.m.id, call.ToolName, jsonString(call.Input, "id")) })
	}
	k.safely(func() { k.m.setState(k.s, "running", activity(call)) })
	k.safely(func() { k.Sink.ToolStart(a, call) })
}

// ToolEnd records progress and evidence (including progress on a task, for the stall sweep),
// restores running state after wait, and safely forwards completion to the sink.
func (k *memberSink) ToolEnd(a string, call core.Block, res *tools.Result, took time.Duration) {
	k.m.touch(k.s)
	k.safely(func() { k.ev.Observe(call, res, k.s.deps.Now()) })
	k.safely(func() {
		if k.s.progressCall(call, res) {
			k.m.madeProgress()
			k.m.worked.Store(k.m.turns.Load() + 1)
		}
	})
	if call.ToolName == "wait" {
		k.safely(func() { k.m.setState(k.s, "running", "reviewing results") })
	}
	k.safely(func() { k.Sink.ToolEnd(a, call, res, took) })
}

// ---- runs --------------------------------------------------------------------------

// reserve claims an idle member for a run: it makes it running and counts the run
// in the swarm's WaitGroup. It fails if the swarm is not running or the member is
// not idle (some other path started it first).
func (s *Swarm) reserve(m *member) (*runState, context.Context, bool) {
	s.mu.Lock()
	if s.closed || s.rootCtx == nil || s.rootCtx.Err() != nil {
		s.mu.Unlock()
		return nil, nil, false
	}
	root := s.rootCtx
	s.wg.Add(1)
	s.mu.Unlock()

	m.mu.Lock()
	if m.life != lifeIdle {
		m.mu.Unlock()
		s.wg.Done()
		return nil, nil, false
	}
	m.life = lifeRunning
	m.runSeq++
	ctx, cancel := context.WithCancel(root)
	rs := &runState{id: m.runSeq, tasks: map[string]uint64{}, cancel: cancel, started: s.deps.Now(), mailSeq: m.mailSeq}
	m.run = rs
	m.retireOnIdle = false // a request to retire belongs to the run it was made in
	m.stuckWarn = false
	m.progress.Store(rs.started.UnixNano())
	m.mu.Unlock()
	return rs, ctx, true
}

// unreserve gives a reservation back (the run never started).
func (s *Swarm) unreserve(m *member, rs *runState) {
	rs.cancel()
	m.mu.Lock()
	if m.run == rs {
		m.run = nil
		m.life = lifeIdle
	}
	m.mu.Unlock()
	s.wg.Done()
}

// adopt records the tasks the agent is doing now as the run's assignments, and points its evidence at the one its status shows.
func (s *Swarm) adopt(m *member, rs *runState) {
	snap := s.Board.Snapshot()
	m.mu.Lock()
	for _, t := range snap.Tasks {
		if t.Owner == m.id && t.Status == StatusDoing {
			rs.tasks[t.ID] = t.Rev
			s.review.noteBegan(t.ID, t.Rev, rs.started)
			if m.task == "" || m.task != t.ID {
				m.task = t.ID
			}
		}
	}
	task := m.task
	m.mu.Unlock()
	if t, ok := snap.Task(task); ok && t.Owner == m.id && t.Status == StatusDoing {
		m.ev.Begin(task)
	}
	if s.roleOf(m.id) == ReviewerRole {
		for _, t := range snap.Tasks {
			if t.Owner == m.id && t.Status == StatusDoing {
				go s.noteReviewBegan(t) // it reads the work tree; a spawn holds the spawn lock here
			}
		}
	}
}

// editTask names the task an edit of path by agent belongs to: the one task it is doing whose scope holds the path, or "" (its
// current task) when none of its scopes holds the path or several do. A pool task is not one: its work is its children's, and
// a leader's edit inside its own child's scope is the child's. It reads the board only.
func (s *Swarm) editTask(agent, path string) string {
	rel, ok := s.Leases.rel(path)
	if !ok {
		return ""
	}
	hit := ""
	for _, t := range s.Board.Snapshot().Tasks {
		if t.Owner != agent || t.Status != StatusDoing || t.Pool != nil || len(t.Files) == 0 || !inScope(t.Files, rel) {
			continue
		}
		if hit != "" {
			return ""
		}
		hit = t.ID
	}
	return hit
}

// trackClaim adds a task the agent claimed during its run to the run's assignments.
func (s *Swarm) trackClaim(agentID, taskID string) {
	m := s.get(agentID)
	if m == nil {
		return
	}
	t, ok := s.Board.Snapshot().Task(taskID)
	if !ok {
		return
	}
	m.mu.Lock()
	if m.run != nil {
		m.run.tasks[taskID] = t.Rev
		s.review.noteBegan(taskID, t.Rev, s.deps.Now())
	}
	m.task = taskID
	m.gateTries = 0
	m.mailWakes, m.wakeLimited = 0, false
	m.mu.Unlock()
	m.madeProgress()
	m.ev.Begin(taskID)
	if s.roleOf(agentID) == ReviewerRole {
		s.noteReviewBegan(t)
	}
	card := claimedCards(s.Board.Snapshot(), agentID, nil)
	if m.tree != nil {
		card += isolationCard(integrationRefOf(m.tree))
	}
	m.a.QueueAssignment(card)
}

// launch starts the run goroutine of a reserved member.
func (s *Swarm) launch(m *member, rs *runState, ctx context.Context, start runStart) {
	s.adopt(m, rs)
	m.setState(s, "running", "starting")
	go s.runMember(m, rs, ctx, start)
}

// Asking records that an agent is waiting for the person to answer a question (an approval prompt) and returns the call that records the
// answer. The watchdog still stops a worker that waits for too long, and counts it as an attempt, but says what it was waiting for: a
// worker whose question nobody answered is not a worker that hung, and the manager (and the person, through it) should be able to tell.
// A question of an agent that is not a worker is not recorded.
func (s *Swarm) Asking(agentID, what string) (answered func()) {
	m := s.get(agentID)
	if m == nil || m.manager || m.service {
		return func() {}
	}
	what = cleanText(what, 140)
	m.mu.Lock()
	m.asking++
	m.askWhat = what
	m.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			if m.asking > 0 {
				m.asking--
			}
			m.mu.Unlock()
		})
	}
}

// wake starts an idle worker (mail arrived for it). It reports whether it did.
func (s *Swarm) wake(m *member) bool {
	if m.manager {
		return false
	}
	if rs, ctx, ok := s.reserve(m); ok {
		s.launch(m, rs, ctx, runStart{})
		return true
	}
	return false
}

// wakeForMail is wake for a message from another worker: it counts against the bound
// on mail wakes of the worker's current task (Config.MaxMailWakes), so a conversation
// between agents cannot keep them running for ever. The manager's mail is the person's
// authority and the harness's own is the run's bookkeeping: neither counts.
func (s *Swarm) wakeForMail(m *member, from string) {
	if m.manager || m.service || s.cfg.MaxMailWakes <= 0 || !s.isPeer(from) {
		s.wake(m)
		return
	}
	s.wakeWithinBound(m)
}

// isPeer reports whether mail from the sender is another worker's: not the manager's and not
// the harness's.
func (s *Swarm) isPeer(from string) bool { return from != s.ManagerID() && from != harnessSender }

// wakeWithinBound wakes an idle worker for mail from other workers if the bound on such wakes
// for its task is not reached; past it the mail waits in the inbox and the manager is told,
// once. A run that peer mail starts is counted here whether the mail found the worker idle or
// was left waiting by a run that ended (afterIdle), and the check, the wake and the count are
// one step: two senders at once, or a sender and a run's end, cannot both see room for the
// last wake.
func (s *Swarm) wakeWithinBound(m *member) {
	limit := s.cfg.MaxMailWakes
	m.wakeMu.Lock()
	m.mu.Lock()
	over := m.mailWakes >= limit
	m.mu.Unlock()
	if !over && s.wake(m) {
		m.mu.Lock()
		m.mailWakes++
		m.mu.Unlock()
	}
	m.wakeMu.Unlock()
	if over {
		s.noteMailWakeLimit(m, limit)
	}
}

// noteMailWakeLimit tells the manager, once per assignment, that a worker's mail no
// longer wakes it.
func (s *Swarm) noteMailWakeLimit(m *member, limit int) {
	m.mu.Lock()
	first := !m.wakeLimited
	m.wakeLimited = true
	task := m.task
	m.mu.Unlock()
	if !first {
		return
	}
	s.emitAs(m.id, events.TypeSwarmWakeLimit, map[string]any{"limit": limit, "task": task})
	s.notifyManager(fmt.Sprintf("%s was woken %d times by mail from other workers for %s and will not be again until it is given a new task; its mail waits in its inbox. If it is caught in a conversation, stop it or give it new work.",
		m.id, limit, safeToken(taskLabel(task), 24)))
}

// taskLabel substitutes a generic task description when no task label is available.
func taskLabel(s string) string {
	if s == "" {
		return "its task"
	}
	return s
}

func (s *Swarm) runMember(m *member, rs *runState, ctx context.Context, start runStart) {
	defer s.wg.Done()
	defer rs.cancel()
	var res *agent.Result
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				st := debug.Stack()
				err = &panicError{val: r, stack: st}
				s.emitAs(m.id, "agent.panic", map[string]any{"id": m.id, "panic": fmt.Sprint(r), "stack": string(st)})
			}
		}()
		// A task from the harness, not the person's word: see runStart.
		brief := start.brief
		if start.kickoff {
			brief = s.startedBrief(ctx, m, brief)
		}
		res, err = m.a.RunTask(ctx, brief, start.card)
	}()
	func() {
		defer func() {
			if r := recover(); r != nil {
				s.emitAs(m.id, "agent.panic", map[string]any{"id": m.id, "panic": fmt.Sprint(r), "stack": string(debug.Stack()), "in": "finishRun"})
				s.forceIdle(m, rs)
			}
		}()
		s.finishRun(m, rs, ctx, res, err)
	}()
}

// startedBrief gives the user's SubagentStart hooks (agent.WorkerHooks) their say
// about a new worker: what they print is added to the end of its first task message,
// under the same label as a UserPromptSubmit hook's, and never to a cached layer. It
// runs on the worker's own goroutine, so the manager's spawn call does not wait for
// the hook, and a failure of the hook machinery never costs the worker its task.
func (s *Swarm) startedBrief(ctx context.Context, m *member, brief string) string {
	wh, ok := s.deps.Hooks.(agent.WorkerHooks)
	if !ok || m.service {
		return brief
	}
	m.mu.Lock()
	task := m.task
	m.mu.Unlock()
	extra := ""
	func() {
		defer func() { _ = recover() }()
		extra = wh.WorkerStarted(ctx, m.id, m.role, task)
	}()
	if strings.TrimSpace(extra) == "" {
		return brief
	}
	return brief + "\n\n[context from your hooks]\n" + extra
}

// forceIdle is the last resort when finishRun itself failed: the member must not
// stay running forever. Cleanup from an older run cannot alter its successor.
func (s *Swarm) forceIdle(m *member, rs *runState) {
	m.mu.Lock()
	current := m.run == rs
	m.mu.Unlock()
	if !current {
		return
	}
	s.Leases.ReleaseAll(m.id)
	m.setState(s, "failed", "run ended abnormally")
	m.mu.Lock()
	if m.run == rs {
		m.run = nil
		m.life = lifeIdle
		m.idleAt = s.deps.Now()
	}
	m.mu.Unlock()
}

// workerWrapUp is what a worker is told to do with the steps it has left when its run is about to end at the step limit
// (agent.Config.WrapUp): a successor that gets the task with a summary loses nothing, one that finds it in todo starts over.
const workerWrapUp = "If your task is not finished, hand it over (task handover, text = what is done, what is left, what you learned): " +
	"a fresh worker of your role carries on from your work. If it is finished, mark it done."

// poolWrapUp is workerWrapUp for persistent pools, which keep their workers and refuse a handover (Handover): the worker's
// task comes back to it with its conversation, so what it needs is a status that says where the work stands.
const poolWrapUp = "If your task is not finished, record where it stands (task update, text = one line: what is done, what is left): " +
	"the task comes back to you with this conversation. If it is finished, mark it done."

// wrapUp is the wrap-up instruction for this swarm's workers.
func (s *Swarm) wrapUp() string {
	if s.cfg.PersistentPools {
		return poolWrapUp
	}
	return workerWrapUp
}

type stopKind int

const (
	stopClean       stopKind = iota
	stopInterrupted          // cancelled from outside (shutdown, the manager was cancelled)
	stopHarness              // the harness or manager stopped it for a stated reason
	stopFailed               // crashed, errored, ran out of steps or budget
)

// classifyStop says how a run ended.
func classifyStop(err error, reason string, reasonCounts bool, ctx context.Context) (kind stopKind, why string, count bool) {
	switch {
	case err == nil:
		return stopClean, "", false
	case reason != "":
		return stopHarness, reason, reasonCounts
	}
	var pe *panicError
	switch {
	case errors.As(err, &pe):
		return stopFailed, "crashed: " + cleanText(fmt.Sprint(pe.val), 80), true
	case errors.Is(err, errSwarmBudget):
		return stopHarness, "swarm budget exhausted", false
	case errors.Is(err, agent.ErrBudget):
		return stopFailed, "budget limit reached", true
	case errors.Is(err, context.Canceled), ctx.Err() != nil:
		return stopInterrupted, "interrupted", false
	case strings.Contains(err.Error(), "step limit"):
		return stopFailed, "step limit reached", true
	}
	return stopFailed, "stopped with an error: " + cleanText(err.Error(), 100), true
}

// finishRun settles a finished run. The harness closes the loop even if the model
// did not: a task the worker left in doing is verified (evidence and the configured
// verifier) and moves to review, or goes back to work with the failure, or, if the
// worker stopped abnormally, returns to todo for someone else (failed after
// MaxAttempts). Leases are released and the manager hears of a stop in one line.
func (s *Swarm) finishRun(m *member, rs *runState, ctx context.Context, res *agent.Result, err error) {
	if m.service {
		s.finishService(m, rs, ctx, err)
		return
	}
	m.mu.Lock()
	if m.run != rs || m.life != lifeRunning {
		m.mu.Unlock()
		return // the watchdog already settled this run
	}
	tasks := make(map[string]uint64, len(rs.tasks))
	for id, rev := range rs.tasks {
		tasks[id] = rev
	}
	reason, reasonCounts := rs.reason, rs.count
	m.mu.Unlock()

	kind, why, count := classifyStop(err, reason, reasonCounts, ctx)
	failed := kind == stopFailed || (kind == stopHarness && count)
	var pe *provider.Error
	outage := kind == stopFailed && errors.As(err, &pe) && pe.Outage() != provider.NotOutage
	var line string
	var again *pendingRestart // the automatic restart of the worker (restart.go), nil when there is none
	tried := 0                // the automatic restarts already made, when the bound stops one more
	switch kind {
	case stopClean:
		line = s.settleClean(ctx, m, tasks, res)
		s.resumeReturnedWork(ctx, m, rs, tasks)
		m.mu.Lock()
		m.restarts = 0 // a run that ended on its own is a recovery: the next failure starts a new row
		m.mu.Unlock()
	default:
		again, tried, line = s.settleFailed(m, rs, tasks, kind, why, count, err)
	}
	s.Leases.ReleaseAll(m.id)
	s.Board.ClearAlertKey("stuck", stuckAlertKey(m.id, rs.id))
	if s.deps.RunEnded != nil && !m.manager { // what the manager starts may be for the person: it ends with the session
		s.deps.RunEnded(m.id)
	}

	state, stateLine := "idle", ""
	if failed {
		state, stateLine = "failed", why
	}
	snap := s.Board.Snapshot()
	m.mu.Lock()
	if m.run != rs { // abandoned while the gate ran
		m.mu.Unlock()
		return
	}
	m.task = currentTask(snap, m.id)
	m.mu.Unlock()
	// Keep the run reserved until its terminal events are published. Otherwise
	// mail or reassignment can start a new run whose status is overwritten by
	// this run's late agent.end event.
	if again != nil {
		// The run failed and the worker is restarted: the end of the run says how it ended, and the state that follows it says
		// what comes next, so neither interface shows a worker that the harness is restarting as one that is stuck.
		s.emitAs(m.id, events.TypeAgentEnd, map[string]any{"id": m.id, "state": state, "evidence": m.ev.Summary()})
		m.setState(s, "idle", "restarting: "+why)
	} else {
		m.setState(s, state, stateLine)
		s.emitAs(m.id, events.TypeAgentEnd, map[string]any{"id": m.id, "state": state, "evidence": m.ev.Summary()})
	}
	m.mu.Lock()
	if m.run != rs {
		m.mu.Unlock()
		return
	}
	m.run = nil
	m.life = lifeIdle
	m.idleAt = s.deps.Now()
	m.outage = time.Time{}
	if outage {
		m.outage = m.idleAt
	}
	// A new message is a recovery trigger even if it arrived just before the
	// failed run returned. Mail already present at reserve must not repeatedly
	// restart a run that fails before it can drain its inbox (for example budget).
	// The end of this run also settles an automatic restart scheduled after an earlier
	// one: this run's outcome is what decides whether one waits now.
	restart := kind == stopClean || (kind == stopFailed && m.mailSeq > rs.mailSeq)
	retire := m.retireOnIdle
	if retire {
		restart = false
	}
	// The restart's event is made before the restart is published: once m.restart holds it, the supervisor's restartTick may take
	// it and start it while this run still settles.
	var scheduled map[string]any
	if again != nil {
		scheduled = again.event("scheduled")
	}
	m.restart = again
	m.mu.Unlock()
	if again != nil {
		s.emitAs(m.id, eventRestart, scheduled)
		stopped := s.alive() != nil
		m.mu.Lock()
		again.logged = true
		own := stopped && m.restart == again
		if own {
			m.restart = nil
		}
		m.mu.Unlock()
		if own {
			// The swarm stopped while the run was settled, after Shutdown called off the restarts it found: this one would never
			// start, so it is called off here.
			s.dropRestart(m, again, "the swarm stopped")
		}
	}
	if line == "" && failed {
		line = fmt.Sprintf("%s stopped (%s). Check task list for its current assignments; send recovery instructions or reassign unfinished work with spawn.", m.id, why)
	}
	if tried > 0 && failed {
		line = strings.TrimSuffix(line, ".") + fmt.Sprintf(". The harness restarted %s automatically %s on this work, and does not restart it again.", m.id, timesText(tried))
		s.emitAs(m.id, eventRestart, map[string]any{"phase": "exhausted", "n": tried, "max": maxAutoRestarts, "why": why})
	}
	// Publish the failure after making the worker available, so a manager's
	// immediate recovery action can reserve it.
	if line != "" {
		s.notifyManager(line)
	}
	s.afterIdle(m, restart)
	if retire {
		_ = s.Retire(m.id) // unread mail keeps it; idle retirement takes it later
	}
	s.managerEvent() // a worker finished, failed or stopped: an idle manager may want to know
}

// currentTask picks the assignment an idle agent's status shows: what it still
// holds (doing, then blocked, then review).
func currentTask(snap *Snapshot, agentID string) string {
	best, rank := "", 9
	for _, t := range snap.Tasks {
		if t.Owner != agentID {
			continue
		}
		r := map[TaskStatus]int{StatusDoing: 0, StatusBlocked: 1, StatusReview: 2}[t.Status]
		if t.Status == StatusDoing || t.Status == StatusBlocked || t.Status == StatusReview {
			if r < rank {
				best, rank = t.ID, r
			}
		}
	}
	return best
}

// askForReport asks a read-only worker that stopped on t with no answer text for its report, at most maxGateTries times, and
// reports whether it asked. A read-only task's deliverable is its report: submitted empty, it reaches the manager with nothing
// to decide on. A reviewer whose last turn was reasoning alone stopped so, and its manager spent four minutes recalling the
// reviewer's history for the verdict before it mailed for it.
func (s *Swarm) askForReport(m *member, t Task) bool {
	m.mu.Lock()
	m.gateTries++
	tries := m.gateTries
	m.mu.Unlock()
	if tries > maxGateTries {
		return false
	}
	s.notify(m.id, "request", "You stopped on "+t.ID+" without a report: your last answer had no text. Post your findings and verdict with task done (text=...); the manager decides from that text alone.")
	return true
}

// settleClean gates the tasks a worker left in doing after a normal stop. It
// returns a line for the manager, if there is anything to tell.
func (s *Swarm) settleClean(ctx context.Context, m *member, tasks map[string]uint64, res *agent.Result) string {
	var notes []string
	for _, id := range sortedKeys(tasks) {
		rev := tasks[id]
		t, ok := s.Board.Snapshot().Task(id)
		if !ok || t.Owner != m.id || t.Status != StatusDoing || t.Rev != rev {
			continue
		}
		if t.Pool != nil {
			continue
		}
		if t.Kind == TaskKindPlan {
			if ctx.Err() != nil {
				s.Board.Requeue(m.id, id, rev, "interrupted", false, s.cfg.MaxAttempts)
				continue
			}
			if note := s.settleUnsubmittedPlan(m, t); note != "" {
				notes = append(notes, note)
			}
			continue
		}
		summary := ""
		if res != nil {
			summary = strings.TrimSpace(res.Text)
		}
		if summary == "" && m.readOnly && ctx.Err() == nil && s.askForReport(m, t) {
			continue
		}
		vr, gdir := s.gateFor(ctx, t, m, m.dir)
		vcmd := ExpandVerify(s.cfg.VerifyCmd, gdir, t.Files)
		if ctx.Err() != nil { // the swarm is stopping: nothing failed
			s.Board.Requeue(m.id, id, rev, "interrupted", false, s.cfg.MaxAttempts)
			continue
		}
		evid := m.ev.SummaryFor(id)
		if vr.ok && m.tree != nil {
			if s.checkTaskGate(t) != nil {
				s.retryChangedScope(m, t)
				continue
			}
			// Isolated run: the harness commits the tree and merges it on the worker's
			// behalf, exactly as for a worker that calls done.
			out := s.integrate(ctx, m, t, taskCommitMessage(t, summary))
			if s.checkTaskGate(t) != nil {
				s.retryChangedScope(m, t)
				continue
			}
			switch {
			case out.interrupted || ctx.Err() != nil:
				s.Board.Requeue(m.id, id, rev, "interrupted", false, s.cfg.MaxAttempts)
				continue
			case out.infra != nil:
				if s.submitSettledTask(m, t, summary, "NOT MERGED (the merge could not run: "+cleanText(out.infra.Error(), 100)+"); "+evid) {
					notes = append(notes, fmt.Sprintf("%s reached review but could not be merged (%s)", id, cleanText(out.infra.Error(), 80)))
				}
				continue
			case out.bounce != "":
				m.mu.Lock()
				m.gateTries++
				tries := m.gateTries
				m.mu.Unlock()
				if tries <= maxGateTries {
					s.notify(m.id, "request", "You stopped without finishing "+id+". "+out.bounce)
					continue
				}
				if nt, applied := s.Board.Requeue(m.id, id, rev, fmt.Sprintf("merge failed %d times", tries), true, s.cfg.MaxAttempts, taskGateCheck(t)); applied {
					notes = append(notes, s.requeueLine(m.id, nt, "its work kept failing to merge"))
				} else {
					s.retryChangedScope(m, t)
				}
				continue
			}
			evid = out.evidence + "; " + evid
		}
		switch {
		case vr.ok:
			s.submitSettledTask(m, t, summary, evid)
		case vr.infra:
			if s.submitSettledTask(m, t, summary, evid+"; verification could not run: "+cleanText(vr.err.Error(), 100)) {
				notes = append(notes, fmt.Sprintf("%s reached review but the verifier could not run (%s)", id, cleanText(vr.err.Error(), 80)))
			}
		default:
			next, applied := s.recordVerificationFailure(t, vcmd, vr)
			if !applied {
				s.retryChangedScope(m, t)
				continue
			}
			if next.Status == StatusDoing {
				s.notify(m.id, "request", fmt.Sprintf("Not done: you stopped without finishing %s and verification `%s` failed (exit %d). Fix the failures, then call task done.\n%s",
					id, cleanText(vcmd, 80), vr.code, tailText(vr.out, 1500)))
				continue
			}
			notes = append(notes, s.verificationFailureNotice(m.id, next, vcmd, vr))
		}
	}
	return strings.Join(notes, "; ")
}

// resumeReturnedWork keeps an assignment that changed under a clean run from being
// left with an idle owner. A run settles only the assignments it started with
// (runState), and a worker's run does not reopen for mail (NoMailReopen) so that the
// run that owns a reject is the one that reads it. But the agent hands waiting mail
// to the model with each turn's tool results: a task sent back after the worker's
// done call submitted it, while that turn's calls finish, is read by the run that is
// ending. If the worker then stops without calling done again, settleClean skips the
// task and no mail is left to start the next run. The harness then writes the worker
// one message, so the next run starts, adopts the task at its current revision, and
// settles it. It is called while rs is still reserved, so afterIdle starts that run.
func (s *Swarm) resumeReturnedWork(ctx context.Context, m *member, rs *runState, settled map[string]uint64) {
	if ctx.Err() != nil {
		return
	}
	m.mu.Lock()
	skip := m.run != rs || m.a.PendingInbox() > 0 || !m.box.empty()
	m.mu.Unlock()
	if skip {
		return // abandoned by the watchdog, or the waiting mail starts the next run
	}
	var ids []string
	for _, t := range s.Board.Snapshot().Tasks {
		if rev, ok := settled[t.ID]; t.Owner == m.id && t.Status == StatusDoing && (!ok || rev != t.Rev) {
			ids = append(ids, t.ID)
		}
	}
	if len(ids) == 0 {
		return
	}
	what := ids[0] + " is"
	if len(ids) > 1 {
		what = strings.Join(ids, ", ") + " are"
	}
	s.notify(m.id, "request", fmt.Sprintf("%s still yours and not done: returned to you while your last run was ending (task get shows why). Finish, then call task done again.", what))
}

// settleStopped returns the tasks of a run that stopped abnormally to the pool. A pool
// leader's stop is the manager's to resume, and is worth a line only when the manager
// could not have caused it, as for any other stop: not when the leader's run was
// cancelled from outside (the manager's revise of the pool, a shutdown) or stopped by
// the harness for a stated reason that is not a failure. exhausted says the worker's automatic restarts on this work ran out:
// a task it returns is then the manager's to assign, not the repair's to start on the same worker again.
func (s *Swarm) settleStopped(m *member, tasks map[string]uint64, kind stopKind, why string, count, exhausted bool) string {
	var notes []string
	quiet := kind == stopInterrupted || (kind == stopHarness && !count)
	for _, id := range sortedKeys(tasks) {
		if task, ok := s.Board.Snapshot().Task(id); ok && task.Pool != nil {
			if !quiet {
				line := "Pool leader stopped on " + id + ": " + why + ". Resume this pool with its existing members."
				if exhausted && len(task.Pool.Members) > 1 {
					// The harness does not restart the leader on it again: a leader whose model keeps failing holds the pool until the
					// manager excuses it, and the next member leads.
					line = fmt.Sprintf("Pool leader stopped on %s: %s. The harness does not restart %s on it again: resume this pool with its existing members, or, if the failure is %s's own (its model or provider), excuse it (pool action=excuse id=%s member=%s text=<why>) and the next member leads.", id, why, m.id, m.id, id, m.id)
				}
				s.notify(s.ManagerID(), "request", line)
			}
			continue
		}
		nt, applied := s.Board.Requeue(m.id, id, tasks[id], why, count, s.cfg.MaxAttempts)
		switch {
		case !applied:
		case exhausted && s.cfg.PersistentPools && nt.Status == StatusTodo:
			s.mu.Lock()
			if s.handedBack == nil {
				s.handedBack = map[string]uint64{}
			}
			s.handedBack[nt.ID] = nt.Rev
			s.mu.Unlock()
			notes = append(notes, fmt.Sprintf("%s returned to todo after attempt %d: %s stopped (%s); assign it with spawn, to another worker if the failure is %s's own (its model or provider)", nt.ID, nt.Attempts, m.id, why, m.id))
		default:
			notes = append(notes, s.requeueLine(m.id, nt, why))
		}
	}
	// Only failures the manager could not have caused are worth a line.
	if kind == stopInterrupted || (kind == stopHarness && !count) {
		return ""
	}
	return strings.Join(notes, "; ")
}

// requeueLine explains a task's terminal failure or return to todo with attempt counts and the
// worker's stop reason.
func (s *Swarm) requeueLine(agentID string, t Task, why string) string {
	if t.Status == StatusFailed {
		return fmt.Sprintf("%s failed after %d attempts: %s stopped (%s)", t.ID, t.Attempts, agentID, why)
	}
	if s.cfg.PersistentPools {
		return fmt.Sprintf("%s is retrying with its existing worker after attempt %d: %s", t.ID, t.Attempts, why)
	}
	return fmt.Sprintf("%s returned to todo: %s stopped (%s), attempt %d of %d", t.ID, agentID, why, t.Attempts, s.cfg.MaxAttempts)
}

func sortedKeys(m map[string]uint64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// task ids are T<number>: order by length then text
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && (len(out[j]) < len(out[j-1]) || (len(out[j]) == len(out[j-1]) && out[j] < out[j-1])); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// tailText keeps a verifier's final output within max runes and 40 lines, plus
// an omission marker. It defuses harness markers and control characters.
func tailText(s string, max int) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) > max {
		s = "…" + string(r[len(r)-max:])
	}
	lines := strings.Split(s, "\n")
	if len(lines) > 40 {
		// cleanBlock retains the first 40 lines. Choose the final 39 here,
		// leaving one line for the omission marker so it cannot drop the end.
		lines = append([]string{"…"}, lines[len(lines)-39:]...)
	}
	return cleanBlock(strings.Join(lines, "\n"), max+2)
}

// maxAutoRuns bounds how many runs in a row the harness starts on its own for one
// worker because mail is still waiting after a run; a new message starts the count
// again.
const maxAutoRuns = 3

// afterIdle runs when a worker becomes idle: mail that arrived while it was
// finishing (or feedback the harness just queued for it) starts another run
// instead of waiting for the next message. A failed run only restarts for mail
// received since its reservation; existing unread mail cannot create a retry loop.
// Mail that came only from other workers is bound by Config.MaxMailWakes like mail that
// finds the worker idle (wakeWithinBound); the manager's and the harness's is not.
func (s *Swarm) afterIdle(m *member, restart bool) {
	if m.manager {
		return
	}
	m.pump(s)
	if !restart {
		return
	}
	m.mu.Lock()
	if m.a.PendingInbox() == 0 {
		m.autoRuns = 0
		m.mu.Unlock()
		return
	}
	m.autoRuns++
	n := m.autoRuns
	peerOnly := m.onlyPeerMailUnread()
	m.mu.Unlock()
	switch {
	case n > maxAutoRuns:
	case peerOnly && !m.service && s.cfg.MaxMailWakes > 0:
		// Mail from other workers that arrived as the run ended starts a run like mail that
		// finds the worker idle, and no more of them than the task's bound allows.
		s.wakeWithinBound(m)
	default:
		s.wake(m)
	}
}

// stopRun asks a member's run to stop, giving the reason that finishRun will use.
func (s *Swarm) stopRun(m *member, reason string, count bool) bool {
	return s.stopRunFor(m, reason, count, "", 0)
}

// stopRunFor cancels only a run that still tracks the given assignment. An empty
// task ID matches any run; stale verifier results cannot cancel a newer task.
func (s *Swarm) stopRunFor(m *member, reason string, count bool, taskID string, rev uint64) bool {
	m.mu.Lock()
	rs := m.run
	if rs == nil || m.life != lifeRunning {
		m.mu.Unlock()
		return false
	}
	if taskID != "" {
		assignment, ok := rs.tasks[taskID]
		if !ok || assignment != rev {
			m.mu.Unlock()
			return false
		}
	}
	if rs.reason == "" {
		rs.reason, rs.count = reason, count
	}
	m.mu.Unlock()
	rs.cancel()
	return true
}

// stopWorkers stops every running worker, and calls off the automatic restarts that are waiting (dropRestart): a worker that
// was to be restarted is stopped as well.
func (s *Swarm) stopWorkers(reason string, count bool) int {
	s.mu.Lock()
	ms := make([]*member, 0, len(s.members))
	for _, m := range s.members {
		if !m.manager {
			ms = append(ms, m)
		}
	}
	s.mu.Unlock()
	n := 0
	for _, m := range ms {
		if s.stopRun(m, reason, count) {
			n++
		}
	}
	s.dropRestarts(reason)
	return n
}

// isActive reports whether the member is running a task now.
func (m *member) isActive() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.life == lifeRunning
}

// planOpen is the agent's view of the plan store: how many steps of an agent's plan are open (nil without a store).
func planOpen(p *plan.Store) func(string) int {
	if p == nil {
		return nil
	}
	return p.Open
}
