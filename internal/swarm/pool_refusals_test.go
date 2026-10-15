package swarm

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

// poolRig is a three-member backend pool on one parent task, with helpers that call the swarm tools as a member.
type poolRig struct {
	*rvRig
	parent              Task
	lead, second, third string
}

// newPoolRig starts a backend pool of three on a parent scoped to app/**; it is planning, with nothing proposed.
func newPoolRig(t *testing.T) *poolRig {
	t.Helper()
	r := newRVRig(t, Config{PersistentPools: true, MaxWorkers: 3, Team: []string{"backend", "backend", "backend"}}, idleModel)
	if _, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "backend", Files: []string{"app/**"}, By: "mgr"}); err != nil {
		t.Fatal(err)
	}
	parent := r.sw.Board.Snapshot().Tasks[0]
	m := parent.Pool.Members
	return &poolRig{rvRig: r, parent: parent, lead: m[0], second: m[1], third: m[2]}
}

// pool calls the pool tool on the parent as a backend member.
func (r *poolRig) pool(as string, args map[string]any) *tools.Result {
	r.t.Helper()
	if _, ok := args["id"]; !ok {
		args["id"] = r.parent.ID
	}
	return r.callTool(context.Background(), "pool", as, "backend", args)
}

// task calls the task tool as a backend member (or as the manager for "mgr").
func (r *poolRig) task(as string, args map[string]any) *tools.Result {
	r.t.Helper()
	role := "backend"
	if as == "mgr" {
		role = "manager"
	}
	return r.callTool(context.Background(), "task", as, role, args)
}

// must fails the test on a refusal and returns the result's text.
func (r *poolRig) must(res *tools.Result, what string) string {
	r.t.Helper()
	if res.IsError {
		r.t.Fatalf("%s: %s", what, res.Text)
	}
	return res.Text
}

// refused fails the test unless res is a refusal containing every want, and returns its text.
func (r *poolRig) refused(res *tools.Result, what string, want ...string) string {
	r.t.Helper()
	if !res.IsError {
		r.t.Errorf("%s was accepted: %s", what, res.Text)
		return res.Text
	}
	for _, w := range want {
		if !strings.Contains(res.Text, w) {
			r.t.Errorf("%s: %q lacks %q", what, res.Text, w)
		}
	}
	return res.Text
}

// rev is the parent's current pool revision.
func (r *poolRig) rev() int {
	p, _ := r.sw.Board.Snapshot().Task(r.parent.ID)
	return p.Pool.Revision
}

// mailTo returns the texts of the harness mail sent to id, in order.
func (r *poolRig) mailTo(id string) []string {
	var out []string
	for _, e := range r.log.All() {
		if e.Type != events.TypeMailSend {
			continue
		}
		var m Message
		if err := json.Unmarshal(e.Data, &m); err == nil && m.To == id {
			out = append(out, m.Text)
		}
	}
	return out
}

// executing proposes, has every member agree and executes the parent's plan.
func (r *poolRig) executing() {
	r.t.Helper()
	r.must(r.pool(r.lead, map[string]any{"action": "propose", "text": "split handlers and tests"}), "propose")
	for _, m := range []string{r.lead, r.second, r.third} {
		r.must(r.pool(m, map[string]any{"action": "agree", "revision": r.rev()}), "agree "+m)
	}
	r.must(r.pool(r.lead, map[string]any{"action": "execute"}), "execute")
}

// A vote without a revision counts for the revision the member last read, when that is still the current one; otherwise each
// cause has its own refusal, with the current revision and phase and the exact call. One refusal used to cover four causes, and
// most refused votes in a long run had only left out the revision.
func TestAVoteWithoutRevisionCountsForWhatTheMemberRead(t *testing.T) {
	r := newPoolRig(t)
	id := r.parent.ID
	r.refused(r.pool(r.second, map[string]any{"action": "agree", "revision": 1}), "an agree before any proposal",
		"nothing has been proposed for "+id+" yet", "wait for its leader "+r.lead+"'s proposal")
	r.must(r.pool(r.lead, map[string]any{"action": "propose", "text": "split handlers and tests"}), "propose")
	r.refused(r.pool(r.second, map[string]any{"action": "agree"}), "an agree with no revision from a member that read nothing",
		"pool agree needs revision: "+id+" is at revision 2 (planning)", "task get id="+id, "pool agree id="+id+" revision=2")

	r.must(r.task(r.second, map[string]any{"action": "get", "id": id}), "task get")
	got := r.must(r.pool(r.second, map[string]any{"action": "agree"}), "an agree with no revision after task get")
	for _, want := range []string{"agree recorded for revision 2 (the revision you last read)", "1 of 3 members", "waiting for " + r.lead + ", " + r.third} {
		if !strings.Contains(got, want) {
			t.Errorf("agree result %q lacks %q", got, want)
		}
	}
	// The leader wrote the proposal: it has read it.
	r.must(r.pool(r.lead, map[string]any{"action": "agree"}), "the proposer's agree with no revision")

	// pool get reads the pool too; a new proposal after that read makes the read stale.
	r.must(r.pool(r.third, map[string]any{"action": "get"}), "pool get")
	r.must(r.pool(r.lead, map[string]any{"action": "propose", "text": "split handlers, validation and tests"}), "a second proposal")
	r.refused(r.pool(r.third, map[string]any{"action": "agree"}), "an agree with no revision over a stale read",
		"is at revision 3 (planning), and you last read revision 2", "pool agree id="+id+" revision=3")
	r.refused(r.pool(r.third, map[string]any{"action": "agree", "revision": 2}), "a stale revision",
		"revision 2 is stale: "+id+" is at revision 3 (planning)", "pool agree id="+id+" revision=3")
	r.refused(r.pool(r.third, map[string]any{"action": "agree", "revision": 9}), "an unknown revision",
		"revision 9 is not one of "+id+"'s: it is at revision 3 (planning)")
	r.must(r.pool(r.third, map[string]any{"action": "agree", "revision": 3}), "an agree with the revision")
}

// An agree or confirm in the wrong phase says what the phase calls for, and one from outside the pool says whose it is.
func TestAVoteInTheWrongPhaseSaysWhatTheCallIs(t *testing.T) {
	r := newPoolRig(t)
	id := r.parent.ID
	r.refused(r.pool("fe-1", map[string]any{"action": "agree", "revision": 1}), "a vote from outside the pool",
		"you are not a member of "+id+"'s pool (members: "+r.lead+", "+r.second+", "+r.third+")")
	r.must(r.pool(r.lead, map[string]any{"action": "propose", "text": "split handlers and tests"}), "propose")
	r.refused(r.pool(r.second, map[string]any{"action": "confirm", "revision": 2}), "a confirm while planning",
		id+" is planning revision 2", "agree to the plan instead (pool agree id="+id+" revision=2)")
	for _, m := range []string{r.lead, r.second, r.third} {
		r.must(r.pool(m, map[string]any{"action": "agree", "revision": 2}), "agree")
	}
	r.must(r.pool(r.lead, map[string]any{"action": "execute"}), "execute")
	r.refused(r.pool(r.second, map[string]any{"action": "agree", "revision": 2}), "an agree while executing",
		id+" is already executing revision 2's plan: no vote needed", "the child task its leader "+r.lead+" assigns you")
	r.refused(r.pool(r.lead, map[string]any{"action": "agree"}), "the leader's agree while executing",
		"no vote needed", "task create parent="+id)
	r.refused(r.pool(r.second, map[string]any{"action": "confirm", "revision": 2}), "a confirm while executing",
		"nothing to confirm until its leader "+r.lead+" reconciles")
	r.must(r.pool(r.lead, map[string]any{"action": "reconcile", "text": "the combined contract"}), "reconcile")
	r.refused(r.pool(r.second, map[string]any{"action": "agree", "revision": 3}), "an agree while reconciling",
		id+" is reconciling revision 3", "confirm the combined result instead (pool confirm id="+id+" revision=3)")
	if res := r.callTool(context.Background(), "pool", "mgr", "manager", map[string]any{"action": "excuse", "id": id, "member": r.third, "text": "its model keeps failing"}); res.IsError {
		t.Fatal(res.Text)
	}
	r.refused(r.pool(r.third, map[string]any{"action": "confirm", "revision": 3}), "a vote from an excused member",
		"the manager excused you from "+id+"'s pool")
}

// The leader's refused execute names who has not agreed to the current revision; a member's execute says who executes.
func TestExecuteNamesTheMembersStillToAgree(t *testing.T) {
	r := newPoolRig(t)
	id := r.parent.ID
	r.refused(r.pool(r.lead, map[string]any{"action": "execute"}), "an execute before any proposal", "nothing has been proposed for "+id+" yet", "pool propose id="+id)
	r.must(r.pool(r.lead, map[string]any{"action": "propose", "text": "split handlers and tests"}), "propose")
	r.must(r.pool(r.second, map[string]any{"action": "agree", "revision": 2}), "agree")
	r.refused(r.pool(r.lead, map[string]any{"action": "execute"}), "an execute with votes missing",
		"every member must agree to revision 2 before execution: waiting for "+r.lead+", "+r.third+" (1 of 3 agreed)")
	r.refused(r.pool(r.second, map[string]any{"action": "execute"}), "a member's execute",
		"only "+id+"'s leader "+r.lead+" (or the manager) executes its plan")
	r.refused(r.pool(r.second, map[string]any{"action": "propose", "text": "mine instead"}), "a member's proposal",
		"only "+id+"'s leader "+r.lead+" (or the manager) proposes its plan", "pool agree id="+id+" revision=2")
}

// Single votes are not mailed to the leader. The vote that completes a round tells the leader once what follows (execute, or
// submit), unless the leader cast it, and the caller's answer says it either way.
func TestOnlyTheVoteThatCompletesARoundIsMailedToTheLeader(t *testing.T) {
	r := newPoolRig(t)
	id := r.parent.ID
	r.must(r.pool(r.lead, map[string]any{"action": "propose", "text": "split handlers and tests"}), "propose")
	before := len(r.mailTo(r.lead))
	r.must(r.pool(r.lead, map[string]any{"action": "agree", "revision": 2}), "agree")
	r.must(r.pool(r.second, map[string]any{"action": "agree", "revision": 2}), "agree")
	last := r.must(r.pool(r.third, map[string]any{"action": "agree", "revision": 2}), "the last agree")
	if !strings.Contains(last, "Every member has agreed to revision 2; the leader "+r.lead+" executes it") {
		t.Errorf("the last voter's answer: %q", last)
	}
	r.must(r.pool(r.third, map[string]any{"action": "agree", "revision": 2}), "a repeated agree")
	mail := r.mailTo(r.lead)[before:]
	if len(mail) != 1 || !strings.Contains(mail[0], "Every member has agreed to revision 2: execute it with pool execute id="+id) {
		t.Fatalf("the leader's mail during the round: %q", mail)
	}
	for _, m := range r.mailTo(r.lead) {
		if strings.Contains(m, " recorded ") {
			t.Errorf("a vote was echoed to the leader: %q", m)
		}
	}
	r.must(r.pool(r.lead, map[string]any{"action": "execute"}), "execute")
	r.must(r.pool(r.lead, map[string]any{"action": "reconcile", "text": "the combined contract"}), "reconcile")
	for _, m := range []string{r.second, r.third} {
		r.must(r.pool(m, map[string]any{"action": "confirm", "revision": 3}), "confirm "+m)
	}
	before = len(r.mailTo(r.lead))
	own := r.must(r.pool(r.lead, map[string]any{"action": "confirm", "revision": 3}), "the leader's last confirm")
	if !strings.Contains(own, "Every member has confirmed revision 3: submit the pool's result with task done id="+id) {
		t.Errorf("the leader's own last confirm: %q", own)
	}
	if mail := r.mailTo(r.lead)[before:]; len(mail) != 0 {
		t.Errorf("the leader was mailed its own vote: %q", mail)
	}
}

// Reconcile and the parent's submission name the children that are not finished, with their status and owner, and the members
// whose confirmation is missing.
func TestReconcileAndSubmitNameWhatIsMissing(t *testing.T) {
	r := newPoolRig(t)
	id := r.parent.ID
	r.executing()
	a := r.must(r.task(r.lead, map[string]any{"action": "create", "parent": id, "title": "handlers", "files": []string{"app/handlers.go"}}), "create a child with no role")
	if !strings.HasPrefix(a, "created T2") {
		t.Fatalf("child: %q", a)
	}
	r.must(r.task(r.lead, map[string]any{"action": "create", "parent": id, "role": "backend", "title": "tests", "files": []string{"app/handlers_test.go"}}), "create")
	if _, err := r.sw.Board.assignTask(assignReq{by: r.lead, agent: r.second, id: "T2"}); err != nil {
		t.Fatal(err)
	}
	r.refused(r.pool(r.lead, map[string]any{"action": "reconcile"}), "a reconcile with children open",
		"child T2 is not complete (doing, "+r.second+"); nor are T3 (todo, unassigned): reconcile once every child is accepted")
	r.refused(r.task(r.lead, map[string]any{"action": "done", "id": id, "text": "x"}), "a submission with children open",
		"child T2 is not done (doing, "+r.second+")")
	for _, c := range []string{"T2", "T3"} {
		if err := r.sw.Board.resolve("mgr", c, "not needed"); err != nil {
			t.Fatal(err)
		}
	}
	r.must(r.pool(r.lead, map[string]any{"action": "reconcile", "text": "the combined contract"}), "reconcile")
	r.must(r.pool(r.second, map[string]any{"action": "confirm", "revision": r.rev()}), "confirm")
	r.refused(r.task(r.lead, map[string]any{"action": "done", "id": id, "text": "x"}), "a submission with confirmations missing",
		"waiting for "+r.lead+", "+r.third+" to confirm revision "+strconv.Itoa(r.rev())+" (1 of 3 confirmed)")
}

// A pool leader is refused what is the manager's with the real rule and the condition that failed; a child it creates without a
// role takes its parent's.
func TestPoolAuthorityRefusalsNameTheFailedCondition(t *testing.T) {
	r := newPoolRig(t)
	id := r.parent.ID
	r.must(r.pool(r.lead, map[string]any{"action": "propose", "text": "split handlers and tests"}), "propose")
	r.refused(r.task(r.lead, map[string]any{"action": "create", "parent": id, "title": "early", "files": []string{"app/a.go"}}), "a child while planning",
		"a pool leader the children of the pool it leads while it executes", id+" is still planning")
	for _, m := range []string{r.lead, r.second, r.third} {
		r.must(r.pool(m, map[string]any{"action": "agree", "revision": 2}), "agree")
	}
	r.must(r.pool(r.lead, map[string]any{"action": "execute"}), "execute")
	r.refused(r.task(r.second, map[string]any{"action": "create", "parent": id, "title": "mine", "files": []string{"app/a.go"}}), "a member's child",
		id+"'s pool is led by "+r.lead+", not by you")
	r.refused(r.task(r.lead, map[string]any{"action": "create", "parent": id, "role": "frontend", "title": "web", "files": []string{"app/a.go"}}), "a child of another role",
		"a child of "+id+" has its role, backend, not \"frontend\"")
	r.refused(r.task(r.second, map[string]any{"action": "create", "title": "loose"}), "a worker's task", "only the manager creates tasks")
	r.must(r.task(r.lead, map[string]any{"action": "create", "parent": id, "title": "handlers", "files": []string{"app/a.go"}}), "a child with no role")
	if c, _ := r.sw.Board.Snapshot().Task("T2"); c.Role != "backend" {
		t.Errorf("the child's role: %q", c.Role)
	}
	for _, act := range []string{"revise", "resolve", "reject", "fail", "reopen"} {
		r.refused(r.task(r.lead, map[string]any{"action": act, "id": "T2", "text": "x"}), "the leader's "+act+" of its child",
			"only the manager can "+act+" tasks; as the leader of "+id+" you accept its children in review (task accept id=T2) and nothing more")
	}
	r.refused(r.task(r.lead, map[string]any{"action": "fail", "id": id, "text": "x"}), "the leader's fail of its parent",
		"only the manager can fail tasks: mail the manager what "+id+" needs")
	r.refused(r.task(r.second, map[string]any{"action": "accept", "id": "T2"}), "a member's accept",
		"only the manager accepts tasks, and a pool leader the children of the pool it leads while it executes: "+id+"'s pool is led by "+r.lead)
	r.refused(r.callTool(context.Background(), "spawn", r.lead, "backend", map[string]any{"task": "T2"}), "an assignment without a member",
		"name the member with agent=", "spawn agent=MEMBER task=T2")
	r.refused(r.callTool(context.Background(), "spawn", r.second, "backend", map[string]any{"task": "T2", "agent": r.third}), "a member's assignment",
		id+"'s pool is led by "+r.lead)
	if _, err := r.sw.Board.assignTask(assignReq{by: r.lead, agent: "fe-9", id: "T2", check: r.sw.claimCheck("fe-9", false)}); err == nil ||
		!strings.Contains(err.Error(), "fe-9 is not a member of "+id+"'s pool (members: "+r.lead+", "+r.second+", "+r.third+")") {
		t.Errorf("an assignment outside the pool: %v", err)
	}
}

// pool get reads a pool like task get; another unknown action lists pool's actions and where a pool's state is; inspect outside
// reconciliation says to read the task; a proposal after planning says how a plan changes.
func TestPoolReadsAndMisplacedActionsPointToTheRightCall(t *testing.T) {
	r := newPoolRig(t)
	id := r.parent.ID
	got := r.must(r.pool(r.second, map[string]any{"action": "get"}), "pool get")
	var task Task
	if err := json.Unmarshal([]byte(got), &task); err != nil || task.ID != id || task.Pool == nil {
		t.Fatalf("pool get: %v %q", err, got)
	}
	r.refused(r.pool(r.second, map[string]any{"action": "list", "id": ""}), "pool list",
		`unknown pool action "list": pool's actions are propose, agree, execute, reconcile, inspect, confirm and excuse`, "task get with the pool's parent task id")
	r.refused(r.pool(r.second, map[string]any{"action": "status"}), "pool status", "task get id="+id)
	r.refused(r.pool(r.second, map[string]any{"action": "inspect"}), "an inspect while planning",
		id+" is planning (revision 1): pool inspect is for the combined result once the pool reconciles", "task get id="+id)
	r.executing()
	r.refused(r.pool(r.lead, map[string]any{"action": "propose", "text": "a new plan"}), "the leader's proposal while executing",
		id+" is executing revision 2's plan: a proposal is made while it plans", "task create parent="+id, "the manager to revise "+id)
	if res := r.callTool(context.Background(), "pool", "mgr", "manager", map[string]any{"action": "propose", "id": id, "text": "a new plan"}); !res.IsError ||
		!strings.Contains(res.Text, "task revise id="+id) {
		t.Errorf("the manager's proposal while executing: %q", res.Text)
	}
}

// A child stays claimable while its parent is blocked on it: the leader waits for that child. Another child is not, and the
// refusal says who resumes the parent.
func TestABlockedParentKeepsTheChildItWaitsForClaimable(t *testing.T) {
	r := newPoolRig(t)
	id := r.parent.ID
	r.executing()
	for _, f := range []string{"app/a.go", "app/b.go"} {
		r.must(r.task(r.lead, map[string]any{"action": "create", "parent": id, "role": "backend", "title": f, "files": []string{f}}), "create")
	}
	r.must(r.task(r.lead, map[string]any{"action": "block", "id": id, "target": "T2", "text": "waits for T2"}), "block the parent")
	r.must(r.task(r.second, map[string]any{"action": "claim", "id": "T2"}), "claim the child the parent waits for")
	r.refused(r.task(r.third, map[string]any{"action": "claim", "id": "T3"}), "claim another child",
		"parent pool "+id+" is blocked on T2", "its leader "+r.lead+" resumes it (task resume id="+id+")")
}

// A read counts for a vote only while the pool is at that revision with that agreement: a pool that begins again restarts its
// revisions, and a member that read revision 2 of the old plan has not read revision 2 of the new one.
func TestAPoolReadIsOfOneRevisionAndAgreement(t *testing.T) {
	var r poolReads
	old := PoolState{Revision: 2, Agreement: "old plan"}
	r.note("be-2", Task{ID: "T1", Pool: &old})
	if rev, current := r.last("be-2", "T1", old); rev != 2 || !current {
		t.Errorf("the plan it read: %d %v", rev, current)
	}
	if rev, current := r.last("be-2", "T1", PoolState{Revision: 2, Agreement: "new plan"}); rev != 2 || current {
		t.Errorf("another plan at the same revision: %d %v", rev, current)
	}
	if _, current := r.last("be-3", "T1", old); current {
		t.Error("another member's read")
	}
	r.note("be-2", Task{ID: "T2"}) // not a pool: nothing to remember
	if rev, _ := r.last("be-2", "T2", old); rev != 0 {
		t.Errorf("a task without a pool was remembered: %d", rev)
	}
}
