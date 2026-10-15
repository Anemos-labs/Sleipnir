package swarm

import (
	"context"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/perm"
)

// A pool waits for every member's agreement, so one member that cannot take part held its pool for good: in a trial the second
// frontend worker's model answered with broken tool calls every few minutes, and the frontend pool never left planning. The manager
// excuses such a member: it leaves the pool, the others' agreement suffices, its open subtasks return to the board, the next member
// leads when it led, and it stays read-only while the pool task is open.
func TestTheManagerExcusesAPoolMemberThatCannotTakePart(t *testing.T) {
	// held is the member whose model calls wait for release, and holding is set while one of them waits.
	var held atomic.Value
	var holding atomic.Bool
	release := make(chan struct{})
	r := newRVRig(t, Config{PersistentPools: true, MaxWorkers: 3, Team: []string{"frontend", "frontend", "frontend"}}, func(ctx context.Context, c *rvCall) rvReply {
		if h, _ := held.Load().(string); h != "" && c.Agent == h {
			holding.Store(true)
			select {
			case <-release:
			case <-ctx.Done():
			}
			holding.Store(false)
		}
		return rvReply{Text: "waiting"}
	})
	if _, err := r.sw.Spawn(SpawnReq{Role: "frontend", Title: "build the UI", Files: []string{"web/**"}, By: "mgr"}); err != nil {
		t.Fatal(err)
	}
	parent := r.sw.Board.Snapshot().Tasks[0]
	ids := slices.Clone(parent.Pool.Members)
	lead, peer, broken := ids[0], ids[1], ids[2]
	pool := func(as, role string, args map[string]any) (string, bool) {
		args["id"] = parent.ID
		res := r.callTool(context.Background(), "pool", as, role, args)
		return res.Text, res.IsError
	}
	if _, bad := pool(lead, "frontend", map[string]any{"action": "propose", "text": "Plain modules; split views and charts; node --test."}); bad {
		t.Fatal("proposal refused")
	}
	p, _ := r.sw.Board.Snapshot().Task(parent.ID)
	for _, m := range []string{lead, peer} {
		if msg, bad := pool(m, "frontend", map[string]any{"action": "agree", "revision": p.Pool.Revision}); bad {
			t.Fatal(msg)
		}
	}
	if _, bad := pool(lead, "frontend", map[string]any{"action": "execute"}); !bad {
		t.Fatal("executed without the third member's agreement")
	}
	if msg, bad := pool("mgr", "manager", map[string]any{"action": "agree", "revision": p.Pool.Revision}); !bad || !strings.Contains(msg, "excuse") {
		t.Fatalf("the manager's agree: %q", msg)
	}

	for _, c := range []struct {
		as, role, member, why, want string
	}{
		{lead, "frontend", broken, "slow", "only the manager"},
		{"mgr", "manager", "fe-9", "slow", "not a member"},
		{"mgr", "manager", broken, "", "say why"},
	} {
		if msg, bad := pool(c.as, c.role, map[string]any{"action": "excuse", "member": c.member, "text": c.why}); !bad || !strings.Contains(msg, c.want) {
			t.Errorf("excuse by %s of %s: %q, want an error with %q", c.as, c.member, msg, c.want)
		}
	}
	msg, bad := pool("mgr", "manager", map[string]any{"action": "excuse", "member": broken, "text": "its model answers with broken tool calls"})
	if bad || !strings.Contains(msg, broken+" excused") {
		t.Fatalf("excuse: %q", msg)
	}
	p, _ = r.sw.Board.Snapshot().Task(parent.ID)
	if slices.Contains(p.Pool.Members, broken) || !slices.Contains(p.Pool.Excused, broken) || len(p.Pool.Votes) != 2 {
		t.Fatalf("pool after the excuse: %+v", p.Pool)
	}
	if msg, bad := pool(lead, "frontend", map[string]any{"action": "execute"}); bad {
		t.Fatalf("the remaining members' agreement did not suffice: %q", msg)
	}
	if msg, bad := pool(broken, "frontend", map[string]any{"action": "agree", "revision": p.Pool.Revision}); !bad {
		t.Fatalf("an excused member still votes: %q", msg)
	}
	// Excused, it stays read-only while the pool is open.
	if d, handled := r.sw.poolPermission(context.Background(), broken, perm.AllowAll{}, perm.Request{Tool: "write", Paths: []string{"web/app.js"}, Writes: true}); !handled || d.Allow {
		t.Fatalf("an excused member may write: handled=%v %+v", handled, d)
	}

	// Its open subtasks go back to the board; a leader that is excused hands the lead on. The leader is a live agent: a run of its own
	// that ends with the subtask in doing hands it in for review (settleClean), and work handed in is not taken back. So its model
	// calls wait until the excuse is done: it is woken, and the subtask is given to it only once its model call waits, so no run of
	// its own can end, and hand the subtask in, before the excuse.
	held.Store(lead)
	r.sw.notify(lead, "info", "hold on")
	rvWait(t, "the leader's model call to be held", holding.Load)
	child, err := r.sw.Board.CreateTask(lead, TaskSpec{Parent: parent.ID, Title: "charts", Role: "frontend", Files: []string{"web/charts.js"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.sw.Board.assignTask(assignReq{by: lead, agent: lead, id: child.ID}); err != nil {
		t.Fatal(err)
	}
	msg, bad = pool("mgr", "manager", map[string]any{"action": "excuse", "member": lead, "text": "out of budget"})
	held.Store("")
	close(release)
	if bad || !strings.Contains(msg, child.ID) || !strings.Contains(msg, "leader "+peer) {
		t.Fatalf("excusing the leader: %q", msg)
	}
	before := p.Rev
	p, _ = r.sw.Board.Snapshot().Task(parent.ID)
	c, _ := r.sw.Board.Snapshot().Task(child.ID)
	if p.Owner != peer || p.Rev <= before || c.Owner != "" || c.Status != StatusTodo {
		t.Fatalf("leader %s (rev %d, was %d), child %+v", p.Owner, p.Rev, before, c)
	}
	if msg, bad := pool("mgr", "manager", map[string]any{"action": "excuse", "member": peer, "text": "x"}); !bad || !strings.Contains(msg, "last member") {
		t.Fatalf("the last member was excused: %q", msg)
	}
	// The log rebuilds the same pool, once the board's queued events are written (another goroutine may be flushing them).
	var rp, rc Task
	rvWait(t, "the log to rebuild the pool", func() bool {
		sn, err := ReplayBoard(r.log.All())
		if err != nil {
			t.Fatal(err)
		}
		rp, _ = sn.Task(parent.ID)
		rc, _ = sn.Task(child.ID)
		return rp.Pool != nil && slices.Equal(rp.Pool.Members, p.Pool.Members) && slices.Equal(rp.Pool.Excused, p.Pool.Excused) &&
			rp.Owner == peer && rc.Owner == "" && rc.Status == StatusTodo
	})
}

// An excused member writes nothing while the pool is open, not even in a child task it still owned when it was excused (the excuse
// and the release of its children are two board operations).
func TestAnExcusedMemberDoesNotWriteInItsChild(t *testing.T) {
	r := newRVRig(t, Config{PersistentPools: true, MaxWorkers: 2, Team: []string{"frontend", "frontend"}}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "waiting"} })
	if _, err := r.sw.Spawn(SpawnReq{Role: "frontend", Title: "build the UI", Files: []string{"web/**"}, By: "mgr"}); err != nil {
		t.Fatal(err)
	}
	parent := r.sw.Board.Snapshot().Tasks[0]
	lead, other := parent.Pool.Members[0], parent.Pool.Members[1]
	if err := r.sw.Board.poolUpdate("mgr", parent.ID, func(_ *Snapshot, _ *Task, p *PoolState) error { p.Phase = "executing"; return nil }); err != nil {
		t.Fatal(err)
	}
	child, err := r.sw.Board.CreateTask(lead, TaskSpec{Parent: parent.ID, Title: "charts", Role: "frontend", Files: []string{"web/charts.js"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.sw.Board.assignTask(assignReq{by: lead, agent: other, id: child.ID}); err != nil {
		t.Fatal(err)
	}
	// The state between the two operations: excused, its child still its own and doing.
	if err := r.sw.Board.poolUpdate("mgr", parent.ID, func(_ *Snapshot, _ *Task, p *PoolState) error {
		p.Members, p.Excused = []string{lead}, []string{other}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if c, _ := r.sw.Board.Snapshot().Task(child.ID); c.Owner != other || c.Status != StatusDoing {
		t.Fatalf("setup: %+v", c)
	}
	if d, handled := r.sw.poolPermission(context.Background(), other, perm.AllowAll{}, perm.Request{Tool: "write", Paths: []string{"web/charts.js"}, Writes: true}); !handled || d.Allow {
		t.Fatalf("an excused member writes in its child: handled=%v %+v", handled, d)
	}
}
