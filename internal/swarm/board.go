// Package swarm coordinates many agents over one repository.
//
// Its shared state is deliberately tiny and always fresh: a task board, one-line
// agent statuses, a buffer of proposed shared notes and a few alerts. Every
// agent sees a personalised, budget-capped rendering of it at the tail of its
// prompt (the hot layer), which is what lets a manager dispatch dozens of
// workers without writing each a briefing: they already share the cached
// project context, and the board tells them what everyone else is doing.
package swarm

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/reee344/sleipnir/internal/events"
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
	Result string     `json:"result,omitempty"` // completion summary
	Files  []string   `json:"files,omitempty"`  // areas it touches (informational)
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

// Alert is a short-lived warning (a lease conflict, a stalled agent).
type Alert struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// Snapshot is an immutable view of the board. Readers never lock.
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

// Board is the mutable holder of the current snapshot. Writers serialise on a
// mutex and publish a fresh snapshot; readers load an atomic pointer.
type Board struct {
	mu   sync.Mutex
	snap atomic.Pointer[Snapshot]
	next int
	note int
	ev   events.Emitter
	wake chan struct{}
}

// NewBoard returns an empty board.
func NewBoard(ev events.Emitter) *Board {
	if ev == nil {
		ev = events.Discard{}
	}
	b := &Board{ev: ev, wake: make(chan struct{})}
	b.snap.Store(&Snapshot{})
	return b
}

// Snapshot returns the current immutable view.
func (b *Board) Snapshot() *Snapshot { return b.snap.Load() }

// Changed returns a channel closed at the next mutation; waiters re-read the
// snapshot when it fires.
func (b *Board) Changed() <-chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.wake
}

// mutate applies fn to a copy of the snapshot, bumps the version and publishes.
func (b *Board) mutate(actor, op string, fn func(s *Snapshot) error) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	cur := b.snap.Load()
	cp := &Snapshot{
		Version: cur.Version + 1,
		Tasks:   append([]Task(nil), cur.Tasks...),
		Agents:  append([]AgentInfo(nil), cur.Agents...),
		Notes:   append([]Note(nil), cur.Notes...),
		Alerts:  append([]Alert(nil), cur.Alerts...),
	}
	if err := fn(cp); err != nil {
		return err
	}
	b.snap.Store(cp)
	_, _ = b.ev.Emit(actor, events.TypeBoardOp, map[string]any{"op": op, "version": cp.Version})
	close(b.wake)
	b.wake = make(chan struct{})
	return nil
}

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

// CreateTask adds a todo task.
func (b *Board) CreateTask(by string, spec TaskSpec) (Task, error) {
	var out Task
	title := strings.TrimSpace(spec.Title)
	if title == "" {
		return out, fmt.Errorf("task needs a title")
	}
	err := b.mutate(by, "create", func(s *Snapshot) error {
		for _, d := range spec.Deps {
			if taskIdx(s, d) < 0 {
				return fmt.Errorf("dependency %s does not exist", d)
			}
		}
		b.next++
		out = Task{ID: "T" + strconv.Itoa(b.next), Title: title, Desc: strings.TrimSpace(spec.Desc), Status: StatusTodo, Role: spec.Role, Deps: spec.Deps, Files: spec.Files}
		s.Tasks = append(s.Tasks, out)
		return nil
	})
	return out, err
}

// depsDone reports whether every dependency of t is done.
func depsDone(s *Snapshot, t Task) (bool, string) {
	for _, d := range t.Deps {
		if i := taskIdx(s, d); i >= 0 && s.Tasks[i].Status != StatusDone {
			return false, d
		}
	}
	return true, ""
}

// Claim assigns a todo (or blocked-by-self) task to an agent.
func (b *Board) Claim(agent, id string) error {
	return b.mutate(agent, "claim", func(s *Snapshot) error {
		i := taskIdx(s, id)
		if i < 0 {
			return fmt.Errorf("no task %s", id)
		}
		t := s.Tasks[i]
		if t.Owner != "" && t.Owner != agent {
			return fmt.Errorf("%s is already owned by %s", id, t.Owner)
		}
		if t.Status == StatusDone || t.Status == StatusFailed {
			return fmt.Errorf("%s is already %s", id, t.Status)
		}
		if ok, dep := depsDone(s, t); !ok {
			return fmt.Errorf("%s waits for %s", id, dep)
		}
		t.Owner, t.Status = agent, StatusDoing
		s.Tasks[i] = t
		return nil
	})
}

// Assign force-assigns a task (used by the manager when spawning).
func (b *Board) Assign(by, agent, id string) error {
	return b.mutate(by, "assign", func(s *Snapshot) error {
		i := taskIdx(s, id)
		if i < 0 {
			return fmt.Errorf("no task %s", id)
		}
		t := s.Tasks[i]
		if t.Status == StatusDone {
			return fmt.Errorf("%s is already done", id)
		}
		t.Owner, t.Status = agent, StatusDoing
		s.Tasks[i] = t
		return nil
	})
}

// Update sets a task's one-line progress.
func (b *Board) Update(agent, id, line string) error {
	return b.mutate(agent, "update", func(s *Snapshot) error {
		i := taskIdx(s, id)
		if i < 0 {
			return fmt.Errorf("no task %s", id)
		}
		if s.Tasks[i].Owner != agent {
			return fmt.Errorf("%s belongs to %s", id, ownerOrNone(s.Tasks[i].Owner))
		}
		s.Tasks[i].Line = oneLine(line, 140)
		return nil
	})
}

// Finish completes a task with a result line.
func (b *Board) Finish(agent, id string, status TaskStatus, result string) error {
	if status != StatusDone && status != StatusFailed && status != StatusReview {
		return fmt.Errorf("bad final status %q", status)
	}
	return b.mutate(agent, "finish", func(s *Snapshot) error {
		i := taskIdx(s, id)
		if i < 0 {
			return fmt.Errorf("no task %s", id)
		}
		if s.Tasks[i].Owner != agent && agent != "manager" {
			return fmt.Errorf("%s belongs to %s", id, ownerOrNone(s.Tasks[i].Owner))
		}
		s.Tasks[i].Status = status
		s.Tasks[i].Result = oneLine(result, 200)
		s.Tasks[i].Line = ""
		return nil
	})
}

// Block marks a task blocked with a reason.
func (b *Board) Block(agent, id, reason string) error {
	return b.mutate(agent, "block", func(s *Snapshot) error {
		i := taskIdx(s, id)
		if i < 0 {
			return fmt.Errorf("no task %s", id)
		}
		if s.Tasks[i].Owner != agent {
			return fmt.Errorf("%s belongs to %s", id, ownerOrNone(s.Tasks[i].Owner))
		}
		s.Tasks[i].Status = StatusBlocked
		s.Tasks[i].Line = oneLine(reason, 140)
		return nil
	})
}

// Resume returns a blocked task to doing.
func (b *Board) Resume(agent, id string) error {
	return b.mutate(agent, "resume", func(s *Snapshot) error {
		i := taskIdx(s, id)
		if i < 0 {
			return fmt.Errorf("no task %s", id)
		}
		if s.Tasks[i].Owner != agent {
			return fmt.Errorf("%s belongs to %s", id, ownerOrNone(s.Tasks[i].Owner))
		}
		s.Tasks[i].Status = StatusDoing
		s.Tasks[i].Line = ""
		return nil
	})
}

// SetAgent upserts an agent's status.
func (b *Board) SetAgent(info AgentInfo) {
	_ = b.mutate(info.ID, "agent", func(s *Snapshot) error {
		for i := range s.Agents {
			if s.Agents[i].ID == info.ID {
				s.Agents[i] = info
				return nil
			}
		}
		s.Agents = append(s.Agents, info)
		sort.Slice(s.Agents, func(i, j int) bool { return s.Agents[i].ID < s.Agents[j].ID })
		return nil
	})
}

// RemoveAgent drops an agent from the roster.
func (b *Board) RemoveAgent(id string) {
	_ = b.mutate(id, "agent-remove", func(s *Snapshot) error {
		out := s.Agents[:0:0]
		for _, a := range s.Agents {
			if a.ID != id {
				out = append(out, a)
			}
		}
		s.Agents = out
		return nil
	})
}

// AddNote records a fact proposed for the shared context. Identical text is
// ignored, so several agents discovering the same convention add it once.
func (b *Board) AddNote(from, scope, role, text string) (int, error) {
	text = oneLine(text, 300)
	if text == "" {
		return 0, fmt.Errorf("empty note")
	}
	if scope != "shared" && scope != "role" {
		return 0, fmt.Errorf("scope must be shared or role")
	}
	id := 0
	err := b.mutate(from, "note", func(s *Snapshot) error {
		for _, n := range s.Notes {
			if n.Text == text && n.Scope == scope && n.Role == role {
				id = n.ID
				return nil
			}
		}
		b.note++
		id = b.note
		s.Notes = append(s.Notes, Note{ID: id, From: from, Scope: scope, Role: role, Text: text})
		return nil
	})
	return id, err
}

// TakeNotes removes and returns pending notes (curator merging them).
func (b *Board) TakeNotes(ids ...int) []Note {
	var taken []Note
	_ = b.mutate("curator", "notes-take", func(s *Snapshot) error {
		want := map[int]bool{}
		for _, i := range ids {
			want[i] = true
		}
		keep := s.Notes[:0:0]
		for _, n := range s.Notes {
			if len(want) == 0 || want[n.ID] {
				taken = append(taken, n)
			} else {
				keep = append(keep, n)
			}
		}
		s.Notes = keep
		return nil
	})
	return taken
}

// RaiseAlert adds an alert unless an identical one is already showing.
func (b *Board) RaiseAlert(kind, text string) {
	text = oneLine(text, 160)
	_ = b.mutate("harness", "alert", func(s *Snapshot) error {
		for _, a := range s.Alerts {
			if a.Kind == kind && a.Text == text {
				return nil
			}
		}
		s.Alerts = append(s.Alerts, Alert{Kind: kind, Text: text})
		if len(s.Alerts) > 8 {
			s.Alerts = s.Alerts[len(s.Alerts)-8:]
		}
		return nil
	})
}

// ClearAlerts removes alerts of a kind.
func (b *Board) ClearAlerts(kind string) {
	_ = b.mutate("harness", "alert-clear", func(s *Snapshot) error {
		keep := s.Alerts[:0:0]
		for _, a := range s.Alerts {
			if a.Kind != kind {
				keep = append(keep, a)
			}
		}
		s.Alerts = keep
		return nil
	})
}

func ownerOrNone(o string) string {
	if o == "" {
		return "nobody"
	}
	return o
}

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if max > 0 && len(r) > max {
		return string(r[:max-1]) + "…"
	}
	return s
}

// Wait blocks until the board changes or the timeout passes. It returns the new
// snapshot.
func (b *Board) Wait(after uint64, timeout time.Duration) *Snapshot {
	deadline := time.After(timeout)
	for {
		if s := b.Snapshot(); s.Version > after {
			return s
		}
		select {
		case <-b.Changed():
		case <-deadline:
			return b.Snapshot()
		}
	}
}
