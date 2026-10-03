package swarm

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/kv"
	"github.com/anemos-labs/sleipnir/internal/workspace"
)

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

// alive reports why the swarm cannot take new work, if it cannot.
func (s *Swarm) alive() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.closed:
		return errors.New("the swarm has shut down")
	case s.rootCtx == nil || s.rootCtx.Err() != nil:
		return errors.New("swarm not started")
	}
	return nil
}

// isClosed reads the swarm shutdown flag under its lock.
func (s *Swarm) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// writerCapError reports the active-writer limit and suggests ways to continue without adding a
// concurrent writer.
func writerCapError(active, limit int) error {
	return fmt.Errorf("%d writers are already active (limit %d): concurrent writers collide on shared code. Wait for one to finish, reuse an idle worker, or use a read-only role (reviewer, scout) for this", active, limit)
}

// Spawn starts (or reuses) a worker for a task and returns its id. Every decision
// that depends on the state of the swarm (the agent and writer limits, who owns the
// task, scope overlap, dependencies) is made under one lock and one board operation,
// so concurrent spawns, reuses and self-claims cannot exceed a limit or give two
// agents the same task; a refused spawn leaves nothing behind.
func (s *Swarm) Spawn(req SpawnReq) (string, error) {
	s.spawnMu.Lock()
	defer s.spawnMu.Unlock()
	if err := s.alive(); err != nil {
		return "", err
	}
	if err := s.budgetErr(); err != nil {
		return "", err
	}
	if req.TaskID == "" && strings.TrimSpace(req.Title) == "" {
		return "", errors.New("spawn needs a task id or a title")
	}
	if req.Agent == "" && req.TaskID != "" {
		if task, ok := s.Board.Snapshot().Task(strings.TrimSpace(req.TaskID)); ok && task.Owner != "" {
			if m := s.get(task.Owner); m != nil {
				m.mu.Lock()
				if m.recovered {
					req.Agent = m.id
				}
				m.mu.Unlock()
			}
		}
	}
	files, err := s.normScopes(req.Files)
	if err != nil {
		return "", err
	}
	if req.Agent != "" {
		return s.spawnReuse(req, files)
	}
	return s.spawnNew(req, files)
}

// assignFor builds the board operation that gives a task to an agent.
func (s *Swarm) assignFor(req SpawnReq, agentID string, role Role, files []string) assignReq {
	r := assignReq{by: req.By, agent: agentID, check: s.claimCheck(agentID, role.ReadOnly)}
	if req.TaskID != "" {
		r.id = strings.TrimSpace(req.TaskID)
		if len(files) > 0 {
			r.spec, r.setScope = TaskSpec{Files: files}, true
		}
		return r
	}
	r.spec = TaskSpec{Title: req.Title, Desc: req.Brief, Role: role.Name, Files: files}
	return r
}

// precheckScope reports a scope overlap before the limits are consulted, so the
// manager is told the most useful thing first. The board repeats the check inside its
// critical section; this one is only for the message.
func (s *Swarm) precheckScope(req SpawnReq, files []string, role Role) error {
	if role.ReadOnly || s.isolated() {
		return nil
	}
	sn := s.Board.Snapshot()
	var t Task
	if req.TaskID != "" {
		t, _ = sn.Task(strings.TrimSpace(req.TaskID))
	}
	if len(files) > 0 {
		t.Files = files
	}
	if len(t.Files) == 0 {
		return nil
	}
	return s.scopeConflictIn(sn, t)
}

func (s *Swarm) spawnReuse(req SpawnReq, files []string) (string, error) {
	m := s.get(req.Agent)
	if m == nil || m.service {
		return "", fmt.Errorf("no agent %q", cleanText(req.Agent, 40))
	}
	if m.manager {
		return "", errors.New("the manager cannot be given a worker's task")
	}
	role := s.roles[m.role]
	if err := s.precheckScope(req, files, role); err != nil {
		return "", err
	}
	rs, ctx, ok := s.reserve(m)
	if !ok {
		return "", fmt.Errorf("%s is still working; wait for it or spawn a new worker", m.id)
	}
	if !role.ReadOnly && !s.isolated() {
		// The reserved member counts as active: the cap applies to reuse as well. (An
		// isolated run has no writer cap: every writer edits a tree of its own.)
		if w := s.activeWriters(); w > s.cfg.MaxWriters {
			s.unreserve(m, rs)
			return "", writerCapError(w-1, s.cfg.MaxWriters)
		}
	}
	task, err := s.Board.assignTask(s.assignFor(req, m.id, role, files))
	if err != nil {
		s.unreserve(m, rs)
		return "", err
	}
	m.mu.Lock()
	m.task, m.gateTries = task.ID, 0
	m.recovered = false
	m.mailWakes, m.wakeLimited = 0, false
	m.mu.Unlock()
	s.emitAs(m.id, "agent.assign", map[string]any{"id": m.id, "role": m.role, "task": task.ID, "by": req.By})
	start := reassignStart(s.Board.Snapshot(), m)
	// A reused writer starts the new task from what has been merged since.
	if note, err := s.syncTree(ctx, m); err != nil {
		start.brief += "\n(The harness could not bring the merged work into your tree: " + cleanText(err.Error(), 160) + ". Merge the integration branch yourself before you start, or ask the manager.)"
	} else if note != "" {
		start.brief += "\n" + note
	}
	s.launch(m, rs, ctx, start)
	return m.id, nil
}

func (s *Swarm) spawnNew(req SpawnReq, files []string) (string, error) {
	role, ok := s.roles[req.Role]
	if !ok || req.Role == "manager" || s.isService(req.Role) {
		return "", fmt.Errorf("unknown role %q (roles: %s)", cleanText(req.Role, 40), strings.Join(s.spawnableRoles(), ", "))
	}
	if err := s.precheckScope(req, files, role); err != nil {
		return "", err
	}
	s.mu.Lock()
	total := len(s.members)
	id := fmt.Sprintf("%s-%d", role.Short, s.seq[role.Name]+1)
	shared := s.shared
	s.mu.Unlock()
	if total >= s.cfg.MaxAgents {
		return "", fmt.Errorf("agent limit reached (%d); reuse an idle worker with spawn agent=… or wait", s.cfg.MaxAgents)
	}
	if !role.ReadOnly && !s.isolated() {
		// (An isolated run has no writer cap: every writer edits a tree of its own.)
		if w := s.activeWriters(); w >= s.cfg.MaxWriters {
			return "", writerCapError(w, s.cfg.MaxWriters)
		}
	}
	task, err := s.Board.assignTask(s.assignFor(req, id, role, files))
	if err != nil {
		return "", err
	}
	undo := func(err error) (string, error) {
		s.Board.Requeue(id, task.ID, task.Rev, "the worker could not be started", false, s.cfg.MaxAttempts)
		return "", err
	}
	if s.isolated() && s.deps.Isolation.Resumable {
		if _, err := s.deps.Events.Emit(id, "agent.prepare", map[string]any{"id": id, "role": role.Name, "task": task.ID}); err != nil {
			return undo(err)
		}
		if log, ok := s.deps.Events.(interface{ Flush() error }); ok {
			if err := log.Flush(); err != nil {
				return undo(err)
			}
		}
	}
	var tree *workspace.Tree
	card := taskCard(task, id, true)
	if s.isolated() && !role.ReadOnly {
		if tree, err = s.createTree(id); err != nil {
			return undo(fmt.Errorf("could not create a git worktree for %s: %w", id, err))
		}
		card += isolationCard
	}
	notes := kv.NewLayer("notes:"+id, kv.KindNotes, 1, []kv.Segment{{Key: "assignment", Text: card, Vol: kv.VolFrozen}})
	m, err := s.newMember(id, role, notes, NewEvidence(), tree)
	if err != nil {
		s.dropTree(tree)
		return undo(err)
	}
	rs, ctx, ok := s.reserve(m)
	if !ok {
		s.dropTree(tree)
		return undo(errors.New("the swarm is shutting down"))
	}
	s.mu.Lock()
	s.seq[role.Name]++
	s.mu.Unlock()
	s.register(m, shared)
	s.bindTree(m)
	m.mu.Lock()
	m.task = task.ID
	m.mu.Unlock()
	s.emit(events.TypeAgentSpawn, map[string]any{"id": id, "role": role.Name, "task": task.ID, "by": req.By, "parent": req.By, "model": s.modelFor(role.Name).ID})
	s.launch(m, rs, ctx, runStart{brief: taskCard(task, id, false), kickoff: true}) // the notes hold the card since the spawn
	return id, nil
}

// ---- scopes ------------------------------------------------------------------------

// scopeCheck is the board-side check that a task's scope does not overlap another
// active writer's. Read-only roles neither take part nor are blocked: they cannot
// write, so their tasks may share any area. In an isolated run writers do not
// collide (each edits a tree of its own; the merge queue settles overlaps), so
// overlapping scopes are allowed there: a scope still says which files a task may
// touch, and the queue enforces it.
func (s *Swarm) scopeCheck(readOnly bool) TaskCheck {
	return func(sn *Snapshot, t Task) error {
		if t.Kind == TaskKindPlan && !readOnly {
			return fmt.Errorf("%s is a read-only planning task: assign a scout, reviewer, or custom read-only role", t.ID)
		}
		if readOnly || len(t.Files) == 0 || s.isolated() {
			return nil
		}
		return s.scopeConflictIn(sn, t)
	}
}

// ownerReadOnly checks the owner's role in a board snapshot, returning false for an unknown owner.
func (s *Swarm) ownerReadOnly(sn *Snapshot, owner string) bool {
	a, ok := sn.Agent(owner)
	if !ok {
		return false
	}
	return s.roles[a.Role].ReadOnly
}

// scopeConflictIn reports overlap between a task's scope and the scopes of the
// other tasks writers are working on.
func (s *Swarm) scopeConflictIn(sn *Snapshot, t Task) error {
	for _, o := range sn.Tasks {
		if o.ID == t.ID || len(o.Files) == 0 || (o.Status != StatusDoing && o.Status != StatusBlocked) {
			continue
		}
		if s.ownerReadOnly(sn, o.Owner) {
			continue
		}
		for _, a := range t.Files {
			for _, b := range o.Files {
				if scopesOverlap(a, b) {
					return fmt.Errorf("scope %q overlaps %s (%s, held by %s). Give the two tasks disjoint areas, or make %s depend on %s", a, o.ID, b, o.Owner, t.ID, o.ID)
				}
			}
		}
	}
	return nil
}

// roots are the directories a scope is relative to.
func (s *Swarm) roots() []string {
	var rs []string
	for _, r := range []string{s.deps.Root, s.deps.Workdir} {
		if r == "" {
			continue
		}
		if abs, err := filepath.Abs(r); err == nil {
			r = abs
		}
		rs = append(rs, filepath.Clean(r))
	}
	if len(rs) == 0 {
		if wd, err := os.Getwd(); err == nil {
			rs = append(rs, wd)
		}
	}
	return rs
}

// normScopes validates a scope list from a model: absolute paths inside the
// repository become repo-relative, and everything is cleaned and bounded.
func (s *Swarm) normScopes(in []string) ([]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(in))
	for _, p := range in {
		p = strings.TrimSpace(p)
		if filepath.IsAbs(p) {
			done := false
			for _, r := range s.roots() {
				if rel, ok := relTo(r, p); ok {
					if rel == "." {
						rel = "**"
					}
					p, done = rel, true
					break
				}
			}
			if !done {
				return nil, fmt.Errorf("scope %q is outside the repository", cleanText(p, 80))
			}
		}
		out = append(out, p)
	}
	return cleanScopes(out)
}

// ---- retiring ----------------------------------------------------------------------

// Retire removes an idle agent that has nothing waiting for it.
func (s *Swarm) Retire(id string) error {
	m := s.get(id)
	if m == nil {
		return fmt.Errorf("no agent %q", id)
	}
	if m.manager {
		return fmt.Errorf("%s is the manager", id)
	}
	if m.service {
		return fmt.Errorf("%s is the harness's own agent", id)
	}
	m.mu.Lock()
	switch {
	case m.life == lifeRunning:
		m.mu.Unlock()
		return fmt.Errorf("%s is running", id)
	case m.life == lifeRetired:
		m.mu.Unlock()
		return fmt.Errorf("no agent %q", id)
	case !m.box.empty() || m.a.PendingInbox() > 0:
		m.mu.Unlock()
		return fmt.Errorf("%s has unread mail", id)
	}
	m.life = lifeRetired
	m.mu.Unlock()
	s.detach(m)
	return nil
}

// detach removes a retired member from the swarm: its spend goes into the budget
// ledger, its tasks return to todo, its leases and its board entry are dropped.
func (s *Swarm) detach(m *member) {
	s.mu.Lock()
	if s.members[m.id] == m {
		delete(s.members, m.id)
		_, c := m.a.Usage()
		s.spent += c
	}
	delete(s.lastSeen, m.id)
	s.mu.Unlock()
	m.stopTimers()
	_ = m.a.Close() // cancels and waits for its compaction job, releases its archive index
	requeued := s.Board.RequeueOwned(m.id, "its worker was retired")
	s.Leases.ReleaseAll(m.id)
	s.retireTree(m) // an isolated worker's tree goes with it when nothing would be lost
	m.pubMu.Lock()
	s.Board.RemoveAgent(m.id)
	m.pubMu.Unlock()
	if len(requeued) > 0 {
		ids := make([]string, len(requeued))
		for i, t := range requeued {
			ids[i] = t.ID
		}
		s.notifyManager(fmt.Sprintf("%s was retired; %s returned to todo", m.id, strings.Join(ids, ", ")))
	}
}
