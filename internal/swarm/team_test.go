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
