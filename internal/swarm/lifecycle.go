package swarm

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/reee344/sleipnir/internal/agent"
	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/kv"
	"github.com/reee344/sleipnir/internal/perm"
	"github.com/reee344/sleipnir/internal/provider"
	"github.com/reee344/sleipnir/internal/tools"
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

// maxGateTries is how many times a worker that stopped without finishing is sent
// back to work with the verifier's output before its task is requeued.
const maxGateTries = 2

// runState is one run of a member's agent. Its tasks are the assignments (id and
// rev) it owns: at the end of the run only those are settled, and only if the
// board still shows the same rev, so a run that ends late cannot undo a newer
// assignment (a reject, a reuse).
type runState struct {
	id      uint64
	tasks   map[string]uint64
	cancel  context.CancelFunc
	started time.Time
	reason  string // why the harness stopped the run ("" = it was not asked to stop)
	count   bool   // whether that stop counts as a failed attempt at the task
	abortAt time.Time
}

// member is one registered agent.
type member struct {
	id, role string
	a        *agent.Agent
	ev       *Evidence
	manager  bool
	readOnly bool
	// notify is signalled (never blocking) whenever mail is queued for the member.
	notify chan struct{}

	mu        sync.Mutex
	life      life
	task      string // the assignment shown in its status
	run       *runState
	runSeq    uint64
	state     string
	line      string
	lastPush  time.Time
	dirty     bool
	flushT    *time.Timer
	idleAt    time.Time
	gateTries int
	stuckWarn bool
	autoRuns  int
	box       overflow
	emitted   string

	// progress is the unix time of the last sign of life (a model or tool event).
	progress atomic.Int64
	// pubMu serialises status publication: whoever publishes last reads the
	// newest state, so the board never keeps a stale one.
	pubMu sync.Mutex
}

func (m *member) touch(s *Swarm) { m.progress.Store(s.deps.Now().UnixNano()) }

// panicError is what a run that panicked returns to finishRun.
type panicError struct {
	val   any
	stack []byte
}

func (p *panicError) Error() string { return fmt.Sprintf("panic: %v", p.val) }

// emit writes a swarm-level event. An emitter that panics or fails is contained.
func (s *Swarm) emit(typ string, data any) {
	defer func() { _ = recover() }()
	_, _ = s.deps.Events.Emit("swarm", typ, data)
}

func (s *Swarm) emitAs(agentID, typ string, data any) {
	defer func() { _ = recover() }()
	_, _ = s.deps.Events.Emit(agentID, typ, data)
}

// nextHarnessID numbers the harness's own mail.
func (s *Swarm) nextHarnessID() string { return fmt.Sprintf("h%d", s.hseq.Add(1)) }

// newMember builds an agent and its member record; it registers nothing.
func (s *Swarm) newMember(id string, r Role, notes *kv.Layer, ev *Evidence) (*member, error) {
	d := s.deps
	isMgr := r.Name == "manager"
	requester := perm.Requester(d.Perm)
	if requester == nil {
		requester = perm.DenyAll{} // fail closed: see perm.DenyAll
	}
	requester = roleRequester{inner: requester, role: r}
	sink := agent.Sink(agent.NopSink{})
	if d.NewSink != nil {
		sink = d.NewSink(id)
	}
	m := &member{id: id, role: r.Name, ev: ev, state: "idle", manager: isMgr, readOnly: r.ReadOnly, notify: make(chan struct{}, 1)}
	m.box.init()
	model, prov := d.Model, d.Provider
	if rm, ok := d.RoleModels[r.Name]; ok && rm.Provider != nil {
		model, prov = rm.Model, rm.Provider
	}
	cfg := agent.Config{
		ID: id, Role: r.Name, Model: model, Provider: prov, Tools: d.Registry, ToolSpecs: d.ToolSpecs,
		CaptureTokens: d.CaptureTokens,
		Const:         d.Const, Shared: s.currentShared(), RoleL: s.roleLay[r.Name], Notes: notes,
		Params: d.Params,
		Hot: func(agentID string) []core.Block {
			snap := s.Board.Snapshot()
			txt := RenderHot(snap, agentID, r.Name, isMgr, s.cfg.Hot, d.Est)
			return []core.Block{core.Text(txt)}
		},
		Events: d.Events, Blobs: d.Blobs, Archive: d.Archive, Files: d.Files, Guard: guardWithAfter{s.Leases, d.OnWrite}, Snap: d.Snap,
		Handles: d.Handles, Perm: requester, Limiter: s.Gov, Gate: s.Gate,
		Sink:    &memberSink{Sink: sink, s: s, m: m, ev: ev},
		Workdir: d.Workdir, Root: d.Root, Limits: d.Limits,
		Planner: d.Planner, SessionID: s.cfg.SessionID, AffinityShards: s.cfg.AffinityShards,
		OnPromote: s.onPromote, Est: d.Est, Now: d.Now, MaxSteps: r.MaxSteps, Priority: r.Priority,
		BudgetUSD: s.cfg.AgentBudgetUSD, Hooks: d.Hooks, NoMailReopen: !isMgr, // a worker's mail is read by its next run (afterIdle), which owns what the mail changes
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

func (s *Swarm) get(id string) *member {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.members[id]
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
func (m *member) setState(s *Swarm, state, line string) {
	line = cleanText(line, 70)
	m.mu.Lock()
	if m.life == lifeRetired || (m.state == state && m.line == line) {
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
	s.Board.SetAgent(info)
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

func (k *memberSink) safely(fn func()) {
	defer func() {
		if r := recover(); r != nil {
			k.s.emit("sink.panic", map[string]any{"agent": k.m.id, "panic": fmt.Sprint(r)})
		}
	}()
	fn()
}

func (k *memberSink) Text(a, d string) {
	k.m.touch(k.s)
	k.safely(func() { k.Sink.Text(a, d) })
}

func (k *memberSink) Thinking(a, d string) {
	k.m.touch(k.s)
	k.safely(func() { k.Sink.Thinking(a, d) })
}

func (k *memberSink) Response(a string, r *provider.Response, hit float64) {
	k.m.touch(k.s)
	k.safely(func() { k.Sink.Response(a, r, hit) })
}

func (k *memberSink) Notice(a, level, msg string) {
	k.safely(func() { k.Sink.Notice(a, level, msg) })
}

func (k *memberSink) ToolStart(a string, call core.Block) {
	k.m.touch(k.s)
	k.safely(func() { k.m.setState(k.s, "running", activity(call)) })
	k.safely(func() { k.Sink.ToolStart(a, call) })
}

func (k *memberSink) ToolEnd(a string, call core.Block, res *tools.Result, took time.Duration) {
	k.m.touch(k.s)
	k.safely(func() { k.ev.Observe(call, res, k.s.deps.Now()) })
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
	rs := &runState{id: m.runSeq, tasks: map[string]uint64{}, cancel: cancel, started: s.deps.Now()}
	m.run = rs
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

// adopt records the tasks the agent is doing now as the run's assignments.
func (s *Swarm) adopt(m *member, rs *runState) {
	snap := s.Board.Snapshot()
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range snap.Tasks {
		if t.Owner == m.id && t.Status == StatusDoing {
			rs.tasks[t.ID] = t.Rev
			if m.task == "" || m.task != t.ID {
				m.task = t.ID
			}
		}
	}
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
	}
	m.task = taskID
	m.gateTries = 0
	m.mu.Unlock()
}

// launch starts the run goroutine of a reserved member.
func (s *Swarm) launch(m *member, rs *runState, ctx context.Context, input string) {
	s.adopt(m, rs)
	m.setState(s, "running", "starting")
	go s.runMember(m, rs, ctx, input)
}

// wake starts an idle worker (mail arrived for it).
func (s *Swarm) wake(m *member) {
	if m.manager {
		return
	}
	if rs, ctx, ok := s.reserve(m); ok {
		s.launch(m, rs, ctx, "")
	}
}

func (s *Swarm) runMember(m *member, rs *runState, ctx context.Context, input string) {
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
		res, err = m.a.Run(ctx, input)
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

// forceIdle is the last resort when finishRun itself failed: the member must not
// stay running forever.
func (s *Swarm) forceIdle(m *member, rs *runState) {
	s.Leases.ReleaseAll(m.id)
	m.mu.Lock()
	if m.run == rs {
		m.run = nil
		m.life = lifeIdle
		m.idleAt = s.deps.Now()
	}
	m.mu.Unlock()
	m.setState(s, "failed", "run ended abnormally")
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
	var line string
	switch kind {
	case stopClean:
		line = s.settleClean(ctx, m, tasks, res)
	default:
		line = s.settleStopped(m, tasks, kind, why, count)
	}
	if line != "" {
		s.notifyManager(line)
	}
	s.Leases.ReleaseAll(m.id)
	s.Board.ClearAlertKey("stuck", "stuck:"+m.id)

	state, stateLine := "idle", ""
	if kind == stopFailed || (kind == stopHarness && count) {
		state, stateLine = "failed", why
	}
	snap := s.Board.Snapshot()
	m.mu.Lock()
	if m.run != rs { // abandoned while the gate ran
		m.mu.Unlock()
		return
	}
	m.run = nil
	m.life = lifeIdle
	m.idleAt = s.deps.Now()
	m.task = currentTask(snap, m.id)
	m.mu.Unlock()
	m.setState(s, state, stateLine)
	s.emitAs(m.id, events.TypeAgentEnd, map[string]any{"id": m.id, "state": state, "evidence": m.ev.Summary()})
	s.afterIdle(m, kind == stopClean)
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
		vr := s.verify(ctx, s.deps.Workdir)
		if ctx.Err() != nil { // the swarm is stopping: nothing failed
			s.Board.Requeue(m.id, id, rev, "interrupted", false, s.cfg.MaxAttempts)
			continue
		}
		summary := ""
		if res != nil {
			summary = strings.TrimSpace(res.Text)
		}
		evid := m.ev.Summary()
		switch {
		case vr.ok:
			_ = s.Board.SubmitAt(m.id, id, rev, summary, evid)
		case vr.infra:
			_ = s.Board.SubmitAt(m.id, id, rev, summary, evid+"; verification could not run: "+cleanText(vr.err.Error(), 100))
			notes = append(notes, fmt.Sprintf("%s reached review but the verifier could not run (%s)", id, cleanText(vr.err.Error(), 80)))
		default:
			m.mu.Lock()
			m.gateTries++
			tries := m.gateTries
			m.mu.Unlock()
			if tries <= maxGateTries {
				s.notify(m.id, "request", fmt.Sprintf("Not done: you stopped without finishing %s and verification `%s` failed (exit %d). Fix the failures, then call task done.\n%s",
					id, cleanText(s.cfg.VerifyCmd, 80), vr.code, tailText(vr.out, 1500)))
				continue
			}
			if nt, applied := s.Board.Requeue(m.id, id, rev, fmt.Sprintf("verification failed %d times", tries), true, s.cfg.MaxAttempts); applied {
				notes = append(notes, s.requeueLine(m.id, nt, fmt.Sprintf("verification `%s` kept failing", cleanText(s.cfg.VerifyCmd, 60))))
			}
		}
	}
	return strings.Join(notes, "; ")
}

// settleStopped returns the tasks of a run that stopped abnormally to the pool.
func (s *Swarm) settleStopped(m *member, tasks map[string]uint64, kind stopKind, why string, count bool) string {
	var notes []string
	for _, id := range sortedKeys(tasks) {
		nt, applied := s.Board.Requeue(m.id, id, tasks[id], why, count, s.cfg.MaxAttempts)
		if applied {
			notes = append(notes, s.requeueLine(m.id, nt, why))
		}
	}
	// Only failures the manager could not have caused are worth a line.
	if kind == stopInterrupted || (kind == stopHarness && !count) {
		return ""
	}
	return strings.Join(notes, "; ")
}

func (s *Swarm) requeueLine(agentID string, t Task, why string) string {
	if t.Status == StatusFailed {
		return fmt.Sprintf("%s failed after %d attempts: %s stopped (%s)", t.ID, t.Attempts, agentID, why)
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

// tailText keeps the last max runes of a verifier's output, defused for display.
func tailText(s string, max int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) > max {
		s = "…" + string(r[len(r)-max:])
	}
	return cleanBlock(s, max+2)
}

// maxAutoRuns bounds how many runs in a row the harness starts on its own for one
// worker because mail is still waiting after a run; a new message starts the count
// again.
const maxAutoRuns = 3

// afterIdle runs when a worker becomes idle: mail that arrived while it was
// finishing (or feedback the harness just queued for it) starts another run
// instead of waiting for the next message. A run that ended abnormally does not
// restart by itself (it would fail again at once); the mail waits for the next trigger.
func (s *Swarm) afterIdle(m *member, restart bool) {
	if m.manager {
		return
	}
	m.pump(s)
	if !restart {
		return
	}
	if m.a.PendingInbox() == 0 {
		m.mu.Lock()
		m.autoRuns = 0
		m.mu.Unlock()
		return
	}
	m.mu.Lock()
	m.autoRuns++
	n := m.autoRuns
	m.mu.Unlock()
	if n <= maxAutoRuns {
		s.wake(m)
	}
}

// stopRun asks a member's run to stop, giving the reason that finishRun will use.
func (s *Swarm) stopRun(m *member, reason string, count bool) bool {
	m.mu.Lock()
	rs := m.run
	if rs == nil || m.life != lifeRunning {
		m.mu.Unlock()
		return false
	}
	if rs.reason == "" {
		rs.reason, rs.count = reason, count
	}
	m.mu.Unlock()
	rs.cancel()
	return true
}

// stopWorkers stops every running worker.
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
	return n
}

// isActive reports whether the member is running a task now.
func (m *member) isActive() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.life == lifeRunning
}
