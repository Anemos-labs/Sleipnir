package swarm

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/cost"
	"github.com/anemos-labs/sleipnir/internal/perm"
)

// The default team is one slot of each role when the size is the number of roles, and an even share of a larger size otherwise.
func TestDefaultTeamIsOneOfEachRole(t *testing.T) {
	if got := DefaultTeam(WorkerRoleOrder, 8); !slices.Equal(got, WorkerRoleOrder) {
		t.Errorf("--swarm 8: %v, want one of each: %v", got, WorkerRoleOrder)
	}
	if n, _ := TeamCounts(DefaultTeam(WorkerRoleOrder, 9)); n["architect"] != 2 || n["backend"] != 1 || n["docs"] != 1 {
		t.Errorf("--swarm 9 allocates exactly nine slots: %v", n)
	}
	if DefaultTeam(WorkerRoleOrder, 0) != nil || DefaultTeam(nil, 8) != nil {
		t.Error("a single agent or no roles is no team")
	}
	roles := BuiltinRoles()
	for _, r := range WorkerRoleOrder {
		if _, ok := roles[r]; !ok {
			t.Errorf("%s is in the order but not built in", r)
		}
		if RoleSummaries[r] == "" {
			t.Errorf("%s has no summary for the team card", r)
		}
	}
	if len(roles) != len(WorkerRoleOrder)+1 {
		t.Errorf("built-in roles %v: the manager and the eight worker roles", roles.Names())
	}
	if _, ok := roles["fullstack"]; ok {
		t.Error("fullstack is a legacy role, not a built-in one")
	}
}

// A worker of a role starts only while the role has a free slot: a third backend of a team with two, or a role the team has no slot
// of, is refused with the team's occupancy and what to do instead; a retired worker frees its slot; each slot runs on its own model.
func TestTeamSlotsLimitWhoTheManagerCanStart(t *testing.T) {
	cfg := Config{MaxWorkers: 8, Team: []string{"backend", "backend", "tester"}}
	r := newRVRigWith(t, cfg, func(ctx context.Context, c *rvCall) rvReply { return rvReply{Text: "done"} }, func(d *Deps) {
		d.SlotModels = []RoleModel{{}, {Provider: d.Provider, Model: cost.Model{ID: "rv-slot-2", ContextTokens: 1_000_000, Cache: cost.OpenAICacheModel()}}}
	})
	be1, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "one", By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	be2, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "two", By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	if m := r.sw.get(be1); m.model.ID != "rv-1" {
		t.Errorf("%s runs on %s, want the role's model", be1, m.model.ID)
	}
	if m := r.sw.get(be2); m.model.ID != "rv-slot-2" {
		t.Errorf("%s runs on %s, want its slot's model", be2, m.model.ID)
	}
	_, err = r.sw.Spawn(SpawnReq{Role: "backend", Title: "three", By: "mgr"})
	if err == nil || !strings.Contains(err.Error(), "no free backend slot") || !strings.Contains(err.Error(), "backend 2/2") || !strings.Contains(err.Error(), "spawn agent=") {
		t.Errorf("a third backend: %v", err)
	}
	if _, err := r.sw.Spawn(SpawnReq{Role: "frontend", Title: "ui", By: "mgr"}); err == nil || !strings.Contains(err.Error(), "no frontend slot") {
		t.Errorf("a role the team does not have: %v", err)
	}
	if got := r.sw.spawnableRoles(); !slices.Equal(got, []string{"backend", "tester"}) {
		t.Errorf("the spawnable roles are the team's: %v", got)
	}
	if st := r.sw.TeamState(); st != "team: backend 2/2 · tester 0/1" {
		t.Errorf("TeamState = %q", st)
	}
	rvWait(t, be1+" idle", func() bool { return r.idle(be1) })
	if err := r.sw.Retire(be1); err != nil {
		t.Fatal(err)
	}
	if _, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "three again", By: "mgr"}); err != nil {
		t.Errorf("a retired worker's slot is free again: %v", err)
	}
}

// Auto-docs turns accepted work into one catch-up task for the docs worker, and never a task of the docs worker's own.
func TestAutoDocsGivesTheDocsWorkerTheAcceptedWork(t *testing.T) {
	if newDocsKeeper(Config{AutoDocs: true, Team: []string{"backend"}}) != nil {
		t.Error("a team without a docs slot has no keeper")
	}
	if newDocsKeeper(Config{Team: []string{"docs"}}) != nil {
		t.Error("auto-docs off has no keeper")
	}
	cfg := Config{MaxWorkers: 8, AutoDocs: true, Team: []string{"backend", "docs"}}
	r := newRVRigWith(t, cfg, func(ctx context.Context, c *rvCall) rvReply { return rvReply{Text: "noted"} }, nil)
	if !r.sw.AutoDocs() || r.sw.DocsState() != "docs (auto): up to date" {
		t.Fatalf("keeper %v %q", r.sw.AutoDocs(), r.sw.DocsState())
	}
	r.sw.docsTick(r.sw.deps.Now()) // the first look: what is already done is not news
	prev := &Snapshot{Tasks: []Task{
		{ID: "T1", Title: "add the cart endpoint", Status: StatusDone, Role: "backend", Files: []string{"api/"}},
		{ID: "T2", Title: "agree on the cart API", Status: StatusDone, Kind: TaskKindPlan},
		{ID: "T3", Title: DocsTaskTitle + " (T0)", Status: StatusDone, Role: "docs"},
	}}
	r.sw.Board.Restore(prev)
	r.sw.docsTick(r.sw.deps.Now())
	var catchUp Task
	for _, t := range r.sw.Board.Snapshot().Tasks {
		if strings.HasPrefix(t.Title, DocsTaskTitle) && t.ID != "T3" {
			catchUp = t
		}
	}
	if catchUp.ID == "" {
		t.Fatalf("no catch-up task on the board: %+v", r.sw.Board.Snapshot().Tasks)
	}
	if !strings.Contains(catchUp.Title, "T1, T2") || strings.Contains(catchUp.Desc, "T3") {
		t.Errorf("the pass covers the accepted work and not the docs worker's own: %q / %q", catchUp.Title, catchUp.Desc)
	}
	if !strings.Contains(catchUp.Desc, "agreement T2") || !strings.Contains(catchUp.Desc, "AGENTS.md") {
		t.Errorf("the brief: %q", catchUp.Desc)
	}
	if catchUp.Owner == "" || r.sw.roleOf(catchUp.Owner) != DocsRole {
		t.Errorf("the pass went to %q", catchUp.Owner)
	}
	if !r.sw.docsPending() || !strings.Contains(r.sw.DocsState(), "pass "+catchUp.ID+" in progress") {
		t.Errorf("state %q", r.sw.DocsState())
	}

	// A pass that goes back to the board (its worker's run was refused or handed back) is given to the idle docs worker again.
	owner := catchUp.Owner
	rvWait(t, owner+" idle", func() bool { return r.idle(owner) })
	cur, _ := r.sw.Board.Snapshot().Task(catchUp.ID)
	if cur.Status != StatusTodo {
		if err := r.sw.Board.Unassign("mgr", cur.ID, "test: handed back"); err != nil {
			t.Fatal(err)
		}
	}
	r.sw.docsTick(r.sw.deps.Now())
	if again, _ := r.sw.Board.Snapshot().Task(catchUp.ID); again.Owner != owner || again.Status == StatusTodo {
		t.Errorf("the requeued pass is not the docs worker's again: %+v", again)
	}
}

// A manager whose shell the engine judges (Config.JudgeManagerShell: the harness's own plan-mode profile) may chain reads and name
// absolute paths inside the project, which the fallback allowlist refused; a write is refused all the same.
func TestTheEngineJudgesTheManagersShell(t *testing.T) {
	root := t.TempDir()
	eng, err := perm.NewEngine(perm.Config{Mode: perm.ModeDefault, Root: root, Home: t.TempDir(), Roles: map[string]perm.RoleProfile{"manager": {Mode: perm.ModePlan}}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, judged := range []bool{false, true} {
		rr := roleRequester{inner: eng, role: BuiltinRoles()["manager"], denyWrites: managerWritesMsg, strictShell: !judged}
		for _, cmd := range []string{"cd " + root + " && ls -la", "git -C " + root + " status --short", "ls -la " + root} {
			d := rr.Check(ctx, perm.Request{Agent: "mgr", Tool: "bash", Command: cmd, Cwd: root})
			if d.Allow != judged {
				t.Errorf("judged by the engine %v: %q allowed %v (%s)", judged, cmd, d.Allow, d.Reason)
			}
		}
		if d := rr.Check(ctx, perm.Request{Agent: "mgr", Tool: "bash", Command: "touch x", Cwd: root, Writes: true}); d.Allow {
			t.Errorf("a write is allowed: %+v", d)
		}
	}
}

// A docs pass that cannot start (here: the budget is spent) stays one task on the board: retries hand out that task again and never
// create a second one for the same changes.
func TestAutoDocsRetriesTheSamePassWhenItCannotStart(t *testing.T) {
	cfg := Config{MaxWorkers: 8, AutoDocs: true, Team: []string{"backend", "docs"}, BudgetUSD: 1}
	r := newRVRigWith(t, cfg, func(ctx context.Context, c *rvCall) rvReply { return rvReply{Text: "noted"} }, nil)
	now := r.sw.deps.Now()
	r.sw.docsTick(now)
	r.sw.Board.Restore(&Snapshot{Tasks: []Task{{ID: "T1", Title: "add the cart endpoint", Status: StatusDone, Role: "backend"}}})
	r.sw.mu.Lock()
	r.sw.spent = 2 // over the budget: no worker can start
	r.sw.mu.Unlock()
	for i := 0; i < 3; i++ {
		r.sw.docsTick(now.Add(time.Duration(i) * 2 * docsRetry))
	}
	var passes []Task
	for _, tk := range r.sw.Board.Snapshot().Tasks {
		if strings.HasPrefix(tk.Title, DocsTaskTitle) {
			passes = append(passes, tk)
		}
	}
	if len(passes) != 1 || passes[0].Status != StatusTodo || passes[0].Owner != "" {
		t.Fatalf("want one unassigned catch-up task, got %+v", passes)
	}
	if !r.sw.docsPending() {
		t.Error("the pass still counts as pending work")
	}
}

// Same-ID repairs must become fresh documentation work after acceptance, including
// legacy tasks whose first accepted revision is zero.
func TestAutoDocsTracksAcceptedRevisions(t *testing.T) {
	r := newRVRigWith(t, Config{MaxWorkers: 8, AutoDocs: true, Team: []string{"backend", "docs"}}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "noted"} }, nil)
	now := r.sw.deps.Now()
	r.sw.docsTick(now)
	// Hold dispatch so this test observes the changes collected by the keeper.
	r.sw.docs.retryAt = now.Add(docsRetry)
	task := Task{ID: "T1", Title: "add endpoint", Status: StatusDone, Role: "backend"}
	r.sw.Board.Restore(&Snapshot{Tasks: []Task{task}})
	r.sw.docsTick(now)
	r.sw.docsTick(now)
	if got := len(r.sw.docs.pending); got != 1 {
		t.Fatalf("first acceptance collected %d changes, want 1", got)
	}
	r.sw.docs.pending = nil // the first pass consumed the original acceptance
	task.Files = []string{"api/repaired.go"}
	if err := r.sw.Board.revise("mgr", task.ID, TaskSpec{Files: task.Files}, "repair endpoint"); err != nil {
		t.Fatal(err)
	}
	r.sw.docsTick(now)
	if len(r.sw.docs.pending) != 0 {
		t.Fatal("unfinished repair became documentation work")
	}
	if err := r.sw.Board.claim("be-1", task.ID, nil); err != nil {
		t.Fatal(err)
	}
	repaired, _ := r.sw.Board.Snapshot().Task(task.ID)
	if err := r.sw.Board.submitAgreementAt("be-1", task.ID, repaired.Rev, "repaired", "", "", func(*Snapshot, Task) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := r.sw.Board.Accept("mgr", task.ID, ""); err != nil {
		t.Fatal(err)
	}
	r.sw.docsTick(now)
	r.sw.docsTick(now)
	if got := r.sw.docs.pending; len(got) != 1 || got[0].task != task.ID || len(got[0].files) != 1 || got[0].files[0] != task.Files[0] {
		t.Fatalf("repaired acceptance was not collected exactly once: %+v", got)
	}
}

// The roles that write and run tests know to set up a missing runner inside the project, and the manager carries the user's words
// into the tasks and holds the goal until the tests have run.
func TestPinsCarryVerificationAndTheUsersWords(t *testing.T) {
	roles := BuiltinRoles()
	for _, name := range []string{"tester", "devops", "backend"} {
		if !strings.Contains(roles[name].Pin, ".venv") {
			t.Errorf("the %s pin does not say how to set up a missing test runner", name)
		}
	}
	mgr := roles["manager"].Pin
	for _, want := range []string{"Copy the explicit requirements into the tasks verbatim", "Demand evidence", "line by line", "Size the team to the goal"} {
		if !strings.Contains(mgr, want) {
			t.Errorf("the manager pin does not say %q", want)
		}
	}
	if !strings.Contains(roles["architect"].Pin, "never renames what the user named") {
		t.Error("the architect pin lets an agreement rename what the user named")
	}
}

// A docs pass and code work do not wait for each other: the pass takes the Markdown at the root and docs/, so it starts beside a
// task that holds internal/api/** (which may hold a README), and while it runs, tasks scoped to a directory or to a file not there
// yet are claimed. With "**/*.md" the first pass of a 12-worker run waited half an hour behind the backend's directories, and the
// next one, while it ran, kept every SDK and web task from being claimed. A pass that cannot start at all says why.
func TestAutoDocsStartsBesideADirectoryWriter(t *testing.T) {
	cfg := Config{MaxWorkers: 8, AutoDocs: true, Team: []string{"backend", "devops", "docs"}}
	r := newRVRigWith(t, cfg, func(ctx context.Context, c *rvCall) rvReply { return rvReply{Text: "noted"} }, nil)
	now := r.sw.deps.Now()
	r.sw.docsTick(now)
	r.sw.Board.Restore(&Snapshot{Tasks: []Task{
		{ID: "T1", Title: "add the cart endpoint", Status: StatusDone, Role: "backend"},
		{ID: "T2", Title: "add the order endpoint", Status: StatusTodo, Role: "backend", Files: []string{"internal/api/**"}},
		{ID: "T3", Title: "packaging", Status: StatusTodo, Role: "devops", Files: []string{"README.md", "docs/**"}},
	}})
	for _, c := range [][2]string{{"be-1", "T2"}, {"dv-1", "T3"}} {
		if err := r.sw.Board.claim(c[0], c[1], nil); err != nil {
			t.Fatal(err)
		}
	}
	r.sw.docsTick(now)
	pass, _ := r.sw.Board.Snapshot().Task("T4")
	if !strings.HasPrefix(pass.Title, DocsTaskTitle) || pass.Owner != "" {
		t.Fatalf("T4: %+v", pass)
	}
	if st := r.sw.DocsState(); !strings.Contains(st, "pass T4 waiting to start: scope") || !strings.Contains(st, "overlaps T3") {
		t.Errorf("state while T3 holds README.md: %q", st)
	}
	if err := r.sw.Board.resolve("mgr", "T3", "not needed"); err != nil {
		t.Fatal(err)
	}
	r.sw.docsTick(now.Add(docsRetry))
	pass, _ = r.sw.Board.Snapshot().Task("T4")
	if pass.Status != StatusDoing || pass.Owner == "" || strings.Join(pass.Files, " ") != "*.md docs/**" {
		t.Fatalf("beside T2 on internal/api/** the pass is %s, owned by %q, scoped %v", pass.Status, pass.Owner, pass.Files)
	}
	if st := r.sw.DocsState(); !strings.Contains(st, "pass T4 in progress") {
		t.Errorf("state once started: %q", st)
	}
	for _, c := range []struct{ id, scope string }{{"T5", "sdk/python/**"}, {"T6", "web/index.html"}} {
		if _, err := r.sw.Board.CreateTask("mgr", TaskSpec{Title: c.scope, Role: "backend", Files: []string{c.scope}}); err != nil {
			t.Fatal(err)
		}
		if err := r.sw.Board.claim("be-2", c.id, r.sw.claimCheck("be-2", false)); err != nil {
			t.Errorf("a task on %s beside the docs pass: %v", c.scope, err)
		}
		if err := r.sw.Board.resolve("mgr", c.id, "not needed"); err != nil {
			t.Fatal(err)
		}
	}
}
