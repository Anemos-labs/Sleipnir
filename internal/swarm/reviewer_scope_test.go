package swarm

import (
	"context"
	"strings"
	"testing"
)

// In a shared tree a reviewer's task may be scoped to the whole project while implementation tasks are open, and they may start
// while it runs: its scope names the work it reviews, not an area it builds. Writers still collide with each other. Held to the
// overlap check, a reviewer scoped to "**" could not start while any implementation task was open, and a manager revised its
// scope three times to find a made-up disjoint one.
func TestAReviewerScopeSharesTheWholeProject(t *testing.T) {
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	r := newRVRigWith(t, Config{MaxWorkers: 5, PersistentPools: true, Team: []string{"backend", "backend", "backend", "reviewer"}},
		func(ctx context.Context, c *rvCall) rvReply {
			select {
			case <-block:
			case <-ctx.Done():
			}
			return rvReply{Text: "noted"}
		}, nil)
	if r.sw.isolated() {
		t.Fatal("the rig is isolated: this is about the shared tree")
	}
	b := r.sw.Board
	build, _ := b.CreateTask("mgr", TaskSpec{Title: "store", Role: "backend", Files: []string{"internal/store/**"}})
	spawn := func(in map[string]any) (string, bool) {
		t.Helper()
		res := r.callTool(context.Background(), "spawn", "mgr", "manager", in)
		return res.Text, res.IsError
	}
	if got, bad := spawn(map[string]any{"role": "backend", "task": build.ID, "solo": true}); bad {
		t.Fatalf("spawn %s: %s", build.ID, got)
	}
	review, _ := b.CreateTask("mgr", TaskSpec{Title: "review the merged result", Role: ReviewerRole, Files: []string{"**"}})
	if got, bad := spawn(map[string]any{"role": ReviewerRole, "task": review.ID}); bad {
		t.Fatalf("a reviewer scoped to ** was refused while %s is open: %s", build.ID, got)
	}
	more, _ := b.CreateTask("mgr", TaskSpec{Title: "cli", Role: "backend", Files: []string{"cmd/**"}})
	if got, bad := spawn(map[string]any{"role": "backend", "task": more.ID, "solo": true}); bad {
		t.Fatalf("an implementation task was refused while the review holds **: %s", got)
	}
	clash, _ := b.CreateTask("mgr", TaskSpec{Title: "store again", Role: "backend", Files: []string{"internal/**"}})
	if got, bad := spawn(map[string]any{"role": "backend", "task": clash.ID, "solo": true}); !bad || !strings.Contains(got, "overlaps") {
		t.Fatalf("two writers' overlapping scopes were not refused: %q", got)
	}
}
