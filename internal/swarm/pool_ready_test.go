package swarm

import (
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/events"
)

// A pool that cannot begin leaves nothing behind: a parent whose dependency is open is refused before any member is made. The
// members used to be made first, and three frontend members stood on the roster as starting for the forty minutes until the
// dependency was done.
func TestARefusedPoolMakesNoMembers(t *testing.T) {
	r := newRVRig(t, Config{PersistentPools: true, MaxWorkers: 4, Team: []string{"backend", "frontend", "frontend", "frontend"}}, idleModel)
	api, err := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "api", Role: "backend", Files: []string{"api/**"}})
	if err != nil {
		t.Fatal(err)
	}
	ui, err := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "interface", Role: "frontend", Files: []string{"web/**"}, Deps: []string{api.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.sw.Spawn(SpawnReq{Role: "frontend", TaskID: ui.ID, By: "mgr"}); err == nil || !strings.Contains(err.Error(), "dependency "+api.ID+" is not complete") {
		t.Fatalf("a pool on a parent whose dependency is open: %v", err)
	}
	if n := len(r.sw.poolMembers("frontend")); n != 0 {
		t.Errorf("the refused pool made %d members", n)
	}
	for _, e := range r.log.All() {
		if e.Type == events.TypeAgentSpawn && strings.Contains(string(e.Data), `"role":"frontend"`) {
			t.Errorf("a member was announced: %s", e.Data)
		}
	}
	if err := r.sw.Board.resolve("mgr", api.ID, "done elsewhere"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.sw.Spawn(SpawnReq{Role: "frontend", TaskID: ui.ID, By: "mgr"}); err != nil {
		t.Fatalf("once the dependency is done: %v", err)
	}
	if n := len(r.sw.poolMembers("frontend")); n != 3 {
		t.Errorf("the pool has %d members, want 3", n)
	}
}
