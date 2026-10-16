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
	return b.mutate(by, "assign", func(d *draft) error { return beginPoolIn(d, id, members) })
}

// splitPool begins the pool of id, in one change of the board, with the members of order (in that order, which decides the leader):
// those that release names leave the pools that spared them (release: parent -> members), unless one has meanwhile become its pool's
// leader or the owner of one of its open subtasks, or left it; it then stays where it is and is not in the new pool. released is what
// each pool let go.
func (b *Board) splitPool(by, id string, order []string, release map[string][]string) (released map[string][]string, err error) {
	err = b.mutate(by, "assign", func(d *draft) error {
		released = map[string][]string{}
		from := map[string]string{} // member -> the parent that is to release it
		for parent, ids := range release {
			for _, m := range ids {
				from[m] = parent
			}
		}
		var members []string
		for _, m := range order {
			parent, leaving := from[m]
			if !leaving {
				members = append(members, m)
				continue
			}
			i := taskIdx(d.Snapshot, parent)
			if i < 0 {
				continue
			}
			t := d.Tasks[i]
			if t.Pool == nil || t.Status == StatusDone || t.Status == StatusFailed {
				members = append(members, m) // the pool closed: it holds nobody
				continue
			}
			if !slices.Contains(t.Pool.Members, m) || t.Owner == m || ownsOpenChild(d.Snapshot, m, parent) {
				continue
			}
			members = append(members, m)
			released[parent] = append(released[parent], m)
		}
		if len(members) == 0 {
			return fmt.Errorf("no member of the role is free for %s any more: the ones that were took work in their pools; assign it again when one is free (call wait)", id)
		}
		if err := beginPoolIn(d, id, members); err != nil {
			return err
		}
		for parent, ids := range released {
			i := taskIdx(d.Snapshot, parent)
			t := d.Tasks[i]
			p := *t.Pool
			p.Members = slices.DeleteFunc(slices.Clone(p.Members), func(m string) bool { return slices.Contains(ids, m) })
			p.Votes = slices.DeleteFunc(slices.Clone(p.Votes), func(m string) bool { return slices.Contains(ids, m) })
			p.Inspected = slices.DeleteFunc(slices.Clone(p.Inspected), func(m string) bool { return slices.Contains(ids, m) })
			t.Pool = &p
			if err := d.put(i, t); err != nil {
				return err
			}
		}
		return nil
	})
	return released, err
}

// ownsOpenChild reports whether member owns a subtask of parent that is not done or failed.
func ownsOpenChild(snap *Snapshot, member, parent string) bool {
	for _, c := range snap.Tasks {
		if c.Parent == parent && c.Owner == member && c.Status != StatusDone && c.Status != StatusFailed {
			return true
		}
	}
	return false
}

// beginPoolIn binds a ready task to members inside a board change (beginPool, splitPool).
func beginPoolIn(d *draft, id string, members []string) error {
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
	if err := poolReady(d.Snapshot, t); err != nil {
		return err
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
}

// poolReady says why the task t cannot begin a pool now, or nil: it must be an unassigned parent task whose dependencies are done.
func poolReady(sn *Snapshot, t Task) error {
	if t.Status != StatusTodo || t.Parent != "" {
		return fmt.Errorf("%s is not an unassigned parent task", t.ID)
	}
	if ok, why := depsDone(sn, t); !ok {
		return fmt.Errorf("dependency %s is not complete", why.ID)
	}
	return nil
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
		s.emit(events.TypeAgentSpawn, map[string]any{"id": id, "role": role, "model": m.model.ID})
		// A member is made idle, so setState would publish nothing, and a reader of the log would take the spawn for the start of a
		// run: the member is said to be idle until its pool starts it (a pool that could not begin left three members "starting").
		m.publish(s)
	}
	return s.poolMembers(role), nil
}

// spareMembers is the members of req.Role that can form the pool of a new parent: those in no open pool of the role, and those an
// executing pool can spare (not its leader, no open subtask of it), with release, the members each such pool is to let go once the
// new pool has begun (they are not excused: they owe it nothing more). Nothing changes here. It says why when there are none: a pool
// still planning needs every member, and an executing one may have every member busy. The member named in req.Agent leads when it is
// among them.
func (s *Swarm) spareMembers(req SpawnReq, all []*member) ([]*member, map[string][]string, error) {
	snap := s.Board.Snapshot()
	inPool := map[string]string{} // member -> the open parent whose pool holds it
	var busy []Task
	for _, t := range snap.Tasks {
		if t.Role != req.Role || t.Pool == nil || t.Status == StatusDone || t.Status == StatusFailed || t.ID == req.TaskID {
			continue
		}
		busy = append(busy, t)
		for _, m := range t.Pool.Members {
			inPool[m] = t.ID
		}
	}
	if len(busy) == 0 {
		return all, nil, nil
	}
	openChild := map[string]bool{} // member+parent that owns an open subtask
	for _, c := range snap.Tasks {
		if c.Parent != "" && c.Owner != "" && c.Status != StatusDone && c.Status != StatusFailed {
			openChild[c.Owner+"\x00"+c.Parent] = true
		}
	}
	release := map[string][]string{} // parent -> members it releases
	var spare []*member
	for _, m := range all {
		parent, held := inPool[m.id]
		if !held {
			spare = append(spare, m)
			continue
		}
		t, _ := snap.Task(parent)
		if t.Pool.Phase == "executing" && t.Owner != m.id && !openChild[m.id+"\x00"+parent] {
			spare = append(spare, m)
			release[parent] = append(release[parent], m.id)
		}
	}
	if len(spare) == 0 {
		t := busy[0]
		if t.Pool.Phase != "executing" {
			return nil, nil, fmt.Errorf("the %s pool is still agreeing on %s's plan with every member; once it executes, members without a subtask of it can take another parent. Wait for that (call wait), or have %s's leader %s take this work as a child of %s",
				req.Role, t.ID, t.ID, t.Owner, t.ID)
		}
		return nil, nil, fmt.Errorf("every %s member is busy on %s (its leader %s, or an open subtask); assign this parent again when one is free (call wait), or have %s take it as a child of %s",
			req.Role, t.ID, t.Owner, t.Owner, t.ID)
	}
	if req.Agent != "" {
		for i, m := range spare {
			if m.id == req.Agent {
				spare[0], spare[i] = spare[i], spare[0]
				break
			}
		}
	}
	return spare, release, nil
}

// notifyPoolRelease tells the leader of parent which members another parent of its role took, so it does not wait for their
// confirmation or assign them its next subtask.
func (s *Swarm) notifyPoolRelease(parent string, ids []string, next string) {
	t, ok := s.Board.Snapshot().Task(parent)
	if !ok || t.Owner == "" || t.Pool == nil {
		return
	}
	s.notify(t.Owner, "info", fmt.Sprintf("Pool %s: %s left it for %s (they held none of its subtasks); its members are now %s.",
		parent, strings.Join(ids, ", "), next, strings.Join(t.Pool.Members, ", ")))
}

// startPool assigns a parent and wakes its members to cooperate, not to write.
func (s *Swarm) startPool(req SpawnReq, files []string) (string, error) {
	// A pool that cannot begin leaves nothing behind: its members are made only for a task that is ready. They used to be made first,
	// and a parent whose dependency was open left three frontend members on the roster, "starting", for the forty minutes until it was.
	if req.TaskID != "" {
		sn := s.Board.Snapshot()
		t, ok := sn.Task(req.TaskID)
		if !ok {
			return "", fmt.Errorf("no task %s", cleanText(req.TaskID, 20))
		}
		if err := poolReady(sn, t); err != nil {
			return "", err
		}
	}
	members, err := s.ensurePool(req.Role)
	if err != nil {
		return "", err
	}
	// A role's members work one parent per pool. When the role already has an open parent, the members that parent can spare (it
	// executes its plan, they do not lead it and hold none of its open subtasks) leave its pool and form this parent's, so independent
	// parts run side by side; a parent still agreeing on its plan needs every member, and one whose members are all busy spares none.
	members, release, err := s.spareMembers(req, members)
	if err != nil {
		return "", err
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
	// The new pool begins and the members leave the pools that spared them in one change of the board: one that took work in its pool
	// meanwhile (a subtask claimed or assigned since it was chosen) stays there.
	released, err := s.Board.splitPool(req.By, id, ids, release)
	if err != nil {
		return "", err
	}
	for parent, left := range released {
		s.notifyPoolRelease(parent, left, id)
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

// poolPermission makes planning and reconciliation read-only for pool members, who keep the research and checks of a read-only role
// (perm.ReadOnlyAllow: the web, the project's tests) within what the session allows. A refusal says why in the pool's terms (poolReadOnly):
// the plan-mode engine's own words ("present a plan instead of acting", "no one to ask") would send a member that is waiting for its
// subtask round in circles.
func (s *Swarm) poolPermission(ctx context.Context, id string, inner perm.Requester, req perm.Request) (perm.Decision, bool) {
	if req.Tool == perm.ToolMemory {
		// The agent's own notes are no change to the project the pool coordinates: the session decides, and puts the note to the
		// person (a long run lost twelve lessons about pool procedure that members tried to keep while their pool planned).
		return perm.Decision{}, false
	}
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
			if po := e.PlanOnly(perm.ReadOnlyAllow...); po != nil {
				d := po.Check(ctx, req)
				if !d.Allow {
					d.Reason = poolReadOnly(t, id) + " Refused: " + planCause(d.Reason)
					return d, true
				}
				// Read-only, or the research and checks of a read-only role (the web, the project's tests): a member plans with what a
				// reviewer may use, and the session's own engine still decides, so the pool never grants more than the session does.
				return inner.Check(ctx, req), true
			}
			return perm.Decision{Reason: poolReadOnly(t, id)}, true
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

// Run applies one phase transition after checking leadership and membership. A refusal names the condition that failed and the
// call that would succeed (pool_refusals.go).
func (t *poolTool) Run(ctx context.Context, c *tools.Call) (*tools.Result, error) {
	var in struct {
		Action, ID, Text, Member string
		Revision                 int
	}
	if r := decode(c.Input, &in); r != nil {
		return r, nil
	}
	s, by := t.s, c.Env.Agent
	in.ID = strings.TrimSpace(in.ID)
	switch in.Action {
	case "get":
		// Not an action of the schema: models reach for it to read a pool's state, which task get shows.
		return s.readTask(c, in.ID, "pool get"), nil
	case "propose", "agree", "execute", "reconcile", "inspect", "confirm", "excuse":
	default:
		where := "task get id=" + cleanText(in.ID, 20)
		if in.ID == "" {
			where = "task get with the pool's parent task id (task list shows them)"
		}
		return tools.Errorf("unknown pool action %q: pool's actions are propose, agree, execute, reconcile, inspect, confirm and excuse. A pool's state (phase, revision, agreement, votes) is in %s", cleanText(in.Action, 30), where), nil
	}
	agreement, err := cleanAgreement(in.Text)
	if err != nil {
		return tools.Errorf("%v", err), nil
	}
	manager := c.Env.Role == "manager"
	if in.Action == "inspect" {
		parent, ok := s.Board.Snapshot().Task(in.ID)
		if !ok {
			parent.ID = in.ID
		}
		if inspectEarly(parent, ok, by, manager) {
			// Nothing is combined before the pool reconciles; the read the member wanted is the pool as it stands.
			return s.readTaskNoted(c, in.ID, "pool inspect", func(t Task) string {
				return fmt.Sprintf("%s is %s (revision %d): there is no combined result to inspect until it reconciles, so this is the pool as task get shows it.", t.ID, t.Pool.Phase, t.Pool.Revision)
			}), nil
		}
		if err := inspectRefusal(parent, ok, by, manager); err != nil {
			return tools.Errorf("%v", err), nil
		}
		if m := s.get(by); m != nil {
			note, err := s.syncTree(ctx, m)
			if err != nil || note != "" {
				return tools.Errorf("repair checkout before confirming: %s %v", note, err), nil
			}
		}
	}
	if in.Action == "excuse" && manager {
		if why := s.excuseRefusal(in.Member); why != "" {
			return tools.Errorf("%s", why), nil
		}
	}
	stale := ""          // the leader of a pool whose reconciliation a confirm found superseded
	turn := s.turnOf(by) // constant during this batch of calls: taken outside the board's lock
	var vote poolVote
	read := false       // the caller wrote the pool's current revision with this action (propose, reconcile)
	var after PoolState // the pool as this action left it
	err = s.Board.poolUpdate(by, in.ID, func(sn *Snapshot, task *Task, p *PoolState) error {
		member := slices.Contains(p.Members, by)
		switch in.Action {
		case "propose":
			if err := proposeRefusal(*task, *p, by, manager, agreement); err != nil {
				return err
			}
			if by != task.Owner && len(p.Votes) > 0 {
				// a new proposal resets the votes: from the manager, while members agree on the leader's, it only restarts the round
				return fmt.Errorf("the pool is agreeing on revision %d (%s agreed); a new proposal would reset that. Mail the leader %s what should change, or wait for the agreement", p.Revision, strings.Join(p.Votes, ", "), task.Owner)
			}
			p.Agreement = agreement
			p.Revision++
			p.Votes = nil
			p.Inspected = nil
			read = true
		case "inspect":
			if !member || p.Phase != "reconciling" {
				return fmt.Errorf("%s is not reconciling any more: read it with task get id=%s", task.ID, task.ID)
			}
			if !slices.Contains(p.Inspected, by) {
				p.Inspected = append(p.Inspected, by)
			}
		case "agree", "confirm":
			if err := voterRefusal(*task, *p, by, in.Action, manager); err != nil {
				return err
			}
			if err := votePhaseRefusal(*task, *p, by, in.Action, s.isolated()); err != nil {
				return err
			}
			if in.Action == "confirm" {
				if err := s.poolCurrent(sn, *task); err != nil {
					stale = task.Owner
					return err
				}
				if s.isolated() && !slices.Contains(p.Inspected, by) {
					return fmt.Errorf("call pool inspect id=%s first: it brings the combined result of revision %d into your checkout; read it, then pool confirm id=%s revision=%d", task.ID, p.Revision, task.ID, p.Revision)
				}
			}
			rev, implicit := in.Revision, false
			got, _ := s.poolReads.last(by, task.ID)
			current := got.of(*p)
			if rev == 0 && current && got.seenBefore(turn) {
				// No revision, but the member's last read is this revision with this agreement, and it had seen that read when it
				// wrote the vote: the vote binds to what it read.
				rev, implicit = p.Revision, true
			}
			if rev != p.Revision {
				return voteRevisionRefusal(*task, *p, in.Action, in.Revision, got.rev, current && !got.seenBefore(turn))
			}
			again := slices.Contains(p.Votes, by)
			if !again {
				p.Votes = append(p.Votes, by)
				sort.Strings(p.Votes)
			}
			missing := missingVotes(*p)
			vote = poolVote{rev: p.Revision, implicit: implicit, voted: len(p.Members) - len(missing), members: len(p.Members),
				missing: missing, completes: !again && len(missing) == 0, leader: task.Owner}
		case "execute":
			if err := executeRefusal(*task, *p, by, manager); err != nil {
				return err
			}
			p.Phase = "executing"
			p.Votes = nil
		case "reconcile":
			if err := reconcileRefusal(sn, *task, *p, by, manager); err != nil {
				return err
			}
			if p.Phase == "reconciling" && len(p.Votes) == len(p.Members) && s.poolCurrent(sn, *task) == nil {
				// (every child is complete) reconciling again would only reopen the confirmations of a result nothing has changed
				return fmt.Errorf("every member has confirmed revision %d and nothing has changed since: the pool's result is ready, submit it with task done id=%s", p.Revision, task.ID)
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
			read = agreement != "" // without text it keeps an agreement the caller is shown below, not one it wrote
		case "excuse":
			switch {
			case !manager:
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
		}
		after = *p
		return nil
	})
	if err == nil && read {
		s.poolReads.note(by, Task{ID: in.ID, Pool: &after}, turn, true)
	}
	if err != nil {
		if stale != "" && stale != by {
			// Only the leader reconciles again; a member that is merely refused keeps inspecting a result nobody will renew.
			s.notify(stale, "request", fmt.Sprintf("Pool %s: %s could not confirm, because a merge or task revision superseded the reconciliation. Call pool reconcile again; then every member inspects and confirms the new revision.", in.ID, by))
			return tools.Errorf("%v. The leader %s has been told; wait for its new revision (wait), then inspect and confirm it.", err, stale), nil
		}
		return tools.Errorf("%v", err), nil
	}
	switch in.Action {
	case "excuse":
		return s.excused(by, in.ID, in.Member, agreement), nil
	case "reconcile":
		if s.isolated() {
			rep := s.Integrate(ctx)
			if !rep.Applied && rep.News() {
				_ = s.Board.poolUpdate(by, in.ID, func(_ *Snapshot, _ *Task, p *PoolState) error { p.Phase = "executing"; return nil })
				return tools.Errorf("combined result could not be applied: %s", rep.Message), nil
			}
		}
	case "execute":
		task, _ := s.Board.Snapshot().Task(in.ID)
		steps := poolExecuteSteps(task)
		s.wakePool(in.ID, "Pool updated: execute. "+steps)
		return text("%s: execute recorded. %s", in.ID, steps), nil
	case "agree", "confirm":
		return s.voteRecorded(by, in.ID, in.Action, vote), nil
	case "inspect":
		// The member's checkout holds the combined result; the answer is the pool's task in full (task get), the read its confirm
		// without a revision counts for.
		res := s.readTask(c, in.ID, "pool inspect")
		if !res.IsError {
			res.Text = fmt.Sprintf("%s: inspect recorded; your checkout holds the combined result. Examine it, then confirm. The pool's task:\n%s", in.ID, res.Text)
		}
		return res, nil
	}
	if in.Action == "propose" || in.Action == "reconcile" {
		s.wakePool(in.ID, "Pool updated: "+in.Action+". "+agreement)
		if !read {
			// A reconciliation without text keeps the earlier agreement: the answer is the pool's task in full, the read it counts.
			res := s.readTask(c, in.ID, "pool reconcile")
			if !res.IsError {
				res.Text = fmt.Sprintf("%s: reconcile recorded as revision %d; it keeps the agreement shown below. The pool's task:\n%s", in.ID, after.Revision, res.Text)
			}
			return res, nil
		}
		// The caller wrote this revision's text: the answer names the revision, and the text is the one it sent.
		return text("%s: %s recorded as revision %d", in.ID, in.Action, after.Revision), nil
	}
	return text("%s: %s recorded", in.ID, in.Action), nil
}

// poolVote is what an agree or confirm that counted leaves for its answer: the revision, whether it was the voter's last read
// (implicit: no revision was given), the round's count and the members still to vote, whether this vote completed the round, and
// the pool's leader.
type poolVote struct {
	rev, voted, members int
	implicit, completes bool
	missing             []string
	leader              string
}

// voteRecorded answers a member whose agree or confirm counted: the revision, who has yet to vote, and, once every member has, what
// follows. The leader is told once, by the vote that completes the round when another member cast it; single votes are not mailed
// to it (the leader's refused execute names the members still to agree).
func (s *Swarm) voteRecorded(by, id, action string, v poolVote) *tools.Result {
	head := fmt.Sprintf("%s: %s recorded for revision %d", id, action, v.rev)
	if v.implicit {
		head += " (the revision you last read)"
	}
	if len(v.missing) > 0 {
		return text("%s; %d of %d members, waiting for %s.", head, v.voted, v.members, strings.Join(v.missing, ", "))
	}
	next := poolRoundDone(id, action, v.rev)
	if v.completes && v.leader != by {
		s.notify(v.leader, "request", "Pool "+id+": "+next+".")
	}
	if v.leader == by {
		return text("%s. %s.", head, next)
	}
	if action == "agree" {
		return text("%s. Every member has agreed to revision %d; the leader %s executes it.", head, v.rev, v.leader)
	}
	return text("%s. Every member has confirmed revision %d; the leader %s submits it.", head, v.rev, v.leader)
}

// poolRoundDone is what the leader does once every member has agreed to revision rev (execute it) or confirmed it (submit the
// result). In a trial the leader called reconcile again after every member had confirmed, which only reopened the confirmations.
func poolRoundDone(id, action string, rev int) string {
	if action == "agree" {
		return fmt.Sprintf("Every member has agreed to revision %d: execute it with pool execute id=%s", rev, id)
	}
	return fmt.Sprintf("Every member has confirmed revision %d: submit the pool's result with task done id=%s (reconciling again would only reopen the confirmations)", rev, id)
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

// poolComplete prevents aggregate submission before all members reconcile: it names the children that are not done, or the
// members whose confirmation is missing.
func poolComplete(sn *Snapshot, t Task) error {
	if t.Pool == nil {
		return nil
	}
	if open := unfinishedChildren(sn, t.ID); len(open) > 0 {
		return fmt.Errorf("%s", notComplete(open, "done"))
	}
	if t.Pool.Phase != "reconciling" {
		return fmt.Errorf("%s needs reconciliation confirmed by every pool member: it is %s; its leader %s reconciles (pool reconcile id=%s) once every child is accepted, then every member confirms", t.ID, t.Pool.Phase, t.Owner, t.ID)
	}
	if missing := missingVotes(*t.Pool); len(missing) > 0 {
		return fmt.Errorf("%s needs reconciliation confirmed by every pool member: waiting for %s to confirm revision %d (%d of %d confirmed)",
			t.ID, strings.Join(missing, ", "), t.Pool.Revision, len(t.Pool.Members)-len(missing), len(t.Pool.Members))
	}
	return nil
}

// poolAssignment verifies that children stay in their agreed pool and parent scope. A child is created, claimed and assigned while
// its parent executes, and also while the parent is blocked on that very child (its leader waits for it: the child must be able to
// start).
func poolAssignment(sn *Snapshot, t Task) error {
	if t.Parent == "" {
		return nil
	}
	p, ok := sn.Task(t.Parent)
	switch {
	case !ok:
		return fmt.Errorf("parent %s is missing", t.Parent)
	case p.Pool == nil:
		return fmt.Errorf("%s is not a role pool's parent task: a child (parent=) belongs to one", p.ID)
	case p.Pool.Phase != "executing":
		return fmt.Errorf("parent pool %s is %s: its children are created, claimed and assigned while it executes", p.ID, p.Pool.Phase)
	case p.Status == StatusBlocked && p.BlockedOn != "" && p.BlockedOn == t.ID:
		// the parent waits for this child
	case p.Status == StatusBlocked:
		return fmt.Errorf("parent pool %s is blocked%s: its children are claimed and assigned while it executes; its leader %s resumes it (task resume id=%s)", p.ID, onTask(p.BlockedOn), ownerOrUnassigned(p.Owner), p.ID)
	case p.Status != StatusDoing:
		return fmt.Errorf("parent pool %s is %s: its children are claimed and assigned while it executes", p.ID, p.Status)
	}
	if t.Role != p.Role {
		return fmt.Errorf("a child of %s has its role, %s, not %q", p.ID, p.Role, t.Role)
	}
	if t.Owner != "" && !slices.Contains(p.Pool.Members, t.Owner) {
		return fmt.Errorf("%s is not a member of %s's pool (members: %s): a child of it goes to one of them", t.Owner, p.ID, strings.Join(p.Pool.Members, ", "))
	}
	return poolScope(p, t)
}

// poolScope prevents child revisions from broadening their parent's file scope: every entry of the child's scope lies inside the
// parent's (scopeContains).
func poolScope(p, t Task) error {
	if len(p.Files) > 0 {
		if len(t.Files) == 0 {
			return fmt.Errorf("child needs an explicit scope inside its parent %s (%s)", p.ID, strings.Join(p.Files, ", "))
		}
		for _, f := range t.Files {
			if !scopeContains(p.Files, f) {
				return fmt.Errorf("child scope %s is outside the scope of its parent %s (%s)", f, p.ID, strings.Join(p.Files, ", "))
			}
		}
	}
	return nil
}

// poolCurrent rejects votes or submissions over a superseded combined result: one that a merge, a manager change or a task revision
// may have changed since the pool reconciled it, because it touched the pool's files (or files that are not known). A merge elsewhere
// in the tree leaves it current.
func (s *Swarm) poolCurrent(_ *Snapshot, task Task) error {
	if task.Pool == nil {
		return nil
	}
	if n := s.review.touchedSince(task.Pool.Generation, task.Files); n != 0 {
		return fmt.Errorf("combined result changed: a merge or task revision in the pool's files superseded this reconciliation, even if its agreement text is unchanged; the pool leader must call pool reconcile again, then every member must inspect and confirm the new revision")
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

// excuseRefusal is why the manager may not excuse member now ("" when it may): the member is running and has made progress (an
// edit, a test, a task update: progressCall) within the stall check's bound of model turns. A member that is not in the live view,
// or quiet in mail, may still be working; excusing it releases its subtasks and leases to others while it writes. One that has gone
// the bound without progress, has made none at all, or has stopped, may be excused: a loop of reads is not progress, so a stuck
// member is not shielded.
func (s *Swarm) excuseRefusal(member string) string {
	m := s.get(member)
	if m == nil {
		return ""
	}
	m.mu.Lock()
	running := m.life == lifeRunning
	m.mu.Unlock()
	k := s.stallTurns()
	if k == 0 {
		k = defaultStallTurns
	}
	worked := m.worked.Load()
	if !running || worked == 0 {
		return ""
	}
	since := m.turns.Load() - (worked - 1)
	if since >= k {
		return ""
	}
	return fmt.Sprintf("%s is working: it is running and made progress %d model turns ago. Ask it what it is doing (mail), or excuse it once it has stopped or gone %d turns without progress", member, since, k)
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
