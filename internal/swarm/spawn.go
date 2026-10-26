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
	// Solo gives a top-level task of a role with several members to one member (Agent, or the first free one) without the role's
	// pool, and marks the task so (Task.Solo): it is for small work with one owner and no shared decision.
	Solo bool
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
	return fmt.Errorf("%d writers are already active (limit %d): concurrent writers collide on shared code. Wait for one to finish, reuse an idle worker, or use a read-only role (architect, scout, reviewer) for this", active, limit)
}

// Spawn starts (or reuses) a worker for a task and returns its id. Every decision
// that depends on the state of the swarm (the agent and writer limits, who owns the
// task, scope overlap, dependencies) is made under one lock and one board operation,
// so concurrent spawns, reuses and self-claims cannot exceed a limit or give two
// agents the same task; a refused spawn leaves nothing behind.
func (s *Swarm) Spawn(req SpawnReq) (string, error) {
	r, err := s.spawn(req)
	return r.Agent, err
}

// SpawnResult says what a spawn did.
type SpawnResult struct {
	// Agent is the worker that was started, or the member the task was reserved for.
	Agent string
	// Reserved reports that Agent, a member of the task's pool, was still running: the
	// task waits for it, todo and owned by it, and it was told to claim it (reserveChild).
	Reserved bool
}

// spawn is Spawn with the full result.
func (s *Swarm) spawn(req SpawnReq) (SpawnResult, error) {
	s.spawnMu.Lock()
	defer s.spawnMu.Unlock()
	started := func(id string, err error) (SpawnResult, error) { return SpawnResult{Agent: id}, err }
	if err := s.alive(); err != nil {
		return SpawnResult{}, err
	}
	if err := s.budgetErr(); err != nil {
		return SpawnResult{}, err
	}
	if req.TaskID == "" && strings.TrimSpace(req.Title) == "" {
		return SpawnResult{}, errors.New("spawn needs a task id or a title")
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
		return SpawnResult{}, err
	}
	if s.cfg.PersistentPools {
		if req.Role == "" && req.TaskID != "" {
			if task, ok := s.Board.Snapshot().Task(req.TaskID); ok {
				req.Role = task.Role
			}
		}
		counts, _ := TeamCounts(s.cfg.Team)
		task, _ := s.Board.Snapshot().Task(req.TaskID)
		if task.Solo {
			req.Solo = true // restarted, repaired or queued again: it stays with one member
		}
		// A pool agreeing on one parent's plan holds every member of the role, so the other tasks of that role wait behind it. While
		// such tasks are ready, the parent goes to one member and the rest of the role stays free for them: they run side by side.
		if !req.Solo && counts[req.Role] > 1 && task.Parent == "" && task.Pool == nil && len(readyBeside(s.Board.Snapshot(), req.Role, task.ID)) > 0 {
			req.Solo = true
		}
		if req.Solo && counts[req.Role] > 1 && task.Parent == "" && task.Pool == nil {
			if err := s.soloMember(&req); err != nil {
				return SpawnResult{}, err
			}
			return s.spawnReuse(req, files)
		}
		if counts[req.Role] > 1 && task.Parent == "" {
			if task.Status != StatusDoing || task.Pool == nil {
				return started(s.startPool(req, files))
			}
		}
		if req.Agent == "" {
			for _, m := range s.poolMembers(req.Role) {
				m.mu.Lock()
				idle := m.life == lifeIdle
				m.mu.Unlock()
				if idle {
					req.Agent = m.id
					break
				}
			}
		}
		if ms := s.poolMembers(req.Role); req.Agent == "" && len(ms) > 0 {
			// every member is in a run: the same wait as a role whose pools hold every member (noSpareMember), which the spawn
			// tool queues
			var held []string
			for _, m := range ms {
				m.mu.Lock()
				on := m.task
				m.mu.Unlock()
				if on != "" {
					held = append(held, m.id+" works on "+on)
				} else {
					held = append(held, m.id+" is in a run")
				}
			}
			return SpawnResult{}, &noSpareMember{role: req.Role, held: strings.Join(held, ", "),
				msg: fmt.Sprintf("%s pool is busy (%s); wait and reuse its existing members", req.Role, strings.Join(held, ", "))}
		}
	}
	if req.Agent != "" {
		return s.spawnReuse(req, files)
	}
	return started(s.spawnNew(req, files))
}

// readyBeside lists the top-level tasks of role, other than id, that could start now: unassigned, outside any pool, their
// dependencies done.
func readyBeside(sn *Snapshot, role, id string) []string {
	var out []string
	for _, t := range sn.Tasks {
		if t.ID == id || t.Role != role || t.Parent != "" || t.Status != StatusTodo || t.Owner != "" || t.Pool != nil {
			continue
		}
		if ok, _ := depsDone(sn, t); ok {
			out = append(out, t.ID)
		}
	}
	return out
}

// soloMember chooses the member of req.Role that takes a solo task (SpawnReq.Solo) and sets req.Agent to it: the named member when
// it is free, else the first free member in leadership order. A member is free when its run has ended, no open pool of the role
// keeps it (poolHolder) and it owns no other open solo task (soloTaskOf). The role's members are made first, idle, as for a pool, so the choice is among all of them. When none is
// free the answer is noSpareMember, which the spawn tool queues for an existing task. Caller holds spawnMu.
func (s *Swarm) soloMember(req *SpawnReq) error {
	members, err := s.ensurePool(req.Role)
	if err != nil {
		return err
	}
	snap := s.Board.Snapshot()
	busy := func(m *member) string { // what keeps m from the task, "" when it is free
		m.mu.Lock()
		idle, on := m.life == lifeIdle, m.task
		m.mu.Unlock()
		switch h := poolHolder(snap, m.id); {
		case h != "":
			return m.id + " is in " + h + "'s pool"
		case soloTaskOf(snap, m.id, req.TaskID) != "":
			return m.id + " works on " + soloTaskOf(snap, m.id, req.TaskID) + " alone"
		case idle:
			return ""
		case on != "":
			return m.id + " works on " + on
		}
		return m.id + " is in a run"
	}
	var held []string
	for _, m := range members {
		why := busy(m)
		switch {
		case req.Agent != "" && m.id == req.Agent && why != "":
			return fmt.Errorf("%s: give the task to a free %s member, or leave out agent= so it starts on the first free one", why, req.Role)
		case req.Agent != "" && m.id == req.Agent:
			return nil
		case req.Agent == "" && why == "":
			req.Agent = m.id
			return nil
		}
		held = append(held, why)
	}
	if req.Agent != "" {
		return nil // not a member of the role: spawnReuse answers for it
	}
	return &noSpareMember{role: req.Role, held: strings.Join(held, ", "),
		msg: fmt.Sprintf("no %s member is free for a solo task (%s); wait and reuse an existing member", req.Role, strings.Join(held, ", "))}
}

// assignFor builds the board operation that gives a task to an agent.
func (s *Swarm) assignFor(req SpawnReq, agentID string, role Role, files []string) assignReq {
	r := assignReq{by: req.By, agent: agentID, check: s.claimCheck(agentID, role.ReadOnly), solo: req.Solo}
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

func (s *Swarm) spawnReuse(req SpawnReq, files []string) (SpawnResult, error) {
	m := s.get(req.Agent)
	if m == nil || m.service {
		return SpawnResult{}, fmt.Errorf("no agent %q", cleanText(req.Agent, 40))
	}
	if m.manager {
		return SpawnResult{}, errors.New("the manager cannot be given a worker's task")
	}
	role := s.roles[m.role]
	transfer := false
	if req.TaskID != "" {
		sn := s.Board.Snapshot()
		if task, ok := sn.Task(req.TaskID); ok {
			transfer = leaderTransfer(sn, req.By, task)
			task.Owner = m.id
			if err := poolAssignment(sn, task); err != nil {
				return SpawnResult{}, err
			}
		}
	}
	if err := s.precheckScope(req, files, role); err != nil {
		return SpawnResult{}, err
	}
	rs, ctx, ok := s.reserve(m)
	if !ok {
		if s.cfg.PersistentPools {
			if r, handled, err := s.reserveChild(req, m, role, files, transfer); handled {
				return r, err
			}
			return SpawnResult{}, fmt.Errorf("%s is still working; mail it the existing task ID to claim, or wait and reuse it", m.id)
		}
		return SpawnResult{}, fmt.Errorf("%s is still working; wait for it or spawn a new worker", m.id)
	}
	if !role.ReadOnly && !s.isolated() {
		// The reserved member counts as active: the cap applies to reuse as well. (An
		// isolated run has no writer cap: every writer edits a tree of its own.)
		if w := s.activeWriters(); w > s.cfg.MaxWriters {
			s.unreserve(m, rs)
			return SpawnResult{}, writerCapError(w-1, s.cfg.MaxWriters)
		}
	}
	ar := s.assignFor(req, m.id, role, files)
	ar.transfer = transfer
	task, err := s.Board.assignTask(ar)
	if err != nil {
		s.unreserve(m, rs)
		return SpawnResult{}, err
	}
	s.mu.Lock()
	delete(s.poolReserved, task.ID)
	s.mu.Unlock()
	m.mu.Lock()
	m.task, m.gateTries = task.ID, 0
	m.recovered = false
	m.mailWakes, m.wakeLimited = 0, false
	m.mu.Unlock()
	m.madeProgress()
	s.emitAs(m.id, "agent.assign", map[string]any{"id": m.id, "role": m.role, "task": task.ID, "by": req.By})
	start := reassignStart(s.Board.Snapshot(), m)
	// A reused writer starts the new task from what has been merged since.
	if note, err := s.syncTree(ctx, m); err != nil {
		start.brief += "\n(The harness could not bring the merged work into your tree: " + cleanText(err.Error(), 160) + ". Merge the integration branch yourself before you start, or ask the manager.)"
	} else if note != "" {
		start.brief += "\n" + note
	}
	s.launch(m, rs, ctx, start)
	return SpawnResult{Agent: m.id}, nil
}

// leaderTransfer reports whether by is the leader of t's pool giving away a child it
// owns itself: a leader that claimed a child it meant for a member (a member it could
// not reach at that moment) can still assign it with spawn.
func leaderTransfer(sn *Snapshot, by string, t Task) bool {
	if by == "" || t.Owner != by || t.Parent == "" || (t.Status != StatusDoing && t.Status != StatusTodo) {
		return false
	}
	p, ok := sn.Task(t.Parent)
	return ok && p.Pool != nil && p.Owner == by
}

// reserveChild gives a child of an executing pool to a member of that pool whose run
// has not ended: it is waiting for its share, or finishing a turn, so it cannot be
// started, but it can be told. The child stays todo, owned by the member, so no one
// else claims it; the member is told by harness mail to claim it, and if its run ends
// first the repair pass starts it on the child (repairTick). A member with work of
// its own in progress is refused, with the members who have none. handled is false
// when this does not apply (not a pool child, or a member that is retiring).
func (s *Swarm) reserveChild(req SpawnReq, m *member, role Role, files []string, transfer bool) (SpawnResult, bool, error) {
	sn := s.Board.Snapshot()
	t, ok := sn.Task(strings.TrimSpace(req.TaskID))
	if !ok || t.Parent == "" {
		return SpawnResult{}, false, nil
	}
	p, ok := sn.Task(t.Parent)
	if !ok || p.Pool == nil {
		return SpawnResult{}, false, nil
	}
	m.mu.Lock()
	running := m.life == lifeRunning
	m.mu.Unlock()
	if !running {
		return SpawnResult{}, false, nil
	}
	if t.Owner == m.id && t.Status != StatusTodo {
		return SpawnResult{}, true, fmt.Errorf("%s already has %s (%s)", m.id, t.ID, t.Status)
	}
	if cur := inProgress(sn, m.id, t.ID, p.ID); cur != "" {
		var free []string
		for _, id := range p.Pool.Members {
			if id != m.id && inProgress(sn, id, t.ID, p.ID) == "" {
				free = append(free, id)
			}
		}
		if len(free) == 0 {
			return SpawnResult{}, true, fmt.Errorf("%s is working on %s and every member of %s's pool has a child in progress: wait for one to finish, then assign %s", m.id, cur, p.ID, t.ID)
		}
		return SpawnResult{}, true, fmt.Errorf("%s is working on %s: give %s to a member with no child in progress (%s), or wait for %s", m.id, cur, t.ID, strings.Join(free, ", "), m.id)
	}
	ar := s.assignFor(req, m.id, role, files)
	ar.reserve, ar.transfer = true, transfer
	got, err := s.Board.assignTask(ar)
	if err != nil {
		return SpawnResult{}, true, err
	}
	s.mu.Lock()
	s.poolReserved[got.ID] = m.id
	s.mu.Unlock()
	if t.Owner != m.id { // a repeated reservation tells the member nothing new
		s.notify(m.id, "request", fmt.Sprintf("%s (a child of %s) is reserved for you by %s: claim it now (task claim id=%s) and start; task get id=%s shows it.",
			got.ID, p.ID, safeToken(req.By, 24), got.ID, got.ID))
	}
	return SpawnResult{Agent: m.id, Reserved: true}, true, nil
}

// inProgress is the task agent is working on, other than skip and the pool parent
// parent: a doing or blocked task it owns, or "".
func inProgress(sn *Snapshot, agent, skip, parent string) string {
	for _, t := range sn.Tasks {
		if t.Owner == agent && t.ID != skip && t.ID != parent && (t.Status == StatusDoing || t.Status == StatusBlocked) {
			return t.ID
		}
	}
	return ""
}

func (s *Swarm) spawnNew(req SpawnReq, files []string) (string, error) {
	role, ok := s.roles[req.Role]
	if !ok || req.Role == "manager" || s.isService(req.Role) || s.hidden[req.Role] {
		return "", fmt.Errorf("unknown role %q (roles: %s)", cleanText(req.Role, 40), strings.Join(s.spawnableRoles(), ", "))
	}
	if err := s.precheckScope(req, files, role); err != nil {
		return "", err
	}
	s.mu.Lock()
	workers := 0
	for _, m := range s.members {
		if !m.manager && !m.service {
			workers++
		}
	}
	id := fmt.Sprintf("%s-%d", role.Short, s.seq[role.Name]+1)
	shared := s.shared
	slotErr := s.freeSlotErr(role.Name)
	s.mu.Unlock()
	if workers >= s.cfg.MaxWorkers {
		return "", fmt.Errorf("worker limit reached (%d); reuse an idle worker with spawn agent=… or wait", s.cfg.MaxWorkers)
	}
	if slotErr != nil {
		return "", slotErr
	}
	if !role.ReadOnly && !s.isolated() {
		// (An isolated run has no writer cap: every writer edits a tree of its own.)
		if w := s.activeWriters(); w >= s.cfg.MaxWriters {
			return "", writerCapError(w, s.cfg.MaxWriters)
		}
	}
	defer s.arrive(id)() // from the assignment until the agent is registered (or the spawn is undone)
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
		card += isolationCard(integrationRefOf(tree))
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
	s.emit(events.TypeAgentSpawn, map[string]any{"id": id, "role": role.Name, "task": task.ID, "by": req.By, "parent": req.By, "model": m.model.ID})
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
		if err := poolAssignment(sn, t); err != nil {
			return err
		}
		if t.Kind == TaskKindPlan && !readOnly {
			return fmt.Errorf("%s is a read-only planning task: the manager must use spawn with task=%s and role=architect (or scout, reviewer, or a custom read-only role); a writer cannot claim it", t.ID, t.ID)
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
// other tasks writers are working on. An entry that names a file in the shared
// tree covers that file alone (scopeFiles).
func (s *Swarm) scopeConflictIn(sn *Snapshot, t Task) error {
	file := s.scopeFiles()
	for _, o := range sn.Tasks {
		if o.Pool != nil || o.ID == t.ID || (t.ID != "" && o.Parent == t.ID) || len(o.Files) == 0 || (o.Status != StatusDoing && o.Status != StatusBlocked) {
			// a pool parent's own children are inside its scope by construction, not in its way
			continue
		}
		if s.ownerReadOnly(sn, o.Owner) {
			continue
		}
		for _, a := range t.Files {
			for _, b := range o.Files {
				if scopesOverlapIn(a, b, file) {
					this := t.ID
					if this == "" {
						this = "this task"
					}
					err := fmt.Sprintf("scope %q overlaps %s (%s, held by %s). Give the two tasks disjoint areas, or make %s depend on %s", a, o.ID, b, o.Owner, this, o.ID)
					if p, ok := sn.Task(t.Parent); ok && t.Parent != "" && t.Parent == o.Parent && p.Pool != nil && t.ID != "" {
						// two children of one pool: its leader can part them while this one is unclaimed (leadsUnclaimedChild)
						err += fmt.Sprintf("; %s's leader %s can narrow %s while nobody has claimed it: task update id=%s files=[...] inside %s's scope", p.ID, p.Owner, t.ID, t.ID, p.ID)
					}
					return errors.New(err)
				}
			}
		}
	}
	return nil
}

// scopeFiles returns the test scopesOverlapIn uses to tell a file from a directory: a relative scope entry without a glob names a
// file when it is a regular file under the repository root (Deps.Root, or Workdir without one). A directory, a path that does not
// exist yet, and every entry when no root is known keep the reading that covers what lies below them too. Answers are kept for
// the one check the function serves.
func (s *Swarm) scopeFiles() func(string) bool {
	root := s.deps.Root
	if root == "" {
		root = s.deps.Workdir
	}
	if root == "" {
		return nil
	}
	known := map[string]bool{}
	return func(p string) bool {
		isFile, ok := known[p]
		if !ok {
			fi, err := os.Stat(filepath.Join(root, filepath.FromSlash(p)))
			isFile = err == nil && fi.Mode().IsRegular()
			known[p] = isFile
		}
		return isFile
	}
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
	if s.cfg.PersistentPools {
		return fmt.Errorf("persistent pool members retain their context; reuse %s", id)
	}
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
//
// The spend is read after the agent is closed: Close waits for a compaction job,
// whose response can still be accounted while it stops, and a figure read before
// would leave that response out of the ledger for good. Until then the member is
// in retiring, which TotalCost and TotalListCost count as they count members, so
// the budget counts its spend exactly once throughout.
func (s *Swarm) detach(m *member) {
	s.mu.Lock()
	own := s.members[m.id] == m
	if own {
		delete(s.members, m.id)
		if s.retiring == nil {
			s.retiring = map[*member]struct{}{}
		}
		s.retiring[m] = struct{}{}
	}
	delete(s.lastSeen, m.id)
	s.mu.Unlock()
	m.stopTimers()
	_ = closeAgent(m.a) // cancels and waits for its compaction job, releases its archive index
	if own {
		cost, list, plan := agentSpend(m.a)
		s.mu.Lock()
		delete(s.retiring, m)
		s.spent += cost
		s.spentList += list
		s.spentPlan = s.spentPlan || plan
		s.mu.Unlock()
	}
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
