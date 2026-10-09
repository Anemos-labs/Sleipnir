package swarm

import (
	"context"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/perm"
)

// While the session is planning, the manager's requests go straight to the permission engine, whose plan mode judges every shell command
// and every write itself. That is no weaker than the fallback allowlist the manager is held to otherwise: the engine refuses what the
// allowlist refuses and more (it refuses `go test`, which the allowlist accepts), and its refusal says the session is planning instead of
// sending the manager off to spawn a worker for a change nobody can make yet.
func TestTheManagerInPlanModeIsJudgedByTheEnginesOwnProfile(t *testing.T) {
	root := t.TempDir()
	eng, err := perm.NewEngine(perm.Config{Mode: perm.ModePlan, Root: root, Home: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	rr := roleRequester{inner: eng, role: BuiltinRoles()["manager"], denyWrites: managerWritesMsg, strictShell: true}
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
	// The engine is the stricter judge: the allowlist would have accepted `go test`, the engine's plan mode does not.
	if !readOnlyCommand("go test ./...") {
		t.Error("the fallback allowlist no longer accepts go test: the comparison this test documents has changed")
	}
}
