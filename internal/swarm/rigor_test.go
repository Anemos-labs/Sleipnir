package swarm

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/tools"
)

func rigorCreate(r *rvRig, args map[string]any) *tools.Result {
	args["action"] = "create"
	return r.callTool(context.Background(), "task", "mgr", "manager", args)
}

// rigor=reviewed puts a plan by a read-only planner and a reviewer's check of it before the work, which waits for both and
// inherits their agreements; rigor=plan only the plan.
func TestRigorAddsPlanAndReviewBeforeTheWork(t *testing.T) {
	r := newRVRigWith(t, Config{MaxWorkers: 8, Team: []string{"architect", "backend", "reviewer"}}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "ok"} }, nil)
	res := rigorCreate(r, map[string]any{"title": "Migrate the users table", "description": "add a column", "role": "backend", "rigor": "reviewed"})
	if res.IsError {
		t.Fatal(res.Text)
	}
	sn := r.sw.Board.Snapshot()
	if len(sn.Tasks) != 3 {
		t.Fatalf("board holds %d tasks, want 3: %s", len(sn.Tasks), res.Text)
	}
	plan, rv, work := sn.Tasks[0], sn.Tasks[1], sn.Tasks[2]
	if plan.Kind != TaskKindPlan || plan.Role != "architect" || !strings.Contains(plan.Desc, "add a column") {
		t.Fatalf("plan: %+v", plan)
	}
	if rv.Kind != TaskKindPlan || rv.Role != ReviewerRole || !slices.Equal(rv.Deps, []string{plan.ID}) {
		t.Fatalf("review: %+v", rv)
	}
	if work.Kind != "" || work.Role != "backend" || !slices.Equal(work.Deps, []string{plan.ID, rv.ID}) || !strings.HasPrefix(work.Desc, "Follow the plan") || !strings.Contains(work.Desc, "add a column") {
		t.Fatalf("work: %+v", work)
	}
	if !strings.Contains(res.Text, "created "+work.ID) || !strings.Contains(res.Text, "start with "+plan.ID) {
		t.Fatalf("reply: %s", res.Text)
	}

	res = rigorCreate(r, map[string]any{"title": "Rename a flag", "role": "backend", "rigor": "plan"})
	if res.IsError {
		t.Fatal(res.Text)
	}
	sn = r.sw.Board.Snapshot()
	if len(sn.Tasks) != 5 || !slices.Equal(sn.Tasks[4].Deps, []string{sn.Tasks[3].ID}) {
		t.Fatalf("rigor=plan: %+v", sn.Tasks[3:])
	}
}

// rigor is refused, leaving the board as it was, when the team cannot staff the reviewer, on a child task, and on a duplicate
// title.
func TestRigorRefusals(t *testing.T) {
	r := newRVRigWith(t, Config{MaxWorkers: 8, Team: []string{"architect", "backend"}}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "ok"} }, nil)
	if res := rigorCreate(r, map[string]any{"title": "x", "role": "backend", "rigor": "reviewed"}); !res.IsError || !strings.Contains(res.Text, "reviewer slot") {
		t.Fatalf("no reviewer: %+v", res)
	}
	if res := rigorCreate(r, map[string]any{"title": "x", "role": "backend", "kind": "plan", "rigor": "plan"}); !res.IsError {
		t.Fatalf("a plan with rigor: %+v", res)
	}
	if n := len(r.sw.Board.Snapshot().Tasks); n != 0 {
		t.Fatalf("refusals left %d tasks", n)
	}
	if res := rigorCreate(r, map[string]any{"title": "x", "role": "backend"}); res.IsError {
		t.Fatal(res.Text)
	}
	if res := rigorCreate(r, map[string]any{"title": "X", "role": "backend", "rigor": "plan"}); !res.IsError || !strings.Contains(res.Text, "already open") {
		t.Fatalf("twin: %+v", res)
	}
	if n := len(r.sw.Board.Snapshot().Tasks); n != 1 {
		t.Fatalf("twin left %d tasks", n)
	}
	// a twin of a prerequisite's title refuses the chain before any of it is created
	rv := newRVRigWith(t, Config{MaxWorkers: 8, Team: []string{"architect", "backend", "reviewer"}}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "ok"} }, nil)
	if res := rigorCreate(rv, map[string]any{"title": "Review plan: z", "role": "backend"}); res.IsError {
		t.Fatal(res.Text)
	}
	if res := rigorCreate(rv, map[string]any{"title": "z", "role": "backend", "rigor": "reviewed"}); !res.IsError || !strings.Contains(res.Text, "already open") {
		t.Fatalf("prerequisite twin: %+v", res)
	}
	if n := len(rv.sw.Board.Snapshot().Tasks); n != 1 {
		t.Fatalf("prerequisite twin left %d tasks", n)
	}
	b := newRVRigWith(t, Config{MaxWorkers: 8, Team: []string{"backend", "reviewer"}}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "ok"} }, nil)
	if res := rigorCreate(b, map[string]any{"title": "y", "role": "backend", "rigor": "plan"}); !res.IsError || !strings.Contains(res.Text, "read-only role to plan") {
		t.Fatalf("no planner: %+v", res)
	}
}
