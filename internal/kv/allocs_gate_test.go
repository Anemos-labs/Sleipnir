//go:build !race

package kv_test

import (
	"testing"

	"github.com/anemos-labs/sleipnir/internal/kv"
)

// The allocations of the calls that run before every request, held to what they are (with a fifth to spare for what another version of
// Go does). Time is for BenchmarkRender*; the number of allocations does not depend on the machine, so it can be a test, and a change
// that makes Render allocate a block more per turn, or per layer, fails here and not in somebody's bill. Not under -race: the race
// detector's own bookkeeping allocates.
func TestRenderAllocationsAreHeldToWhatTheyAre(t *testing.T) {
	const runs = 20
	for _, c := range []struct {
		route string
		mode  kv.HotMode
		max   float64 // measured: anthropic 67-72, openai 39-44
	}{
		{"anthropic", kv.HotInline, 85}, {"anthropic", kv.HotPersist, 82}, {"anthropic", kv.HotTurnScoped, 88},
		{"openai", kv.HotInline, 50}, {"openai", kv.HotPersist, 48}, {"openai", kv.HotTurnScoped, 54},
	} {
		var rt route
		for _, r := range routes() {
			if r.name == c.route {
				rt = r
			}
		}
		stack, hot := goldenStack(t, c.mode)
		caps := rt.caps
		caps.HotMode = c.mode
		caps.TurnScopedSystem = c.mode == kv.HotTurnScoped
		opts := kv.RenderOpts{Hot: hot, Caps: caps, Policy: rt.policy, Params: rt.params, CacheKey: rt.cacheKey, Est: goldenEstimator()}
		got := testing.AllocsPerRun(runs, func() { _ = kv.Render(stack, opts) })
		if got > c.max {
			t.Errorf("Render on %s/%s allocates %.0f times, more than the %.0f it is held to (docs/BUILDING.md, 'Performance')", c.route, c.mode, got, c.max)
		}
	}
}

// What Render allocates grows with the thread, a little more than twice per exchange, and no faster: a render that does something per
// pair of turns, or per block of the layers, for every turn, shows as the quadratic it is.
func TestRenderAllocationsGrowLinearlyWithTheThread(t *testing.T) {
	allocs := func(n int) float64 {
		stack, opts := longStack(t, n)
		return testing.AllocsPerRun(10, func() { _ = kv.Render(stack, opts) })
	}
	a50, a200 := allocs(50), allocs(200)
	if a50 > 160 || a200 > 520 {
		t.Errorf("a thread of 50 exchanges costs %.0f allocations and one of 200 costs %.0f; held to 160 and 520", a50, a200)
	}
	if ratio := a200 / a50; ratio > 4.5 {
		t.Errorf("4 times the exchanges cost %.1f times the allocations (linear is about 3.3): something in Render is not linear in the thread", ratio)
	}
}

func TestPrefixKeyAndHashesAllocationsAreHeld(t *testing.T) {
	stack, _ := goldenStack(t, kv.HotInline)
	got := testing.AllocsPerRun(50, func() {
		_ = stack.PrefixKey()
		_ = stack.GlobalKey()
		_ = stack.Shared.Hash()
		_ = stack.Notes.Hash()
	})
	if got > 21 { // measured: 17
		t.Errorf("the planner's keys and hashes allocate %.0f times, more than the 21 they are held to", got)
	}
}
