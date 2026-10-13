package swarm

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Auto-docs (Config.AutoDocs) keeps the team's documentation current without the
// manager asking for it. Every task the board closes as done (an accepted
// implementation task or an accepted agreement) that the docs worker did not do
// becomes a pending change; when the docs worker is idle, or the team's docs slot is
// free, the harness turns all pending changes into one "docs: catch up" task and
// gives it to the docs worker (reusing an idle one, whose context is warm). It never
// interrupts a running docs worker, and a docs task's own acceptance is not a change,
// so the keeper cannot feed itself. The first pass waits for the first accepted
// implementation task: an agreement accepted before any code exists describes code
// that is not there yet, so it stays pending and is folded into that first pass.

// DocsRole is the role auto-docs gives its catch-up tasks to.
const DocsRole = "docs"

// DocsTaskTitle starts the title of every catch-up task the harness creates.
const DocsTaskTitle = "docs: catch up"

// docsScope is the scope of a catch-up task: Markdown files anywhere and the docs
// directory. Code stays with the roles that own it.
var docsScope = []string{"**/*.md", "docs/**"}

// docsRetry is how long the keeper waits before it tries again to start a catch-up
// task that could not be started (no free slot, a scope overlap, the budget).
const docsRetry = 30 * time.Second

// docsChange is one change waiting for the docs worker.
type docsChange struct {
	task, title, kind string
	files             []string
}

// docsKeeper is the auto-docs state of a swarm.
type docsKeeper struct {
	mu      sync.Mutex
	primed  bool
	seen    map[string]uint64 // last observed terminal revision; same-ID repairs are new changes
	pending []docsChange
	open    string    // the catch-up task in progress ("" none)
	last    string    // the last task a finished pass covered
	passes  int       // finished passes
	retryAt time.Time // no new attempt before this
	coded   bool      // an implementation task has been accepted: agreements alone may start a pass
}

// newDocsKeeper returns the keeper of a swarm whose team has a docs slot and asks
// for auto-docs, else nil.
func newDocsKeeper(cfg Config) *docsKeeper {
	if !cfg.AutoDocs {
		return nil
	}
	if n, _ := TeamCounts(cfg.Team); n[DocsRole] == 0 {
		return nil
	}
	return &docsKeeper{seen: map[string]uint64{}}
}

// AutoDocs reports whether the harness keeps the docs worker at work by itself.
func (s *Swarm) AutoDocs() bool { return s.docs != nil }

// docsTick is the keeper's supervisor step: collect newly finished tasks, close a
// finished pass, and start the next one when there is something to do and a docs
// worker to do it.
func (s *Swarm) docsTick(now time.Time) {
	k := s.docs
	if k == nil {
		return
	}
	snap := s.Board.Snapshot()
	k.mu.Lock()
	if !k.primed {
		// What was done before the keeper first looked (a resumed session) is not news.
		for _, t := range snap.Tasks {
			if t.Status == StatusDone || t.Status == StatusFailed {
				k.seen[t.ID] = t.Rev
				k.coded = k.coded || t.Status == StatusDone && s.implementationTask(t)
			}
		}
		k.primed = true
	}
	for _, t := range snap.Tasks {
		if rev, seen := k.seen[t.ID]; (seen && rev == t.Rev) || (t.Status != StatusDone && t.Status != StatusFailed) {
			continue
		}
		k.seen[t.ID] = t.Rev
		if t.Status != StatusDone || s.isDocsTask(t) || t.Kind != TaskKindPlan && !s.implementationTask(t) {
			continue
		}
		kind := "change"
		if t.Kind == TaskKindPlan {
			kind = "agreement"
		} else {
			k.coded = true
		}
		k.pending = append(k.pending, docsChange{task: t.ID, title: t.Title, kind: kind, files: t.Files})
	}
	if k.open != "" {
		t, ok := snap.Task(k.open)
		if ok && t.Status == StatusTodo && t.Owner == "" {
			// The pass went back to the board (its run was refused, interrupted or handed
			// back): it is the docs worker's again as soon as one is idle.
			open, due := k.open, !now.Before(k.retryAt)
			k.mu.Unlock()
			if agentID, busy := s.docsWorker(); due && !busy {
				if _, err := s.Spawn(SpawnReq{Role: DocsRole, Agent: agentID, TaskID: open, By: "harness"}); err != nil {
					k.mu.Lock()
					k.retryAt = now.Add(docsRetry)
					k.mu.Unlock()
				}
			}
			return
		}
		if ok && t.Status != StatusDone && t.Status != StatusFailed {
			k.mu.Unlock()
			return
		}
		if ok && t.Status == StatusDone {
			k.passes++
		}
		k.open = ""
	}
	if len(k.pending) == 0 || !k.coded || now.Before(k.retryAt) {
		k.mu.Unlock()
		return
	}
	batch := append([]docsChange(nil), k.pending...)
	k.mu.Unlock()

	agentID, busy := s.docsWorker()
	if busy {
		return // the docs worker is at work: the changes wait for it
	}
	// The pass is put on the board first and then assigned by id: a start that fails
	// leaves this one task in todo, which the next tick hands out again (above),
	// never a second task for the same changes.
	task, err := s.Board.CreateTask("harness", TaskSpec{Title: DocsTaskTitle + " (" + docsSummary(batch) + ")", Desc: docsBrief(batch), Role: DocsRole, Files: docsScope})
	k.mu.Lock()
	if err != nil {
		k.retryAt = now.Add(docsRetry)
		k.mu.Unlock()
		return
	}
	k.pending = k.pending[len(batch):]
	k.open, k.last = task.ID, batch[len(batch)-1].task
	k.mu.Unlock()
	id, err := s.Spawn(SpawnReq{Role: DocsRole, Agent: agentID, TaskID: task.ID, By: "harness"})
	if err != nil {
		k.mu.Lock()
		k.retryAt = now.Add(docsRetry)
		k.mu.Unlock()
		return
	}
	s.emit("docs.pass", map[string]any{"agent": id, "task": task.ID, "changes": len(batch)})
}

// isDocsTask reports whether t is the docs worker's own work: a catch-up task, or a
// task owned by a docs worker.
func (s *Swarm) isDocsTask(t Task) bool {
	if strings.HasPrefix(t.Title, DocsTaskTitle) {
		return true
	}
	if t.Role == DocsRole {
		return true
	}
	return t.Owner != "" && s.roleOf(t.Owner) == DocsRole
}

// docsWorker returns an idle docs worker to reuse ("" when there is none) and
// whether every docs worker is busy.
func (s *Swarm) docsWorker() (string, bool) {
	s.mu.Lock()
	var ms []*member
	for _, m := range s.members {
		if m.role == DocsRole && !m.manager && !m.service {
			ms = append(ms, m)
		}
	}
	s.mu.Unlock()
	sort.Slice(ms, func(i, j int) bool { return ms[i].id < ms[j].id })
	for _, m := range ms {
		m.mu.Lock()
		idle := m.life == lifeIdle
		m.mu.Unlock()
		if idle {
			return m.id, false
		}
	}
	if len(ms) == 0 {
		return "", false
	}
	s.mu.Lock()
	free := s.freeSlotErr(DocsRole) == nil
	s.mu.Unlock()
	return "", !free
}

// docsSummary is the short form of a batch for the task title: "T3, T5 and 2 more".
func docsSummary(b []docsChange) string {
	ids := make([]string, 0, len(b))
	for _, c := range b {
		ids = append(ids, c.task)
	}
	if len(ids) > 3 {
		return strings.Join(ids[:3], ", ") + fmt.Sprintf(" and %d more", len(ids)-3)
	}
	return strings.Join(ids, ", ")
}

// docsBrief is the catch-up task's description: what changed, and what to keep
// current.
func docsBrief(b []docsChange) string {
	var sb strings.Builder
	sb.WriteString("Standing docs pass, started by the harness. Read each change (task get <id>; the code it touched) and bring the documentation up to date with what is merged or accepted:\n")
	for _, c := range b {
		fmt.Fprintf(&sb, "- %s %s: %s", c.kind, c.task, cleanText(c.title, maxTitleRunes))
		if len(c.files) > 0 {
			fmt.Fprintf(&sb, " (%s)", strings.Join(c.files, ", "))
		}
		sb.WriteString("\n")
	}
	sb.WriteString("Keep three readers current: people (README, docs/), agents of later sessions (AGENTS.md: commands, layout, conventions, decisions), and this team (AGENTS.md is also its map). Change only what these changes made wrong or missing; keep it short and factual. Finish with task done and a one-line summary of what you changed.")
	return sb.String()
}

// DocsState is the keeper's line for the live view, or "" without auto-docs:
// "docs (auto): 2 changes pending · last pass after T7".
func (s *Swarm) DocsState() string {
	k := s.docs
	if k == nil {
		return ""
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	var parts []string
	switch {
	case k.open != "":
		parts = append(parts, "pass "+k.open+" in progress")
	case len(k.pending) > 0 && !k.coded:
		parts = append(parts, fmt.Sprintf("%d agreement(s) pending until the first implementation task is accepted", len(k.pending)))
	case len(k.pending) > 0:
		parts = append(parts, fmt.Sprintf("%d change(s) pending", len(k.pending)))
	default:
		parts = append(parts, "up to date")
	}
	if k.open != "" && len(k.pending) > 0 {
		parts = append(parts, fmt.Sprintf("%d more pending", len(k.pending)))
	}
	if k.last != "" {
		parts = append(parts, "last pass covered up to "+k.last)
	}
	return "docs (auto): " + strings.Join(parts, " · ")
}

// docsPending reports whether auto-docs still has work: a pass in progress, or pending
// changes that may start one (agreements alone, before any implementation task was
// accepted, start nothing and hold nobody).
func (s *Swarm) docsPending() bool {
	k := s.docs
	if k == nil {
		return false
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.open != "" || len(k.pending) > 0 && k.coded
}
