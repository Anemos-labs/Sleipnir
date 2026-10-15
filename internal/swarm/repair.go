package swarm

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/tools"
)

// repairTick retries failed assignments on their original worker. The supervisor
// owns retryAt; cancellation, permission waits and budget exhaustion never restart work.
func (s *Swarm) repairTick(now time.Time) {
	if !s.cfg.PersistentPools || s.wk.heldNow() || s.alive() != nil || s.budgetErr() != nil {
		return
	}
	if s.retryAt == nil {
		s.retryAt = map[string]time.Time{}
	}
	for _, t := range s.Board.Snapshot().Tasks {
		if t.Status != StatusTodo || t.LastOwner == "" || now.Before(s.retryAt[t.ID]) {
			continue
		}
		m := s.get(t.LastOwner)
		if m == nil {
			continue
		}
		m.mu.Lock()
		idle := m.life == lifeIdle
		asking := m.asking > 0
		m.mu.Unlock()
		if !idle || asking {
			continue
		}
		s.retryAt[t.ID] = now.Add(30 * time.Second)
		if _, err := s.Spawn(SpawnReq{Role: m.role, Agent: m.id, TaskID: t.ID, By: "harness"}); err != nil {
			continue
		}
		s.notify(m.id, "request", "Repair the existing task "+t.ID+" using its recorded failure evidence; do not replace it. Ask the pool leader or manager to revise the assignment if needed.")
	}
}

// revise changes a task in place after its current execution has stopped. Its
// result and evidence leave the board and remain in the event log; old submissions
// cannot pass the new revision.
func (b *Board) revise(by, id string, spec TaskSpec, reason string) error {
	return b.mutate(by, "requeue", func(d *draft) error {
		i := taskIdx(d.Snapshot, id)
		if i < 0 {
			return fmt.Errorf("no task %s", id)
		}
		t := d.Tasks[i]
		if spec.Kind != "" {
			if spec.Kind != "work" && spec.Kind != TaskKindPlan {
				return fmt.Errorf("task kind must be work or plan")
			}
			t.Kind = spec.Kind
			if t.Kind == "work" {
				t.Kind = ""
			}
		}
		if spec.Title != "" {
			t.Title = cleanText(spec.Title, maxTitleRunes)
		}
		if spec.Desc != "" {
			t.Desc = cleanBlock(spec.Desc, maxDescRunes)
		}
		if spec.Files != nil {
			files, err := cleanScopes(spec.Files)
			if err != nil {
				return err
			}
			t.Files = files
		}
		if spec.Deps != nil {
			if len(spec.Deps) > maxTaskDeps {
				return fmt.Errorf("too many dependencies")
			}
			for _, dep := range spec.Deps {
				if dep == id || taskIdx(d.Snapshot, dep) < 0 || dependsOn(d.Snapshot, dep, id, map[string]bool{}) {
					return fmt.Errorf("invalid dependency %s", dep)
				}
			}
			t.Deps = append([]string(nil), spec.Deps...)
		}
		if err := poolScopes(d.Snapshot, t); err != nil {
			return err
		}
		if t.Owner != "" {
			t.LastOwner = t.Owner
		}
		t.Owner = ""
		t.Status = StatusTodo
		t.Rev = d.Version
		t.Line = cleanText(reason, maxLineRunes)
		t.Result, t.Evidence = "", ""
		t.Agreement = ""
		t.Agreements = nil
		if t.Pool != nil {
			p := *t.Pool
			p.Phase = "planning"
			p.Revision++
			p.Votes = nil
			p.Inspected = nil
			t.Pool = &p
		}
		d.setNewTask(t)
		return d.put(i, t)
	})
}

// reviseTask stops the current owner before revising its assignment, and reopens
// the aggregate when a child's accepted work needs repair.
func (t *taskTool) reviseTask(ctx context.Context, c *tools.Call, in taskIn) *tools.Result {
	s := t.s
	before := s.Board.Snapshot()
	task, ok := before.Task(in.ID)
	if !ok {
		return tools.Errorf("no task %s", in.ID)
	}
	if strings.TrimSpace(in.Text) == "" {
		return tools.Errorf("revision needs text explaining the failure or changed requirements")
	}
	if in.Kind != "" && in.Kind != "work" && in.Kind != TaskKindPlan {
		return tools.Errorf("task kind must be work or plan")
	}
	if in.Kind == TaskKindPlan && !s.roles[task.Role].ReadOnly {
		return tools.Errorf("a planning task requires a read-only role")
	}
	if r := t.widenInPlace(c, in, task); r != nil {
		return r
	}
	owners := []string{task.Owner}
	if task.Pool != nil {
		owners = task.Pool.Members
	}
	leader := "" // the leader of the pool a revised child belongs to
	if p, ok := before.Task(task.Parent); ok && task.Parent != "" && p.Pool != nil {
		leader = p.Owner
	}
	for _, id := range owners {
		if id == leader {
			// The leader's own child: its run also leads the pool, which a revised child does not stop. The child goes back
			// to the board, the leader is told (childRevised), and its writes stop until it claims the child again.
			continue
		}
		m := s.get(id)
		if m == nil {
			continue
		}
		m.mu.Lock()
		rs := m.run
		m.mu.Unlock()
		if rs != nil {
			rs.cancel()
		}
		ticker := time.NewTicker(20 * time.Millisecond)
		for {
			m.mu.Lock()
			idle := m.life != lifeRunning
			m.mu.Unlock()
			if idle {
				break
			}
			select {
			case <-ctx.Done():
				ticker.Stop()
				return tools.Errorf("revision paused: %v", ctx.Err())
			case <-ticker.C:
			}
		}
		ticker.Stop()
	}
	if in.Action == "resolve" {
		if err := s.Board.resolve(c.Env.Agent, in.ID, in.Text); err != nil {
			return tools.Errorf("%v", err)
		}
		return text("%s complete: no longer needed (%s)", in.ID, in.Text)
	}
	if in.Files != nil {
		files, err := s.normScopes(in.Files)
		if err != nil {
			return tools.Errorf("%v", err)
		}
		in.Files = files
	}
	if err := s.Board.revise(c.Env.Agent, in.ID, TaskSpec{Title: in.Title, Desc: in.Description, Kind: in.Kind, Files: in.Files, Deps: in.Deps}, in.Text); err != nil {
		return tools.Errorf("%v", err)
	}
	// Accepted dependents must be checked again against the revised prerequisite.
	for _, dependent := range before.Tasks {
		if dependent.ID != task.ID && (dependent.Status == StatusDone || dependent.Status == StatusReview) && dependsOn(before, dependent.ID, task.ID, map[string]bool{}) {
			if err := s.Board.revise(c.Env.Agent, dependent.ID, TaskSpec{}, "prerequisite "+task.ID+" changed; revalidate the existing work"); err != nil {
				return tools.Errorf("%v", err)
			}
		}
	}
	if task.Parent != "" {
		if err := s.childRevised(c.Env.Agent, task, in.Text); err != nil {
			return tools.Errorf("parent repair: %v", err)
		}
	}
	s.review.mu.Lock()
	s.review.landLocked(task.ID, append(slices.Clone(task.Files), in.Files...)) // what it covered and what it covers now
	s.review.mu.Unlock()
	return text("%s revised in place; its existing worker will retry", in.ID)
}

// childRevised brings the pool parent of a revised child back in line with it, and tells the two agents it concerns: the
// pool's leader and the child's owner (by, who revised it, knows). It changes the parent no more than the revision requires:
//
//   - A parent whose combined result was being confirmed, or was submitted or accepted, has lost it: the pool goes back to
//     executing, and its revision moves on with no confirmation and no inspection, so a confirmation of the old result
//     can neither count nor arrive late. That is what keeps an agreement honest: a vote is for one revision.
//   - A parent that was submitted or accepted (review, done) gets a new assignment of the leader, a new rev, as a send-back
//     or a reopen gives one.
//   - Any other parent keeps its assignment and, unless it was reconciling, its phase, revision and votes, which a child's
//     revision does not touch. Its rev does not move while its leader works on it (doing, blocked), so the leader's run is
//     not told that its task changed under it; one that is not assigned (todo, after a revise of the pool itself put it
//     back to planning) stays so, and the pool agrees and executes before its children start again.
func (s *Swarm) childRevised(by string, child Task, why string) error {
	parent, ok := s.Board.Snapshot().Task(child.Parent)
	if !ok || parent.Pool == nil {
		return nil
	}
	if parent.Status == StatusDone {
		if err := s.Board.Reopen(by, parent.ID); err != nil {
			return err
		}
	}
	hadResult := parent.Status == StatusReview || parent.Status == StatusDone // a submitted or accepted result is no longer whole
	reset := false
	var cur Task
	err := s.Board.poolUpdate(by, parent.ID, func(sn *Snapshot, p *Task, state *PoolState) error {
		if hadResult {
			p.Rev = sn.Version
			p.Status = StatusDoing
			p.Owner = parent.Owner
			if p.Owner == "" {
				p.Owner = state.Members[0]
			}
		}
		if hadResult || state.Phase == "reconciling" {
			reset = state.Phase != "executing" || len(state.Votes) > 0 || len(state.Inspected) > 0
			state.Phase = "executing"
			state.Revision++
			state.Votes = nil
			state.Inspected = nil
		}
		cur = *p
		cur.Pool = state
		return nil
	})
	if err != nil {
		return err
	}
	why = cleanText(why, 200)
	var lead, own string
	switch {
	case child.Owner == cur.Owner:
		lead = fmt.Sprintf("%s revised your child %s (%s): it is todo again; claim it (task claim id=%s) to continue it.", by, child.ID, why, child.ID)
	case s.cfg.PersistentPools && child.Owner != "":
		lead = fmt.Sprintf("%s revised child %s of %s (%s): it is todo again, and the harness restarts %s on it.", by, child.ID, child.Owner, why, child.Owner)
		own = fmt.Sprintf("%s revised your child task %s (%s): stop work on it; the harness restarts you on it.", by, child.ID, why)
	default:
		lead = fmt.Sprintf("%s revised child %s (%s): it is todo again; assign it (spawn agent=MEMBER task=%s) or claim it.", by, child.ID, why, child.ID)
		if child.Owner != "" {
			own = fmt.Sprintf("%s revised your child task %s (%s): stop work on it; the leader %s assigns it again.", by, child.ID, why, cur.Owner)
		}
	}
	if reset {
		lead += " The pool is executing again: reconcile once every child is done."
	}
	tell := func(to, msg string) {
		if to != "" && to != by && msg != "" {
			s.notify(to, "request", fmt.Sprintf("Pool task %s, leader %s, phase %s, revision %d. %s Read task get %s for its scope.",
				cur.ID, cur.Owner, cur.Pool.Phase, cur.Pool.Revision, msg, child.ID))
		}
	}
	leader := cur.Owner
	if leader == "" {
		leader = cur.LastOwner // a parent back in todo: the leader the harness restarts on it
	}
	tell(leader, lead)
	tell(child.Owner, own)
	return nil
}

// resolve records an explicit disposition without representing abandoned work as verified.
func (b *Board) resolve(by, id, reason string) error {
	return b.mutate(by, "finish", func(d *draft) error {
		i := taskIdx(d.Snapshot, id)
		if i < 0 {
			return fmt.Errorf("no task %s", id)
		}
		for _, child := range d.Tasks {
			if child.Parent == id && child.Status != StatusDone {
				return fmt.Errorf("resolve child %s first", child.ID)
			}
		}
		t := d.Tasks[i]
		if t.Status == StatusDone {
			return fmt.Errorf("%s is already done", id)
		}
		if t.Owner != "" {
			t.LastOwner = t.Owner
		}
		t.Owner = ""
		t.Rev = d.Version
		t.Status = StatusDone
		t.Closure = closeAs(CloseUnneeded)
		t.Result = cleanText(reason, maxResultRunes)
		return d.put(i, t)
	})
}

// dependsOn detects transitive dependency cycles before a revision is published.
func dependsOn(sn *Snapshot, from, target string, seen map[string]bool) bool {
	if from == target {
		return true
	}
	if seen[from] {
		return false
	}
	seen[from] = true
	t, ok := sn.Task(from)
	if !ok {
		return false
	}
	for _, dep := range t.Deps {
		if dependsOn(sn, dep, target, seen) {
			return true
		}
	}
	return false
}

// widenInPlace handles a revise whose only change is a wider scope of a task that is under way: the globs it had and more, nothing
// else revised. That is what update files does, so it is done the same way: the scope grows in place and nobody stops. A revise
// otherwise stops the task's workers, sends its children back to the board and puts a pool back to planning, which in a trial
// threw away two workers' progress and made a pool agree again because the manager added .gitignore to its scope. It returns nil
// for any other revise.
func (t *taskTool) widenInPlace(c *tools.Call, in taskIn, task Task) *tools.Result {
	if in.Action != "revise" || in.Files == nil || in.Title != "" || in.Description != "" || in.Kind != "" || in.Deps != nil ||
		task.Status != StatusDoing {
		return nil
	}
	files, err := t.s.normScopes(in.Files)
	if err != nil {
		return nil // the revise reports it
	}
	added := 0
	for _, f := range files {
		if !slices.Contains(task.Files, f) {
			added++
		}
	}
	for _, f := range task.Files {
		if !slices.Contains(files, f) {
			return nil // a narrower or different scope: a real revision
		}
	}
	if added == 0 {
		return nil
	}
	// a planning task, or one a read-only role's agent holds, is held to the read-only check, as when it was assigned (a task's role
	// is only the one suggested for it; its owner is who works on it), judged on the board the scope change is made against
	check := func(sn *Snapshot, cand Task) error {
		return t.s.scopeCheck(cand.Kind == TaskKindPlan || t.s.ownerReadOnly(sn, cand.Owner))(sn, cand)
	}
	if err := t.s.Board.widenScope(c.Env.Agent, task, files, check); err != nil {
		return tools.Errorf("cannot widen the scope of %s: %v", in.ID, err)
	}
	return text("%s: scope widened in place (%s); its workers keep going. (%s) Widening a scope needs no revise: update with files does it.",
		in.ID, strings.Join(files, ", "), cleanText(in.Text, 200))
}

// widenScope sets the wider scope files on the task as it was read (was): inside the board mutation the task must still be doing, at
// the same revision, with every glob it had, or nothing changes. A revision or a requeue between the read and the change would
// otherwise be overwritten with a scope computed from the old task, and the caller would report workers kept going that had stopped.
func (b *Board) widenScope(by string, was Task, files []string, check TaskCheck) error {
	return b.mutate(by, "scope", func(d *draft) error {
		i := taskIdx(d.Snapshot, was.ID)
		if i < 0 {
			return fmt.Errorf("no task %s", was.ID)
		}
		t := d.Tasks[i]
		if t.Status != StatusDoing || t.Rev != was.Rev || t.Owner != was.Owner {
			return fmt.Errorf("%s changed while its scope was being widened (now %s, owner %q): read it again with task get", was.ID, t.Status, t.Owner)
		}
		for _, f := range t.Files {
			if !slices.Contains(files, f) {
				return fmt.Errorf("%s's scope changed while it was being widened: read it again with task get", was.ID)
			}
		}
		nf, err := cleanScopes(files)
		if err != nil {
			return err
		}
		t.Files = nf
		if err := poolScopes(d.Snapshot, t); err != nil {
			return err
		}
		if err := check(d.Snapshot, t); err != nil {
			return err
		}
		return d.put(i, t)
	})
}
