package swarm

import (
	"encoding/json"
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
	if _, err := r.sw.Spawn(SpawnReq{Role: "frontend", TaskID: "T99", By: "mgr"}); err == nil || !strings.Contains(err.Error(), "no task T99") {
		t.Errorf("a pool on a task that does not exist: %v", err)
	}
	if n := len(r.sw.poolMembers("frontend")); n != 0 {
		t.Errorf("a pool on a missing task made %d members", n)
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

// A member is said to be idle as soon as it is made: whatever starts it later says so, and until then no reader of the log takes the
// spawn for the start of a run (the terminal and the page showed members of a pool that never began as starting, and active).
func TestAPoolMemberIsIdleUntilItsPoolStartsIt(t *testing.T) {
	r := newRVRig(t, Config{PersistentPools: true, MaxWorkers: 2, Team: []string{"frontend", "frontend"}}, idleModel)
	r.sw.spawnMu.Lock()
	_, err := r.sw.ensurePool("frontend")
	r.sw.spawnMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	spawned := map[string]bool{}
	idle := map[string]bool{}
	for _, e := range r.log.All() {
		var p struct{ ID, State string }
		if json.Unmarshal(e.Data, &p) != nil {
			continue
		}
		switch e.Type {
		case events.TypeAgentSpawn:
			spawned[p.ID] = true
		case events.TypeAgentState:
			if spawned[p.ID] && p.State == "idle" {
				idle[p.ID] = true
			}
		}
	}
	for _, id := range []string{"fe-1", "fe-2"} {
		if !spawned[id] || !idle[id] {
			t.Errorf("%s: spawned %v, said idle after it %v", id, spawned[id], idle[id])
		}
	}
}
