package sim

// Regression tests for the prompt-cache economics review (R16): the simulator
// follows the code. Its fork reads the parent's prefix (true since the fork keeps
// every request parameter), its breakpoints come from the real planner, its gate
// is keyed and timed like the real one, it judges warmth the way the agent does
// and feeds the planner no oracle, and it prices the preserved-thinking route.

import (
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/kv"
)

func TestSimPlacesMarkersWithTheRealPlanner(t *testing.T) {
	p := Anthropic()
	segs := []seg{{"G0", 9000, false}, {"G1", 8000, false}, {"G2:worker", 10000, false}, {"G3:w00:0", 500, false},
		{"o:w00:1:0:0", 200, false}, {"r:w00:1:0:1", 1100, false}, {"hot:w00", 700, true}}
	got := layeredBP(p, segs)
	// thread (last persistent segment), shared, role; notes (500 tokens) is under
	// the floor, and the free fourth slot goes to the constitution.
	want := map[int]bool{5: true, 1: true, 2: true, 0: true}
	if len(got) != len(want) {
		t.Fatalf("markers %v, want indexes %v", got, want)
	}
	for _, i := range got {
		if !want[i] {
			t.Fatalf("markers %v, want indexes %v", got, want)
		}
	}
	// Automatic-cache providers get none.
	if bp := layeredBP(Marketplace(3), segs); bp != nil {
		t.Fatalf("automatic providers place no markers: %v", bp)
	}
	// A notes layer over the floor claims a slot before the constitution does.
	segs[3].tokens = 2000
	got = layeredBP(p, segs)
	has := map[int]bool{}
	for _, i := range got {
		has[i] = true
	}
	if !has[3] || has[0] || len(got) != 4 {
		t.Fatalf("with big notes: %v", got)
	}
}

// The compactor's fork is the agent's own request byte for byte plus an
// ephemeral instruction with the same request parameters, so it READS the
// parent's whole prefix. (Until the fork stopped changing tool_choice this was an
// assumption the simulator made and the code did not honour; the fork then
// re-wrote the messages tier at the write premium.)
func TestSimForkReadsTheParentsPrefix(t *testing.T) {
	p := Anthropic()
	c := newCache(p)
	parent := layeredSegs(DefaultWorkload(1), "w00", 1, 0, 500, 0, []seg{{"o:1", 240, false}, {"r:1", 3000, false}, {"o:2", 240, false}, {"r:2", 6000, false}}, 700)
	bp := layeredBP(p, parent)
	_, _, w0, _, ttfb := c.request(0, 0, parent, bp)
	if w0 == 0 {
		t.Fatal("the parent's first request writes")
	}
	fork := layeredSegs(DefaultWorkload(1), "w00", 1, 0, 500, 0, []seg{{"o:1", 240, false}, {"r:1", 3000, false}, {"o:2", 240, false}, {"r:2", 6000, false}}, 0)
	fork = append(fork, seg{"compact-instr", 1400, true})
	unc, rd, wr, _, _ := c.request(0, ttfb+10*time.Second, fork, layeredBP(p, fork))
	persistent := 0
	for _, s := range fork {
		if !s.ephemeral {
			persistent += s.tokens
		}
	}
	if rd != persistent || wr != 0 || unc != 1400 {
		t.Fatalf("fork must read the parent's %d tokens and pay plain input for the instruction only: read=%d write=%d plain=%d", persistent, rd, wr, unc)
	}
}

func TestSimGateIsKeyedAndTimedLikeTheRealOne(t *testing.T) {
	g := newPrimerGate(true, 5*time.Minute)
	if g.margin != 20*time.Second {
		t.Fatalf("margin %v", g.margin)
	}
	g.touch("G|R:worker", 0, 45*time.Second) // a primer whose first byte takes 45s
	if wait := g.wait(10*time.Second, "G|R:worker"); wait != 35*time.Second {
		t.Fatalf("followers wait for the primer's first byte: %v", wait)
	}
	// Warm from the START of the request, less the margin: 4m40s, not first byte + that.
	if st := g.keys["G|R:worker"]; st.warmUntil != 4*time.Minute+40*time.Second {
		t.Fatalf("warmUntil = %v", st.warmUntil)
	}
	// A request over a level that has gone cold becomes the new primer.
	g.touch("G|R:worker", 5*time.Minute, 30*time.Second)
	if wait := g.wait(5*time.Minute+time.Second, "G|R:worker"); wait != 29*time.Second {
		t.Fatalf("a cold level is primed again: %v", wait)
	}
	off := newPrimerGate(false, 5*time.Minute)
	off.touch("k", 0, time.Minute)
	if off.wait(time.Second, "k") != 0 {
		t.Fatal("a disabled gate never holds anything")
	}
}

// The headline with the review's fixes in place. It moved only slightly from the
// simulator's original figure (about -37% / -28%): the fork's cache read is now
// true rather than assumed, and what the code now does that the first model
// ignored (commit penalty with tail, spine and notes; masking rate limit; the
// entry-lifetime warm test; no oracle horizon) costs a point or two.
func TestSimHeadlineWithTheFixesStaysInTheDocumentedRange(t *testing.T) {
	w := DefaultWorkload(20)
	p := Anthropic()
	naive := Naive(w, p, NaiveOptions{})
	sum := Naive(w, p, NaiveOptions{CompactAt: 100_000})
	lay := Layered(w, p, DefaultLayered())
	vsNaive, vsSum := (lay.ITE/naive.ITE-1)*100, (lay.ITE/sum.ITE-1)*100
	t.Logf("20 workers, Anthropic-like: layered %.1fM, plain %.1fM (%+.0f%%), plain+summary %.1fM (%+.0f%%); %d forks, %d mask commits", lay.ITE/1e6, naive.ITE/1e6, vsNaive, sum.ITE/1e6, vsSum, lay.Compactions, lay.MaskCommits)
	if vsNaive > -30 || vsNaive < -42 || vsSum > -20 || vsSum < -34 {
		t.Fatalf("headline %+.0f%% vs plain / %+.0f%% vs plain+summary left the documented range (about -36%% / -26%%): update docs/CACHE-DESIGN.md §8 with the new numbers", vsNaive, vsSum)
	}
	if lay.Compactions == 0 || lay.MaskCommits == 0 {
		t.Fatalf("both compaction paths must be exercised: forks=%d masks=%d", lay.Compactions, lay.MaskCommits)
	}
}

// A model that enforces preserved thinking cannot take an inline hot tail (every
// signature after the first would be void), so the route persists the board on
// change. In the model that costs a few percent, growing with how often it is
// refreshed; the default interval is 3 requests.
func TestSimPersistedHotOnAPreservedThinkingRoute(t *testing.T) {
	w := DefaultWorkload(20)
	sum := Naive(w, Anthropic(), NaiveOptions{CompactAt: 100_000})
	inline := Layered(w, Anthropic(), DefaultLayered())
	persist := Layered(w, AnthropicPreserved(), DefaultLayered())
	every := DefaultLayered()
	every.HotEvery = 1
	each := Layered(w, AnthropicPreserved(), every)
	t.Logf("inline %.2fM, persisted every 3rd request %.2fM (%+.1f%%), persisted every request %.2fM (%+.1f%%)",
		inline.ITE/1e6, persist.ITE/1e6, (persist.ITE/inline.ITE-1)*100, each.ITE/1e6, (each.ITE/inline.ITE-1)*100)
	if d := persist.ITE/inline.ITE - 1; d > 0.06 || d < -0.06 {
		t.Fatalf("persisting the board every 3rd request should cost within a few percent of inline, got %+.1f%%", d*100)
	}
	if persist.ITE >= sum.ITE {
		t.Fatalf("the preserved-thinking route must still beat plain + summary compaction: %.1fM vs %.1fM", persist.ITE/1e6, sum.ITE/1e6)
	}
	if each.ITE <= persist.ITE {
		t.Fatalf("throttling must pay: every request %.2fM vs every third %.2fM", each.ITE/1e6, persist.ITE/1e6)
	}
	// Turn-scoped system messages sit after the last marker like the inline tail.
	ts := Layered(w, Anthropic().WithHot(kv.HotTurnScoped), DefaultLayered())
	if ts.ITE != inline.ITE {
		t.Fatalf("turn-scoped is priced like inline: %.3fM vs %.3fM", ts.ITE/1e6, inline.ITE/1e6)
	}
}
