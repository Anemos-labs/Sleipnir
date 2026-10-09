package swarm

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

// While the session is planning, the manager's requests go to the permission engine, whose plan mode judges every shell command and every
// write itself. The engine's plan mode and the fallback allowlist the manager is held to otherwise are two different policies, not one
// inside the other: the engine refuses what it cannot prove read-only, `go test` among it, which the allowlist accepts, and it allows read-only
// compound commands that the allowlist rejects (`git status && ls src | wc -l`). What this test pins is what they share: every write is
// refused, and the refusal says the session is planning instead of sending the manager off to spawn a worker for a change nobody can make yet.
func TestTheManagerInPlanModeIsJudgedByTheEnginesOwnProfile(t *testing.T) {
	root := t.TempDir()
	eng, err := perm.NewEngine(perm.Config{Mode: perm.ModePlan, Root: root, Home: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	rr := roleRequester{inner: eng, role: BuiltinRoles()["manager"], denyWrites: managerWritesMsg, strictShell: true, planOnly: eng.PlanOnly()}
	ctx := context.Background()
	for _, cmd := range []string{"echo hi > f.txt", "rm -rf build", "git checkout main -- a.txt", "go test ./...", "curl https://example.com"} {
		d := rr.Check(ctx, perm.Request{Tool: "bash", Command: cmd, Cwd: root})
		if d.Allow {
			t.Errorf("plan mode let the manager run %q", cmd)
		}
		if strings.Contains(d.Reason, "spawn a worker") {
			t.Errorf("%q: the refusal sends the manager off to spawn a worker, but the session is planning: %q", cmd, d.Reason)
		}
	}
	if d := rr.Check(ctx, perm.Request{Tool: "write", Writes: true, Paths: []string{root + "/a.txt"}}); d.Allow || strings.Contains(d.Reason, "spawn a worker") {
		t.Errorf("a write in plan mode: %+v", d)
	}
	if d := rr.Check(ctx, perm.Request{Tool: "bash", Command: "git log --oneline", Cwd: root}); !d.Allow {
		t.Errorf("the manager may still inspect while planning: %+v", d)
	}
	// The two policies differ both ways: the engine proves a read-only compound command that the allowlist rejects, and refuses go test, which
	// the allowlist accepts.
	const compound = "git status && ls src | wc -l"
	if d := rr.Check(ctx, perm.Request{Tool: "bash", Command: compound, Cwd: root}); !d.Allow || readOnlyCommand(compound) {
		t.Errorf("a read-only compound command: engine %+v, allowlist %v", d, readOnlyCommand(compound))
	}
	if !readOnlyCommand("go test ./...") {
		t.Error("the fallback allowlist no longer accepts go test: the comparison this test documents has changed")
	}
}

// planExceptions are what a person may carve out of plan mode: a file the session may write and two commands.
var planExceptions = []string{"Edit(docs/plan.md)", "Bash(rm:*)", "Bash(go test:*)"}

// An allow rule is the person's word for the session, and plan mode honours it for everyone, but it does not lift what the manager may do: the
// manager edits no file in any run. A request that only a rule lets through is held to the manager's own policy (no file tool that writes, one
// plain read-only shell command, go test among them); what plan mode proves read-only on its own, compound commands included, stays allowed.
func TestTheManagerInPlanModeIsNotLoosenedByAllowRules(t *testing.T) {
	root := t.TempDir()
	eng, err := perm.NewEngine(perm.Config{Mode: perm.ModePlan, Root: root, Home: t.TempDir(), Allow: planExceptions})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	plan := perm.Request{Tool: "write", Writes: true, Paths: []string{root + "/docs/plan.md"}}
	rm := perm.Request{Tool: "bash", Command: "rm -rf build", Cwd: root}
	mgr := roleRequester{inner: eng, role: BuiltinRoles()["manager"], denyWrites: managerWritesMsg, strictShell: true, planOnly: eng.PlanOnly()}

	if d := mgr.Check(ctx, plan); d.Allow || !strings.Contains(d.Reason, "spawn a worker") {
		t.Errorf("the plan file, through the manager, by a rule: %+v", d)
	}
	if d := mgr.Check(ctx, rm); d.Allow || !strings.Contains(d.Reason, "one plain read-only command at a time") {
		t.Errorf("rm, through the manager, by a rule: %+v", d)
	}
	if d := mgr.Check(ctx, perm.Request{Tool: "bash", Command: "go test ./...", Cwd: root}); !d.Allow {
		t.Errorf("go test is a rule and on the manager's own list: %+v", d)
	}
	if d := mgr.Check(ctx, perm.Request{Tool: "bash", Command: "git status && ls src | wc -l", Cwd: root}); !d.Allow {
		t.Errorf("plan mode proves this read-only on its own: %+v", d)
	}
	if d := mgr.Check(ctx, perm.Request{Tool: "read", Paths: []string{root + "/README.md"}}); !d.Allow {
		t.Errorf("a read: %+v", d)
	}

	// The same rules, for anyone else, do what the person wrote.
	worker := roleRequester{inner: eng, role: BuiltinRoles()["backend"]}
	if d := worker.Check(ctx, plan); !d.Allow {
		t.Errorf("a worker may edit the plan file the person allowed: %+v", d)
	}
	if d := worker.Check(ctx, rm); !d.Allow {
		t.Errorf("a worker may run the command the person allowed: %+v", d)
	}
}

// The swarm builds the manager's requester with the plan-only engine: the manager's own tools see the same refusal as the requester above.
func TestTheSwarmHoldsItsManagerToThePolicyInPlanMode(t *testing.T) {
	root := t.TempDir()
	eng, err := perm.NewEngine(perm.Config{Mode: perm.ModePlan, Root: root, Home: t.TempDir(), Allow: planExceptions})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	got := map[string]perm.Decision{}
	r := newRVRigWith(t, Config{}, func(ctx context.Context, c *rvCall) rvReply {
		if !c.Sees("probe-done") {
			return rvReply{Tools: []rvToolCall{{Name: "probe", Args: map[string]any{}}}}
		}
		return rvReply{Text: "finished"}
	}, func(d *Deps) { d.Perm = eng })
	r.sw.deps.Registry.Register(rvFakeTool{name: "probe", ro: true, run: func(ctx context.Context, c *tools.Call) *tools.Result {
		mu.Lock()
		defer mu.Unlock()
		got["plan file"] = c.Env.Perm.Check(ctx, perm.Request{Agent: c.Env.Agent, Tool: "write", Writes: true, Paths: []string{root + "/docs/plan.md"}})
		got["rm"] = c.Env.Perm.Check(ctx, perm.Request{Agent: c.Env.Agent, Tool: "bash", Command: "rm -rf build", Cwd: root})
		got["go test"] = c.Env.Perm.Check(ctx, perm.Request{Agent: c.Env.Agent, Tool: "bash", Command: "go test ./...", Cwd: root})
		return &tools.Result{Text: "probe-done"}
	}})
	if _, err := r.sw.RunManager(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 3 {
		t.Fatalf("the probe did not run: %v", got)
	}
	if got["plan file"].Allow || got["rm"].Allow {
		t.Errorf("the manager's own requester let a rule lift the policy: plan file %+v, rm %+v", got["plan file"], got["rm"])
	}
	if !got["go test"].Allow {
		t.Errorf("go test is a rule and on the manager's own list: %+v", got["go test"])
	}
}
