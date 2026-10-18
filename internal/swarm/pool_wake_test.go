package swarm

import (
	"context"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/events"
)

// leaderRequests counts the harness requests to agent that contain text.
func leaderRequests(r *rvRig, agent, text string) int {
	n := 0
	for _, e := range r.log.OfType(events.TypeMailSend) {
		if strings.Contains(string(e.Data), `"to":"`+agent+`"`) && strings.Contains(string(e.Data), text) {
			n++
		}
	}
	return n
}

// executingPool starts a two-member backend pool whose members end every turn at once, puts it in its executing phase and
// returns its task, its leader and the other member, both idle.
func executingPool(t *testing.T) (*rvRig, Task, string, string) {
	t.Helper()
	quick := func(context.Context, *rvCall) rvReply { return rvReply{Text: "waiting"} }
	r := newRVRig(t, Config{PersistentPools: true, MaxWorkers: 2, Team: []string{"backend", "backend"}}, quick)
	if _, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "backend", Files: []string{"app/**"}, By: "mgr"}); err != nil {
		t.Fatal(err)
	}
	parent := r.sw.Board.Snapshot().Tasks[0]
	lead, other := parent.Pool.Members[0], parent.Pool.Members[1]
	if err := r.sw.Board.poolUpdate("mgr", parent.ID, func(_ *Snapshot, _ *Task, p *PoolState) error { p.Phase = "executing"; return nil }); err != nil {
		t.Fatal(err)
	}
	rvWait(t, "the pool's members to be idle", func() bool { return r.idle(lead) && r.idle(other) })
	return r, parent, lead, other
}

// submitChild assigns child to owner (on the board, starting no run) and has the owner submit it.
func submitChild(t *testing.T, r *rvRig, lead, owner string, child Task) {
	t.Helper()
	if _, err := r.sw.Board.assignTask(assignReq{by: lead, agent: owner, id: child.ID}); err != nil {
		t.Fatal(err)
	}
	sawEdit(t, r, owner, child.Files[0])
	if res := r.callTool(context.Background(), "task", owner, "backend", map[string]any{"action": "done", "id": child.ID, "text": "implemented"}); res.IsError {
		t.Fatalf("task done %s: %s", child.ID, res.Text)
	}
}

// newChild creates a todo child of parent with one file in its scope.
func newChild(t *testing.T, r *rvRig, parent Task, lead, file string) Task {
	t.Helper()
	c, err := r.sw.Board.CreateTask(lead, TaskSpec{Parent: parent.ID, Title: "part " + file, Role: "backend", Files: []string{file}})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// acceptAs accepts a task in review as agent `as` with role `role`.
func acceptAs(t *testing.T, r *rvRig, as, role, id string) {
	t.Helper()
	if res := r.callTool(context.Background(), "task", as, role, map[string]any{"action": "accept", "id": id}); res.IsError {
		t.Fatalf("task accept %s: %s", id, res.Text)
	}
}

// An executing pool whose children are all accepted while its leader is idle wakes the leader to reconcile and submit, once per
// state of the children. Without the wake the pool stays executing with its leader idle, and work queued for a free member of its
// role never starts.
func TestAnIdleLeaderIsWokenWhenItsPoolsChildrenSettle(t *testing.T) {
	r, parent, lead, other := executingPool(t)
	settle := func() {
		t.Helper()
		rvWait(t, "the pool's members to be idle", func() bool { return r.idle(lead) && r.idle(other) })
		r.sw.poolWakeTick()
	}
	const woken = "Every child of pool "

	// The leader's child is accepted, the member's has not started: nothing to wake the leader for.
	mine, theirs := newChild(t, r, parent, lead, "app/api.py"), newChild(t, r, parent, lead, "app/test_api.py")
	submitChild(t, r, lead, lead, mine)
	acceptAs(t, r, lead, "backend", mine.ID)
	settle()
	if n := leaderRequests(r, lead, woken); n != 0 {
		t.Fatalf("the leader was woken while %s is todo: %d requests", theirs.ID, n)
	}

	// The last child reaches review: the idle leader is told to accept it, then reconcile and submit.
	submitChild(t, r, lead, other, theirs)
	settle()
	if n := leaderRequests(r, lead, "is still in review"); n != 1 {
		t.Fatalf("the idle leader with a child in review was told %d times, want once", n)
	}
	if n := leaderRequests(r, lead, "task accept id="+theirs.ID); n == 0 {
		t.Fatal("the request does not name the child to accept")
	}
	// Its turns end again with nothing changed: no second request.
	settle()
	settle()
	if n := leaderRequests(r, lead, woken); n != 1 {
		t.Fatalf("an unchanged pool woke its leader %d times, want once", n)
	}

	// The last child is accepted while the leader is idle: one request to reconcile and submit.
	acceptAs(t, r, "mgr", "manager", theirs.ID)
	settle()
	if n := leaderRequests(r, lead, "is accepted and the pool is still executing"); n != 1 {
		t.Fatalf("the idle leader of a pool whose children are all accepted was told %d times, want once", n)
	}
	if n := leaderRequests(r, lead, "pool reconcile id="+parent.ID); n == 0 {
		t.Fatal("the request does not say how to reconcile")
	}
	settle()
	if n := leaderRequests(r, lead, woken); n != 2 {
		t.Fatalf("the leader was woken %d times in all, want 2", n)
	}
	if n := leaderRequests(r, other, woken); n != 0 {
		t.Fatalf("a member that does not lead was woken %d times", n)
	}

	// Reconciling moves the pool out of executing: nothing more is sent.
	if res := r.callTool(context.Background(), "pool", lead, "backend", map[string]any{"action": "reconcile", "id": parent.ID}); res.IsError {
		t.Fatal(res.Text)
	}
	settle()
	if n := leaderRequests(r, lead, woken); n != 2 {
		t.Fatalf("a reconciling pool woke its leader: %d requests in all", n)
	}
}

// The supervisor's pass runs the wake: a leader idle on a settled pool is woken without any other event.
func TestTheSupervisorWakesAnIdleLeaderOfASettledPool(t *testing.T) {
	r, parent, lead, other := executingPool(t)
	c := newChild(t, r, parent, lead, "app/api.py")
	submitChild(t, r, lead, lead, c)
	acceptAs(t, r, lead, "backend", c.ID)
	rvWait(t, "the pool's members to be idle", func() bool { return r.idle(lead) && r.idle(other) })
	r.sw.superviseOnce(r.sw.deps.Now())
	if n := leaderRequests(r, lead, "Every child of pool "+parent.ID+" is accepted"); n != 1 {
		t.Fatalf("the supervisor woke the idle leader %d times, want once", n)
	}
}
