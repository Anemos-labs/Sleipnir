package swarm

import (
	"fmt"
	"hash/fnv"
	"slices"
	"strings"
	"sync"
)

// The pool protocol's refusals and its memory of what members read. A refusal names the condition that failed (role, phase,
// revision, a missing vote, an unfinished child) and the call that would succeed, so a member does not retry the same call
// blindly. They are thread text, in the caller's own conversation: short, deterministic (members in pool order, children in board
// order), and free of timestamps.

// poolReads remembers, per agent and pool task, the revision and agreement the agent last read in full: task get (or pool get),
// pool inspect, a reconciliation without text (whose answer is the pool's task), or a proposal or reconciliation text it wrote
// itself. A vote without a revision counts for the current revision when the voter's last read is that revision with that
// agreement, and the read's result reached the model before it wrote the vote: a read the model wrote itself, or one from an
// earlier model response than the vote's (a read in the same batch of calls as the vote runs before the vote, but the model had
// not seen it). So a vote still binds to a text the member has seen. It is held in memory only: after a restart a member reads the
// task again.
type poolReads struct {
	mu   sync.Mutex
	seen map[[2]string]poolRead // agent, task id -> what it read
}

// poolRead is one read of a pool: its revision and a hash of its agreement (a pool that begins again restarts its revisions), the
// reader's model response count when it read (turn; -1 when not known), and whether the reader wrote the text itself.
type poolRead struct {
	rev      int
	text     uint64
	turn     int64
	authored bool
}

// agreementHash is the hash poolRead keeps of an agreement.
func agreementHash(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return h.Sum64()
}

// note records that agent read pool task t as it is now, during its model response number turn (-1 when not known), or wrote
// its text itself (authored).
func (r *poolReads) note(agent string, t Task, turn int64, authored bool) {
	if agent == "" || t.Pool == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seen == nil {
		r.seen = map[[2]string]poolRead{}
	}
	r.seen[[2]string{agent, t.ID}] = poolRead{rev: t.Pool.Revision, text: agreementHash(t.Pool.Agreement), turn: turn, authored: authored}
}

// last returns agent's last read of pool task id, and whether there is one.
func (r *poolReads) last(agent, id string) (poolRead, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	got, ok := r.seen[[2]string{agent, id}]
	return got, ok
}

// of reports whether the read is of p as it is now: the same revision with the same agreement.
func (pr poolRead) of(p PoolState) bool {
	return pr.rev == p.Revision && pr.text == agreementHash(p.Agreement)
}

// seenBefore reports whether the read's result reached the model before its response number now: it wrote the text itself, or
// the read came in an earlier response. A turn that is not known (-1) never counts.
func (pr poolRead) seenBefore(now int64) bool {
	return pr.authored || pr.turn >= 0 && now >= 0 && pr.turn < now
}

// missingVotes lists the members whose agreement or confirmation of the current revision is missing, in the pool's member order.
func missingVotes(p PoolState) []string {
	var out []string
	for _, m := range p.Members {
		if !slices.Contains(p.Votes, m) {
			out = append(out, m)
		}
	}
	return out
}

// voterRefusal refuses an agree or confirm from someone who is not a member of t's pool.
func voterRefusal(t Task, p PoolState, by, action string, manager bool) error {
	switch {
	case slices.Contains(p.Members, by):
		return nil
	case manager:
		return fmt.Errorf("only the pool's members %s; the manager proposes, and excuses a member that cannot take part", action)
	case slices.Contains(p.Excused, by):
		return fmt.Errorf("the manager excused you from %s's pool: your %s no longer counts", t.ID, action)
	}
	return fmt.Errorf("you are not a member of %s's pool (members: %s): only its members %s", t.ID, strings.Join(p.Members, ", "), action)
}

// votePhaseRefusal refuses an agree outside planning, a confirm outside reconciliation, and a vote with nothing to vote on, with
// the call that fits the pool's phase.
func votePhaseRefusal(t Task, p PoolState, by, action string, isolated bool) error {
	switch {
	case action == "agree" && p.Phase == "executing" && by == t.Owner:
		return fmt.Errorf("%s is already executing revision %d's plan: no vote needed. As its leader, create its children (task create parent=%s), claim yours and assign the others (spawn agent=MEMBER task=CHILD)", t.ID, p.Revision, t.ID)
	case action == "agree" && p.Phase == "executing":
		return fmt.Errorf("%s is already executing revision %d's plan: no vote needed. Work in the child task its leader %s assigns you", t.ID, p.Revision, t.Owner)
	case action == "agree" && p.Phase == "reconciling":
		return fmt.Errorf("%s is reconciling revision %d: agree is for a plan; confirm the combined result instead (%s)", t.ID, p.Revision, confirmCall(t, p, by, isolated))
	case p.Agreement == "":
		return fmt.Errorf("nothing has been proposed for %s yet: wait for its leader %s's proposal, which arrives by mail", t.ID, t.Owner)
	case action == "confirm" && p.Phase == "planning":
		return fmt.Errorf("%s is planning revision %d: confirm is for a reconciled result; agree to the plan instead (pool agree id=%s revision=%d)", t.ID, p.Revision, t.ID, p.Revision)
	case action == "confirm" && p.Phase == "executing":
		return fmt.Errorf("%s is executing: there is nothing to confirm until its leader %s reconciles the combined result (pool reconcile) once every child is accepted", t.ID, t.Owner)
	}
	return nil
}

// confirmCall is the call that confirms t's reconciled result for member by: in an isolated run, an inspect first until it has
// inspected.
func confirmCall(t Task, p PoolState, by string, isolated bool) string {
	call := fmt.Sprintf("pool confirm id=%s revision=%d", t.ID, p.Revision)
	if isolated && !slices.Contains(p.Inspected, by) {
		return fmt.Sprintf("pool inspect id=%s, read the combined result, then %s", t.ID, call)
	}
	return call
}

// voteRevisionRefusal refuses a vote whose revision is missing (given 0), stale or unknown: it says the current revision and
// phase and the exact call. read is the revision the voter last read (0 for none); sameBatch says that read is of the current
// revision but ran in the same batch of calls as the vote, before the model had seen it.
func voteRevisionRefusal(t Task, p PoolState, action string, given, read int, sameBatch bool) error {
	call := fmt.Sprintf("pool %s id=%s revision=%d", action, t.ID, p.Revision)
	next := fmt.Sprintf("read it (task get id=%s), then call %s", t.ID, call)
	switch {
	case given == 0 && sameBatch:
		return fmt.Errorf("pool %s needs revision: %s is at revision %d (%s), and your read of it ran in the same batch of calls as this vote, before you had seen it. Read its result first, then vote with revision=%d: %s", action, t.ID, p.Revision, p.Phase, p.Revision, call)
	case given == 0 && read == 0:
		return fmt.Errorf("pool %s needs revision: %s is at revision %d (%s); %s", action, t.ID, p.Revision, p.Phase, next)
	case given == 0:
		return fmt.Errorf("pool %s needs revision: %s is at revision %d (%s), and you last read revision %d; %s", action, t.ID, p.Revision, p.Phase, read, next)
	case given < p.Revision:
		return fmt.Errorf("revision %d is stale: %s is at revision %d (%s); %s", given, t.ID, p.Revision, p.Phase, next)
	}
	return fmt.Errorf("revision %d is not one of %s's: it is at revision %d (%s); %s", given, t.ID, p.Revision, p.Phase, next)
}

// proposeRefusal refuses pool propose unless the leader (or the manager) proposes a nonempty agreement while the pool plans; after
// planning it says how the plan changes instead.
func proposeRefusal(t Task, p PoolState, by string, manager bool, agreement string) error {
	switch {
	case by != t.Owner && !manager && p.Phase == "planning":
		return fmt.Errorf("only %s's leader %s (or the manager) proposes its plan: mail %s what should change, or agree to revision %d (pool agree id=%s revision=%d)", t.ID, t.Owner, t.Owner, p.Revision, t.ID, p.Revision)
	case by != t.Owner && !manager:
		return fmt.Errorf("only %s's leader %s (or the manager) proposes its plan, and only while it plans: mail %s what should change", t.ID, t.Owner, t.Owner)
	case p.Phase != "planning" && manager:
		return fmt.Errorf("%s is %s (revision %d): a proposal is made while it plans. To change its plan, revise %s (task revise id=%s text=why), which returns the pool to planning", t.ID, p.Phase, p.Revision, t.ID, t.ID)
	case p.Phase == "executing":
		return fmt.Errorf("%s is executing revision %d's plan: a proposal is made while it plans. Within the plan, add children (task create parent=%s); the combined contract is recorded with pool reconcile text=... once every child is accepted. A different plan needs the manager to revise %s, which returns the pool to planning: mail the manager", t.ID, p.Revision, t.ID, t.ID)
	case p.Phase != "planning":
		return fmt.Errorf("%s is %s (revision %d): a proposal is made while it plans. The combined contract is the text of pool reconcile (call it again with text=... to replace it); a different plan needs the manager to revise %s, which returns the pool to planning: mail the manager", t.ID, p.Phase, p.Revision, t.ID)
	case agreement == "":
		return fmt.Errorf("pool propose needs the agreement in text")
	}
	return nil
}

// executeRefusal refuses pool execute unless the leader (or the manager) asks while the pool plans, with an agreement every
// member has agreed to; it names the members whose agreement to the current revision is missing.
func executeRefusal(t Task, p PoolState, by string, manager bool) error {
	switch {
	case by != t.Owner && !manager:
		return fmt.Errorf("only %s's leader %s (or the manager) executes its plan; a member agrees to it (pool agree id=%s revision=%d)", t.ID, t.Owner, t.ID, p.Revision)
	case p.Phase == "executing":
		return fmt.Errorf("%s is already executing revision %d's plan", t.ID, p.Revision)
	case p.Phase == "reconciling":
		return fmt.Errorf("%s is reconciling: its plan was executed, and every member confirms the combined result (pool confirm)", t.ID)
	case p.Agreement == "":
		return fmt.Errorf("nothing has been proposed for %s yet: propose the plan first (pool propose id=%s text=...)", t.ID, t.ID)
	}
	if missing := missingVotes(p); len(missing) > 0 {
		return fmt.Errorf("every member must agree to revision %d before execution: waiting for %s (%d of %d agreed). Each has the proposal by mail; mail one that does not answer, and the manager can excuse one that cannot take part",
			p.Revision, strings.Join(missing, ", "), len(p.Members)-len(missing), len(p.Members))
	}
	return nil
}

// reconcileRefusal refuses pool reconcile unless the leader (or the manager) asks after execution with every child accepted: it
// says who takes a child out of review, or names every child that is not complete with its status and owner.
func reconcileRefusal(sn *Snapshot, t Task, p PoolState, by string, manager bool) error {
	switch {
	case by != t.Owner && !manager:
		return fmt.Errorf("only %s's leader %s (or the manager) reconciles its result", t.ID, t.Owner)
	case p.Phase == "planning":
		return fmt.Errorf("%s is planning: it reconciles after it executes and every child is accepted", t.ID)
	}
	for _, child := range sn.Tasks {
		if child.Parent != t.ID || child.Status != StatusReview {
			continue
		}
		if manager {
			return fmt.Errorf("child %s waits in review: accept it (task accept id=%s) or reject it with feedback (task reject id=%s text=...); reconcile once every child is accepted", child.ID, child.ID, child.ID)
		}
		return fmt.Errorf("child %s waits in review: as the leader, check it and accept it (task accept id=%s), or mail the manager what to fix so it rejects it; reconcile once every child is accepted", child.ID, child.ID)
	}
	if open := unfinishedChildren(sn, t.ID); len(open) > 0 {
		return fmt.Errorf("%s: reconcile once every child is accepted", notComplete(open, "complete"))
	}
	return nil
}

// unfinishedChildren returns the children of parent that are not done, in board order.
func unfinishedChildren(sn *Snapshot, parent string) []Task {
	var out []Task
	for _, c := range sn.Tasks {
		if c.Parent == parent && c.Status != StatusDone {
			out = append(out, c)
		}
	}
	return out
}

// notComplete names unfinished children with their status and owner: "child T5 is not complete (doing, be-2); nor are T6 (todo,
// unassigned)". word is what they are not ("complete", "done").
func notComplete(open []Task, word string) string {
	state := func(c Task) string { return fmt.Sprintf("(%s, %s)", c.Status, ownerOrUnassigned(c.Owner)) }
	msg := fmt.Sprintf("child %s is not %s %s", open[0].ID, word, state(open[0]))
	if len(open) > 1 {
		rest := make([]string, len(open)-1)
		for i, c := range open[1:] {
			rest[i] = c.ID + " " + state(c)
		}
		msg += "; nor are " + strings.Join(rest, ", ")
	}
	return msg
}

// ownerOrUnassigned is a task's owner, or "unassigned".
func ownerOrUnassigned(owner string) string {
	if owner == "" {
		return "unassigned"
	}
	return owner
}

// inspectRefusal refuses pool inspect outside a reconciling pool the caller is a member of, and says what reads a pool instead.
// ok says whether the task exists.
func inspectRefusal(t Task, ok bool, by string, manager bool) error {
	switch {
	case manager:
		return fmt.Errorf("only a reconciling pool member can inspect: the manager reads the combined result with the file tools, and accepts the parent once every member has confirmed it")
	case !ok:
		return fmt.Errorf("no task %s: pool inspect acts on a reconciling pool's parent task; task list shows them", t.ID)
	case t.Pool == nil:
		return fmt.Errorf("%s is not a pool task: pool inspect brings a reconciling pool's combined result into a member's checkout. Read %s with task get id=%s", t.ID, t.ID, t.ID)
	case t.Status == StatusDone || t.Status == StatusFailed:
		return fmt.Errorf("%s is %s: its pool has nothing to inspect. Read it with task get id=%s", t.ID, t.Status, t.ID)
	case !slices.Contains(t.Pool.Members, by):
		return fmt.Errorf("you are not a member of %s's pool (members: %s): read it with task get id=%s", t.ID, strings.Join(t.Pool.Members, ", "), t.ID)
	case t.Pool.Phase != "reconciling":
		return fmt.Errorf("%s is %s (revision %d): pool inspect is for the combined result once the pool reconciles. Read the pool's state and plan with task get id=%s", t.ID, t.Pool.Phase, t.Pool.Revision, t.ID)
	}
	return nil
}

// poolChildAuthority says why by may not author children of parent (create them, accept them, assign members to them), or nil when
// it may: parent is a role pool's parent task that by leads, it is executing, and role is its role.
func poolChildAuthority(sn *Snapshot, by, parent, role string) error {
	p, ok := sn.Task(parent)
	switch {
	case parent == "":
		return fmt.Errorf("it names no parent pool (parent=)")
	case !ok:
		return fmt.Errorf("there is no task %s", parent)
	case p.Pool == nil:
		return fmt.Errorf("%s is not a role pool's parent task", parent)
	case p.Owner != by:
		return fmt.Errorf("%s's pool is led by %s, not by you", parent, ownerOrUnassigned(p.Owner))
	case p.Pool.Phase == "planning":
		return fmt.Errorf("%s is still planning: its leader creates, accepts and assigns children once every member has agreed and it executes (pool execute)", parent)
	case p.Pool.Phase != "executing":
		return fmt.Errorf("%s is %s: its leader creates, accepts and assigns children while it executes", parent, p.Pool.Phase)
	case p.Role != role:
		return fmt.Errorf("a child of %s has its role, %s, not %q", parent, p.Role, role)
	}
	return nil
}

// onTask is " on T5" for a blocked task that names what it waits for, "" otherwise.
func onTask(id string) string {
	if id == "" {
		return ""
	}
	return " on " + id
}

// turnOf returns how many model responses agent has received (its current batch of calls belongs to the last), or -1 for an agent
// the swarm does not run. A member's count changes only between its batches of calls.
func (s *Swarm) turnOf(agent string) int64 {
	if m := s.get(agent); m != nil {
		return m.turns.Load()
	}
	return -1
}
