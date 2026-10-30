package swarm

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

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

// nextResponse stands for a new model response of member id: the results of its earlier calls have reached it.
func (r *poolRig) nextResponse(id string) {
	r.t.Helper()
	m := r.sw.get(id)
	if m == nil {
		r.t.Fatalf("no member %s", id)
	}
	m.turns.Add(1)
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
	r.nextResponse(r.second)
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

// pool get reads a pool like task get; another unknown action, or none, lists pool's actions and where a pool's state is; inspect outside
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
	r.refused(r.pool(r.second, map[string]any{}), "an empty pool call", "pool was called without an action: give one of propose, agree", "leave it out")
	early := r.must(r.pool(r.second, map[string]any{"action": "inspect"}), "an inspect while planning")
	head, body, _ := strings.Cut(early, "\n\n")
	if head != id+" is planning (revision 1): there is no combined result to inspect until it reconciles, so this is the pool as task get shows it." {
		t.Errorf("an inspect while planning: %q", head)
	}
	if err := json.Unmarshal([]byte(body), &task); err != nil || task.ID != id || task.Pool == nil {
		t.Errorf("an inspect while planning reads the pool: %v %q", err, body)
	}
	r.refused(r.pool(r.third+"x", map[string]any{"action": "inspect"}), "an inspect by a stranger", "you are not a member of "+id+"'s pool")
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

// A read counts for a vote only while the pool is at that revision with that agreement (a pool that begins again restarts its
// revisions, and a member that read revision 2 of the old plan has not read revision 2 of the new one), and only once its result
// has reached the model: a read from an earlier response, or a text it wrote itself.
func TestAPoolReadIsOfOneRevisionAndAgreement(t *testing.T) {
	var r poolReads
	old := PoolState{Revision: 2, Agreement: "old plan"}
	r.note("be-2", Task{ID: "T1", Pool: &old}, 4, false)
	got, ok := r.last("be-2", "T1")
	if !ok || got.rev != 2 || !got.of(old) {
		t.Errorf("the plan it read: %+v %v", got, ok)
	}
	if got.of(PoolState{Revision: 2, Agreement: "new plan"}) {
		t.Error("another plan at the same revision counts as read")
	}
	if got.seenBefore(4) || !got.seenBefore(5) || got.seenBefore(-1) {
		t.Error("a read counts only from a later response")
	}
	if _, ok := r.last("be-3", "T1"); ok {
		t.Error("another member's read")
	}
	r.note("be-2", Task{ID: "T1", Pool: &old}, 4, true)
	if got, _ := r.last("be-2", "T1"); !got.seenBefore(4) {
		t.Error("a text the member wrote counts in the same response")
	}
	r.note("be-2", Task{ID: "T2"}, 4, false) // not a pool: nothing to remember
	if _, ok := r.last("be-2", "T2"); ok {
		t.Error("a task without a pool was remembered")
	}
}

// pool inspect answers with the pool's task in full, which is the read a confirm without a revision counts for; a proposal or a
// reconciliation answers with the revision it made.
func TestInspectShowsThePoolItRecordsAsRead(t *testing.T) {
	r := newPoolRig(t)
	id := r.parent.ID
	if got := r.must(r.pool(r.lead, map[string]any{"action": "propose", "text": "split handlers and tests"}), "propose"); got != id+": propose recorded as revision 2" {
		t.Errorf("the proposal's answer: %q", got)
	}
	for _, m := range []string{r.lead, r.second, r.third} {
		r.must(r.pool(m, map[string]any{"action": "agree", "revision": 2}), "agree")
	}
	r.must(r.pool(r.lead, map[string]any{"action": "execute"}), "execute")
	if got := r.must(r.pool(r.lead, map[string]any{"action": "reconcile", "text": "the combined contract"}), "reconcile"); got != id+": reconcile recorded as revision 3" {
		t.Errorf("the reconciliation's answer: %q", got)
	}
	got := r.must(r.pool(r.second, map[string]any{"action": "inspect"}), "inspect")
	r.nextResponse(r.second)
	head, body, _ := strings.Cut(got, "\n")
	if !strings.Contains(head, "inspect recorded") {
		t.Errorf("inspect's answer: %q", head)
	}
	var task Task
	if err := json.Unmarshal([]byte(body), &task); err != nil || task.Pool == nil || task.Pool.Revision != 3 || task.Pool.Agreement != "the combined contract" ||
		task.Pool.Phase != "reconciling" {
		t.Fatalf("inspect does not show the pool: %v %q", err, body)
	}
	if got := r.must(r.pool(r.second, map[string]any{"action": "confirm"}), "a confirm with no revision after inspect"); !strings.Contains(got, "revision 3 (the revision you last read)") {
		t.Errorf("confirm after inspect: %q", got)
	}
}

// A leader that accepts a task that does not exist is told so: it may accept its children, so the id is the mistake. The actions
// that are the manager's whatever the id names keep the refusal of the role.
func TestAnUnknownTaskIsNamedToALeaderThatAcceptsIt(t *testing.T) {
	r := newPoolRig(t)
	r.executing()
	r.refused(r.task(r.lead, map[string]any{"action": "accept", "id": "T99"}), "accept of an unknown task", "no task T99")
	r.refused(r.task(r.lead, map[string]any{"action": "revise", "id": "T99", "text": "x"}), "revise of an unknown task", "only the manager can revise tasks")
}

// A reconciliation without text keeps the earlier agreement, which its caller did not write: the answer shows the pool's task in
// full, so the leader's confirm without a revision counts for a text it was shown.
func TestAReconciliationWithoutTextShowsTheAgreementItKeeps(t *testing.T) {
	r := newPoolRig(t)
	r.executing()
	got := r.must(r.pool(r.lead, map[string]any{"action": "reconcile"}), "reconcile without text")
	head, body, _ := strings.Cut(got, "\n")
	if !strings.Contains(head, "reconcile recorded as revision 3; it keeps the agreement shown below") {
		t.Errorf("the answer: %q", head)
	}
	var task Task
	if err := json.Unmarshal([]byte(body), &task); err != nil || task.Pool == nil || task.Pool.Agreement != "split handlers and tests" || task.Pool.Revision != 3 {
		t.Fatalf("the answer does not show the pool: %v %q", err, body)
	}
	r.refused(r.pool(r.lead, map[string]any{"action": "confirm"}), "the leader's confirm with no revision in the reconcile's batch", "same batch of calls")
	r.nextResponse(r.lead)
	if got := r.must(r.pool(r.lead, map[string]any{"action": "confirm"}), "the leader's confirm with no revision"); !strings.Contains(got, "(the revision you last read)") {
		t.Errorf("the leader's confirm: %q", got)
	}
}

// A read counts for a vote without a revision only once its result has reached the model. A read and a vote in one batch of
// calls run in order, but the model wrote the vote before it saw the read: that vote is refused, and the same vote in the next
// response counts.
func TestAReadInTheVotesOwnBatchDoesNotCount(t *testing.T) {
	r := newPoolRig(t)
	id := r.parent.ID
	r.must(r.pool(r.lead, map[string]any{"action": "propose", "text": "split handlers and tests"}), "propose")
	r.nextResponse(r.second)
	r.must(r.task(r.second, map[string]any{"action": "get", "id": id}), "task get")
	r.refused(r.pool(r.second, map[string]any{"action": "agree"}), "an agree with no revision in the read's batch",
		"pool agree needs revision: "+id+" is at revision 2 (planning), and your read of it ran in the same batch of calls as this vote",
		"vote with revision=2: pool agree id="+id+" revision=2")
	r.nextResponse(r.second)
	if got := r.must(r.pool(r.second, map[string]any{"action": "agree"}), "the agree in the next response"); !strings.Contains(got, "(the revision you last read)") {
		t.Errorf("the agree in the next response: %q", got)
	}
	// The leader wrote the proposal itself: it may agree in the same batch.
	r.must(r.pool(r.lead, map[string]any{"action": "agree"}), "the proposer's agree")
}

// Accepting a task that is already done (a pool child whose verification closed it, or one accepted earlier) answers that there is
// nothing to accept instead of refusing: in a 12-worker run leaders and the manager accepted verified children five times in the
// first 45 minutes, and each refusal cost a turn. A task that is not in review and not done is still refused.
func TestAcceptingADoneTaskIsNotAMistake(t *testing.T) {
	r := newPoolRig(t)
	r.executing()
	r.must(r.task(r.lead, map[string]any{"action": "create", "parent": r.parent.ID, "title": "handlers", "files": []string{"app/handlers.go"}}), "create")
	r.refused(r.task(r.lead, map[string]any{"action": "accept", "id": "T2"}), "an accept of a todo child", "T2 is todo: a task is accepted from review")
	if err := r.sw.Board.resolve("mgr", "T2", "not needed"); err != nil {
		t.Fatal(err)
	}
	for _, as := range []string{r.lead, "mgr"} {
		if got := r.must(r.task(as, map[string]any{"action": "accept", "id": "T2"}), "an accept of a done child by "+as); got != "T2 is already done (unneeded): nothing to accept" {
			t.Errorf("%s: %q", as, got)
		}
	}
}

// An inspect while the pool plans is a read of the pool: an agree without a revision that follows it counts for the revision it
// showed, as after task get.
func TestAnEarlyInspectCountsAsAReadForAVote(t *testing.T) {
	r := newPoolRig(t)
	id := r.parent.ID
	r.must(r.pool(r.lead, map[string]any{"action": "propose", "text": "split handlers and tests"}), "propose")
	got := r.must(r.pool(r.second, map[string]any{"action": "inspect"}), "an inspect while planning")
	if head, _, _ := strings.Cut(got, "\n\n"); head != id+" is planning (revision 2): there is no combined result to inspect until it reconciles, so this is the pool as task get shows it." {
		t.Errorf("an inspect while planning: %q", head)
	}
	r.nextResponse(r.second)
	if got := r.must(r.pool(r.second, map[string]any{"action": "agree"}), "an agree with no revision after the inspect"); !strings.Contains(got, "agree recorded for revision 2 (the revision you last read)") {
		t.Errorf("agree after the inspect: %q", got)
	}
}

// A pool's leader may set the scope of a child of its pool that nobody has claimed, inside the pool's scope: when two of its children
// overlap it is the one that parts them. A member may not, nor may the leader once the child is claimed, nor beyond the pool's scope;
// a claim refused for overlapping a sibling says that the leader can narrow it.
func TestAPoolLeaderNarrowsAnUnclaimedChild(t *testing.T) {
	r := newPoolRig(t)
	if err := r.sw.Board.poolUpdate("mgr", r.parent.ID, func(_ *Snapshot, _ *Task, p *PoolState) error { p.Phase = "executing"; return nil }); err != nil {
		t.Fatal(err)
	}
	child := func(title string) Task {
		c, err := r.sw.Board.CreateTask(r.lead, TaskSpec{Parent: r.parent.ID, Title: title, Role: "backend", Files: []string{"app/**"}})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	a, b := child("scaffold"), child("pages")
	r.refused(r.task(r.second, map[string]any{"action": "update", "id": b.ID, "files": []string{"app/pages/**"}}), "a member's scope change",
		"only the manager can change a task's scope (and a pool's leader the scope of a child of its pool that nobody has claimed)")
	r.refused(r.task(r.lead, map[string]any{"action": "update", "id": b.ID, "files": []string{"web/**"}}), "a scope beyond the pool's", "outside the scope of its parent "+r.parent.ID)
	if err := r.sw.Board.Assign(r.lead, r.second, a.ID); err != nil {
		t.Fatal(err)
	}
	if res := r.task(r.third, map[string]any{"action": "claim", "id": b.ID}); !res.IsError || !strings.Contains(res.Text, r.parent.ID+"'s leader "+r.lead+" can narrow "+b.ID) {
		t.Errorf("a claim refused for overlapping a sibling does not say who can part them: %+v", res)
	}
	r.must(r.task(r.lead, map[string]any{"action": "update", "id": b.ID, "files": []string{"app/pages/**"}}), "the leader narrows an unclaimed child")
	if got, _ := r.sw.Board.Snapshot().Task(b.ID); strings.Join(got.Files, ",") != "app/pages/**" {
		t.Errorf("%s's scope is %v", b.ID, got.Files)
	}
	r.refused(r.task(r.lead, map[string]any{"action": "update", "id": a.ID, "files": []string{"app/lib/**"}}), "a claimed child's scope", "only the manager can change a task's scope")
}

// After a resume a pool's parent is todo, reserved for its leader, until it is started again; a child of it cannot be assigned before. The
// refusal says how to start the parent and that the manager does it: in a resumed team run the manager tried to spawn the child five
// times, and a resumed leader tried to spawn the parent itself.
func TestAChildOfAResumedPoolSaysHowToStartTheParent(t *testing.T) {
	r := newPoolRig(t)
	if err := r.sw.Board.poolUpdate("mgr", r.parent.ID, func(_ *Snapshot, _ *Task, p *PoolState) error { p.Phase = "executing"; return nil }); err != nil {
		t.Fatal(err)
	}
	child, err := r.sw.Board.CreateTask(r.lead, TaskSpec{Parent: r.parent.ID, Title: "handlers", Role: "backend", Files: []string{"app/handlers/**"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.sw.Board.mutate("harness", "requeue", func(d *draft) error { // what Board.RestoreOwners leaves after a resume
		i := taskIdx(d.Snapshot, r.parent.ID)
		p := d.Tasks[i]
		p.Status, p.Line = StatusTodo, resumedLine
		return d.put(i, p)
	}); err != nil {
		t.Fatal(err)
	}
	want := "the manager starts " + r.parent.ID + " first (spawn task=" + r.parent.ID + " agent=" + r.lead + "), then its children"
	r.refused(r.task("mgr", map[string]any{"action": "claim", "id": child.ID}), "a child of a parent waiting to restart", want)
	// The resumed leader is told the same: only the manager starts the parent (a leader told to start it tried spawn, refused again).
	r.refused(r.task(r.lead, map[string]any{"action": "claim", "id": child.ID}), "the leader claiming a child of its parent waiting to restart", want)
	if _, err := r.sw.Spawn(SpawnReq{Agent: r.second, TaskID: child.ID, By: "mgr"}); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("spawning the child: %v, want it to say %q", err, want)
	}
}

// A leader that proposed and went idle waits for the others while they wait for its own vote. The vote that leaves only the
// leader's own to give tells the leader once.
func TestTheVoteThatLeavesOnlyTheLeadersOwnIsMailedToTheLeader(t *testing.T) {
	r := newPoolRig(t)
	id := r.parent.ID
	r.must(r.pool(r.lead, map[string]any{"action": "propose", "text": "split handlers and tests"}), "propose")
	before := len(r.mailTo(r.lead))
	r.must(r.pool(r.second, map[string]any{"action": "agree", "revision": 2}), "agree")
	if mail := r.mailTo(r.lead)[before:]; len(mail) != 0 {
		t.Fatalf("the leader was mailed while another member's vote was missing: %q", mail)
	}
	r.must(r.pool(r.third, map[string]any{"action": "agree", "revision": 2}), "agree")
	r.must(r.pool(r.third, map[string]any{"action": "agree", "revision": 2}), "a repeated agree")
	mail := r.mailTo(r.lead)[before:]
	if len(mail) != 1 || !strings.Contains(mail[0], "Pool "+id+": every other member has agreed to revision 2; only your own agree is missing (pool agree id="+id+" revision=2)") {
		t.Fatalf("the leader's mail: %q", mail)
	}

	r.must(r.pool(r.lead, map[string]any{"action": "agree", "revision": 2}), "the leader's agree")
	r.must(r.pool(r.lead, map[string]any{"action": "execute"}), "execute")
	r.must(r.pool(r.lead, map[string]any{"action": "reconcile", "text": "the combined contract"}), "reconcile")
	before = len(r.mailTo(r.lead))
	r.must(r.pool(r.second, map[string]any{"action": "confirm", "revision": 3}), "confirm")
	if mail := r.mailTo(r.lead)[before:]; len(mail) != 0 {
		t.Fatalf("the leader was mailed while another member's confirm was missing: %q", mail)
	}
	r.must(r.pool(r.third, map[string]any{"action": "confirm", "revision": 3}), "confirm")
	r.must(r.pool(r.third, map[string]any{"action": "confirm", "revision": 3}), "a repeated confirm")
	mail = r.mailTo(r.lead)[before:]
	if len(mail) != 1 || !strings.Contains(mail[0], "Pool "+id+": every other member has confirmed revision 3; only your own confirm is missing (pool confirm id="+id+" revision=3)") {
		t.Fatalf("the leader's mail during reconciliation: %q", mail)
	}
}

// After a resume nothing but a spawn starts the interrupted work again. In a resumed team run the manager read "waiting for T1, T5 to
// restart" as the harness's job and waited beside it; its wait now says what to start, the parent and not the parent's child.
func TestTheManagersWaitAfterAResumeSaysWhatToStartAgain(t *testing.T) {
	r := newPoolRig(t)
	r.executing()
	child, err := r.sw.Board.CreateTask(r.lead, TaskSpec{Parent: r.parent.ID, Title: "handlers", Role: "backend", Files: []string{"app/handlers/**"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.sw.Board.mutate("harness", "requeue", func(d *draft) error { // what Board.RestoreOwners leaves after a resume
		for _, id := range []string{r.parent.ID, child.ID} {
			i := taskIdx(d.Snapshot, id)
			p := d.Tasks[i]
			p.Status, p.Owner, p.Line = StatusTodo, r.lead, resumedLine
			if err := d.put(i, p); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	m := r.sw.get(r.lead) // a recovered worker is idle until its task starts again
	m.mu.Lock()
	rs := m.run
	m.mu.Unlock()
	if rs != nil {
		rs.cancel()
	}
	rvWait(t, r.lead+" idle", func() bool { return r.idle(r.lead) })
	res := r.callTool(context.Background(), "wait", "mgr", "manager", map[string]any{"timeout_sec": 3})
	want := "spawn task=" + r.parent.ID + " agent=" + r.lead
	if !res.IsError || !strings.Contains(res.Text, want) || strings.Contains(res.Text, "task="+child.ID) {
		t.Fatalf("the manager's wait after a resume: %q, want a refusal naming %q and not the child", res.Text, want)
	}
	if err := r.sw.Board.Unblock("mgr", r.parent.ID); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("resuming the reserved parent: %v, want it to name %q", err, want)
	}
}

// Starting a pool on a task that cannot begin one says what is wrong with it: a resumed manager re-spawned a done review task and
// was told only that it was "not an unassigned parent task".
func TestAPoolStartOnAFinishedTaskSaysItIsFinished(t *testing.T) {
	r := newPoolRig(t)
	if err := r.sw.Board.mutate("mgr", "finish", func(d *draft) error {
		i := taskIdx(d.Snapshot, r.parent.ID)
		p := d.Tasks[i]
		p.Status, p.Closure = StatusDone, closeAs(CloseVerified)
		return d.put(i, p)
	}); err != nil {
		t.Fatal(err)
	}
	want := r.parent.ID + " is done: a pool starts only on a todo task"
	if _, err := r.sw.Spawn(SpawnReq{Agent: r.lead, TaskID: r.parent.ID, By: "mgr"}); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("spawning a done pool task: %v, want it to say %q", err, want)
	}
	// The other refusals, straight from poolReady: spawn routes neither a child nor a task in doing to a pool start.
	sn := r.sw.Board.Snapshot()
	for _, c := range []struct {
		task Task
		want string
	}{
		{Task{ID: "T9", Parent: "T4", Status: StatusTodo}, "T9 is a child of T4: a pool starts on a top-level task"},
		{Task{ID: "T7", Status: StatusDoing, Owner: "be-2"}, "T7 is doing, owned by be-2: a pool starts only on a todo task"},
	} {
		if err := poolReady(sn, c.task); err == nil || err.Error() != c.want {
			t.Errorf("poolReady(%s) = %v, want %q", c.task.ID, err, c.want)
		}
	}
}

// A resolve or revise the board refuses stops nobody: the refusal comes before the members are stopped. In a team run the
// manager's resolve of a pool parent with an open child cancelled all three members mid-request and was then refused.
func TestARefusedResolveStopsNobody(t *testing.T) {
	r := newPoolRig(t)
	id := r.parent.ID
	r.executing()
	r.must(r.task(r.lead, map[string]any{"action": "create", "parent": id, "title": "handlers", "files": []string{"app/handlers.go"}}), "create")
	stopped := false
	m := r.sw.get(r.second)
	m.mu.Lock()
	m.run = &runState{cancel: func() { stopped = true }}
	m.mu.Unlock()
	v := r.sw.Board.Snapshot().Version
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second) // stopping a running member would wait for it
	defer cancel()
	r.refused(r.callTool(ctx, "task", "mgr", "manager", map[string]any{"action": "resolve", "id": id, "text": "merged into T2"}),
		"a resolve with an open child", "resolve child T2 first", "nobody was stopped")
	r.refused(r.callTool(ctx, "task", "mgr", "manager", map[string]any{"action": "revise", "id": id, "text": "x", "deps": []string{"T9"}}),
		"a revise with a bad dependency", "invalid dependency T9", "nobody was stopped")
	if stopped {
		t.Error("a refused change stopped a pool member")
	}
	if got := r.sw.Board.Snapshot().Version; got != v {
		t.Errorf("a refused change moved the board from version %d to %d", v, got)
	}
}
