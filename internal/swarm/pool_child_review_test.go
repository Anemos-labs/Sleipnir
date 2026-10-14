package swarm

import (
	"context"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

// A pool child in review is accepted by the pool's leader (or the manager), and the pool reconciles once every child is accepted.
// In a trial the leader submitted both children and waited for the manager, the manager waited for the children, and the leader's
// reconcile was refused with "child T4 is not complete": nobody said who takes a child out of review.
func TestWhoAcceptsAPoolChildInReviewIsSaid(t *testing.T) {
	r := newRVRig(t, Config{PersistentPools: true, MaxWorkers: 2, Team: []string{"backend", "backend"}}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "working"} })
	if _, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "backend", Files: []string{"app/**"}, By: "mgr"}); err != nil {
		t.Fatal(err)
	}
	parent := r.sw.Board.Snapshot().Tasks[0]
	lead, other := parent.Pool.Members[0], parent.Pool.Members[1]
	if err := r.sw.Board.poolUpdate("mgr", parent.ID, func(_ *Snapshot, _ *Task, p *PoolState) error { p.Phase = "executing"; return nil }); err != nil {
		t.Fatal(err)
	}
	child := func(title, owner, file string) Task {
		c, err := r.sw.Board.CreateTask(lead, TaskSpec{Parent: parent.ID, Title: title, Role: "backend", Files: []string{file}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.sw.Board.assignTask(assignReq{by: lead, agent: owner, id: c.ID}); err != nil {
			t.Fatal(err)
		}
		return c
	}
	mine, theirs := child("api", lead, "app/api.py"), child("tests", other, "app/test_api.py")
	done := func(as, id string) string {
		res := r.callTool(context.Background(), "task", as, "backend", map[string]any{"action": "done", "id": id, "text": "implemented"})
		if res.IsError {
			t.Fatalf("done %s: %s", id, res.Text)
		}
		return res.Text
	}
	if got := done(other, theirs.ID); !strings.Contains(got, "The leader of pool "+parent.ID+", "+lead+", accepts it") {
		t.Errorf("a member's child in review: %q", got)
	}
	told := false
	for _, e := range r.log.All() {
		if e.Type == events.TypeMailSend && strings.Contains(string(e.Data), "Child "+theirs.ID+" of pool "+parent.ID+" is in review") && strings.Contains(string(e.Data), `"to":"`+lead+`"`) {
			told = true
		}
	}
	if !told {
		t.Error("the leader was not told that a member's child is in review")
	}
	if got := done(lead, mine.ID); !strings.Contains(got, "As the leader of pool "+parent.ID+", check it and accept it yourself (task accept id="+mine.ID+")") {
		t.Errorf("the leader's own child in review: %q", got)
	}
	res := r.callTool(context.Background(), "pool", lead, "backend", map[string]any{"action": "reconcile", "id": parent.ID})
	if !res.IsError || !strings.Contains(res.Text, "waits in review") || !strings.Contains(res.Text, "task accept id=") {
		t.Errorf("reconcile with children in review: %q", res.Text)
	}
	// pool on a task that is not a pool parent, and the manager's inspect, say what pool works on
	if res := r.callTool(context.Background(), "pool", "mgr", "manager", map[string]any{"action": "propose", "id": "root", "text": "x"}); !res.IsError || !strings.Contains(res.Text, "role pool's parent task") {
		t.Errorf("pool on no task: %q", res.Text)
	}
	if res := r.callTool(context.Background(), "pool", "mgr", "manager", map[string]any{"action": "inspect", "id": parent.ID}); !res.IsError || !strings.Contains(res.Text, "the manager reads the combined result") {
		t.Errorf("the manager's inspect: %q", res.Text)
	}
}

// Once every member has confirmed the reconciled result, the leader is told to submit it, and a second reconcile of the unchanged
// result is refused with the same advice. In a trial the leader reconciled again after both confirmations, which reopened them. A
// child reopened after that is what the refusal names: submitting would be refused while it is not complete.
func TestAConfirmedPoolIsToldToSubmit(t *testing.T) {
	r := newRVRig(t, Config{PersistentPools: true, MaxWorkers: 2, Team: []string{"backend", "backend"}}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "working"} })
	if _, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "backend", Files: []string{"app/**"}, By: "mgr"}); err != nil {
		t.Fatal(err)
	}
	parent := r.sw.Board.Snapshot().Tasks[0]
	lead, other := parent.Pool.Members[0], parent.Pool.Members[1]
	if err := r.sw.Board.poolUpdate("mgr", parent.ID, func(_ *Snapshot, _ *Task, p *PoolState) error { p.Phase = "executing"; return nil }); err != nil {
		t.Fatal(err)
	}
	pool := func(as string, args map[string]any) string {
		args["id"] = parent.ID
		res := r.callTool(context.Background(), "pool", as, "backend", args)
		if res.IsError {
			t.Fatalf("%s %v: %s", as, args["action"], res.Text)
		}
		return res.Text
	}
	c, err := r.sw.Board.CreateTask(lead, TaskSpec{Parent: parent.ID, Title: "api", Role: "backend", Files: []string{"app/api.py"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.sw.Board.assignTask(assignReq{by: lead, agent: lead, id: c.ID}); err != nil {
		t.Fatal(err)
	}
	for _, args := range []map[string]any{{"action": "done", "id": c.ID, "text": "implemented"}, {"action": "accept", "id": c.ID}} {
		if res := r.callTool(context.Background(), "task", lead, "backend", args); res.IsError {
			t.Fatalf("task %v: %s", args["action"], res.Text)
		}
	}
	pool(lead, map[string]any{"action": "reconcile", "text": "the combined contract"})
	p, _ := r.sw.Board.Snapshot().Task(parent.ID)
	pool(lead, map[string]any{"action": "confirm", "revision": p.Pool.Revision})
	pool(other, map[string]any{"action": "confirm", "revision": p.Pool.Revision})
	ready := false
	for _, e := range r.log.All() {
		if e.Type == events.TypeMailSend && strings.Contains(string(e.Data), "Every member has confirmed revision") && strings.Contains(string(e.Data), "task done id="+parent.ID) {
			ready = true
		}
	}
	if !ready {
		t.Error("the leader was not told to submit once every member confirmed")
	}
	res := r.callTool(context.Background(), "pool", lead, "backend", map[string]any{"action": "reconcile", "id": parent.ID})
	if !res.IsError || !strings.Contains(res.Text, "submit it with task done id="+parent.ID) {
		t.Errorf("a second reconcile of a confirmed result: %q", res.Text)
	}
	if err := r.sw.Board.Reopen("mgr", c.ID); err != nil {
		t.Fatal(err)
	}
	res = r.callTool(context.Background(), "pool", lead, "backend", map[string]any{"action": "reconcile", "id": parent.ID})
	if !res.IsError || !strings.Contains(res.Text, "child "+c.ID+" is not complete") {
		t.Errorf("a reconcile with a reopened child: %q", res.Text)
	}
}

// A confirm refused because a merge superseded the reconciliation tells the leader, the one who can reconcile again, and tells the
// member to wait for the new revision. In a trial the member, told only that "the pool leader must call pool reconcile again", kept
// calling pool inspect while the leader polled the task.
func TestASupersededReconciliationReachesTheLeader(t *testing.T) {
	r := newRVRig(t, Config{PersistentPools: true, MaxWorkers: 2, Team: []string{"frontend", "frontend"}}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "working"} })
	if _, err := r.sw.Spawn(SpawnReq{Role: "frontend", Title: "web", Files: []string{"web/**"}, By: "mgr"}); err != nil {
		t.Fatal(err)
	}
	parent := r.sw.Board.Snapshot().Tasks[0]
	lead, other := parent.Pool.Members[0], parent.Pool.Members[1]
	if err := r.sw.Board.poolUpdate("mgr", parent.ID, func(_ *Snapshot, _ *Task, p *PoolState) error { p.Phase = "executing"; return nil }); err != nil {
		t.Fatal(err)
	}
	if res := r.callTool(context.Background(), "pool", lead, "frontend", map[string]any{"action": "reconcile", "id": parent.ID, "text": "combined"}); res.IsError {
		t.Fatal(res.Text)
	}
	r.sw.review.mu.Lock()
	r.sw.review.merges++ // another task's work lands
	r.sw.review.mu.Unlock()
	p, _ := r.sw.Board.Snapshot().Task(parent.ID)
	res := r.callTool(context.Background(), "pool", other, "frontend", map[string]any{"action": "confirm", "id": parent.ID, "revision": p.Pool.Revision})
	if !res.IsError || !strings.Contains(res.Text, "The leader "+lead+" has been told") {
		t.Fatalf("the member's refused confirm: %q", res.Text)
	}
	told := false
	for _, e := range r.log.All() {
		if e.Type == events.TypeMailSend && strings.Contains(string(e.Data), "Call pool reconcile again") && strings.Contains(string(e.Data), `"to":"`+lead+`"`) {
			told = true
		}
	}
	if !told {
		t.Error("the leader was not told to reconcile again")
	}
}

// While members agree on the leader's proposal, the manager's proposal is refused: it would reset their votes and restart the round,
// as it did several times in a demo run. Without votes the manager may still propose (a leader that cannot).
func TestTheManagerDoesNotResetAPoolThatIsAgreeing(t *testing.T) {
	r := newRVRig(t, Config{PersistentPools: true, MaxWorkers: 2, Team: []string{"frontend", "frontend"}}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "working"} })
	if _, err := r.sw.Spawn(SpawnReq{Role: "frontend", Title: "web", Files: []string{"web/**"}, By: "mgr"}); err != nil {
		t.Fatal(err)
	}
	parent := r.sw.Board.Snapshot().Tasks[0]
	lead, other := parent.Pool.Members[0], parent.Pool.Members[1]
	call := func(as, role string, args map[string]any) *tools.Result {
		args["id"] = parent.ID
		return r.callTool(context.Background(), "pool", as, role, args)
	}
	if res := call("mgr", "manager", map[string]any{"action": "propose", "text": "a plan from the manager"}); res.IsError {
		t.Fatalf("the manager's proposal with no votes: %s", res.Text)
	}
	if res := call(lead, "frontend", map[string]any{"action": "propose", "text": "the leader's plan"}); res.IsError {
		t.Fatal(res.Text)
	}
	p, _ := r.sw.Board.Snapshot().Task(parent.ID)
	if res := call(other, "frontend", map[string]any{"action": "agree", "revision": p.Pool.Revision}); res.IsError {
		t.Fatal(res.Text)
	}
	res := call("mgr", "manager", map[string]any{"action": "propose", "text": "another plan"})
	if !res.IsError || !strings.Contains(res.Text, "Mail the leader "+lead) {
		t.Fatalf("the manager's proposal during agreement: %q", res.Text)
	}
	if q, _ := r.sw.Board.Snapshot().Task(parent.ID); len(q.Pool.Votes) != 1 || q.Pool.Revision != p.Pool.Revision {
		t.Fatalf("the votes were reset: %+v", q.Pool)
	}
}
