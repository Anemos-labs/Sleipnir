package swarm

// Retiring an agent and shutting the swarm down end the agent's background work and give
// back its archive index (agent.Close). These are the two call sites of that method.

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/reee344/sleipnir/internal/agent"
	"github.com/reee344/sleipnir/internal/core"
)

func archiveSomeTurns(t *testing.T, r *rvRig, id string, n int) {
	t.Helper()
	for i := 1; i <= n; i++ {
		turn := core.Turn{ID: core.TurnID(i), Role: core.RoleUser, Blocks: []core.Block{core.Text(fmt.Sprintf("turn %d of %s", i, id))}}
		if err := r.sw.deps.Archive.Put(id, turn); err != nil {
			t.Fatal(err)
		}
	}
	if got := r.sw.deps.Archive.Len(id); got != n {
		t.Fatalf("%s: %d archived turns, want %d", id, got, n)
	}
}

func TestRetireClosesTheAgentAndReleasesItsArchiveIndex(t *testing.T) {
	r := newRVRig(t, Config{MaxWriters: 4, MaxAgents: 10}, func(ctx context.Context, c *rvCall) rvReply { return rvReply{Text: "ok"} })
	if _, err := r.sw.StartManager(); err != nil {
		t.Fatal(err)
	}
	id, err := r.sw.Spawn(SpawnReq{Role: "reviewer", Title: "look at it", By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	rvWait(t, id+" to finish its work", func() bool { return r.idle(id) })
	other, err := r.sw.Spawn(SpawnReq{Role: "reviewer", Title: "look at something else", By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	rvWait(t, other+" to finish its work", func() bool { return r.idle(other) })

	archiveSomeTurns(t, r, id, 3)
	archiveSomeTurns(t, r, other, 2)
	retired := r.sw.get(id).a

	if err := r.sw.Retire(id); err != nil {
		t.Fatal(err)
	}
	if n := r.sw.deps.Archive.Len(id); n != 0 {
		t.Fatalf("a retired agent's archive index is still in memory: %d entries", n)
	}
	if n := r.sw.deps.Archive.Len(other); n != 2 {
		t.Fatalf("retiring one agent must leave the others' indexes alone: %d entries, want 2", n)
	}
	if _, err := retired.Run(context.Background(), "again"); !errors.Is(err, agent.ErrClosed) {
		t.Fatalf("a retired agent must be closed, Run says %v", err)
	}
}

func TestShutdownClosesEveryAgentAndReleasesTheirArchiveIndexes(t *testing.T) {
	r := newRVRig(t, Config{MaxWriters: 4, MaxAgents: 10}, func(ctx context.Context, c *rvCall) rvReply { return rvReply{Text: "ok"} })
	if _, err := r.sw.StartManager(); err != nil {
		t.Fatal(err)
	}
	ids := []string{r.sw.ManagerID()}
	for i := 0; i < 2; i++ {
		id, err := r.sw.Spawn(SpawnReq{Role: "reviewer", Title: fmt.Sprintf("look at part %d", i), By: "mgr"})
		if err != nil {
			t.Fatal(err)
		}
		rvWait(t, id+" to finish its work", func() bool { return r.idle(id) })
		ids = append(ids, id)
	}
	agents := map[string]*agent.Agent{}
	for _, id := range ids {
		archiveSomeTurns(t, r, id, 2)
		agents[id] = r.sw.get(id).a
	}

	r.sw.Shutdown()
	for _, id := range ids {
		if n := r.sw.deps.Archive.Len(id); n != 0 {
			t.Errorf("%s: archive index still in memory after Shutdown: %d entries", id, n)
		}
		if _, err := agents[id].Run(context.Background(), "again"); !errors.Is(err, agent.ErrClosed) {
			t.Errorf("%s: Run after Shutdown says %v, want ErrClosed", id, err)
		}
	}
	r.sw.Shutdown() // twice is fine
}
