package swarm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/tools"
	"github.com/anemos-labs/sleipnir/internal/workspace"
)

// poolGateRig starts an isolated pool of two over app.txt, lands body as the lead's result on the integration branch, and
// has every member inspect and confirm it. The merge queue passes everything, so only the pool's own gate judges the
// combined result.
func poolGateRig(t *testing.T, body string) (*isoRig, Task, func(as string, name string, args map[string]any) *tools.Result) {
	t.Helper()
	ctx := context.Background()
	pass := func(string) (string, int) { return "", 0 }
	r := newIsoRig(t, isoOpts{cfg: Config{PersistentPools: true, MaxWorkers: 2, Team: []string{"backend", "backend"}},
		queueVerify: pass, files: map[string]string{"app.txt": "draft\n"}}, idleModel)
	r.mustSpawn("backend", "app", "app.txt")
	parent := r.sw.Board.Snapshot().Tasks[0]
	if parent.Pool == nil {
		t.Fatalf("%s is no pool", parent.ID)
	}
	lm := r.sw.get(parent.Pool.Members[0])
	if lm == nil || lm.tree == nil {
		t.Fatal("the lead has no checkout")
	}
	if err := r.sw.Board.poolUpdate("mgr", parent.ID, func(_ *Snapshot, _ *Task, p *PoolState) error { p.Phase = "executing"; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lm.tree.Path, "app.txt"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := lm.tree.Commit(ctx, "the lead's result"); err != nil {
		t.Fatal(err)
	}
	if res, err := r.q.Submit(ctx, workspace.Submission{Tree: lm.tree, Task: parent.ID}); err != nil || !res.Merged() {
		t.Fatalf("submit: %+v, %v", res, err)
	}
	call := func(as, name string, args map[string]any) *tools.Result {
		return r.callTool(ctx, name, as, "backend", args)
	}
	if res := call(parent.Owner, "pool", map[string]any{"action": "reconcile", "id": parent.ID, "text": "combined"}); res.IsError {
		t.Fatal(res.Text)
	}
	for _, m := range parent.Pool.Members {
		if res := call(m, "pool", map[string]any{"action": "inspect", "id": parent.ID}); res.IsError {
			t.Fatalf("inspect by %s: %s", m, res.Text)
		}
		p, _ := r.sw.Board.Snapshot().Task(parent.ID)
		if res := call(m, "pool", map[string]any{"action": "confirm", "id": parent.ID, "revision": p.Pool.Revision}); res.IsError {
			t.Fatalf("confirm by %s: %s", m, res.Text)
		}
	}
	return r, parent, call
}

// A combined result that fails its gate puts the pool back to executing, where the leader can create the fix as a child.
// In the trial two reconciling pools failed verification and stayed reconciling, where no member writes and no child is
// created: each leader alternated between a done that failed and an edit that was refused for half an hour.
func TestAPoolResultThatFailsVerificationExecutesAgain(t *testing.T) {
	r, parent, call := poolGateRig(t, "fixed\nBROKEN again\n")
	before, _ := r.sw.Board.Snapshot().Task(parent.ID)
	res := call(parent.Owner, "task", map[string]any{"action": "done", "id": parent.ID, "text": "combined"})
	if !res.IsError {
		t.Fatalf("a failing pool result was submitted: %s", res.Text)
	}
	for _, want := range []string{"failed verification", "executes again", "task create parent=" + parent.ID, "app.txt contains BROKEN"} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("the refusal does not say %q: %s", want, res.Text)
		}
	}
	after, _ := r.sw.Board.Snapshot().Task(parent.ID)
	if after.Pool.Phase != "executing" || after.Pool.Revision <= before.Pool.Revision || len(after.Pool.Votes) != 0 {
		t.Fatalf("the pool did not execute again at a new revision: %+v (was %+v)", *after.Pool, *before.Pool)
	}
	if after.Status != StatusDoing {
		t.Fatalf("%s is %s", parent.ID, after.Status)
	}
	if res := call(parent.Owner, "task", map[string]any{"action": "create", "parent": parent.ID, "title": "fix app", "files": []string{"app.txt"}}); res.IsError {
		t.Fatalf("the leader could not create the fix: %s", res.Text)
	}
}

// A failure the gate found in a revision the pool has since replaced leaves the newer revision and its votes alone.
func TestAPoolGateFailureOnAnOlderRevisionKeepsTheNewerOne(t *testing.T) {
	r, parent, call := poolGateRig(t, "fixed\nBROKEN again\n")
	gate := r.sw.cfg.Verify
	r.sw.cfg.Verify = func(ctx context.Context, dir, cmd string) (string, int, error) {
		// The leader reconciles again while the gate runs; one member confirms the new revision.
		if err := r.sw.Board.poolUpdate("mgr", parent.ID, func(_ *Snapshot, _ *Task, p *PoolState) error {
			p.Revision++
			p.Votes, p.Inspected = []string{parent.Owner}, []string{parent.Owner}
			return nil
		}); err != nil {
			t.Error(err)
		}
		return gate(ctx, dir, cmd)
	}
	before, _ := r.sw.Board.Snapshot().Task(parent.ID)
	if res := call(parent.Owner, "task", map[string]any{"action": "done", "id": parent.ID, "text": "combined"}); !res.IsError || strings.Contains(res.Text, "executes again") {
		t.Fatalf("a stale failure reset the pool: %s", res.Text)
	}
	after, _ := r.sw.Board.Snapshot().Task(parent.ID)
	if after.Pool.Phase != "reconciling" || after.Pool.Revision != before.Pool.Revision+1 || len(after.Pool.Votes) != 1 {
		t.Fatalf("the newer revision was reset: %+v", *after.Pool)
	}
}
