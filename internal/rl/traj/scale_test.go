package traj_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/rl"
	"github.com/reee344/sleipnir/internal/rl/traj"
	"github.com/reee344/sleipnir/internal/rl/traj/trajtest"
)

// TestScale builds a long run (4000 main steps, periodic compaction, 6 agents) and
// checks the pipeline stays roughly linear: it must finish in seconds, not
// minutes.
func TestScale(t *testing.T) {
	if testing.Short() {
		t.Skip("scale test")
	}
	b := trajtest.New()
	mgr := b.SpawnRoot("mgr", "manager", "m")
	mgr.User("go")
	agents := []*trajtest.Agent{mgr}
	for i := 0; i < 5; i++ {
		agents = append(agents, b.Spawn("mgr", fmt.Sprintf("w-%d", i), "backend", "m", fmt.Sprintf("T%d", i)))
		agents[len(agents)-1].User("work")
	}
	const steps = 700
	turnID := make([]int, len(agents))
	for s := 0; s < steps; s++ {
		for i, a := range agents {
			a.Step(trajtest.Call{Text: "t", Tool: "read", Input: fmt.Sprintf(`{"path":"f%d"}`, s%37), Result: "content of the file"})
			turnID[i] += 2
			if s%60 == 59 {
				a.Fork(`{}`, "")
				a.Commit(core.TurnID(turnID[i]-3), false, "test")
			}
		}
	}
	start := time.Now()
	r := traj.OpenWith(b.Events(), b.Blobs)
	if ms := r.Verify(); len(ms) != 0 {
		t.Fatal(ms[0])
	}
	verify := time.Since(start)
	ep, err := r.Episode(traj.Options{Policy: rl.PolicyRef{Model: "m"}})
	if err != nil {
		t.Fatal(err)
	}
	total := time.Since(start)
	n := 0
	for _, a := range ep.Agents {
		n += len(a.Steps)
	}
	t.Logf("%d events, %d steps: verify %v, episode %v total", len(b.Events()), n, verify, total)
	if total > 20*time.Second {
		t.Fatalf("pipeline too slow: %v", total)
	}
}
