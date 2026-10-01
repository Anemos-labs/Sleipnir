package traj_test

import (
	"fmt"
	"runtime"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/rl"
	"github.com/anemos-labs/sleipnir/internal/rl/traj"
	"github.com/anemos-labs/sleipnir/internal/rl/traj/trajtest"
)

// buildLongRun is a manager and five workers, each taking steps steps, with a compaction every sixty.
func buildLongRun(steps int) *trajtest.Run {
	b := trajtest.New()
	mgr := b.SpawnRoot("mgr", "manager", "m")
	mgr.User("go")
	agents := []*trajtest.Agent{mgr}
	for i := 0; i < 5; i++ {
		agents = append(agents, b.Spawn("mgr", fmt.Sprintf("w-%d", i), "backend", "m", fmt.Sprintf("T%d", i)))
		agents[len(agents)-1].User("work")
	}
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
	return b
}

// mallocsOf is the number of allocations that the whole pipeline (open, verify the replay, extract the episode) makes for a run,
// and the number of steps the episode has.
func mallocsOf(t *testing.T, b *trajtest.Run) (mallocs uint64, steps int) {
	t.Helper()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	r := traj.OpenWith(b.Events(), b.Blobs)
	if ms := r.Verify(); len(ms) != 0 {
		t.Fatal(ms[0])
	}
	ep, err := r.Episode(traj.Options{Policy: rl.PolicyRef{Model: "m"}})
	if err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&after)
	for _, a := range ep.Agents {
		steps += len(a.Steps)
	}
	return after.Mallocs - before.Mallocs, steps
}

// TestScale builds a long run (4,200 steps over six agents, periodic compaction) and checks that the pipeline stays roughly
// linear: twice the run costs about twice the work, not four times. The work is counted in allocations, which do not depend on how
// busy the machine is; a stopwatch here failed for good whenever the machine was.
func TestScale(t *testing.T) {
	if testing.Short() {
		t.Skip("scale test")
	}
	small, smallSteps := mallocsOf(t, buildLongRun(350))
	big, bigSteps := mallocsOf(t, buildLongRun(700))
	t.Logf("%d steps: %d allocations; %d steps: %d allocations", smallSteps, small, bigSteps, big)
	if bigSteps < smallSteps*19/10 {
		t.Fatalf("the long run has %d steps, the short one %d: the runs are not the sizes the test means", bigSteps, smallSteps)
	}
	if ratio := float64(big) / float64(small); ratio > 3 {
		t.Errorf("twice the steps cost %.1f times the allocations: the pipeline is no longer linear in the run", ratio)
	}
}
