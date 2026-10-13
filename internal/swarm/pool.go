package swarm

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/tools"
	"github.com/anemos-labs/sleipnir/internal/workspace"
)

// PoolState is a parent's persistent cooperation protocol. Votes name members
// that acknowledged the current revision; every change copies the slices.
type PoolState struct {
	Members    []string `json:"members"`
	Phase      string   `json:"phase"`
	Revision   int      `json:"revision"`
	Agreement  string   `json:"agreement"`
	Votes      []string `json:"votes"`
	Inspected  []string `json:"inspected,omitempty"`
	Generation int      `json:"generation"`
}

// poolUpdate serializes a cooperation step with task transitions.
func (b *Board) poolUpdate(by, id string, fn func(*Snapshot, *Task, *PoolState) error) error {
	return b.mutate(by, "update", func(d *draft) error {
		i := taskIdx(d.Snapshot, id)
		if i < 0 {
			return fmt.Errorf("no task %s", id)
		}
		t := d.Tasks[i]
		if t.Pool == nil || t.Status == StatusDone || t.Status == StatusFailed {
			return fmt.Errorf("%s is not an active pool task", id)
		}
		p := *t.Pool
		p.Members = slices.Clone(p.Members)
		p.Votes = slices.Clone(p.Votes)
		p.Inspected = slices.Clone(p.Inspected)
		if err := fn(d.Snapshot, &t, &p); err != nil {
			return err
		}
		t.Pool = &p
		return d.put(i, t)
	})
}

// beginPool binds a ready task to the role's persistent members.
func (b *Board) beginPool(by, id string, members []string) error {
	return b.mutate(by, "assign", func(d *draft) error {
		i := taskIdx(d.Snapshot, id)
		if i < 0 || len(members) == 0 {
			return fmt.Errorf("pool needs a task and members")
		}
		t := d.Tasks[i]
		if t.Status != StatusTodo || t.Parent != "" {
			return fmt.Errorf("%s is not an unassigned parent task", id)
		}
		if ok, why := depsDone(d.Snapshot, t); !ok {
			return fmt.Errorf("dependency %s is not complete", why.ID)
		}
		t.Owner, t.Status, t.Rev = members[0], StatusDoing, d.Version
		t.Pool = &PoolState{Members: slices.Clone(members), Phase: "planning", Revision: 1}
		return d.put(i, t)
	})
}

// poolMembers returns members in configuration order, which defines leadership.
func (s *Swarm) poolMembers(role string) []*member {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*member
	for _, m := range s.members {
		if m.role == role && !m.manager && !m.service {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].slot == out[j].slot {
			return out[i].id < out[j].id
		}
		return out[i].slot < out[j].slot
	})
	return out
}

// ensurePool allocates every configured member once. Caller holds spawnMu.
func (s *Swarm) ensurePool(role string) ([]*member, error) {
	counts, _ := TeamCounts(s.cfg.Team)
	r, ok := s.roles[role]
	if !ok || counts[role] == 0 {
		return nil, fmt.Errorf("no pool for role %q", role)
	}
	for len(s.poolMembers(role)) < counts[role] {
		s.mu.Lock()
		id := fmt.Sprintf("%s-%d", r.Short, s.seq[role]+1)
		s.mu.Unlock()
		if s.isolated() && s.deps.Isolation.Resumable {
			if _, err := s.deps.Events.Emit(id, "agent.prepare", map[string]any{"id": id, "role": role}); err != nil {
				return nil, err
			}
			if log, ok := s.deps.Events.(interface{ Flush() error }); ok {
				if err := log.Flush(); err != nil {
					return nil, err
				}
			}
		}
		var tree *workspace.Tree
		var err error
		if s.isolated() && !r.ReadOnly {
			tree, err = s.createTree(id)
			if err != nil {
				return nil, err
			}
		}
		m, err := s.newMember(id, r, nil, NewEvidence(), tree)
		if err != nil {
			s.dropTree(tree)
			return nil, err
		}
		s.mu.Lock()
		s.seq[role]++
		s.mu.Unlock()
		s.register(m, s.currentShared())
		s.bindTree(m)
		m.setState(s, "idle", "")
		s.emit(events.TypeAgentSpawn, map[string]any{"id": id, "role": role, "model": m.model.ID})
	}
	return s.poolMembers(role), nil
}

// startPool assigns a parent and wakes its members to cooperate, not to write.
func (s *Swarm) startPool(req SpawnReq, files []string) (string, error) {
	members, err := s.ensurePool(req.Role)
	if err != nil {
		return "", err
	}
	for _, t := range s.Board.Snapshot().Tasks {
		if t.Role == req.Role && t.Pool != nil && t.Status != StatusDone && t.Status != StatusFailed && t.ID != req.TaskID {
			return "", fmt.Errorf("%s pool is working on %s; wait before assigning another parent", req.Role, t.ID)
		}
	}
	id := req.TaskID
	if id == "" {
		t, e := s.Board.CreateTask(req.By, TaskSpec{Title: req.Title, Desc: req.Brief, Files: files, Role: req.Role})
		if e != nil {
			return "", e
		}
		id = t.ID
	}
	ids := make([]string, len(members))
	for i, m := range members {
		ids[i] = m.id
	}
	if err := s.Board.beginPool(req.By, id, ids); err != nil {
		return "", err
	}
	s.wakePool(id, "Plan together. The first member leads. Use pool propose, agree, execute, reconcile and confirm. The leader creates scoped child tasks with task create parent="+id+" and assigns them with spawn (use task claim for its own child). All members contribute before execution, and inspect the combined result before confirming reconciliation.")
	return ids[0], nil
}

// wakePool uses harness mail so idle members retain their existing conversation.
func (s *Swarm) wakePool(id, message string) {
	t, ok := s.Board.Snapshot().Task(id)
	if !ok || t.Pool == nil {
		return
	}
	for _, member := range t.Pool.Members {
		s.notify(member, "request", fmt.Sprintf("Pool task %s, leader %s, phase %s, revision %d. %s Read task get for the full shared state.", id, t.Owner, t.Pool.Phase, t.Pool.Revision, message))
	}
}

// poolPermission makes planning and reconciliation read-only for pool members.
func (s *Swarm) poolPermission(ctx context.Context, id string, inner perm.Requester, req perm.Request) (perm.Decision, bool) {
	for _, t := range s.Board.Snapshot().Tasks {
		if t.Pool == nil || t.Status == StatusDone || t.Status == StatusFailed || !slices.Contains(t.Pool.Members, id) {
			continue
		}
		if t.Pool.Phase == "executing" {
			for _, child := range s.Board.Snapshot().Tasks {
				if child.Parent == t.ID && child.Owner == id && child.Status == StatusDoing {
					return perm.Decision{}, false
				}
			}
		}
		if e, ok := inner.(*perm.Engine); ok {
			return e.PlanOnly().Check(ctx, req), true
		}
		if req.Writes || (req.Tool == "bash" && !readOnlyCommand(req.Command)) {
			return perm.Decision{Reason: "pool members may write only their assigned subtasks after agreement"}, true
		}
	}
	return perm.Decision{}, false
}

// poolTool exposes cooperation without granting worker-creation authority.
type poolTool struct{ s *Swarm }

// Spec describes the persistent pool protocol shared by every agent.
func (t *poolTool) Spec() core.ToolSpec {
	return core.ToolSpec{Name: "pool", NoTreeWrites: true, Description: "Cooperate on a role pool's parent task. Leader: propose an agreement (theme/style, interfaces, work division, acceptance); execute after everyone agrees; reconcile after all children are accepted. Every member: call inspect during reconciliation to update your checkout, examine the combined result, then confirm. Agree to the proposal or confirm the reconciled result, with the exact revision from task get. Use mail to discuss objections; the manager resolves disagreements. During execution the leader may create scoped child tasks and assign existing members. Return one parent result with task done after every member confirms.", InputSchema: json.RawMessage(`{"type":"object","properties":{"action":{"type":"string","enum":["propose","agree","execute","reconcile","inspect","confirm"]},"id":{"type":"string"},"revision":{"type":"integer"},"text":{"type":"string"}},"required":["action","id"]}`)}
}

// Run applies one phase transition after checking leadership and membership.
func (t *poolTool) Run(ctx context.Context, c *tools.Call) (*tools.Result, error) {
	var in struct {
		Action, ID, Text string
		Revision         int
	}
	if r := decode(c.Input, &in); r != nil {
		return r, nil
	}
	s, by := t.s, c.Env.Agent
	agreement, err := cleanAgreement(in.Text)
	if err != nil {
		return tools.Errorf("%v", err), nil
	}
	if in.Action == "inspect" {
		parent, ok := s.Board.Snapshot().Task(in.ID)
		if !ok || parent.Pool == nil || parent.Pool.Phase != "reconciling" || !slices.Contains(parent.Pool.Members, by) {
			return tools.Errorf("only a reconciling pool member can inspect"), nil
		}
		if m := s.get(by); m != nil {
			note, err := s.syncTree(ctx, m)
			if err != nil || note != "" {
				return tools.Errorf("repair checkout before confirming: %s %v", note, err), nil
			}
		}
	}
	err = s.Board.poolUpdate(by, in.ID, func(sn *Snapshot, task *Task, p *PoolState) error {
		leader := by == task.Owner || c.Env.Role == "manager"
		member := slices.Contains(p.Members, by)
		switch in.Action {
		case "propose":
			if !leader || p.Phase != "planning" || agreement == "" {
				return fmt.Errorf("leader must propose a nonempty agreement during planning")
			}
			p.Agreement = agreement
			p.Revision++
			p.Votes = nil
			p.Inspected = nil
		case "inspect":
			if !member || p.Phase != "reconciling" {
				return fmt.Errorf("pool is not reconciling")
			}
			if !slices.Contains(p.Inspected, by) {
				p.Inspected = append(p.Inspected, by)
			}
		case "agree", "confirm":
			phase := "planning"
			if in.Action == "confirm" {
				phase = "reconciling"
				if err := s.poolCurrent(sn, *task); err != nil {
					return err
				}
				if s.isolated() && !slices.Contains(p.Inspected, by) {
					return fmt.Errorf("call pool inspect, read the combined result, then confirm")
				}
			}
			if !member || p.Phase != phase || p.Agreement == "" || in.Revision != p.Revision {
				return fmt.Errorf("read the current pool agreement and revision before acknowledging %s", phase)
			}
			if !slices.Contains(p.Votes, by) {
				p.Votes = append(p.Votes, by)
				sort.Strings(p.Votes)
			}
		case "execute":
			if !leader || p.Phase != "planning" || len(p.Votes) != len(p.Members) {
				return fmt.Errorf("every pool member must agree before execution")
			}
			p.Phase = "executing"
			p.Votes = nil
		case "reconcile":
			if !leader || (p.Phase != "executing" && p.Phase != "reconciling") {
				return fmt.Errorf("only the leader starts reconciliation after execution")
			}
			for _, child := range sn.Tasks {
				if child.Parent == task.ID && child.Status != StatusDone {
					return fmt.Errorf("child %s is not complete", child.ID)
				}
			}
			p.Phase = "reconciling"
			task.Status = StatusDoing
			s.review.mu.Lock()
			p.Generation = s.review.merges
			s.review.mu.Unlock()
			p.Revision++
			p.Votes = nil
			p.Inspected = nil
		default:
			return fmt.Errorf("unknown pool action")
		}
		return nil
	})
	if err != nil {
		return tools.Errorf("%v", err), nil
	}
	if in.Action == "reconcile" && s.isolated() {
		rep := s.Integrate(ctx)
		if !rep.Applied && rep.News() {
			_ = s.Board.poolUpdate(by, in.ID, func(_ *Snapshot, _ *Task, p *PoolState) error { p.Phase = "executing"; return nil })
			return tools.Errorf("combined result could not be applied: %s", rep.Message), nil
		}
	}
	if in.Action == "propose" || in.Action == "execute" || in.Action == "reconcile" {
		s.wakePool(in.ID, "Pool updated: "+in.Action+". "+agreement)
	} else {
		task, _ := s.Board.Snapshot().Task(in.ID)
		s.notify(task.Owner, "info", by+" recorded "+in.Action+" for "+in.ID)
	}
	return text("%s: %s recorded", in.ID, in.Action), nil
}

// poolComplete prevents aggregate submission before all members reconcile.
func poolComplete(sn *Snapshot, t Task) error {
	if t.Pool != nil {
		for _, child := range sn.Tasks {
			if child.Parent == t.ID && child.Status != StatusDone {
				return fmt.Errorf("child %s is not done", child.ID)
			}
		}
	}
	if t.Pool != nil && (t.Pool.Phase != "reconciling" || len(t.Pool.Votes) != len(t.Pool.Members)) {
		return fmt.Errorf("%s needs reconciliation confirmed by every pool member", t.ID)
	}
	return nil
}

// poolChildAuthor permits a leader to create only children of its active pool.
func (s *Swarm) poolChildAuthor(by, parent, role string) bool {
	p, ok := s.Board.Snapshot().Task(parent)
	return ok && p.Owner == by && p.Pool != nil && p.Pool.Phase == "executing" && p.Role == role
}

// poolAssignment verifies that children stay in their agreed pool and parent scope.
func poolAssignment(sn *Snapshot, t Task) error {
	if t.Parent == "" {
		return nil
	}
	p, ok := sn.Task(t.Parent)
	if !ok || p.Pool == nil || p.Pool.Phase != "executing" || p.Status != StatusDoing {
		return fmt.Errorf("parent pool must be executing")
	}
	if t.Role != p.Role {
		return fmt.Errorf("child role must match its parent pool")
	}
	if t.Owner != "" && !slices.Contains(p.Pool.Members, t.Owner) {
		return fmt.Errorf("child owner must be a member of its parent pool")
	}
	return poolScope(p, t)
}

// poolScope prevents child revisions from broadening their parent's file scope.
func poolScope(p, t Task) error {
	if len(p.Files) > 0 {
		if len(t.Files) == 0 {
			return fmt.Errorf("child needs an explicit scope inside its parent")
		}
		for _, f := range t.Files {
			if !slices.Contains(p.Files, f) && !scopeContains(p.Files, f) {
				return fmt.Errorf("child scope %s is outside parent scope", f)
			}
		}
	}
	return nil
}

// scopeContains admits exact paths and recursive descendants, never a broader glob.
func scopeContains(parents []string, child string) bool {
	for _, p := range parents {
		if p == "**" || p == child {
			return true
		}
		if strings.HasSuffix(p, "/**") && strings.HasPrefix(child, strings.TrimSuffix(p, "**")) {
			return true
		}
	}
	return false
}

// poolCurrent rejects votes or submissions over a superseded combined result.
func (s *Swarm) poolCurrent(_ *Snapshot, task Task) error {
	if task.Pool == nil {
		return nil
	}
	s.review.mu.Lock()
	generation := s.review.merges
	s.review.mu.Unlock()
	if task.Pool.Generation != generation {
		return fmt.Errorf("combined result changed; restart pool reconcile, inspect and confirm")
	}
	return nil
}

// poolScopes validates a task against its parent and preserves existing child scopes.
func poolScopes(sn *Snapshot, t Task) error {
	if t.Parent != "" {
		parent, ok := sn.Task(t.Parent)
		if !ok {
			return fmt.Errorf("parent %s is missing", t.Parent)
		}
		if err := poolScope(parent, t); err != nil {
			return err
		}
	}
	for _, child := range sn.Tasks {
		if child.Parent == t.ID {
			if err := poolScope(t, child); err != nil {
				return err
			}
		}
	}
	return nil
}
