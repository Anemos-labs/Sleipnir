package sim

import (
	"strings"
	"testing"
	"time"
)

func TestDeterministic(t *testing.T) {
	w := DefaultWorkload(8)
	a := Layered(w, Anthropic(), DefaultLayered())
	b := Layered(w, Anthropic(), DefaultLayered())
	if a.ITE != b.ITE || a.Requests != b.Requests {
		t.Fatalf("same seed must give the same result: %v vs %v", a.ITE, b.ITE)
	}
}

func TestCacheSemantics(t *testing.T) {
	p := Anthropic()
	c := newCache(p)
	segs := []seg{{"a", 4000, false}, {"b", 4000, false}, {"t1", 1000, false}, {"hot", 500, true}}
	bp := []int{1, 2}
	u, r, w, hit, ttfb := c.request(0, 0, segs, bp)
	if r != 0 || hit || w != 9000 || u != 500 {
		t.Fatalf("cold: unc=%d read=%d write=%d", u, r, w)
	}
	// Not readable before the first byte, and a second cold request arriving in
	// the meantime does not push readiness back.
	if _, r2, _, _, _ := c.request(0, ttfb/2, segs, bp); r2 != 0 {
		t.Fatalf("entry must not be readable before first byte, read %d", r2)
	}
	u, r, w, hit, _ = c.request(0, ttfb+time.Second, segs, bp)
	if r != 9000 || w != 0 || !hit {
		t.Fatalf("warm: unc=%d read=%d write=%d", u, r, w)
	}
	// Growth: reads the old breakpoint, writes the new tail.
	grown := append([]seg{}, segs[:3]...)
	grown = append(grown, seg{"t2", 2000, false}, seg{"hot", 500, true})
	_, r, w, _, _ = c.request(0, 2*time.Minute, grown, []int{1, 3})
	if r != 9000 || w != 2000 {
		t.Fatalf("growth: read=%d write=%d", r, w)
	}
	// TTL expiry: after 6 minutes idle everything is cold again.
	_, r, w, _, _ = c.request(0, 9*time.Minute, grown, []int{1, 3})
	if r != 0 || w != 11000 {
		t.Fatalf("expired: read=%d write=%d", r, w)
	}
}

func TestCommonRandomNumbers(t *testing.T) {
	// Every policy faces exactly the same tasks: the only difference in step
	// counts is the orientation each policy needs.
	w := DefaultWorkload(12)
	p := Anthropic()
	n := Naive(w, p, NaiveOptions{})
	l := Layered(w, p, DefaultLayered())
	want := w.Tasks * (w.ExploreSteps - w.OrientSteps)
	if got := n.Steps - l.Steps; got != want {
		t.Fatalf("naive %d steps vs layered %d: difference %d, want %d", n.Steps, l.Steps, got, want)
	}
	// Scheduling order must not change a task's content.
	if a, b := w.spec(3), w.spec(3); len(a.work) != len(b.work) || a.work[0] != b.work[0] {
		t.Fatalf("spec must be a pure function of (seed, index)")
	}
}

func TestLayeredBeatsNaiveAtScale(t *testing.T) {
	for _, p := range []Provider{Anthropic(), Marketplace(1)} {
		w := DefaultWorkload(20)
		naive := Naive(w, p, NaiveOptions{})
		sum := Naive(w, p, NaiveOptions{CompactAt: 100_000})
		lay := Layered(w, p, DefaultLayered())
		if lay.ITE >= sum.ITE {
			t.Fatalf("%s: layered %.1fM must beat even naive+summary compaction %.1fM", p.Name, lay.ITE/1e6, sum.ITE/1e6)
		}
		if lay.ITE >= naive.ITE*0.85 {
			t.Fatalf("%s: layered %.1fM should be well under naive %.1fM", p.Name, lay.ITE/1e6, naive.ITE/1e6)
		}
		if lay.AvgContext >= naive.AvgContext {
			t.Fatalf("%s: compaction should shrink the average context (%.0f vs %.0f)", p.Name, lay.AvgContext, naive.AvgContext)
		}
		if lay.WallSeconds >= naive.WallSeconds {
			t.Fatalf("%s: skipping orientation should finish sooner (%.0fs vs %.0fs)", p.Name, lay.WallSeconds, naive.WallSeconds)
		}
	}
}

func TestAblationsMatterInTheRightDirection(t *testing.T) {
	// Compaction: without it the layered prompt grows like a naive one.
	{
		w := DefaultWorkload(20)
		p := Anthropic()
		full := Layered(w, p, DefaultLayered())
		nc := DefaultLayered()
		nc.Compaction = false
		if noComp := Layered(w, p, nc); noComp.ITE <= full.ITE || noComp.AvgContext <= full.AvgContext {
			t.Fatalf("compaction must lower cost and context: %.1fM/%.0fk vs %.1fM/%.0fk", noComp.ITE/1e6, noComp.AvgContext/1000, full.ITE/1e6, full.AvgContext/1000)
		}
	}
	// Warm gate: every cold launch of a swarm otherwise pays the role layer once
	// per agent.
	{
		w := DefaultWorkload(30)
		w.Waves = 3
		p := Anthropic()
		full := Layered(w, p, DefaultLayered())
		ng := DefaultLayered()
		ng.Gate = false
		if noGate := Layered(w, p, ng); noGate.ITE <= full.ITE {
			t.Fatalf("removing the warm gate must not make cold fan-outs cheaper: %.2f vs %.2f", noGate.ITE/1e6, full.ITE/1e6)
		}
	}
	// Multi-engine marketplace: dropping affinity costs cold prefills.
	{
		mp := Marketplace(4)
		w := DefaultWorkload(24)
		fa := Layered(w, mp, DefaultLayered())
		na := DefaultLayered()
		na.Affinity = false
		if noAff := Layered(w, mp, na); noAff.ITE <= fa.ITE {
			t.Fatalf("affinity should save money on multi-engine providers: without %.1f vs with %.1f", noAff.ITE/1e6, fa.ITE/1e6)
		}
	}
}

func TestColdMaskingHelpsAfterLongWaits(t *testing.T) {
	// One agent, long tool waits that outlive the cache: masking for free while
	// cold beats waiting for a warm moment to pay for a fork.
	w := DefaultWorkload(2)
	w.LongToolProb = 0.25
	p := Anthropic()
	full := Layered(w, p, DefaultLayered())
	nm := DefaultLayered()
	nm.ColdMask = false
	noMask := Layered(w, p, nm)
	if full.MaskCommits == 0 {
		t.Fatalf("expected cold-moment mask commits with frequent long waits")
	}
	if noMask.ITE < full.ITE {
		t.Fatalf("cold masking should not cost more than waiting: %.2fM vs %.2fM", full.ITE/1e6, noMask.ITE/1e6)
	}
}

// TestPinDensityRule pins down the design's central caveat: layering pays only
// while the pins are dense, i.e. not much larger than the orientation reading
// they replace. A bloated pin loses to a plain harness.
func TestPinDensityRule(t *testing.T) {
	p := Anthropic()
	base := DefaultWorkload(20)
	dense := RunScenario("dense", base, p)
	if dense.VsNaive > -10 {
		t.Fatalf("dense pins should win clearly: %+.0f%%", dense.VsNaive)
	}
	bloated := base
	bloated.SharedTokens, bloated.RoleTokens = 60000, 40000
	b := RunScenario("bloated", bloated, p)
	if b.VsSummary < 0 {
		t.Fatalf("100k of pins against 30k of exploration must lose to the compacting baseline, got %+.0f%%", b.VsSummary)
	}
	small := base
	small.ExploreTokens, small.ExploreSteps = 0, 0
	s := RunScenario("nothing to explore", small, p)
	if s.VsNaive < dense.VsNaive {
		t.Fatalf("with nothing to explore the advantage must shrink, got %+.0f%% (dense %+.0f%%)", s.VsNaive, dense.VsNaive)
	}
}

func TestScenarioTablesRender(t *testing.T) {
	out, rows := ScenarioTable(6, Anthropic())
	if len(rows) != len(Scenarios()) || !strings.Contains(out, "typical") {
		t.Fatalf("unexpected table:\n%s", out)
	}
	out, pts := PinSweep(6, Anthropic(), []int{4000, 18000, 60000})
	if len(pts) != 3 || pts[0].VsNaive >= pts[2].VsNaive {
		t.Fatalf("a bigger pin must be relatively worse: %+v\n%s", pts, out)
	}
}

func TestPrintComparison(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	for _, n := range []int{1, 10, 50} {
		for _, p := range []Provider{Anthropic(), Marketplace(3)} {
			c := Compare(DefaultWorkload(n), p)
			t.Log("\n" + c.Table())
		}
	}
	for _, p := range []Provider{Anthropic(), Marketplace(3)} {
		s, _ := ScenarioTable(20, p)
		t.Log("\n" + s)
		s, _ = PinSweep(20, p, []int{4000, 9000, 18000, 30000, 45000, 70000, 100000})
		t.Log("\n" + s)
	}
	t.Log("\n" + AgentSweep(Anthropic(), []int{1, 2, 5, 10, 20, 50, 100}))
	// Cold launches show the warm gate; long waits show cold-moment masking.
	cold := DefaultWorkload(30)
	cold.Waves = 3
	t.Log("\n" + Compare(cold, Anthropic()).Table())
	waits := DefaultWorkload(4)
	waits.LongToolProb = 0.2
	t.Log("\n" + Compare(waits, Anthropic()).Table())
}
