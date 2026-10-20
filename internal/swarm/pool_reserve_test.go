package swarm

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
)

// heldModel answers every model call at once, except those of the members it holds, which stay in their turn until their run is
// cancelled: such a member is running (a leader at work, a member waiting for its share of the pool), which a spawn cannot start.
type heldModel struct {
	mu   sync.Mutex
	held map[string]bool
}

// hold keeps id's later model calls in their turn, as the leader's are.
func (h *heldModel) hold(id string) {
	h.mu.Lock()
	h.held[id] = true
	h.mu.Unlock()
}

func (h *heldModel) unhold(id string) {
	h.mu.Lock()
	delete(h.held, id)
	h.mu.Unlock()
}

func (h *heldModel) call(ctx context.Context, c *rvCall) rvReply {
	h.mu.Lock()
	held := h.held[c.Agent]
	h.mu.Unlock()
	if held {
		<-ctx.Done()
		return rvReply{Text: "stopped"}
	}
	return rvReply{Text: "noted"}
}

// newHeldPoolRig is newPoolRig whose leader and second member are in a turn (the second until released, release); the third
// member's turns end at once.
func newHeldPoolRig(t *testing.T) (*poolRig, *heldModel) {
	t.Helper()
	h := &heldModel{held: map[string]bool{"be-1": true, "be-2": true}}
	r := newRVRig(t, Config{PersistentPools: true, MaxWorkers: 3, Team: []string{"backend", "backend", "backend"}}, h.call)
	if _, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "backend", Files: []string{"app/**"}, By: "mgr"}); err != nil {
		t.Fatal(err)
	}
	parent := r.sw.Board.Snapshot().Tasks[0]
	m := parent.Pool.Members
	pr := &poolRig{rvRig: r, parent: parent, lead: m[0], second: m[1], third: m[2]}
	if pr.lead != "be-1" || pr.second != "be-2" {
		t.Fatalf("members %v", m)
	}
	rvWait(t, "the leader and "+pr.second+" in a turn", func() bool { return r.running(pr.lead) && r.running(pr.second) })
	return pr, h
}

// release ends the turn of the held member and every later one at once.
func (r *poolRig) release(h *heldModel, id string) {
	r.t.Helper()
	h.unhold(id)
	m := r.sw.get(id)
	m.mu.Lock()
	rs := m.run
	m.mu.Unlock()
	if rs != nil {
		rs.cancel()
	}
}

// spawn calls the spawn tool as a backend member (or as the manager for "mgr").
func (r *poolRig) spawn(as string, args map[string]any) string {
	r.t.Helper()
	role := "backend"
	if as == "mgr" {
		role = "manager"
	}
	res := r.callTool(context.Background(), "spawn", as, role, args)
	if res.IsError {
		r.t.Fatalf("spawn %v as %s: %s", args, as, res.Text)
	}
	return res.Text
}

// A child a leader assigns to a member that is still in a turn is reserved for that member: it stays todo and the member's,
// the member is told to claim it, nobody else can take it, and the member's claim succeeds. Refusing that spawn made a pool
// leader claim three children itself in a 12-worker run, and nobody could take them back: 25 refused calls and 20 mails.
func TestAChildForAMemberInATurnIsReservedForIt(t *testing.T) {
	r, _ := newHeldPoolRig(t)
	r.executing()
	r.must(r.task(r.lead, map[string]any{"action": "create", "parent": r.parent.ID, "title": "handlers", "files": []string{"app/handlers/**"}}), "create")
	got := r.spawn(r.lead, map[string]any{"agent": r.second, "task": "T2"})
	if !strings.Contains(got, r.second+" is still in a turn, so T2 is reserved for it") {
		t.Errorf("spawn result: %q", got)
	}
	c, _ := r.sw.Board.Snapshot().Task("T2")
	if c.Status != StatusTodo || c.Owner != r.second {
		t.Fatalf("T2 is %s, owned by %q: want todo, reserved for %s", c.Status, c.Owner, r.second)
	}
	mail := strings.Join(r.mailTo(r.second), "\n")
	if !strings.Contains(mail, "T2 (a child of "+r.parent.ID+") is reserved for you by "+r.lead+": claim it now (task claim id=T2)") {
		t.Errorf("mail to %s: %q", r.second, mail)
	}
	// The same spawn again changes nothing and tells the member nothing new.
	r.spawn(r.lead, map[string]any{"agent": r.second, "task": "T2"})
	if n := strings.Count(strings.Join(r.mailTo(r.second), "\n"), "is reserved for you"); n != 1 {
		t.Errorf("the member was told %d times", n)
	}
	r.refused(r.task(r.third, map[string]any{"action": "claim", "id": "T2"}), "another member's claim", "T2 is already owned by "+r.second)
	r.must(r.task(r.second, map[string]any{"action": "claim", "id": "T2"}), "the member's claim")
	if c, _ := r.sw.Board.Snapshot().Task("T2"); c.Status != StatusDoing || c.Owner != r.second {
		t.Fatalf("after the claim T2 is %s, owned by %q", c.Status, c.Owner)
	}
}

// A reserved child whose member ends its run without claiming it is started for that member by the repair pass.
func TestAReservedChildStartsWhenItsMemberGoesIdle(t *testing.T) {
	r, h := newHeldPoolRig(t)
	r.executing()
	r.must(r.task(r.lead, map[string]any{"action": "create", "parent": r.parent.ID, "title": "handlers", "files": []string{"app/handlers/**"}}), "create")
	r.spawn(r.lead, map[string]any{"agent": r.second, "task": "T2"})
	now := r.sw.deps.Now()
	r.sw.repairTick(now)
	if c, _ := r.sw.Board.Snapshot().Task("T2"); c.Status != StatusTodo {
		t.Fatalf("T2 started while its member was in a turn: %s", c.Status)
	}
	r.release(h, r.second) // its runs end without a claim
	// The start is the assignment in the log: the member, no longer held, ends that run at once and its work moves on (to review),
	// so the board may already be past doing when the test looks; on a loaded runner it was.
	started := func() bool {
		for _, e := range r.log.OfType(events.TypeBoardOp) {
			var op struct{ Op, Task, Owner, Status string }
			if json.Unmarshal(e.Data, &op) == nil && op.Op == "assign" && op.Task == "T2" && op.Owner == r.second && op.Status == "doing" {
				return true
			}
		}
		return false
	}
	i := 0
	rvWait(t, "T2 started for "+r.second, func() bool {
		i++
		r.sw.repairTick(now.Add(time.Duration(i) * time.Minute))
		return started()
	})
	r.sw.repairTick(now.Add(time.Duration(i+1) * time.Minute))
	r.sw.mu.Lock()
	left := len(r.sw.poolReserved)
	r.sw.mu.Unlock()
	if left != 0 {
		t.Errorf("%d reservations left after the start", left)
	}
}

// A member already working on another child is not given a second one: the refusal names the members who have none.
func TestAChildIsNotReservedForAMemberWithWorkInProgress(t *testing.T) {
	r, _ := newHeldPoolRig(t)
	r.executing()
	r.must(r.task(r.lead, map[string]any{"action": "create", "parent": r.parent.ID, "title": "handlers", "files": []string{"app/handlers/**"}}), "create")
	r.must(r.task(r.lead, map[string]any{"action": "create", "parent": r.parent.ID, "title": "store", "files": []string{"app/store/**"}}), "create")
	if _, err := r.sw.Board.assignTask(assignReq{by: r.lead, agent: r.second, id: "T2"}); err != nil {
		t.Fatal(err)
	}
	res := r.callTool(context.Background(), "spawn", r.lead, "backend", map[string]any{"agent": r.second, "task": "T3"})
	r.refused(res, "a second child for a busy member", r.second+" is working on T2: give T3 to a member with no child in progress ("+r.lead+", "+r.third+")")
	if c, _ := r.sw.Board.Snapshot().Task("T3"); c.Owner != "" {
		t.Errorf("T3 was given to %s", c.Owner)
	}
}

// A leader that claimed a child it meant for a member gives it away with spawn: to a member in a turn as a reservation, to an
// idle one as an assignment. Before, the board only said the task was the leader's, and only the manager could revise it.
func TestALeaderGivesAwayAChildItClaimed(t *testing.T) {
	r, h := newHeldPoolRig(t)
	r.executing()
	r.must(r.task(r.lead, map[string]any{"action": "create", "parent": r.parent.ID, "title": "handlers", "files": []string{"app/handlers/**"}}), "create")
	r.must(r.task(r.lead, map[string]any{"action": "create", "parent": r.parent.ID, "title": "store", "files": []string{"app/store/**"}}), "create")
	r.must(r.task(r.lead, map[string]any{"action": "claim", "id": "T2"}), "the leader's claim")
	r.must(r.task(r.lead, map[string]any{"action": "claim", "id": "T3"}), "the leader's claim")

	r.spawn(r.lead, map[string]any{"agent": r.second, "task": "T2"})
	if c, _ := r.sw.Board.Snapshot().Task("T2"); c.Status != StatusTodo || c.Owner != r.second {
		t.Fatalf("T2 is %s, owned by %q: want todo, reserved for %s", c.Status, c.Owner, r.second)
	}
	// Idle after a turn, with no mail to start another: a member is idle before its first turn starts too (on a loaded runner
	// that turn began after this check, and T3 was reserved instead of assigned).
	rvWait(t, r.third+" idle after its first turn", func() bool {
		m := r.sw.get(r.third)
		if m == nil {
			return false
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.life == lifeIdle && m.runSeq > 0 && m.a.PendingInbox() == 0 && m.box.empty()
	})
	// Held in the turn the spawn starts: a member whose turn ends at once submits its child before the test looks (on a loaded
	// runner T3 was already in review).
	h.hold(r.third)
	r.spawn(r.lead, map[string]any{"agent": r.third, "task": "T3"})
	if c, _ := r.sw.Board.Snapshot().Task("T3"); c.Status != StatusDoing || c.Owner != r.third {
		t.Fatalf("T3 is %s, owned by %q: want doing, %s", c.Status, c.Owner, r.third)
	}
	// A member cannot give away its child: only the leader assigns.
	res := r.callTool(context.Background(), "spawn", r.third, "backend", map[string]any{"agent": r.lead, "task": "T3"})
	r.refused(res, "a member giving away its child", "led by "+r.lead+", not by you")
}
