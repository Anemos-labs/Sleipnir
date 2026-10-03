// Package swarm coordinates many agents over one repository.
//
// Its shared state is deliberately tiny and always fresh: a task board, one-line
// agent statuses, a buffer of proposed shared notes and a few alerts. Every
// agent sees a personalised, budget-capped rendering of it at the tail of its
// prompt (the hot layer), which is what lets a manager dispatch dozens of
// workers without writing each a briefing: they already share the cached
// project context, and the board tells them what everyone else is doing.
//
// The harness, not the models, owns the state: models propose (create a task,
// finish it, mail a teammate) and deterministic code validates, applies and
// records. docs/SWARM-PROTOCOL.md is the normative description.
package swarm

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
)

// TaskStatus is a task's lifecycle state.
type TaskStatus string

const (
	StatusTodo    TaskStatus = "todo"
	StatusDoing   TaskStatus = "doing"
	StatusBlocked TaskStatus = "blocked"
	StatusReview  TaskStatus = "review"
	StatusDone    TaskStatus = "done"
	StatusFailed  TaskStatus = "failed"
)

// Limits on what one agent can put on the board. Every agent's prompt carries a
// rendering of the board, so what one agent can add is what every agent pays for.
const (
	maxTitleRunes  = 160
	maxDescRunes   = 2000
	maxRoleRunes   = 32
	maxTaskDeps    = 16
	maxLineRunes   = 140
	maxResultRunes = 160
	maxEvidRunes   = 320
	maxNoteRunes   = 300
	maxAlertRunes  = 160
	maxPendingEvts = 4096
)

// Task is one unit of work.
type Task struct {
	ID     string     `json:"id"`
	Title  string     `json:"title"`
	Desc   string     `json:"desc,omitempty"`
	Status TaskStatus `json:"status"`
	Owner  string     `json:"owner,omitempty"`
	Role   string     `json:"role,omitempty"` // suggested role
	Deps   []string   `json:"deps,omitempty"`
	Line   string     `json:"line,omitempty"`   // latest one-line progress
	Result string     `json:"result,omitempty"` // what the worker said when it finished
	Files  []string   `json:"files,omitempty"`  // the scope: paths the task may touch
	// Evidence is what the harness observed (edited files, the last test command
	// and its exit status). Only the harness writes it.
	Evidence string `json:"evidence,omitempty"`
	// Attempts counts assignments that ended without the work reaching review.
	Attempts int `json:"attempts,omitempty"`
	// VerificationFailures counts failed verification gates in the current attempt.
	// It survives interruption and resets after a successful submission or a counted attempt.
	VerificationFailures int `json:"verification_failures,omitempty"`
	// Rev identifies the current assignment: it changes whenever the task is
	// (re)assigned, sent back or requeued, so a run that started under an earlier
	// assignment can tell that it no longer owns the task.
	Rev uint64 `json:"rev,omitempty"`
}

// AgentInfo is an agent's public status.
type AgentInfo struct {
	ID        string  `json:"id"`
	Role      string  `json:"role"`
	State     string  `json:"state"` // idle | running | waiting | done | failed
	Task      string  `json:"task,omitempty"`
	Line      string  `json:"line,omitempty"`
	CtxTokens int     `json:"ctx_tokens,omitempty"`
	CostUSD   float64 `json:"cost_usd,omitempty"`
}

// Note is a fact proposed for the shared context.
type Note struct {
	ID    int    `json:"id"`
	From  string `json:"from"`
	Scope string `json:"scope"` // "shared" | "role"
	Role  string `json:"role,omitempty"`
	Text  string `json:"text"`
}

// Alert is a short-lived warning (a lease conflict, a stalled agent). It expires.
type Alert struct {
	Kind string    `json:"kind"`
	Text string    `json:"text"`
	Key  string    `json:"key,omitempty"`
	At   time.Time `json:"at,omitempty"`
}

// Snapshot is an immutable view of the board. Readers never lock. Nothing that is
// reachable from a Snapshot may be modified.
type Snapshot struct {
	Version uint64
	Tasks   []Task
	Agents  []AgentInfo
	Notes   []Note
	Alerts  []Alert
}

// Task returns a task by id.
func (s *Snapshot) Task(id string) (Task, bool) {
	for _, t := range s.Tasks {
		if t.ID == id {
			return t, true
		}
	}
	return Task{}, false
}

// Agent returns an agent's info.
func (s *Snapshot) Agent(id string) (AgentInfo, bool) {
	for _, a := range s.Agents {
		if a.ID == id {
			return a, true
		}
	}
	return AgentInfo{}, false
}

// BoardLimits bound the board.
type BoardLimits struct {
	MaxTasks         int           // tasks ever created (default 1000)
	MaxNotes         int           // pending proposed notes; the oldest are evicted (default 48)
	MaxNotesPerAgent int           // pending notes one agent may have (default 8)
	MaxAlerts        int           // alerts shown at once (default 8)
	AlertTTL         time.Duration // alerts expire after this long (default 2 minutes)
}

// DefaultBoardLimits returns the default limits.
func DefaultBoardLimits() BoardLimits {
	return BoardLimits{MaxTasks: 1000, MaxNotes: 48, MaxNotesPerAgent: 8, MaxAlerts: 8, AlertTTL: 2 * time.Minute}
}

// withDefaults returns a copy with nonpositive task, note, alert, and alert-lifetime limits
// replaced by defaults.
func (l BoardLimits) withDefaults() BoardLimits {
	d := DefaultBoardLimits()
	if l.MaxTasks <= 0 {
		l.MaxTasks = d.MaxTasks
	}
	if l.MaxNotes <= 0 {
		l.MaxNotes = d.MaxNotes
	}
	if l.MaxNotesPerAgent <= 0 {
		l.MaxNotesPerAgent = d.MaxNotesPerAgent
	}
	if l.MaxAlerts <= 0 {
		l.MaxAlerts = d.MaxAlerts
	}
	if l.AlertTTL <= 0 {
		l.AlertTTL = d.AlertTTL
	}
	return l
}

// Board is the mutable holder of the current snapshot. Writers serialise on a
// mutex, apply one operation to a copy and publish it; readers (Snapshot, Changed)
// never lock. Every operation is one version and one board.op event that carries
// its operands, so the log can rebuild the board.
type Board struct {
	mu   sync.Mutex
	snap atomic.Pointer[Snapshot]
	wake atomic.Pointer[chan struct{}]
	next int
	note int
	ev   events.Emitter
	now  func() time.Time
	lim  BoardLimits

	// Events are queued under mu (so they keep the order of the versions) and
	// emitted after it is released by whichever writer finds no flush in progress:
	// a slow event log slows one writer, never readers or waiters.
	evq      []pendingEvent
	flushing bool
	dropped  int
}

type pendingEvent struct {
	actor string
	data  map[string]any
}

// NewBoard returns an empty board.
func NewBoard(ev events.Emitter) *Board {
	if ev == nil {
		ev = events.Discard{}
	}
	b := &Board{ev: ev, now: time.Now, lim: DefaultBoardLimits()}
	ch := make(chan struct{})
	b.wake.Store(&ch)
	b.snap.Store(&Snapshot{})
	return b
}

// SetLimits replaces the limits (zero fields keep their defaults).
func (b *Board) SetLimits(l BoardLimits) {
	b.mu.Lock()
	b.lim = l.withDefaults()
	b.mu.Unlock()
}

// SetClock replaces the clock used to stamp and expire alerts.
func (b *Board) SetClock(now func() time.Time) {
	if now == nil {
		now = time.Now
	}
	b.mu.Lock()
	b.now = now
	b.mu.Unlock()
}

// Snapshot returns the current immutable view.
func (b *Board) Snapshot() *Snapshot { return b.snap.Load() }

// Changed returns a channel closed at the next mutation; waiters re-read the
// snapshot when it fires. It never blocks. To wait without losing a wake-up, load
// the channel BEFORE reading the snapshot: a change published in between closes
// the channel that was loaded.
func (b *Board) Changed() <-chan struct{} { return *b.wake.Load() }

// errNoChange is returned by an operation that found nothing to do: the snapshot
// is not republished, the version does not move, nobody is woken.
var errNoChange = errors.New("no change")

// draft is the working copy an operation edits. Slices are shared with the
// published snapshot until an operation asks to edit them (copy on write), so an
// operation that touches only agents does not copy the tasks.
type draft struct {
	*Snapshot
	b              *Board
	ct, ca, cn, cl bool
	evt            map[string]any
}

// tasks copies the draft's task slice on first access so edits do not mutate the prior snapshot's
// slice.
func (d *draft) tasks() []Task {
	if !d.ct {
		d.Tasks = append(make([]Task, 0, len(d.Tasks)+1), d.Tasks...)
		d.ct = true
	}
	return d.Tasks
}

// agents copies the draft's agent slice on first access so edits do not mutate the prior
// snapshot's slice.
func (d *draft) agents() []AgentInfo {
	if !d.ca {
		d.Agents = append(make([]AgentInfo, 0, len(d.Agents)+1), d.Agents...)
		d.ca = true
	}
	return d.Agents
}

// notes copies the draft's note slice on first access so edits do not mutate the prior snapshot's
// slice.
func (d *draft) notes() []Note {
	if !d.cn {
		d.Notes = append(make([]Note, 0, len(d.Notes)+1), d.Notes...)
		d.cn = true
	}
	return d.Notes
}

// alerts copies the draft's alert slice on first access so edits do not mutate the prior
// snapshot's slice.
func (d *draft) alerts() []Alert {
	if !d.cl {
		d.Alerts = append(make([]Alert, 0, len(d.Alerts)+1), d.Alerts...)
		d.cl = true
	}
	return d.Alerts
}

// set records an operand of the operation on its board.op event.
func (d *draft) set(k string, v any) {
	if d.evt == nil {
		d.evt = map[string]any{}
	}
	d.evt[k] = v
}

// setTask records the mutable state of a task on the operation's event: enough, with
// the operands of its creation, to rebuild the task from the log (ReplayBoard).
func (d *draft) setTask(t Task) {
	d.set("task", t.ID)
	d.set("status", string(t.Status))
	d.set("owner", t.Owner)
	d.set("line", t.Line)
	d.set("result", t.Result)
	d.set("evidence", t.Evidence)
	d.set("attempts", t.Attempts)
	d.set("verification_failures", t.VerificationFailures)
	d.set("rev", t.Rev)
	d.set("files", t.Files)
}

// setNewTask is setTask for a task that has just been created: it also records the
// fields that never change afterwards.
func (d *draft) setNewTask(t Task) {
	d.setTask(t)
	d.set("title", t.Title)
	d.set("desc", t.Desc)
	d.set("role", t.Role)
	d.set("deps", t.Deps)
}

// mutate applies fn to a copy of the snapshot, bumps the version and publishes.
// An fn that returns errNoChange publishes nothing and mutate returns nil.
//
// The op names of the board.op event are kept stable for its consumers (the
// inspector): create, claim, assign (a task handed to an agent, or sent back to it),
// update, scope, finish (to review, done or failed: the status operand says which),
// block, resume, requeue (back to todo), agent, agent-remove, note, notes-take, alert,
// alert-clear, alert-expire. Every event names its operands (task, status, owner, ...).
func (b *Board) mutate(actor, op string, fn func(d *draft) error) error {
	b.mu.Lock()
	cur := b.snap.Load()
	d := &draft{Snapshot: &Snapshot{Version: cur.Version + 1, Tasks: cur.Tasks, Agents: cur.Agents, Notes: cur.Notes, Alerts: cur.Alerts}, b: b}
	if err := fn(d); err != nil {
		b.mu.Unlock()
		if errors.Is(err, errNoChange) {
			return nil
		}
		return err
	}
	b.snap.Store(d.Snapshot)
	next := make(chan struct{})
	old := b.wake.Swap(&next)
	close(*old)
	data := map[string]any{"op": op, "version": d.Version}
	for k, v := range d.evt {
		data[k] = v
	}
	flush := b.enqueueLocked(actor, data)
	b.mu.Unlock()
	if flush {
		b.flush()
	}
	return nil
}

// enqueueLocked queues a board event, dropping the oldest pending event when full. The caller must
// hold the board lock; true grants it responsibility for flushing the queue.
func (b *Board) enqueueLocked(actor string, data map[string]any) bool {
	if len(b.evq) >= maxPendingEvts {
		b.evq = b.evq[1:]
		b.dropped++
	}
	b.evq = append(b.evq, pendingEvent{actor, data})
	if b.flushing {
		return false
	}
	b.flushing = true
	return true
}

// flush emits queued events in order until the queue is empty. Only one goroutine
// flushes at a time; the rest hand their events to it and return.
func (b *Board) flush() {
	defer func() {
		if r := recover(); r != nil { // an emitter that panics must not wedge the queue
			b.mu.Lock()
			b.flushing = false
			b.mu.Unlock()
		}
	}()
	for {
		b.mu.Lock()
		batch, dropped := b.evq, b.dropped
		b.evq, b.dropped = nil, 0
		if len(batch) == 0 && dropped == 0 {
			b.flushing = false
			b.mu.Unlock()
			return
		}
		b.mu.Unlock()
		if dropped > 0 {
			_, _ = b.ev.Emit("harness", events.TypeBoardOp, map[string]any{"op": "dropped", "n": dropped})
		}
		for _, e := range batch {
			_, _ = b.ev.Emit(e.actor, events.TypeBoardOp, e.data)
		}
	}
}

// taskIdx returns the first task index matching an ID or -1 when absent.
func taskIdx(s *Snapshot, id string) int {
	for i := range s.Tasks {
		if s.Tasks[i].ID == id {
			return i
		}
	}
	return -1
}

// TaskSpec describes a task to create.
type TaskSpec struct {
	Title, Desc, Role string
	Deps, Files       []string
}

// TaskCheck is evaluated inside the board's critical section on the task an
// operation is about to assign, so a decision that depends on other tasks (scope
// overlap) cannot go stale between the check and the change.
type TaskCheck func(s *Snapshot, t Task) error

// createLocked validates a spec and appends the task. Nothing is modified when it
// returns an error.
func (b *Board) createLocked(d *draft, spec TaskSpec) (Task, error) {
	title := cleanText(spec.Title, maxTitleRunes)
	if title == "" {
		return Task{}, fmt.Errorf(`task needs a title: pass it in the "title" field`)
	}
	if len(d.Tasks) >= b.lim.MaxTasks {
		return Task{}, fmt.Errorf("the board already holds %d tasks (limit %d): reuse or finish existing tasks instead of creating more", len(d.Tasks), b.lim.MaxTasks)
	}
	if len(spec.Deps) > maxTaskDeps {
		return Task{}, fmt.Errorf("a task can depend on at most %d others", maxTaskDeps)
	}
	var deps []string
	seen := map[string]bool{}
	for _, dep := range spec.Deps {
		dep = strings.TrimSpace(dep)
		if taskIdx(d.Snapshot, dep) < 0 {
			return Task{}, fmt.Errorf("dependency %s does not exist", cleanText(dep, 20))
		}
		if !seen[dep] {
			seen[dep] = true
			deps = append(deps, dep)
		}
	}
	files, err := cleanScopes(spec.Files)
	if err != nil {
		return Task{}, err
	}
	b.next++
	t := Task{ID: "T" + strconv.Itoa(b.next), Title: title, Desc: cleanBlock(spec.Desc, maxDescRunes), Status: StatusTodo,
		Role: cleanText(spec.Role, maxRoleRunes), Deps: deps, Files: files}
	d.Tasks = append(d.tasks(), t)
	return t, nil
}

// CreateTask adds a todo task. Text fields are made single-line and bounded; the
// caller's slices are copied.
func (b *Board) CreateTask(by string, spec TaskSpec) (Task, error) {
	var out Task
	err := b.mutate(by, "create", func(d *draft) error {
		t, err := b.createLocked(d, spec)
		if err != nil {
			return err
		}
		out = t
		d.setNewTask(t)
		return nil
	})
	return out, err
}

// depsDone reports whether every dependency of t is done, and the first that is not.
func depsDone(s *Snapshot, t Task) (bool, Task) {
	for _, d := range t.Deps {
		if i := taskIdx(s, d); i >= 0 && s.Tasks[i].Status != StatusDone {
			return false, s.Tasks[i]
		}
	}
	return true, Task{}
}

// depsError identifies an unresolved dependency and adds recovery guidance when the dependency
// failed.
func depsError(id string, dep Task) error {
	if dep.Status == StatusFailed {
		return fmt.Errorf("%s waits for %s, which failed: reopen it or re-plan", id, dep.ID)
	}
	return fmt.Errorf("%s waits for %s", id, dep.ID)
}

// Claim takes a todo task for an agent. It is a compare-and-set: it succeeds only
// while the task is unowned and todo, and its dependencies are done.
func (b *Board) Claim(agent, id string) error { return b.claim(agent, id, nil) }

func (b *Board) claim(agent, id string, check TaskCheck) error {
	return b.mutate(agent, "claim", func(d *draft) error {
		i := taskIdx(d.Snapshot, id)
		if i < 0 {
			return fmt.Errorf("no task %s", id)
		}
		t := d.Tasks[i]
		switch {
		case t.Owner != "" && t.Owner != agent:
			return fmt.Errorf("%s is already owned by %s", id, t.Owner)
		case t.Owner == agent && t.Status == StatusDoing:
			return errNoChange // already yours
		case t.Status != StatusTodo:
			return fmt.Errorf("%s is %s, not todo", id, t.Status)
		}
		if ok, dep := depsDone(d.Snapshot, t); !ok {
			return depsError(id, dep)
		}
		if check != nil {
			if err := check(d.Snapshot, t); err != nil {
				return err
			}
		}
		t.Owner, t.Status, t.Line, t.Rev = agent, StatusDoing, "", d.Version
		d.tasks()[i] = t
		d.setTask(t)
		return nil
	})
}

// Assign hands a task to an agent: a todo task nobody owns, or a doing task the
// agent already owns (the manager resuming it). Dependencies must be done. Like
// Claim it is a compare-and-set, so a worker's own claim and the manager's spawn
// cannot both win.
func (b *Board) Assign(by, agent, id string) error {
	_, err := b.assignTask(assignReq{by: by, agent: agent, id: id})
	return err
}

type assignReq struct {
	by, agent string
	id        string   // an existing task; empty creates one from spec
	spec      TaskSpec // creation spec, or (Files non-nil) the scope to set on an existing task
	setScope  bool     // apply spec.Files to an existing task
	check     TaskCheck
}

// assignTask creates (optionally) and assigns a task in one operation: a refused
// assignment leaves nothing behind on the board.
func (b *Board) assignTask(r assignReq) (Task, error) {
	var out Task
	err := b.mutate(r.by, "assign", func(d *draft) (err error) {
		var t Task
		i := -1
		if r.id == "" {
			created, cerr := b.createLocked(d, r.spec)
			if cerr != nil {
				return cerr
			}
			t, i = created, len(d.Tasks)-1
			defer func() {
				if err != nil {
					b.next-- // the task was never published: keep ids dense
				}
			}()
		} else {
			if i = taskIdx(d.Snapshot, r.id); i < 0 {
				return fmt.Errorf("no task %s", r.id)
			}
			t = d.Tasks[i]
			switch {
			case t.Status == StatusDone || t.Status == StatusFailed:
				return fmt.Errorf("%s is already %s", r.id, t.Status)
			case t.Owner != "" && t.Owner == r.by && t.Owner != r.agent:
				// The assigner is the owner: a manager that claimed a task it meant to hand over. The second real swarm run did,
				// and went on for turns, because the only thing the board said was whose the task was.
				return fmt.Errorf("%s is claimed by you, so no worker can be put on it: drop it with fail and create the task again for the worker "+
					"(a manager hands tasks over, it does not claim them)", r.id)
			case t.Owner != "" && t.Owner != r.agent:
				return fmt.Errorf("%s is already owned by %s", r.id, t.Owner)
			case t.Status == StatusReview:
				return fmt.Errorf("%s is in review: accept it, or reject it to send it back", r.id)
			case t.Status == StatusBlocked:
				return fmt.Errorf("%s is blocked (%s): resolve the blocker first", r.id, oneLine(t.Line, 60))
			}
			if r.setScope {
				files, err := cleanScopes(r.spec.Files)
				if err != nil {
					return err
				}
				t.Files = files
			}
		}
		if ok, dep := depsDone(d.Snapshot, t); !ok {
			return depsError(t.ID, dep)
		}
		if r.check != nil {
			if err := r.check(d.Snapshot, t); err != nil {
				return err
			}
		}
		t.Owner, t.Status, t.Line, t.Rev = r.agent, StatusDoing, "", d.Version
		d.tasks()[i] = t
		if r.id == "" {
			d.setNewTask(t)
		} else {
			d.setTask(t)
		}
		out = t
		return nil
	})
	return out, err
}

// Finish moves a task to a final-ish state: review (Submit), done (Accept, the
// result is appended as the reviewer's note) or failed (Fail). It exists for
// callers that do not need the operation-specific forms.
func (b *Board) Finish(agent, id string, status TaskStatus, result string) error {
	switch status {
	case StatusReview:
		return b.Submit(agent, id, result, "")
	case StatusDone:
		return b.Accept(agent, id, result)
	case StatusFailed:
		return b.Fail(agent, id, result)
	}
	return fmt.Errorf("bad final status %q", status)
}

// SetScope replaces a task's scope (the manager widening or narrowing it).
func (b *Board) SetScope(by, id string, files []string, check TaskCheck) error {
	return b.mutate(by, "scope", func(d *draft) error {
		i := taskIdx(d.Snapshot, id)
		if i < 0 {
			return fmt.Errorf("no task %s", id)
		}
		t := d.Tasks[i]
		if t.Status == StatusDone || t.Status == StatusFailed {
			return fmt.Errorf("%s is already %s", id, t.Status)
		}
		nf, err := cleanScopes(files)
		if err != nil {
			return err
		}
		t.Files = nf
		if check != nil && t.Status == StatusDoing {
			if err := check(d.Snapshot, t); err != nil {
				return err
			}
		}
		d.tasks()[i] = t
		d.setTask(t)
		return nil
	})
}

// owned returns the task at id if agent owns it.
func owned(d *draft, agent, id string) (int, Task, error) {
	i := taskIdx(d.Snapshot, id)
	if i < 0 {
		return -1, Task{}, fmt.Errorf("no task %s", id)
	}
	t := d.Tasks[i]
	if t.Owner != agent {
		return -1, Task{}, errNotOwner(id, t.Owner)
	}
	return i, t, nil
}

// errNotOwner is what an agent is told when it acts on a task that is not its own. The answer says what to do next, because a
// model that reads only "T1 belongs to nobody" has nothing to act on: the first real swarm run spent four turns looking for a
// way to delete a duplicate task that fail would have dropped.
func errNotOwner(id, owner string) error {
	if owner == "" {
		return fmt.Errorf("%s is not claimed by anyone, so there is nothing of yours to update here: claim it first. "+
			"A task that should not be done at all is dropped with fail (manager only)", id)
	}
	return fmt.Errorf("%s belongs to %s, not to you: mail them, or the manager if it should move", id, owner)
}

// Update sets the one-line progress of a task the agent is working on.
func (b *Board) Update(agent, id, line string) error {
	return b.mutate(agent, "update", func(d *draft) error {
		i, t, err := owned(d, agent, id)
		if err != nil {
			return err
		}
		if t.Status != StatusDoing {
			return fmt.Errorf("%s is %s, not doing", id, t.Status)
		}
		line = cleanText(line, maxLineRunes)
		if t.Line == line {
			return errNoChange
		}
		t.Line = line
		d.tasks()[i] = t
		d.setTask(t)
		return nil
	})
}

// Submit moves the owner's doing task to review with the worker's result and the
// harness's evidence. It is the only way a worker's task leaves doing for good.
func (b *Board) Submit(agent, id, result, evidence string) error {
	return b.SubmitAt(agent, id, 0, result, evidence)
}

// SubmitAt is Submit that applies only to the assignment rev (0: any).
func (b *Board) SubmitAt(agent, id string, rev uint64, result, evidence string) error {
	return b.mutate(agent, "finish", func(d *draft) error {
		i, t, err := owned(d, agent, id)
		if err != nil {
			return err
		}
		if t.Status != StatusDoing {
			return fmt.Errorf("%s is %s, not doing", id, t.Status)
		}
		if rev != 0 && t.Rev != rev {
			return fmt.Errorf("%s was reassigned while you worked on it", id)
		}
		t.Status, t.Line = StatusReview, ""
		t.VerificationFailures = 0
		t.Result, t.Evidence = cleanText(result, maxResultRunes), cleanText(evidence, maxEvidRunes)
		d.tasks()[i] = t
		d.setTask(t)
		return nil
	})
}

// Accept marks a reviewed task done. Done is terminal: nothing changes it after.
// Callers check authority (only the manager accepts) and run the verifier first.
func (b *Board) Accept(by, id, note string) error {
	return b.mutate(by, "finish", func(d *draft) error {
		i := taskIdx(d.Snapshot, id)
		if i < 0 {
			return fmt.Errorf("no task %s", id)
		}
		t := d.Tasks[i]
		if t.Status != StatusReview {
			return fmt.Errorf("%s is %s: only a task in review can be accepted", id, t.Status)
		}
		t.Status, t.Line = StatusDone, ""
		if n := cleanText(note, maxResultRunes); n != "" {
			t.Result = truncRunes(strings.TrimSpace(t.Result+" · "+n), 2*maxResultRunes)
		}
		d.tasks()[i] = t
		d.setTask(t)
		return nil
	})
}

// Fail marks a task that is not done as failed.
func (b *Board) Fail(by, id, reason string) error {
	return b.mutate(by, "finish", func(d *draft) error {
		i := taskIdx(d.Snapshot, id)
		if i < 0 {
			return fmt.Errorf("no task %s", id)
		}
		t := d.Tasks[i]
		if t.Status == StatusDone || t.Status == StatusFailed {
			return fmt.Errorf("%s is already %s", id, t.Status)
		}
		t.Status, t.Line, t.Rev = StatusFailed, "", d.Version
		if r := cleanText(reason, maxResultRunes); r != "" {
			t.Result = r
		}
		d.tasks()[i] = t
		d.setTask(t)
		return nil
	})
}

// SendBack returns a reviewed task to its owner (a rejection with feedback).
func (b *Board) SendBack(by, id, feedback string) (Task, error) {
	var out Task
	err := b.mutate(by, "assign", func(d *draft) error {
		i := taskIdx(d.Snapshot, id)
		if i < 0 {
			return fmt.Errorf("no task %s", id)
		}
		t := d.Tasks[i]
		if t.Status != StatusReview {
			return fmt.Errorf("%s is %s: only a task in review can be sent back", id, t.Status)
		}
		if t.Owner == "" {
			return fmt.Errorf("%s has no owner to send feedback to", id)
		}
		t.Status, t.Line, t.Rev = StatusDoing, cleanText(feedback, maxLineRunes), d.Version
		d.tasks()[i] = t
		d.setTask(t)
		out = t
		return nil
	})
	return out, err
}

// Unassign returns a task that is not finished to the pool without an owner (its
// worker is gone). A task in review keeps nothing of the abandoned attempt.
func (b *Board) Unassign(by, id, reason string) error {
	return b.mutate(by, "requeue", func(d *draft) error {
		i := taskIdx(d.Snapshot, id)
		if i < 0 {
			return fmt.Errorf("no task %s", id)
		}
		t := d.Tasks[i]
		switch t.Status {
		case StatusDoing, StatusBlocked, StatusReview:
		default:
			return fmt.Errorf("%s is %s", id, t.Status)
		}
		t.Status, t.Owner, t.Line, t.Rev = StatusTodo, "", cleanText(reason, maxLineRunes), d.Version
		t.Result, t.Evidence = "", ""
		d.tasks()[i] = t
		d.setTask(t)
		return nil
	})
}

// Reopen returns a failed task to todo so it can be assigned again.
func (b *Board) Reopen(by, id string) error {
	return b.mutate(by, "requeue", func(d *draft) error {
		i := taskIdx(d.Snapshot, id)
		if i < 0 {
			return fmt.Errorf("no task %s", id)
		}
		t := d.Tasks[i]
		if t.Status != StatusFailed {
			return fmt.Errorf("%s is %s: only a failed task can be reopened (a task in review is sent back with reject)", id, t.Status)
		}
		t.Status, t.Owner, t.Line, t.Result, t.Evidence, t.Attempts, t.Rev = StatusTodo, "", "", "", "", 0, d.Version
		t.VerificationFailures = 0
		d.tasks()[i] = t
		d.setTask(t)
		return nil
	})
}

// Requeue is what the harness does when the agent that was working on a task
// stops without finishing it: back to todo for someone else (or failed once it
// has used maxAttempts). It applies only if the agent still owns that assignment
// (rev), so a run that ended late cannot undo a newer assignment.
func (b *Board) Requeue(agent, id string, rev uint64, reason string, countAttempt bool, maxAttempts int) (Task, bool) {
	var out Task
	applied := false
	_ = b.mutate("harness", "requeue", func(d *draft) error {
		i := taskIdx(d.Snapshot, id)
		if i < 0 {
			return errNoChange
		}
		t := d.Tasks[i]
		if t.Owner != agent || (t.Status != StatusDoing && t.Status != StatusBlocked) || (rev != 0 && t.Rev != rev) {
			return errNoChange
		}
		t = requeuedTask(t, d.Version, reason, countAttempt, maxAttempts)
		d.tasks()[i] = t
		d.setTask(t)
		out, applied = t, true
		return nil
	})
	return out, applied
}

// requeuedTask releases an assignment or marks its final failure. Only a counted
// attempt resets its verification budget; interruption preserves that budget.
func requeuedTask(t Task, rev uint64, reason string, countAttempt bool, maxAttempts int) Task {
	if countAttempt {
		t.Attempts++
		t.VerificationFailures = 0
	}
	t.Rev, t.Line = rev, ""
	if maxAttempts > 0 && t.Attempts >= maxAttempts {
		t.Status = StatusFailed
		t.Result = cleanText(reason, maxResultRunes)
	} else {
		t.Status, t.Owner = StatusTodo, ""
		t.Line = cleanText(reason, maxLineRunes)
	}
	return t
}

// FailVerification records a failed test gate only while agent owns the doing
// assignment rev. At maxFailures (at least one), it atomically counts an attempt
// and requeues or fails the task. Stale results make no change and return false.
// Infrastructure failures and cancellation must not be passed to this method.
func (b *Board) FailVerification(agent, id string, rev uint64, reason, evidence string, maxFailures, maxAttempts int) (Task, bool) {
	var out Task
	applied := false
	_ = b.mutate("harness", "update", func(d *draft) error {
		i := taskIdx(d.Snapshot, id)
		if i < 0 {
			return errNoChange
		}
		t := d.Tasks[i]
		if t.Owner != agent || t.Status != StatusDoing || t.Rev != rev {
			return errNoChange
		}
		t.VerificationFailures++
		t.Evidence = cleanText(evidence, maxEvidRunes)
		if t.VerificationFailures >= max(1, maxFailures) {
			t = requeuedTask(t, d.Version, reason, true, maxAttempts)
			d.set("op", "requeue") // preserve the board event contract used by live views
		}
		d.tasks()[i] = t
		d.setTask(t)
		out, applied = t, true
		return nil
	})
	return out, applied
}

// Block marks the owner's doing task blocked with a reason.
func (b *Board) Block(agent, id, reason string) error {
	return b.mutate(agent, "block", func(d *draft) error {
		i, t, err := owned(d, agent, id)
		if err != nil {
			return err
		}
		if t.Status != StatusDoing {
			return fmt.Errorf("%s is %s, not doing", id, t.Status)
		}
		t.Status, t.Line = StatusBlocked, cleanText(reason, maxLineRunes)
		d.tasks()[i] = t
		d.setTask(t)
		return nil
	})
}

// Resume returns a blocked task to doing (its owner, once unblocked).
func (b *Board) Resume(agent, id string) error {
	return b.mutate(agent, "resume", func(d *draft) error {
		i, t, err := owned(d, agent, id)
		if err != nil {
			return err
		}
		if t.Status != StatusBlocked {
			return fmt.Errorf("%s is %s, not blocked", id, t.Status)
		}
		t.Status, t.Line = StatusDoing, ""
		d.tasks()[i] = t
		d.setTask(t)
		return nil
	})
}

// Unblock returns a blocked task to doing on the manager's authority.
func (b *Board) Unblock(by, id string) error {
	return b.mutate(by, "resume", func(d *draft) error {
		i := taskIdx(d.Snapshot, id)
		if i < 0 {
			return fmt.Errorf("no task %s", id)
		}
		t := d.Tasks[i]
		if t.Status != StatusBlocked {
			return fmt.Errorf("%s is %s, not blocked", id, t.Status)
		}
		t.Status, t.Line = StatusDoing, ""
		d.tasks()[i] = t
		d.setTask(t)
		return nil
	})
}

// setAgent records an agent's whole status on the operation's event.
func (d *draft) setAgent(a AgentInfo) {
	d.set("agent", a.ID)
	d.set("role", a.Role)
	d.set("state", a.State)
	d.set("task", a.Task)
	d.set("line", a.Line)
	d.set("ctx_tokens", a.CtxTokens)
	d.set("cost_usd", a.CostUSD)
}

// SetAgent upserts an agent's status. Republishing an identical status is a no-op.
func (b *Board) SetAgent(info AgentInfo) {
	info.Line = cleanText(info.Line, 70)
	_ = b.mutate(info.ID, "agent", func(d *draft) error {
		for i := range d.Agents {
			if d.Agents[i].ID == info.ID {
				if d.Agents[i] == info {
					return errNoChange
				}
				d.agents()[i] = info
				d.setAgent(info)
				return nil
			}
		}
		as := append(d.agents(), info)
		sort.Slice(as, func(i, j int) bool { return as[i].ID < as[j].ID })
		d.Agents = as
		d.setAgent(info)
		return nil
	})
}

// RemoveAgent drops an agent from the roster.
func (b *Board) RemoveAgent(id string) {
	_ = b.mutate(id, "agent-remove", func(d *draft) error {
		found := false
		for _, a := range d.Agents {
			if a.ID == id {
				found = true
			}
		}
		if !found {
			return errNoChange
		}
		out := make([]AgentInfo, 0, len(d.Agents))
		for _, a := range d.Agents {
			if a.ID != id {
				out = append(out, a)
			}
		}
		d.Agents, d.ca = out, true
		d.set("agent", id)
		return nil
	})
}

// RequeueOwned releases doing, blocked, and reserved todo tasks when their agent
// goes away. Review results remain available. It returns the tasks it changed.
func (b *Board) RequeueOwned(agent, reason string) []Task {
	var out []Task
	_ = b.mutate("harness", "requeue", func(d *draft) error {
		for i, t := range d.Tasks {
			if t.Owner == agent && (t.Status == StatusDoing || t.Status == StatusBlocked || t.Status == StatusTodo) {
				t.Status, t.Owner, t.Line, t.Rev = StatusTodo, "", cleanText(reason, maxLineRunes), d.Version
				d.tasks()[i] = t
				out = append(out, t)
			}
		}
		if len(out) == 0 {
			return errNoChange
		}
		ids := make([]string, len(out))
		for i, t := range out {
			ids[i] = t.ID
		}
		d.set("tasks", ids)
		d.set("line", cleanText(reason, maxLineRunes))
		return nil
	})
	return out
}

// resumedLine explains why an interrupted task is waiting after recovery.
const resumedLine = "the session was resumed: waiting to restart"

// Restore gives an empty board the tasks and the pending notes of an earlier session's (ReplayBoard of its log): a resumed team starts
// where the last one stopped. Nothing is running, so a task that was somebody's (doing, review, blocked) is todo again with no owner,
// and one that was done or failed stays so. It returns the ids it put back. The ids go on counting where the old board stopped, and the
// whole is one "requeue" operation naming the tasks put back, so that ReplayBoard of the log, the old part and the new, equals this board.
// A board that already holds tasks or notes is left as it is.
func (b *Board) Restore(prev *Snapshot) []string {
	return b.RestoreOwners(prev, nil)
}

// RestoreOwners restores tasks and notes while reserving interrupted tasks for
// workers whose private worktrees were recovered. Reserved tasks are todo, so no
// worker is reported as running until explicitly started again.
func (b *Board) RestoreOwners(prev *Snapshot, owners map[string]string) []string {
	if prev == nil {
		return nil
	}
	var back []string
	_ = b.mutate("harness", "requeue", func(d *draft) error {
		if len(d.Tasks) > 0 || len(d.Notes) > 0 {
			return errNoChange
		}
		d.Version = prev.Version + 1
		tasks := append([]Task(nil), prev.Tasks...)
		for i, t := range tasks {
			switch t.Status {
			case StatusDoing, StatusReview, StatusBlocked, StatusTodo:
				if t.Status == StatusTodo && t.Owner == "" && owners[t.ID] == "" {
					break
				}
				t.Status, t.Owner, t.Line, t.Rev = StatusTodo, owners[t.ID], resumedLine, d.Version
				back = append(back, t.ID)
			}
			tasks[i] = t
			if n, err := strconv.Atoi(strings.TrimPrefix(t.ID, "T")); err == nil && n > b.next {
				b.next = n
			}
		}
		d.Tasks, d.ct = tasks, true
		d.Notes, d.cn = append([]Note(nil), prev.Notes...), true
		for _, n := range d.Notes {
			b.note = max(b.note, n.ID)
		}
		d.set("tasks", back)
		d.set("line", resumedLine)
		if len(owners) > 0 {
			d.set("owners", owners)
		}
		return nil
	})
	return back
}

// AddNote records a fact proposed for the shared context. Identical text is
// ignored, so several agents discovering the same convention add it once (for a
// shared note whichever role found it). An agent may have only a few notes
// pending, and the buffer keeps only the newest: what one agent can add is what
// every agent's prompt carries.
func (b *Board) AddNote(from, scope, role, text string) (int, error) {
	text = cleanText(text, maxNoteRunes)
	if text == "" {
		return 0, fmt.Errorf("empty note")
	}
	if scope != "shared" && scope != "role" {
		return 0, fmt.Errorf("scope must be shared or role")
	}
	if scope == "shared" {
		role = ""
	}
	id := 0
	err := b.mutate(from, "note", func(d *draft) error {
		mine := 0
		for _, n := range d.Notes {
			if n.Text == text && n.Scope == scope && n.Role == role {
				id = n.ID
				return errNoChange
			}
			if n.From == from {
				mine++
			}
		}
		if mine >= b.lim.MaxNotesPerAgent {
			return fmt.Errorf("you already have %d proposed notes waiting to be merged: wait for them, or fold your facts into one note", mine)
		}
		b.note++
		id = b.note
		ns := append(d.notes(), Note{ID: id, From: from, Scope: scope, Role: role, Text: text})
		if over := len(ns) - b.lim.MaxNotes; over > 0 {
			evicted := make([]int, over)
			for i := 0; i < over; i++ {
				evicted[i] = ns[i].ID
			}
			d.set("evicted", evicted)
			ns = append([]Note(nil), ns[over:]...)
		}
		d.Notes = ns
		d.set("note", id)
		d.set("text", text)
		d.set("scope", scope)
		d.set("role", role)
		return nil
	})
	return id, err
}

// TakeNotes removes and returns the pending notes with the given ids (the curator
// merging them). With no ids it takes nothing; TakeAllNotes takes everything.
func (b *Board) TakeNotes(ids ...int) []Note {
	if len(ids) == 0 {
		return nil
	}
	want := map[int]bool{}
	for _, i := range ids {
		want[i] = true
	}
	return b.takeNotes(func(n Note) bool { return want[n.ID] })
}

// TakeAllNotes removes and returns every pending note.
func (b *Board) TakeAllNotes() []Note { return b.takeNotes(func(Note) bool { return true }) }

func (b *Board) takeNotes(pick func(Note) bool) []Note {
	var taken []Note
	_ = b.mutate("curator", "notes-take", func(d *draft) error {
		keep := make([]Note, 0, len(d.Notes))
		for _, n := range d.Notes {
			if pick(n) {
				taken = append(taken, n)
			} else {
				keep = append(keep, n)
			}
		}
		if len(taken) == 0 {
			return errNoChange
		}
		d.Notes, d.cn = keep, true
		ids := make([]int, len(taken))
		for i, n := range taken {
			ids[i] = n.ID
		}
		d.set("notes", ids)
		return nil
	})
	return taken
}

// RaiseAlert adds an alert unless an identical one is already showing.
func (b *Board) RaiseAlert(kind, text string) { b.RaiseAlertKey(kind, "", text) }

// RaiseAlertKey is RaiseAlert with a key that ClearAlertKey can remove it by (the
// path of a contested file, the id of a stalled agent). Alerts expire after the
// board's AlertTTL, and only the newest few are kept.
func (b *Board) RaiseAlertKey(kind, key, text string) {
	text = cleanText(text, maxAlertRunes)
	key = cleanText(key, 200)
	_ = b.mutate("harness", "alert", func(d *draft) error {
		now := b.now()
		d.dropExpired(now, b.lim.AlertTTL)
		for _, a := range d.Alerts {
			if a.Kind == kind && a.Text == text && a.Key == key {
				if d.cl { // expiry already rewrote the list: publish that
					return nil
				}
				return errNoChange
			}
		}
		as := append(d.alerts(), Alert{Kind: kind, Text: text, Key: key, At: now})
		if over := len(as) - b.lim.MaxAlerts; over > 0 {
			as = append([]Alert(nil), as[over:]...)
		}
		d.Alerts = as
		d.set("kind", kind)
		d.set("text", text)
		if key != "" {
			d.set("key", key)
		}
		return nil
	})
}

// dropExpired removes alerts older than ttl from the draft.
func (d *draft) dropExpired(now time.Time, ttl time.Duration) {
	stale := false
	for _, a := range d.Alerts {
		if !a.At.IsZero() && now.Sub(a.At) >= ttl {
			stale = true
			break
		}
	}
	if !stale {
		return
	}
	keep := make([]Alert, 0, len(d.Alerts))
	for _, a := range d.Alerts {
		if a.At.IsZero() || now.Sub(a.At) < ttl {
			keep = append(keep, a)
		}
	}
	d.Alerts, d.cl = keep, true
}

// ExpireAlerts drops alerts that outlived the alert TTL.
func (b *Board) ExpireAlerts() {
	_ = b.mutate("harness", "alert-expire", func(d *draft) error {
		before := len(d.Alerts)
		d.dropExpired(b.now(), b.lim.AlertTTL)
		if len(d.Alerts) == before {
			return errNoChange
		}
		d.set("dropped", before-len(d.Alerts))
		return nil
	})
}

// ClearAlerts removes alerts of a kind.
func (b *Board) ClearAlerts(kind string) {
	_ = b.mutate("harness", "alert-clear", func(d *draft) error {
		d.set("kind", kind)
		return d.clearAlerts(func(a Alert) bool { return a.Kind == kind })
	})
}

// ClearAlertKey removes the alerts of a kind that were raised with a key.
func (b *Board) ClearAlertKey(kind, key string) {
	key = cleanText(key, 200)
	_ = b.mutate("harness", "alert-clear", func(d *draft) error {
		d.set("kind", kind)
		d.set("key", key)
		return d.clearAlerts(func(a Alert) bool { return a.Kind == kind && a.Key == key })
	})
}

// clearAlerts removes alerts selected by drop and marks the draft changed, returning errNoChange
// when no alert is removed.
func (d *draft) clearAlerts(drop func(Alert) bool) error {
	keep := make([]Alert, 0, len(d.Alerts))
	for _, a := range d.Alerts {
		if !drop(a) {
			keep = append(keep, a)
		}
	}
	if len(keep) == len(d.Alerts) {
		return errNoChange
	}
	d.Alerts, d.cl = keep, true
	return nil
}

// ownerOrNone substitutes a readable nobody label for an unowned task.
func ownerOrNone(o string) string {
	if o == "" {
		return "nobody"
	}
	return o
}

// Wait blocks until the board's version exceeds after or the timeout passes. It
// returns the new snapshot.
func (b *Board) Wait(after uint64, timeout time.Duration) *Snapshot {
	t := time.NewTimer(timeout)
	defer t.Stop()
	for {
		ch := b.Changed() // before the snapshot: see Changed
		if s := b.Snapshot(); s.Version > after {
			return s
		}
		select {
		case <-ch:
		case <-t.C:
			return b.Snapshot()
		}
	}
}
