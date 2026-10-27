package swarm

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/events"
)

// childReviseRig is a running backend pool of three, executing, with one child per member in doing. Every model request blocks,
// so every member's run stays running until it is cancelled.
func childReviseRig(t *testing.T) (*rvRig, Task, []Task) {
	t.Helper()
	r := newRVRig(t, Config{PersistentPools: true, MaxWorkers: 3, Team: []string{"backend", "backend", "backend"}}, gatedWorkers(nil))
	r.sw.StartManager()
	if _, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "build API", Files: []string{"api/**"}, By: "mgr"}); err != nil {
		t.Fatal(err)
	}
	parent := r.sw.Board.Snapshot().Tasks[0]
	if err := r.sw.Board.poolUpdate("mgr", parent.ID, func(_ *Snapshot, _ *Task, p *PoolState) error { p.Phase = "executing"; return nil }); err != nil {
		t.Fatal(err)
	}
	var children []Task
	for i, m := range parent.Pool.Members {
		c, err := r.sw.Board.CreateTask(parent.Owner, TaskSpec{Parent: parent.ID, Title: "part " + m, Role: "backend", Files: []string{"api/p" + string(rune('1'+i)) + ".go"}})
		if err != nil {
			t.Fatal(err)
		}
		if c, err = r.sw.Board.assignTask(assignReq{by: parent.Owner, agent: m, id: c.ID}); err != nil {
			t.Fatal(err)
		}
		children = append(children, c)
	}
	for _, m := range parent.Pool.Members {
		rvWait(t, m+" to be running", func() bool { return r.running(m) })
	}
	parent, _ = r.sw.Board.Snapshot().Task(parent.ID)
	return r, parent, children
}

// harnessMail returns the harness's messages to each agent that contain want.
func harnessMail(r *rvRig, want string) map[string]int {
	got := map[string]int{}
	for _, e := range r.log.OfType(events.TypeMailSend) {
		var m struct{ From, To, Text string }
		_ = json.Unmarshal(e.Data, &m)
		if m.From == harnessSender && strings.Contains(m.Text, want) {
			got[m.To]++
		}
	}
	return got
}

// runOf returns the member's current run, nil when it is not running.
func runOf(r *rvRig, id string) *runState {
	m := r.sw.get(id)
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.run
}

// The manager's revise of one member's child is that child's business, its owner's and its leader's: the leader's run goes on,
// the parent keeps its assignment and, while the pool executes, its revision; the leader and the child's owner are told, and the
// rest of the pool is not. A team run sent 149 "Child T was revised" notices to whole pools.
func TestAChildReviseTellsTheLeaderAndTheOwnerOnly(t *testing.T) {
	r, parent, children := childReviseRig(t)
	lead, owner, bystander := parent.Pool.Members[0], parent.Pool.Members[1], parent.Pool.Members[2]
	leadRun := runOf(r, lead)
	res := r.callTool(context.Background(), "task", "mgr", "manager", map[string]any{"action": "revise", "id": children[1].ID, "description": "validate the page size too", "text": "missing validation"})
	if res.IsError {
		t.Fatal(res.Text)
	}
	if runOf(r, lead) != leadRun || !r.running(lead) {
		t.Fatal("the revise of another member's child stopped the leader's run")
	}
	p, _ := r.sw.Board.Snapshot().Task(parent.ID)
	if p.Rev != parent.Rev || p.Owner != lead || p.Status != StatusDoing {
		t.Fatalf("the parent's assignment changed: rev %d -> %d, owner %s, %s", parent.Rev, p.Rev, p.Owner, p.Status)
	}
	if p.Pool.Phase != "executing" || p.Pool.Revision != parent.Pool.Revision {
		t.Fatalf("an executing pool's revision moved: phase %s, revision %d -> %d", p.Pool.Phase, parent.Pool.Revision, p.Pool.Revision)
	}
	told := harnessMail(r, "revised")
	if told[lead] != 1 || told[owner] != 1 || told[bystander] != 0 {
		t.Fatalf("notices: %v (want one each to the leader %s and the owner %s, none to %s)", told, lead, owner, bystander)
	}
	if n := harnessMail(r, "Pool leader stopped")["mgr"]; n != 0 {
		t.Fatalf("the manager was asked %d times to resume what its own revise did", n)
	}
	if c, _ := r.sw.Board.Snapshot().Task(children[1].ID); c.Status != StatusTodo || c.Owner != "" {
		t.Fatalf("the revised child: %s owner %q", c.Status, c.Owner)
	}
}

// The leader's own child is revised without stopping the leader, which also leads the pool: it is told to claim the child again.
func TestRevisingTheLeadersOwnChildLeavesItRunning(t *testing.T) {
	r, parent, children := childReviseRig(t)
	lead := parent.Pool.Members[0]
	leadRun := runOf(r, lead)
	res := r.callTool(context.Background(), "task", "mgr", "manager", map[string]any{"action": "revise", "id": children[0].ID, "description": "split the handler", "text": "too large"})
	if res.IsError {
		t.Fatal(res.Text)
	}
	if runOf(r, lead) != leadRun || !r.running(lead) {
		t.Fatal("the revise of the leader's own child stopped the leader")
	}
	if told := harnessMail(r, "claim it (task claim id="+children[0].ID+")"); told[lead] != 1 || len(told) != 1 {
		t.Fatalf("notices: %v", told)
	}
}

// A pool that was confirming its combined result loses it when a child is revised: it executes again, its revision moves on and
// no confirmation or inspection of the old result is left to count. The leader keeps its assignment.
func TestAChildReviseReopensAReconcilingPool(t *testing.T) {
	r, parent, children := childReviseRig(t)
	lead := parent.Pool.Members[0]
	if err := r.sw.Board.poolUpdate("mgr", parent.ID, func(_ *Snapshot, _ *Task, p *PoolState) error {
		p.Phase, p.Revision, p.Agreement = "reconciling", p.Revision+1, "the combined contract"
		p.Votes, p.Inspected = []string{lead}, []string{lead}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before, _ := r.sw.Board.Snapshot().Task(parent.ID)
	if res := r.callTool(context.Background(), "task", "mgr", "manager", map[string]any{"action": "revise", "id": children[2].ID, "description": "cover the error path", "text": "missing test"}); res.IsError {
		t.Fatal(res.Text)
	}
	p, _ := r.sw.Board.Snapshot().Task(parent.ID)
	if p.Pool.Phase != "executing" || p.Pool.Revision != before.Pool.Revision+1 || len(p.Pool.Votes) != 0 || len(p.Pool.Inspected) != 0 {
		t.Fatalf("pool after the revise: phase %s, revision %d (was %d), votes %v, inspected %v", p.Pool.Phase, p.Pool.Revision, before.Pool.Revision, p.Pool.Votes, p.Pool.Inspected)
	}
	if p.Rev != before.Rev || p.Status != StatusDoing {
		t.Fatalf("the leader's assignment changed: rev %d -> %d, %s", before.Rev, p.Rev, p.Status)
	}
	if told := harnessMail(r, "reconcile once every child is done"); told[lead] != 1 {
		t.Fatalf("the leader was not told the pool executes again: %v", told)
	}
}

// The manager's revise of the pool task itself stops its members, by design, and the harness restarts them: the manager is not
// asked to resume what its own revise stopped. A failure it did not cause still reaches it.
func TestAPoolReviseDoesNotAskTheManagerToResumeIt(t *testing.T) {
	r, parent, _ := childReviseRig(t)
	if res := r.callTool(context.Background(), "task", "mgr", "manager", map[string]any{"action": "revise", "id": parent.ID, "description": "build the API in two packages", "text": "new layout"}); res.IsError {
		t.Fatal(res.Text)
	}
	rvWait(t, "the leader to stop", func() bool { return !r.running(parent.Owner) || runOf(r, parent.Owner) == nil })
	if n := harnessMail(r, "Pool leader stopped")["mgr"]; n != 0 {
		t.Fatalf("the manager was asked %d times to resume the pool its revise stopped", n)
	}
	m := r.sw.get(parent.Owner)
	r.sw.settleStopped(m, map[string]uint64{parent.ID: 0}, stopFailed, "step limit reached", true, false)
	if n := harnessMail(r, "Pool leader stopped on "+parent.ID+": step limit reached")["mgr"]; n != 1 {
		t.Fatalf("a leader that failed on its own was reported %d times", n)
	}
}

// A child revised while its pool is back in planning (the manager revised the pool task itself) leaves the pool planning: the
// parent is not handed out, and its phase, revision and votes stay, so the members agree and execute before any child starts.
func TestAChildReviseKeepsAPlanningPoolPlanning(t *testing.T) {
	r, parent, children := childReviseRig(t)
	lead := parent.Pool.Members[0]
	if err := r.sw.Board.revise("mgr", parent.ID, TaskSpec{Desc: "two packages"}, "new layout"); err != nil {
		t.Fatal(err)
	}
	if err := r.sw.Board.poolUpdate("mgr", parent.ID, func(_ *Snapshot, _ *Task, p *PoolState) error {
		p.Agreement, p.Votes = "split by package", []string{lead}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before, _ := r.sw.Board.Snapshot().Task(parent.ID)
	if before.Status != StatusTodo || before.Pool.Phase != "planning" {
		t.Fatalf("setup: parent %s, phase %s", before.Status, before.Pool.Phase)
	}
	if res := r.callTool(context.Background(), "task", "mgr", "manager", map[string]any{"action": "revise", "id": children[1].ID, "description": "validate input", "text": "missing validation"}); res.IsError {
		t.Fatal(res.Text)
	}
	p, _ := r.sw.Board.Snapshot().Task(parent.ID)
	if p.Status != StatusTodo || p.Rev != before.Rev || p.Pool.Phase != "planning" || p.Pool.Revision != before.Pool.Revision || len(p.Pool.Votes) != 1 {
		t.Fatalf("a planning pool was moved on by a child revise: %s rev %d->%d, phase %s, revision %d->%d, votes %v",
			p.Status, before.Rev, p.Rev, p.Pool.Phase, before.Pool.Revision, p.Pool.Revision, p.Pool.Votes)
	}
}

// A child revised after its pool's result was accepted reopens the pool: the parent is done no more, its leader gets a new
// assignment of it, the pool executes again at a new revision with no confirmation or inspection of the old result, and the
// leader and the child's owner are told.
func TestAChildReviseReopensAnAcceptedPool(t *testing.T) {
	r, parent, children := childReviseRig(t)
	lead, owner, bystander := parent.Pool.Members[0], parent.Pool.Members[1], parent.Pool.Members[2]
	if err := r.sw.Board.poolUpdate("mgr", parent.ID, func(_ *Snapshot, _ *Task, p *PoolState) error {
		p.Phase, p.Revision, p.Agreement = "reconciling", p.Revision+1, "the combined contract"
		p.Votes, p.Inspected = slices.Clone(p.Members), slices.Clone(p.Members)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.sw.Board.Submit(lead, parent.ID, "combined result", "pool reconciled and verified"); err != nil {
		t.Fatal(err)
	}
	if err := r.sw.Board.Accept("mgr", parent.ID, "accepted"); err != nil {
		t.Fatal(err)
	}
	accepted, _ := r.sw.Board.Snapshot().Task(parent.ID)
	if accepted.Status != StatusDone {
		t.Fatalf("setup: parent %s", accepted.Status)
	}
	if res := r.callTool(context.Background(), "task", "mgr", "manager", map[string]any{"action": "revise", "id": children[1].ID, "description": "handle the empty page", "text": "missed case"}); res.IsError {
		t.Fatal(res.Text)
	}
	p, _ := r.sw.Board.Snapshot().Task(parent.ID)
	if p.Status != StatusDoing || p.Owner != lead || p.Rev == accepted.Rev || !p.Closure.IsZero() {
		t.Fatalf("the accepted parent was not reopened to its leader: %s owner %s rev %d->%d closure %v", p.Status, p.Owner, accepted.Rev, p.Rev, p.Closure)
	}
	if p.Pool.Phase != "executing" || p.Pool.Revision != accepted.Pool.Revision+1 || len(p.Pool.Votes) != 0 || len(p.Pool.Inspected) != 0 {
		t.Fatalf("pool after the revise: phase %s, revision %d (was %d), votes %v, inspected %v", p.Pool.Phase, p.Pool.Revision, accepted.Pool.Revision, p.Pool.Votes, p.Pool.Inspected)
	}
	if p.Result != "" || p.Evidence != "" {
		t.Fatalf("the reopened parent keeps its old result: %q / %q", p.Result, p.Evidence)
	}
	told := harnessMail(r, "revised")
	if told[lead] != 1 || told[owner] != 1 || told[bystander] != 0 {
		t.Fatalf("notices: %v (want one each to the leader %s and the owner %s, none to %s)", told, lead, owner, bystander)
	}
	if n := harnessMail(r, "reconcile once every child is done")[lead]; n != 1 {
		t.Fatalf("the leader was not told the pool executes again (%d)", n)
	}
}
