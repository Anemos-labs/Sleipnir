package swarm

import (
	"context"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/events"
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
}
