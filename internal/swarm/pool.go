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
	// Excused are the members the manager took out of the pool (pool excuse): they no longer vote, and stay read-only while the pool
	// task is open.
	Excused []string `json:"excused,omitempty"`
}

// poolUpdate serializes a cooperation step with task transitions.
func (b *Board) poolUpdate(by, id string, fn func(*Snapshot, *Task, *PoolState) error) error {
	return b.mutate(by, "update", func(d *draft) error {
		i := taskIdx(d.Snapshot, id)
		if i < 0 {
			return fmt.Errorf("no task %s: pool acts on a role pool's parent task, one the manager assigned (spawn) to a role with several members; task list shows them", id)
		}
		t := d.Tasks[i]
		if t.Pool == nil || t.Status == StatusDone || t.Status == StatusFailed {
			return fmt.Errorf("%s is not an active pool task: pool acts on a role pool's parent task while it is open; task list shows them", id)
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
		if i < 0 {
			return fmt.Errorf("pool needs a task and members")
		}
		t := d.Tasks[i]
		var excused []string
		if t.Pool != nil {
			excused = t.Pool.Excused
			members = slices.DeleteFunc(slices.Clone(members), func(m string) bool { return slices.Contains(excused, m) })
		}
		if len(members) == 0 {
			return fmt.Errorf("pool needs a task and members")
		}
		if t.Status != StatusTodo || t.Parent != "" {
			return fmt.Errorf("%s is not an unassigned parent task", id)
		}
		if ok, why := depsDone(d.Snapshot, t); !ok {
			return fmt.Errorf("dependency %s is not complete", why.ID)
		}
		t.Owner, t.Status, t.Rev = members[0], StatusDoing, d.Version
		if t.Pool != nil && slices.Equal(t.Pool.Members, members) {
			p := *t.Pool
			if p.Phase == "reconciling" {
				// The resumed checkout must be inspected again before acceptance.
				p.Phase = "executing"
				p.Revision++
				p.Votes, p.Inspected = nil, nil
			}
			t.Pool = &p
		} else {
			t.Pool = &PoolState{Members: slices.Clone(members), Phase: "planning", Revision: 1, Excused: slices.Clone(excused)}
		}
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
	task, _ := s.Board.Snapshot().Task(id) // its owner leads: the first member the manager has not excused
	if task.Pool.Phase == "executing" {
		s.wakePool(id, "Continue the accepted plan and existing child tasks; do not recreate them. Reconcile, inspect and confirm the combined result when the children are complete.")
	} else {
		s.wakePool(id, "Plan together. The first member leads. Use pool propose, agree, execute, reconcile and confirm. The leader creates scoped child tasks with task create parent="+id+" and assigns them with spawn (use task claim for its own child). All members contribute before execution, and inspect the combined result before confirming reconciliation.")
	}
	return task.Owner, nil
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

// poolPermission makes planning and reconciliation read-only for pool members. A refusal says why in the pool's terms (poolReadOnly):
// the plan-mode engine's own words ("present a plan instead of acting", "no one to ask") would send a member that is waiting for its
// subtask round in circles.
func (s *Swarm) poolPermission(ctx context.Context, id string, inner perm.Requester, req perm.Request) (perm.Decision, bool) {
	for _, t := range s.Board.Snapshot().Tasks {
		if t.Pool == nil || t.Status == StatusDone || t.Status == StatusFailed || !slices.Contains(t.Pool.Members, id) && !slices.Contains(t.Pool.Excused, id) {
			continue
		}
		if t.Pool.Phase == "executing" && slices.Contains(t.Pool.Members, id) { // an excused member writes nothing, its children included
			for _, child := range s.Board.Snapshot().Tasks {
				if child.Parent == t.ID && child.Owner == id && child.Status == StatusDoing {
					return perm.Decision{}, false
				}
			}
		}
		if e, ok := inner.(*perm.Engine); ok {
			d := e.PlanOnly().Check(ctx, req)
			if !d.Allow {
				d.Reason = poolReadOnly(t, id) + " Refused: " + planCause(d.Reason)
			}
			return d, true
		}
		if req.Writes || (req.Tool == "bash" && !readOnlyCommand(req.Command)) {
			return perm.Decision{Reason: poolReadOnly(t, id)}, true
		}
	}
	return perm.Decision{}, false
}

// poolReadOnly says why a member of the pool task t may not change anything now, and what lets it: agreement and execution while the
// pool plans, a child task of its own while it executes, confirmation while it reconciles.
func poolReadOnly(t Task, id string) string {
	head := fmt.Sprintf("You are a member of pool task %s (phase %s, revision %d, leader %s): members change nothing", t.ID, t.Pool.Phase, t.Pool.Revision, t.Owner)
	switch t.Pool.Phase {
	case "executing":
		if t.Owner == id {
			return head + " outside a child task. As the leader, create a child task with task create parent=" + t.ID + " and claim it (task claim), or assign it with spawn."
		}
		return head + " outside a child task assigned to them. Ask the leader " + t.Owner + " for one with mail, or wait for its assignment; read the code meanwhile."
	case "reconciling":
		return head + " while the pool reconciles. Inspect the combined result (pool inspect) and confirm it, or mail the leader " + t.Owner + " what is wrong."
	default:
		return head + " until every member agrees and the leader executes. Take part with pool propose or agree (the revision from task get); after execution you write inside the child task assigned to you."
	}
}

// planCause is the cause in a plan-mode refusal, without the plan-mode advice and the note that nobody can be asked, which do not
// apply to a pool member.
func planCause(reason string) string {
	reason = strings.TrimPrefix(reason, "approval required: ")
	reason = strings.TrimPrefix(reason, "plan mode (read-only): ")
	reason = strings.TrimPrefix(reason, "plan mode: ")
	if i := strings.Index(reason, " (this run has no one to ask"); i >= 0 {
		reason = reason[:i]
	}
	return strings.TrimSuffix(strings.TrimSuffix(reason, ". Present a plan instead of acting"), "; approval needed")
}

// poolTool exposes cooperation without granting worker-creation authority.
type poolTool struct{ s *Swarm }

// Spec describes the persistent pool protocol shared by every agent.
func (t *poolTool) Spec() core.ToolSpec {
	return core.ToolSpec{Name: "pool", NoTreeWrites: true, Description: "Cooperate on a role pool's parent task. Leader: propose an agreement (theme/style, interfaces, work division, acceptance); execute after everyone agrees; reconcile after all children are accepted (text = the combined contract to confirm). Every member: call inspect during reconciliation to update your checkout, examine the combined result, then confirm. Agree to the proposal or confirm the reconciled result, with the exact revision from task get. Use mail to discuss objections; the manager resolves disagreements. During execution the leader may create scoped child tasks and assign existing members. Return one parent result with task done after every member confirms. Manager: excuse a member that cannot take part (its model keeps failing or does not answer; member = its id, text = why): it leaves the pool, its votes stop counting, its open subtasks return to the board, and the next member leads if it led.", InputSchema: json.RawMessage(`{"type":"object","properties":{"action":{"type":"string","enum":["propose","agree","execute","reconcile","inspect","confirm","excuse"]},"id":{"type":"string"},"member":{"type":"string"},"revision":{"type":"integer"},"text":{"type":"string"}},"required":["action","id"]}`)}
}

// Run applies one phase transition after checking leadership and membership.
func (t *poolTool) Run(ctx context.Context, c *tools.Call) (*tools.Result, error) {
	var in struct {
		Action, ID, Text, Member string
		Revision                 int
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
			if c.Env.Role == "manager" {
				return tools.Errorf("only a reconciling pool member can inspect: the manager reads the combined result with the file tools, and accepts the parent once every member has confirmed it"), nil
			}
			return tools.Errorf("only a reconciling pool member can inspect"), nil
		}
		if m := s.get(by); m != nil {
			note, err := s.syncTree(ctx, m)
			if err != nil || note != "" {
				return tools.Errorf("repair checkout before confirming: %s %v", note, err), nil
			}
		}
	}
	stale := "" // the leader of a pool whose reconciliation a confirm found superseded
	err = s.Board.poolUpdate(by, in.ID, func(sn *Snapshot, task *Task, p *PoolState) error {
		leader := by == task.Owner || c.Env.Role == "manager"
		member := slices.Contains(p.Members, by)
		switch in.Action {
		case "propose":
			if !leader || p.Phase != "planning" || agreement == "" {
				return fmt.Errorf("leader must propose a nonempty agreement during planning")
			}
			if by != task.Owner && len(p.Votes) > 0 {
				// a new proposal resets the votes: from the manager, while members agree on the leader's, it only restarts the round
				return fmt.Errorf("the pool is agreeing on revision %d (%s agreed); a new proposal would reset that. Mail the leader %s what should change, or wait for the agreement", p.Revision, strings.Join(p.Votes, ", "), task.Owner)
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
			if !member {
				return fmt.Errorf("only the pool's members %s; the manager proposes, and excuses a member that cannot take part", in.Action)
			}
			phase := "planning"
			if in.Action == "confirm" {
				phase = "reconciling"
				if err := s.poolCurrent(sn, *task); err != nil {
					stale = task.Owner
					return err
				}
				if s.isolated() && !slices.Contains(p.Inspected, by) {
					return fmt.Errorf("call pool inspect, read the combined result, then confirm")
				}
			}
			if p.Phase != phase || p.Agreement == "" || in.Revision != p.Revision {
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
			if p.Phase == "reconciling" && len(p.Votes) == len(p.Members) && s.poolCurrent(sn, *task) == nil {
				// reconciling again would only reopen the confirmations of a result nothing has changed
				return fmt.Errorf("every member has confirmed revision %d and nothing has changed since: the pool's result is ready, submit it with task done id=%s", p.Revision, task.ID)
			}
			for _, child := range sn.Tasks {
				if child.Parent == task.ID && child.Status == StatusReview {
					return fmt.Errorf("child %s waits in review: the leader (or the manager) checks it and accepts it (task accept id=%s) or rejects it with feedback; reconcile once every child is accepted", child.ID, child.ID)
				}
				if child.Parent == task.ID && child.Status != StatusDone {
					return fmt.Errorf("child %s is not complete", child.ID)
				}
			}
			if agreement != "" {
				p.Agreement = agreement
			}
			p.Phase = "reconciling"
			task.Status = StatusDoing
			s.review.mu.Lock()
			p.Generation = s.review.merges
			s.review.mu.Unlock()
			p.Revision++
			p.Votes = nil
			p.Inspected = nil
		case "excuse":
			switch {
			case c.Env.Role != "manager":
				return fmt.Errorf("only the manager excuses a pool member")
			case !slices.Contains(p.Members, in.Member):
				return fmt.Errorf("%q is not a member of the %s pool (members: %s)", in.Member, task.ID, strings.Join(p.Members, ", "))
			case agreement == "":
				return fmt.Errorf("say why %s is excused (text)", in.Member)
			case len(p.Members) == 1:
				return fmt.Errorf("%s is the pool's last member: revise or resolve %s instead", in.Member, task.ID)
			}
			gone := func(m string) bool { return m == in.Member }
			p.Members = slices.DeleteFunc(p.Members, gone)
			p.Votes = slices.DeleteFunc(p.Votes, gone)
			p.Inspected = slices.DeleteFunc(p.Inspected, gone)
			p.Excused = append(p.Excused, in.Member)
			if task.Owner == in.Member {
				task.Owner, task.Rev = p.Members[0], sn.Version // a new assignment
			}
		default:
			return fmt.Errorf("unknown pool action")
		}
		return nil
	})
	if err != nil {
		if stale != "" && stale != by {
			// Only the leader reconciles again; a member that is merely refused keeps inspecting a result nobody will renew.
			s.notify(stale, "request", fmt.Sprintf("Pool %s: %s could not confirm, because a merge or task revision superseded the reconciliation. Call pool reconcile again; then every member inspects and confirms the new revision.", in.ID, by))
			return tools.Errorf("%v. The leader %s has been told; wait for its new revision (wait), then inspect and confirm it.", err, stale), nil
		}
		return tools.Errorf("%v", err), nil
	}
	if in.Action == "excuse" {
		return s.excused(by, in.ID, in.Member, agreement), nil
	}
	if in.Action == "reconcile" && s.isolated() {
		rep := s.Integrate(ctx)
		if !rep.Applied && rep.News() {
			_ = s.Board.poolUpdate(by, in.ID, func(_ *Snapshot, _ *Task, p *PoolState) error { p.Phase = "executing"; return nil })
			return tools.Errorf("combined result could not be applied: %s", rep.Message), nil
		}
	}
	if in.Action == "execute" {
		task, _ := s.Board.Snapshot().Task(in.ID)
		steps := poolExecuteSteps(task)
		s.wakePool(in.ID, "Pool updated: execute. "+steps)
		return text("%s: execute recorded. %s", in.ID, steps), nil
	}
	if in.Action == "propose" || in.Action == "reconcile" {
		s.wakePool(in.ID, "Pool updated: "+in.Action+". "+agreement)
	} else {
		task, _ := s.Board.Snapshot().Task(in.ID)
		s.notify(task.Owner, "info", by+" recorded "+in.Action+" for "+in.ID+poolReadyNote(task, in.Action))
	}
	return text("%s: %s recorded", in.ID, in.Action), nil
}

// poolReadyNote is what the leader is told when the last member confirms the reconciled result: it is ready, and the next step is
// to submit it. In a trial the leader called reconcile again after every member had confirmed, which only reopened the
// confirmations. It returns "" before then.
func poolReadyNote(t Task, action string) string {
	if action != "confirm" || t.Pool == nil || t.Pool.Phase != "reconciling" || len(t.Pool.Votes) != len(t.Pool.Members) {
		return ""
	}
	return fmt.Sprintf(". Every member has confirmed revision %d: submit the pool's result with task done id=%s (reconciling again would only reopen the confirmations)", t.Pool.Revision, t.ID)
}

// poolExecuteSteps is what a pool does once it executes: writing opens inside child tasks only, so the leader splits the agreed work
// into children, claims its own and assigns the others'. Without it, members that had agreed tried to write the parent's files, were
// refused, and gave up.
func poolExecuteSteps(t Task) string {
	others := make([]string, 0, len(t.Pool.Members))
	for _, m := range t.Pool.Members {
		if m != t.Owner {
			others = append(others, m)
		}
	}
	return fmt.Sprintf("Writing is open now, inside child tasks only: a member writes while it owns a child task of %s in doing, and is read-only until then. "+
		"The leader %s creates one child per share of the agreed work (task create parent=%s role=%s with its files), claims its own (task claim), "+
		"and assigns each other member (%s) its child (spawn agent=MEMBER task=CHILD).", t.ID, t.Owner, t.ID, t.Role, strings.Join(others, ", "))
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
		return fmt.Errorf("combined result changed: a merge or task revision superseded this reconciliation, even if its agreement text is unchanged; the pool leader must call pool reconcile again, then every member must inspect and confirm the new revision")
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

// excused finishes a manager's pool excuse: the member's open subtasks of the parent return to the board unassigned, the member is told
// to stop, and the rest of the pool is woken with its new membership (the votes it already gave still count).
func (s *Swarm) excused(by, id, member, why string) *tools.Result {
	released := s.Board.requeueChildren(by, id, member, "its owner was excused from the pool")
	ids := make([]string, len(released))
	for i, t := range released {
		ids[i] = t.ID
	}
	task, _ := s.Board.Snapshot().Task(id)
	s.notify(member, "info", fmt.Sprintf("The manager excused you from pool task %s: %s. Stop work on it: its files are not yours to change while it is open.", id, why))
	note := ""
	if len(ids) > 0 {
		note = " Released subtasks, unassigned again: " + strings.Join(ids, ", ") + "."
	}
	s.wakePool(id, fmt.Sprintf("Pool updated: %s excused (%s). Members: %s; leader %s.%s", member, why, strings.Join(task.Pool.Members, ", "), task.Owner, note))
	return text("%s: %s excused; members %s; leader %s.%s", id, member, strings.Join(task.Pool.Members, ", "), task.Owner, note)
}
