package state

import (
	"encoding/json"
	"sort"
	"strconv"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
)

// taskState is a Task and what the merge queue says about it right now.
type taskState struct {
	Task
	queued bool // a submission of it is waiting in the merge queue
}

// boardState is the board's own bookkeeping beyond the tasks.
type boardState struct {
	version uint64
	notes   map[int]struct{}
	alerts  []Alert
	dropped int
}

// newBoardState initializes an empty board with note membership tracking.
func newBoardState() boardState { return boardState{notes: map[int]struct{}{}} }

// boardWire is what a board.op carries. Every operation names the task it changed and its whole state (internal/swarm/board.go
// setTask), so that the log can rebuild the board; the operands of the other ops are the agent's status, a note or an alert.
type boardWire struct {
	Op        string            `json:"op"`
	Version   uint64            `json:"version"`
	Task      string            `json:"task"`
	Status    string            `json:"status"`
	Owner     string            `json:"owner"`
	Owners    map[string]string `json:"owners"`
	Line      string            `json:"line"`
	Result    string            `json:"result"`
	Evidence  string            `json:"evidence"`
	Attempts  int64             `json:"attempts"`
	Rev       *uint64           `json:"rev"`
	Files     []string          `json:"files"`
	Title     string            `json:"title"`
	Role      string            `json:"role"`
	Deps      []string          `json:"deps"`
	Agent     string            `json:"agent"`
	State     string            `json:"state"`
	CtxTokens int64             `json:"ctx_tokens"`
	Note      int               `json:"note"`
	Evicted   []int             `json:"evicted"`
	Notes     []int             `json:"notes"`
	Tasks     []string          `json:"tasks"`
	Kind      string            `json:"kind"`
	Text      string            `json:"text"`
	Key       string            `json:"key"`
	Dropped   int               `json:"dropped"`
	N         int               `json:"n"`
	// Closure, BlockedOn and VerificationFailures are part of the task's whole state (setTask); Kind is the alert's kind on an
	// alert op and the task's kind on a create op.
	Closure              json.RawMessage `json:"closure"`
	BlockedOn            string          `json:"blocked_on"`
	VerificationFailures int64           `json:"verification_failures"`
}

func (s *State) onBoardOp(e events.Event, t time.Time) {
	var p boardWire
	if !s.decode(e.Data, maxPayload, &p) {
		return
	}
	if p.Version > s.board.version {
		s.board.version = p.Version
	}
	switch p.Op {
	case "create", "claim", "assign", "update", "scope", "finish", "block", "resume", "requeue":
		s.taskOp(e, t, &p)
	case "agent":
		id := firstOf(p.Agent, e.Agent)
		if a := s.agent(id, t); a != nil {
			task := p.Task
			s.applyInfo(a, p.State, p.Line, &task, t)
			a.Role = firstOf(a.Role, clip(p.Role, textID))
			a.CtxTokens = clampTokens(p.CtxTokens)
		}
	case "agent-remove":
		if a := s.agentIfAny(firstOf(p.Agent, e.Agent)); a != nil {
			a.Retired = true
		}
	case "note":
		for _, id := range p.Evicted {
			delete(s.board.notes, id)
		}
		s.addNote(p.Note)
	case "notes-take":
		for _, id := range p.Notes {
			delete(s.board.notes, id)
		}
	case "alert":
		s.addAlert(Alert{Kind: clip(p.Kind, textID), Text: clean(p.Text, textLine), Key: clip(p.Key, textID), Seq: e.Seq, T: t})
	case "alert-clear":
		s.clearAlerts(clip(p.Kind, textID), clip(p.Key, textID))
	case "alert-expire":
		n := min(max(p.Dropped, 0), len(s.board.alerts))
		s.board.alerts = append(s.board.alerts[:0], s.board.alerts[n:]...)
	case "dropped":
		s.board.dropped += max(p.N, 0)
	default:
		s.stats.Unknown++
	}
}

func (s *State) addNote(id int) {
	if id <= 0 {
		return
	}
	if _, ok := s.board.notes[id]; !ok && len(s.board.notes) >= MaxNotes {
		oldest := id
		for k := range s.board.notes {
			oldest = min(oldest, k)
		}
		delete(s.board.notes, oldest)
	}
	s.board.notes[id] = struct{}{}
}

// addAlert adds an alert, replacing the one with the same key, and keeps the newest MaxAlerts.
func (s *State) addAlert(a Alert) {
	b := &s.board
	if a.Key != "" {
		for i := range b.alerts {
			if b.alerts[i].Key == a.Key && b.alerts[i].Kind == a.Kind {
				b.alerts = append(b.alerts[:i], b.alerts[i+1:]...)
				break
			}
		}
	}
	b.alerts = append(b.alerts, a)
	if len(b.alerts) > MaxAlerts {
		b.alerts = append(b.alerts[:0], b.alerts[len(b.alerts)-MaxAlerts:]...)
	}
}

// clearAlerts removes the alerts of a kind, or only the one with the key when a key is given.
func (s *State) clearAlerts(kind, key string) {
	keep := s.board.alerts[:0]
	for _, a := range s.board.alerts {
		if (key != "" && a.Key == key && a.Kind == kind) || (key == "" && a.Kind == kind) {
			continue
		}
		keep = append(keep, a)
	}
	s.board.alerts = keep
}

// task returns the task with the id, creating it on first sight (a log that starts mid-session names tasks it never created). When
// the board is full the oldest finished task makes room; with none to spare the new one is dropped and nil is returned.
func (s *State) task(id string) *taskState {
	if t := s.tasks[id]; t != nil {
		s.touchTask(id)
		return t
	}
	if len(s.tasks) >= MaxTasks && !s.evictTask() {
		s.stats.Dropped++
		return nil
	}
	t := &taskState{Task: Task{ID: id, Status: "todo"}}
	s.tasks[id] = t
	s.touchTask(id)
	return t
}

// evictTask removes the lowest-numbered task that is done or failed.
func (s *State) evictTask() bool {
	var victim *taskState
	for _, t := range s.tasks {
		if t.Status != "done" && t.Status != "failed" {
			continue
		}
		if victim == nil || taskLess(t.ID, victim.ID) {
			victim = t
		}
	}
	if victim == nil {
		return false
	}
	delete(s.tasks, victim.ID)
	s.touchTask(victim.ID)
	s.stats.Dropped++
	return true
}

// taskLess orders task ids by number (T2 before T10), ids that are not numbered after them, by text.
func taskLess(a, b string) bool {
	na, nb := taskNum(a), taskNum(b)
	if na != nb {
		return na < nb
	}
	return a < b
}

// taskOp applies the operation that changed one task. An event with "rev" carries the whole state of the task after the
// operation, where an empty owner, line or result means cleared; an older log without it carries only the operands that
// changed, and only those that are present are applied.
func (s *State) taskOp(e events.Event, t time.Time, p *boardWire) {
	id := clip(p.Task, textID)
	if id == "" {
		if p.Op == "requeue" { // RequeueOwned names the tasks it returned to the queue
			for i, tid := range p.Tasks {
				if i >= MaxTasks {
					break
				}
				if ts := s.tasks[clip(tid, textID)]; ts != nil {
					s.touchTask(ts.ID)
					s.touchAgent(ts.Owner)
					s.setStatus(ts, "todo", t, e.Seq)
					ts.Owner, ts.Line = clip(p.Owners[tid], textID), clean(p.Line, textShort)
					ts.Rev = p.Version
				}
			}
			return
		}
		s.bad()
		return
	}
	ts := s.task(id)
	if ts == nil {
		return
	}
	prev, prevOwner := ts.Status, ts.Owner
	full := p.Rev != nil
	if p.Title != "" {
		ts.Title = clean(p.Title, textLine)
		ts.Role = clip(p.Role, textID)
		ts.Deps = clipList(p.Deps, MaxDeps, textID)
		if p.Op == "create" {
			ts.Kind = clip(p.Kind, textID)
		}
	}
	if full {
		s.setStatus(ts, p.Status, t, e.Seq)
		ts.Owner, ts.Line = clip(p.Owner, textID), clean(p.Line, textShort)
		ts.Result, ts.Evidence = clean(p.Result, textLong), clean(p.Evidence, textLong)
		ts.Attempts, ts.Rev = clampTokens(p.Attempts), *p.Rev
		ts.Files = clipList(p.Files, MaxFiles, textPath)
		ts.Closure, ts.BlockedOn = closureText(p.Closure), clip(p.BlockedOn, textID)
		ts.VerificationFailures = clampTokens(p.VerificationFailures)
	} else {
		if c := closureText(p.Closure); c != "" {
			ts.Closure = c
		}
		if p.BlockedOn != "" {
			ts.BlockedOn = clip(p.BlockedOn, textID)
		}
		if p.VerificationFailures > 0 {
			ts.VerificationFailures = clampTokens(p.VerificationFailures)
		}
		if p.Status != "" {
			s.setStatus(ts, p.Status, t, e.Seq)
		}
		if p.Owner != "" {
			ts.Owner = clip(p.Owner, textID)
		}
		if p.Line != "" {
			ts.Line = clean(p.Line, textShort)
		}
		if p.Result != "" {
			ts.Result = clean(p.Result, textLong)
		}
		if p.Evidence != "" {
			ts.Evidence = clean(p.Evidence, textLong)
		}
		if p.Attempts > 0 {
			ts.Attempts = clampTokens(p.Attempts)
		}
		if len(p.Files) > 0 {
			ts.Files = clipList(p.Files, MaxFiles, textPath)
		}
	}
	ts.Seq, ts.Updated = e.Seq, t
	s.touchAgent(prevOwner) // the scope of the one who held it, and of the one who holds it, may have changed
	s.touchAgent(ts.Owner)
	if ts.Status == "done" && prev != "done" {
		if a := s.agentIfAny(ts.Owner); a != nil {
			s.settleDone(a) // its task was accepted: the worker's job is finished
		}
	}
	s.boardLine(e, t, ts, prev, p.Op)
}

// setStatus sets the board's status word. A task that goes back to work (todo, doing, blocked) starts a new assignment, which
// has no merge yet.
func (s *State) setStatus(ts *taskState, status string, t time.Time, seq uint64) {
	switch status {
	case "todo", "doing", "blocked", "review", "done", "failed":
	case "":
		return
	default:
		status = "todo"
	}
	switch status {
	case "todo", "doing", "blocked":
		ts.Merge, ts.Commit = "", ""
	}
	ts.Status = status
}

// boardLine writes the feed line for the operations that matter to a person watching: a task created, finished, failed, blocked or
// returned to the queue.
func (s *State) boardLine(e events.Event, t time.Time, ts *taskState, prev, op string) {
	who := firstOf(ts.Owner, e.Agent)
	switch op {
	case "create":
		s.line(e.Seq, t, e.Agent, FeedBoard, GlyphTask, ts.ID+" created: "+firstOf(ts.Title, "(no title)"), ts.Role)
	case "finish":
		switch ts.Status {
		case "done":
			s.line(e.Seq, t, e.Agent, FeedBoard, GlyphOK, ts.ID+" accepted", ts.Title)
		case "failed":
			s.line(e.Seq, t, e.Agent, FeedBoard, GlyphFail, ts.ID+" failed", firstOf(ts.Result, ts.Line))
		default:
			s.line(e.Seq, t, who, FeedBoard, GlyphTask, ts.ID+" is in review", ts.Title)
		}
	case "block":
		s.line(e.Seq, t, who, FeedBoard, GlyphWarn, ts.ID+" blocked", ts.Line)
	case "requeue":
		if prev != ts.Status || ts.Status == "failed" {
			text := ts.ID + " went back to the queue"
			glyph := GlyphTask
			if ts.Status == "failed" {
				text, glyph = ts.ID+" failed after "+strconv.Itoa(ts.Attempts)+" attempts", GlyphFail
			}
			s.line(e.Seq, t, who, FeedBoard, glyph, text, ts.Line)
		}
	}
}

// clipList bounds a list of strings from a payload: at most n of them, each clipped.
func clipList(in []string, n, maxLen int) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, min(len(in), n))
	for i, v := range in {
		if i >= n {
			break
		}
		out = append(out, clip(v, maxLen))
	}
	return out
}

// sortedTasks lists the tasks by number with the derived state filled in.
func (s *State) sortedTasks() []Task {
	out := make([]Task, 0, len(s.tasks))
	for _, ts := range s.tasks {
		t := ts.Task
		t.State = deriveTask(ts)
		t.Deps, t.Files = append([]string(nil), t.Deps...), append([]string(nil), t.Files...)
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return taskLess(out[i].ID, out[j].ID) })
	return out
}

// deriveTask is the kanban column of a task: see TaskState.
func deriveTask(ts *taskState) TaskState {
	switch ts.Status {
	case "failed":
		return TaskFailed
	case "done":
		return TaskMerged
	case "review":
		if ts.Merge != "" {
			return TaskMerged
		}
		return TaskVerifying
	case "doing", "blocked":
		if ts.queued {
			return TaskVerifying
		}
		return TaskRunning
	}
	return TaskTodo
}

// boardSnapshot copies the board out.
func (s *State) boardSnapshot() Board {
	b := Board{Version: s.board.version, Tasks: s.sortedTasks(), Notes: len(s.board.notes), Dropped: s.board.dropped}
	for _, t := range b.Tasks {
		switch t.State {
		case TaskTodo:
			b.Counts.Todo++
		case TaskRunning:
			b.Counts.Running++
			if t.Status == "blocked" {
				b.Counts.Blocked++
			}
		case TaskVerifying:
			b.Counts.Verifying++
		case TaskMerged:
			b.Counts.Merged++
		case TaskFailed:
			b.Counts.Failed++
		}
	}
	if len(s.board.alerts) > 0 {
		b.Alerts = append([]Alert(nil), s.board.alerts...)
	}
	return b
}
