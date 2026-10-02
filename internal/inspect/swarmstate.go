package inspect

import (
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	maxRecentMail   = 300
	maxRecentLeases = 300
	maxSpawns       = 4000
	maxMailPairs    = 5000
)

// swarmState accumulates coordination activity: board operations, mail, spawns,
// leases and the request concurrency the governor is there to shape.
type swarmState struct {
	spawns    []SpawnView
	nSpawns   int
	boardOps  int
	boardBy   map[string]int
	alerts    int
	mailSent  int
	mailDeliv int
	mailKinds map[string]int
	pairs     map[[2]string]int
	mail      []*mailRec
	mailByID  map[string]*mailRec
	leases    []LeaseView
	nLease    int
	minutes   map[int64]*minuteBucket

	// isolation, mailman and supervise: see isolation.go.
	isolation isoState
	mailman   mailmanState
	supervise superviseState

	inflight     int
	peakInflight int
	govEvents    int
	govLast      map[string]any
	errKinds     map[string]int
	retries      int
	rateLimited  int
	requests     int
}

type mailRec struct {
	MailView
	sent time.Time
}

func newSwarmState() swarmState {
	return swarmState{
		boardBy: map[string]int{}, mailKinds: map[string]int{}, pairs: map[[2]string]int{},
		mailByID: map[string]*mailRec{}, minutes: map[int64]*minuteBucket{}, errKinds: map[string]int{},
	}
}

func (w *swarmState) minute(ts time.Time) *minuteBucket {
	k := ts.Unix() / 60
	m := w.minutes[k]
	if m == nil {
		m = &minuteBucket{}
		w.minutes[k] = m
	}
	return m
}

// oneLine collapses whitespace and cuts to n runes: log strings are shown in
// single-line cells and must never be able to break the layout.
func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if n > 0 && len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// ---- mail --------------------------------------------------------------------

func (s *Session) onMailSend(raw json.RawMessage, agentID string, ts time.Time) {
	var p struct{ ID, From, To, Kind, Text string }
	if json.Unmarshal(raw, &p) != nil {
		s.badPay++
		return
	}
	if p.From == "" {
		p.From = agentID
	}
	w := &s.swarm
	w.mailSent++
	w.minute(ts).mailSent++
	kind := p.Kind
	if kind == "" {
		kind = "info"
	}
	if len(w.mailKinds) < 32 || w.mailKinds[kind] > 0 {
		w.mailKinds[kind]++
	}
	pk := [2]string{p.From, p.To}
	if len(w.pairs) < maxMailPairs || w.pairs[pk] > 0 {
		w.pairs[pk]++
	}
	m := &mailRec{MailView: MailView{T: ts, ID: p.ID, From: p.From, To: p.To, Kind: kind, Text: oneLine(p.Text, 240), LatencyMs: -1}, sent: ts}
	w.mail = append(w.mail, m)
	if p.ID != "" {
		w.mailByID[p.ID] = m
	}
	if len(w.mail) > maxRecentMail {
		old := w.mail[0]
		w.mail = w.mail[1:]
		if old.ID != "" && w.mailByID[old.ID] == old {
			delete(w.mailByID, old.ID)
		}
	}
}

func (s *Session) onMailDeliver(raw json.RawMessage, ts time.Time) {
	var p struct{ ID, From string }
	if json.Unmarshal(raw, &p) != nil {
		s.badPay++
		return
	}
	w := &s.swarm
	w.mailDeliv++
	w.minute(ts).mailDeliv++
	if m := w.mailByID[p.ID]; m != nil && m.LatencyMs < 0 {
		m.LatencyMs = ts.Sub(m.sent).Milliseconds()
	}
}

// ---- leases and governor (logged generically) --------------------------------

func (s *Session) onLease(raw json.RawMessage, agentID string, ts time.Time) {
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	w := &s.swarm
	w.nLease++
	w.minute(ts).leases++
	str := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := m[k].(string); ok && v != "" {
				return oneLine(v, 200)
			}
		}
		return ""
	}
	lv := LeaseView{T: ts, Agent: firstNonEmpty(str("agent", "by"), agentID), Action: str("action", "op", "kind", "event"), Path: str("path", "file"), Holder: str("holder", "owner")}
	w.leases = append(w.leases, lv)
	if len(w.leases) > maxRecentLeases {
		w.leases = w.leases[len(w.leases)-maxRecentLeases:]
	}
}

func (s *Session) onGovernor(raw json.RawMessage) {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return
	}
	s.swarm.govEvents++
	// Keep only scalars: the payload is untrusted and shown as-is.
	last := map[string]any{}
	for k, v := range m {
		switch x := v.(type) {
		case float64, bool:
			last[k] = x
		case string:
			last[k] = oneLine(x, 120)
		}
		if len(last) >= 24 {
			break
		}
	}
	s.swarm.govLast = last
}

func firstNonEmpty(a ...string) string {
	for _, s := range a {
		if s != "" {
			return s
		}
	}
	return ""
}

// ---- board reconstruction ------------------------------------------------------

var taskIDRe = regexp.MustCompile(`^T\d+$`)

// boardTracker rebuilds the task board from the log. The swarm logs the full
// state of a task on every board.op ("exact" logs: the event carries the task's
// rev), and then the events are the truth. Older logs record only the operation
// and the board version, so the details come from the task and spawn tool calls
// that caused them (their arguments are logged), from agent.spawn (task id,
// owner) and from agent.end; fields a board.op does carry win either way.
type boardTracker struct {
	tasks     map[string]*boardTask
	created   int
	pending   map[string][]*pendingCall
	agentTask map[string]string
	manager   string
	touched   bool
	exact     bool // some board.op carried the full task state
}

type boardTask struct {
	TaskView
	num int
}

type pendingCall struct {
	id   string
	call *boardCall
}

func newBoardTracker() *boardTracker {
	return &boardTracker{tasks: map[string]*boardTask{}, pending: map[string][]*pendingCall{}, agentTask: map[string]string{}}
}

// parseBoardCall extracts the arguments of a task or spawn call.
func parseBoardCall(name string, input json.RawMessage) *boardCall {
	if name != "task" && name != "spawn" {
		return nil
	}
	var in struct {
		Action, ID, Title, Description, Role, Text, Task, Brief, Agent string
		Deps, Files                                                    []string
	}
	if json.Unmarshal(input, &in) != nil {
		return nil
	}
	bc := &boardCall{name: name, action: in.Action, id: in.ID, title: oneLine(in.Title, 160), desc: oneLine(in.Description, 240),
		text: oneLine(in.Text, 200), role: oneLine(in.Role, 40), deps: capStrings(in.Deps, 16), files: capStrings(in.Files, 16)}
	if name == "spawn" {
		task := strings.TrimSpace(in.Task)
		if taskIDRe.MatchString(task) {
			bc.id = task
		} else {
			bc.title = oneLine(task, 160)
		}
		bc.desc = oneLine(in.Brief, 240)
		bc.agent = oneLine(in.Agent, 80)
	}
	return bc
}

func capStrings(in []string, n int) []string {
	if len(in) > n {
		in = in[:n]
	}
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = oneLine(s, 120)
	}
	return out
}

func (b *boardTracker) onToolCall(agent, callID string, bc *boardCall) {
	if bc == nil {
		return
	}
	q := b.pending[agent]
	if len(q) >= 16 {
		q = q[1:]
	}
	b.pending[agent] = append(q, &pendingCall{id: callID, call: bc})
}

func (b *boardTracker) onToolResult(agent, callID string) {
	q := b.pending[agent]
	for i, pc := range q {
		if pc.id == callID {
			b.pending[agent] = append(q[:i:i], q[i+1:]...)
			if len(b.pending[agent]) == 0 {
				delete(b.pending, agent)
			}
			return
		}
	}
}

// current is the newest open task/spawn call of an actor. The router and the
// swarm sometimes name the actor "manager" instead of the manager's agent id.
func (b *boardTracker) current(actor string) *boardCall {
	if actor == "manager" && b.manager != "" {
		actor = b.manager
	}
	q := b.pending[actor]
	if len(q) == 0 {
		return nil
	}
	return q[len(q)-1].call
}

func (b *boardTracker) ensure(id string, ts time.Time) *boardTask {
	t := b.tasks[id]
	if t == nil {
		if len(b.tasks) >= 20000 {
			return nil
		}
		n, _ := strconv.Atoi(strings.TrimPrefix(id, "T"))
		t = &boardTask{TaskView: TaskView{ID: id, Status: "todo", Created: ts, Updated: ts}, num: n}
		b.tasks[id] = t
	}
	return t
}

func (b *boardTracker) onBoardOp(actor, op string, raw json.RawMessage, ts time.Time) {
	var extra struct {
		Task, ID, Title, Status, Owner, Line, Result, Evidence, Role string
		Attempts                                                     int
		Rev                                                          *uint64  // present when the event carries the full task state
		Deps, Files, Tasks                                           []string // Tasks: what a requeue returned to the queue
	}
	_ = json.Unmarshal(raw, &extra)
	call := b.current(actor)
	id := firstNonEmpty(extra.Task, extra.ID)
	full := extra.Rev != nil && id != ""
	set := func(t *boardTask) {
		if t == nil {
			return
		}
		t.Updated = ts
		if full {
			// The event is the task after the operation: an empty owner, line or
			// result means cleared, not unknown.
			b.exact = true
			t.Status, t.Owner = extra.Status, extra.Owner
			t.Line, t.Result = oneLine(extra.Line, 160), oneLine(extra.Result, 200)
			t.Evidence, t.Attempts = oneLine(extra.Evidence, 200), extra.Attempts
			t.Files = capStrings(extra.Files, 16)
			if extra.Title != "" {
				t.Title = oneLine(extra.Title, 160)
				t.Role, t.Deps = oneLine(extra.Role, 40), capStrings(extra.Deps, 16)
			}
			b.touched = true
			return
		}
		if extra.Title != "" {
			t.Title = oneLine(extra.Title, 160)
		}
		if extra.Status != "" {
			t.Status = extra.Status
		}
		if extra.Owner != "" {
			t.Owner = extra.Owner
		}
		if extra.Line != "" {
			t.Line = oneLine(extra.Line, 160)
		}
		if extra.Result != "" {
			t.Result = oneLine(extra.Result, 200)
		}
		if extra.Evidence != "" {
			t.Evidence = oneLine(extra.Evidence, 200)
		}
		if extra.Attempts > 0 {
			t.Attempts = extra.Attempts
		}
		b.touched = true
	}
	if op == "requeue" && id == "" && len(extra.Tasks) > 0 {
		// The harness returned all of a stopped worker's tasks to the queue.
		for _, tid := range extra.Tasks {
			if t := b.ensure2(tid, ts); t != nil {
				t.Status, t.Owner, t.Line, t.Updated = "todo", "", oneLine(extra.Line, 160), ts
				b.touched, b.exact = true, b.exact || extra.Rev != nil
			}
		}
		return
	}
	switch op {
	case "create":
		b.created++
		if id == "" {
			id = "T" + strconv.Itoa(b.created)
		}
		t := b.ensure(id, ts)
		if t == nil {
			return
		}
		if call != nil {
			t.Title = firstNonEmpty(call.title, t.Title)
			if t.Title == "" {
				t.Title = call.desc
			}
			t.Role, t.Deps, t.Files = call.role, call.deps, call.files
			call.created = id
		}
		set(t)
	case "claim":
		if id == "" && call != nil {
			id = call.id
		}
		if t := b.ensure2(id, ts); t != nil {
			t.Owner, t.Status = actor, "doing"
			b.agentTask[actor] = id
			set(t)
		}
	case "assign":
		if call != nil {
			id = firstNonEmpty(id, call.created, call.id)
		}
		if t := b.ensure2(id, ts); t != nil {
			t.Status = "doing"
			if call != nil && call.name == "spawn" && call.agent != "" {
				t.Owner = call.agent
				b.agentTask[call.agent] = id
			}
			set(t)
		}
	case "update":
		if id == "" && call != nil {
			id = call.id
		}
		if t := b.ensure2(id, ts); t != nil {
			if call != nil && call.text != "" && extra.Line == "" {
				t.Line = call.text
			}
			set(t)
		}
	case "scope":
		if id == "" && call != nil {
			id = call.id
		}
		if t := b.ensure2(id, ts); t != nil {
			if call != nil && len(call.files) > 0 {
				t.Files = call.files
			}
			set(t)
		}
	case "finish":
		status := "review"
		result := ""
		if call != nil && call.name == "task" {
			id = firstNonEmpty(id, call.id)
			switch call.action {
			case "accept":
				status = "done"
			case "done":
				status = "review"
			}
			result = call.text
		} else if id == "" {
			// The harness closes a worker's task when its run ends.
			id = b.agentTask[actor]
		}
		if t := b.ensure2(id, ts); t != nil {
			t.Status, t.Line = status, ""
			switch {
			case result == "":
			case call != nil && call.action == "accept":
				t.Result = oneLine(t.Result+" "+result, 200) // the manager's note is appended to the worker's result
			default:
				t.Result = result
			}
			set(t)
		}
	case "block":
		if id == "" && call != nil {
			id = call.id
		}
		if t := b.ensure2(id, ts); t != nil {
			t.Status = "blocked"
			if call != nil {
				t.Line = call.text
			}
			set(t)
		}
	case "resume":
		if id == "" && call != nil {
			id = call.id
		}
		if t := b.ensure2(id, ts); t != nil {
			t.Status, t.Line = "doing", ""
			set(t)
		}
	case "requeue":
		// One task went back to the queue (or failed, after too many attempts); only
		// logs with the full task state say which, so there is nothing to guess.
		if t := b.ensure2(id, ts); t != nil && full {
			set(t)
		}
	}
}

// ensure2 finds a task by id, creating a placeholder for ids seen only by
// reference (a log that starts mid-session); the placeholder has no title.
func (b *boardTracker) ensure2(id string, ts time.Time) *boardTask {
	if id == "" {
		return nil
	}
	if !taskIDRe.MatchString(id) && b.tasks[id] == nil {
		return nil
	}
	return b.ensure(id, ts)
}

func (b *boardTracker) onSpawn(id, role, task, by string, ts time.Time) {
	if role == "manager" {
		b.manager = id
	}
	if task == "" {
		return
	}
	if t := b.ensure2(task, ts); t != nil {
		t.Owner, t.Status, t.Updated = id, "doing", ts
		if t.Role == "" {
			t.Role = role
		}
		b.agentTask[id] = task
		b.touched = true
	}
	_ = by
}

func (b *boardTracker) onAgentEnd(id, state string, ts time.Time) {
	if state != "failed" {
		return
	}
	if t := b.tasks[b.agentTask[id]]; t != nil && t.Owner == id && (t.Status == "review" || t.Status == "doing") {
		t.Status, t.Updated = "failed", ts
	}
}

func (b *boardTracker) views() []TaskView {
	out := make([]*boardTask, 0, len(b.tasks))
	for _, t := range b.tasks {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].num != out[j].num {
			return out[i].num < out[j].num
		}
		return out[i].ID < out[j].ID
	})
	res := make([]TaskView, len(out))
	for i, t := range out {
		res[i] = t.TaskView
	}
	return res
}
