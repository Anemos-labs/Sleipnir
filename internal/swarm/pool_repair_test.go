package swarm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/tools"
	"github.com/anemos-labs/sleipnir/internal/workspace"
)

// A reconciling member whose inspect leaves conflicts in its checkout may edit the conflicted files, and nothing else, until the
// merge is concluded: the refusal names the repair, and the next inspect goes through. In a team trial a member kept an uncommitted
// draft from an earlier task; pool inspect merged the combined result over it, left markers, and refused "repair checkout before
// confirming", while the pool refused every edit that would repair it ("members change nothing while the pool reconciles"). The
// member retried the same inspect until the repeat guard stopped it, and the pool went on without it.
func TestAReconcilingMemberMayRepairItsConflictedFiles(t *testing.T) {
	ctx := context.Background()
	r := newIsoRig(t, isoOpts{cfg: Config{PersistentPools: true, MaxWorkers: 2, Team: []string{"backend", "backend"}}}, idleModel)
	r.mustSpawn("backend", "api", "api/**", "shared.txt")
	parent := r.sw.Board.Snapshot().Tasks[0]
	if parent.Pool == nil {
		t.Fatalf("%s is no pool", parent.ID)
	}
	lead, member := parent.Pool.Members[0], parent.Pool.Members[1]
	lm, mm := r.sw.get(lead), r.sw.get(member)
	if lm == nil || lm.tree == nil || mm == nil || mm.tree == nil {
		t.Fatal("the pool's members have no checkouts")
	}
	if err := r.sw.Board.poolUpdate("mgr", parent.ID, func(_ *Snapshot, _ *Task, p *PoolState) error { p.Phase = "executing"; return nil }); err != nil {
		t.Fatal(err)
	}
	pool := func(as string, args map[string]any) *tools.Result {
		args["id"] = parent.ID
		return r.callTool(ctx, "pool", as, "backend", args)
	}
	if res := pool(lead, map[string]any{"action": "reconcile", "text": "combined"}); res.IsError {
		t.Fatal(res.Text)
	}
	// The lead's result lands on the integration branch; the member holds an uncommitted draft on the same line.
	write := func(dir, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "shared.txt"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(lm.tree.Path, "line one\nthe lead's line two\nline three\n")
	if _, err := lm.tree.Commit(ctx, "the lead's result"); err != nil {
		t.Fatal(err)
	}
	if res, err := r.q.Submit(ctx, workspace.Submission{Tree: lm.tree, Task: parent.ID}); err != nil || !res.Merged() {
		t.Fatalf("submit: %+v, %v", res, err)
	}
	write(mm.tree.Path, "line one\nthe member's line two\nline three\n")
	res := pool(member, map[string]any{"action": "inspect"})
	if !res.IsError {
		t.Fatalf("an inspect over a conflicting draft went through: %s", res.Text)
	}
	for _, want := range []string{"repair checkout before confirming", "shared.txt", "you may edit those files", "pool inspect again"} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("the refusal does not say %q: %s", want, res.Text)
		}
	}
	eng, err := perm.NewEngine(perm.Config{Root: r.repo, Home: t.TempDir(), Mode: perm.ModeBypass})
	if err != nil {
		t.Fatal(err)
	}
	repair := perm.Request{Agent: member, Role: "backend", Tool: "write", Paths: []string{filepath.Join(mm.tree.Path, "shared.txt")}, Writes: true}
	if d, handled := r.sw.poolPermission(ctx, member, eng, repair); handled && !d.Allow {
		t.Fatalf("the pool refused the repair of a conflicted file: %s", d.Reason)
	}
	other := perm.Request{Agent: member, Role: "backend", Tool: "write", Paths: []string{filepath.Join(mm.tree.Path, "api/server.go")}, Writes: true}
	if d, handled := r.sw.poolPermission(ctx, member, eng, other); !handled || d.Allow {
		t.Fatalf("a reconciling member wrote outside the repair: %+v %v", d, handled)
	}
	both := perm.Request{Agent: member, Role: "backend", Tool: "write", Paths: []string{filepath.Join(mm.tree.Path, "shared.txt"), filepath.Join(mm.tree.Path, "api/server.go")}, Writes: true}
	if d, handled := r.sw.poolPermission(ctx, member, eng, both); !handled || d.Allow {
		t.Fatalf("a write naming a conflicted file and another went through: %+v %v", d, handled)
	}
	// Resolved, the inspect concludes the merge and the member confirms; its files are the pool's again.
	write(mm.tree.Path, "line one\nboth lines two\nline three\n")
	if res := pool(member, map[string]any{"action": "inspect"}); res.IsError {
		t.Fatalf("the inspect after the repair was refused: %s", res.Text)
	}
	if d, handled := r.sw.poolPermission(ctx, member, eng, repair); !handled || d.Allow {
		t.Fatalf("a member wrote a repaired file after the merge concluded: %+v %v", d, handled)
	}
	p, _ := r.sw.Board.Snapshot().Task(parent.ID)
	if res := pool(member, map[string]any{"action": "confirm", "revision": p.Pool.Revision}); res.IsError {
		t.Fatalf("confirm: %s", res.Text)
	}
	if got, _ := os.ReadFile(filepath.Join(mm.tree.Path, "shared.txt")); !strings.Contains(string(got), "both lines two") {
		t.Fatalf("the repaired file was lost: %q", got)
	}
}
