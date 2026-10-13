package swarm

import (
	"context"
	"fmt"
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
// evidence remains in the event log; old submissions cannot pass the new revision.
func (b *Board) revise(by, id string, spec TaskSpec, reason string) error {
	return b.mutate(by, "requeue", func(d *draft) error {
		i := taskIdx(d.Snapshot, id)
		if i < 0 {
			return fmt.Errorf("no task %s", id)
		}
		t := d.Tasks[i]
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
	owners := []string{task.Owner}
	if task.Pool != nil {
		owners = task.Pool.Members
	}
	for _, id := range owners {
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
	if err := s.Board.revise(c.Env.Agent, in.ID, TaskSpec{Title: in.Title, Desc: in.Description, Files: in.Files, Deps: in.Deps}, in.Text); err != nil {
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
		parent, _ := s.Board.Snapshot().Task(task.Parent)
		if parent.Status == StatusDone {
			if err := s.Board.Reopen(c.Env.Agent, parent.ID); err != nil {
				return tools.Errorf("parent repair: %v", err)
			}
		}
		if err := s.Board.poolUpdate(c.Env.Agent, parent.ID, func(sn *Snapshot, p *Task, state *PoolState) error {
			p.Rev = sn.Version
			p.Status = StatusDoing
			p.Owner = parent.Owner
			if p.Owner == "" {
				p.Owner = state.Members[0]
			}
			state.Phase = "executing"
			state.Revision++
			state.Votes = nil
			state.Inspected = nil
			return nil
		}); err != nil {
			return tools.Errorf("parent repair: %v", err)
		}
		s.wakePool(parent.ID, "Child "+task.ID+" was revised. Repair the existing children and reconcile the combined result again.")
	}
	s.review.mu.Lock()
	s.review.merges++
	s.review.last = task.ID
	s.review.mu.Unlock()
	return text("%s revised in place; its existing worker will retry", in.ID)
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
