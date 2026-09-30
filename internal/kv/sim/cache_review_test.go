package sim

// Adversarial review tests (lens: prompt-cache economics and correctness).
// See docs/reviews/cache-economics.md. Convention: tests PASS while the defect
// is present; REVIEW_STRICT=1 inverts.

import (
	"fmt"
	"os"
	"testing"
	"time"
)

func cxBug(t *testing.T, present bool, format string, args ...any) {
	t.Helper()
	msg := fmt.Sprintf(format, args...)
	if os.Getenv("REVIEW_STRICT") != "" {
		if present {
			t.Fatalf("DEFECT PRESENT: %s", msg)
		}
		return
	}
	if !present {
		t.Fatalf("defect no longer reproduces (%s): invert or delete this review test", msg)
	}
	t.Logf("DEFECT CONFIRMED: %s", msg)
}

// The simulator keys cache entries by segment ids only. The compactor fork is
// billed as "the agent's own prefix, read from cache, plus an instruction"
// (policies.go: Layered, ModeFork). The real fork sets tool_choice=none
// (kv/fork.go:29), which on Anthropic keeps the tools+system cache but
// invalidates the whole messages tier: G1..G5 all live there in Sleipnir's
// layout. This test bills one fork both ways with the simulator's own cache
// model and scales by the number of forks in the reference run.
func TestCacheEcon_SimBillsForksAsCacheReadsButToolChoiceInvalidatesMessages(t *testing.T) {
	p := Anthropic()
	w := DefaultWorkload(20)
	segsFor := func(rekey, agent string, threadTok int) []seg {
		id := func(s string) string { return rekey + s }
		out := []seg{{"G0", w.ConstTokens, false}, // tools + system: survives a tool_choice change
			{id("G1"), w.SharedTokens, false}, {id("G2:worker"), w.RoleTokens, false},
			{id("G3:" + agent), 600, false}, {id("G4:" + agent), 1500, false}}
		per := threadTok / 20
		for i := 0; i < 20; i++ {
			out = append(out, seg{fmt.Sprintf("%s%s:T%d", rekey, agent, i), per, false})
		}
		return out
	}
	// shared=true: another agent's fork (same tool_choice regime) ran 20s earlier
	// and already wrote the shared G1/G2 entries under that regime, which is the
	// favourable case in a busy swarm (one fork every ~45s at 20 workers).
	bill := func(rekey string, threadTok int, shared bool) float64 {
		c := newCache(p)
		main := segsFor("", "w00", threadTok)
		c.request(0, 0, main, layeredBP(main)) // the agent's previous request warmed the prefix
		if shared && rekey != "" {
			other := append(segsFor(rekey, "w01", threadTok), seg{"compact-instr", 1400, true})
			c.request(0, 10*time.Second, other, layeredBP(other))
		}
		fork := append(segsFor(rekey, "w00", threadTok), seg{"compact-instr", 1400, true})
		unc, rd, wr, _, _ := c.request(0, 30*time.Second, fork, layeredBP(fork))
		return float64(unc) + p.W.Read*float64(rd) + p.Write*float64(wr)
	}
	ref := Layered(w, p, DefaultLayered())
	t.Logf("reference run: %d forks, %.1fM ITE", ref.Compactions, ref.ITE/1e6)
	var extra30, extra30Cold float64
	for _, th := range []int{20_000, 30_000, 45_000} {
		asSimulated := bill("", th, false)
		shared, alone := bill("fork:", th, true), bill("fork:", th, false)
		t.Logf("thread %2dk: fork as billed by the sim = %6.0f ITE; tool_choice regime, shared pins warm from another fork = %6.0f (+%.0f); nothing shared = %6.0f (+%.0f)",
			th/1000, asSimulated, shared, shared-asSimulated, alone, alone-asSimulated)
		if th == 30_000 {
			extra30, extra30Cold = shared-asSimulated, alone-asSimulated
		}
	}
	base := Naive(w, p, NaiveOptions{})
	for _, c := range []struct {
		name  string
		extra float64
	}{{"favourable (shared pins kept warm by other agents' forks)", extra30}, {"upper bound (nothing shared)", extra30Cold}} {
		shift := float64(ref.Compactions) * c.extra
		t.Logf("%s: +%.1fM ITE over %d forks = %+.0f%% on the layered bill; headline vs plain %+.0f%% -> about %+.0f%%",
			c.name, shift/1e6, ref.Compactions, shift/ref.ITE*100, (ref.ITE/base.ITE-1)*100, ((ref.ITE+shift)/base.ITE-1)*100)
	}
	cxBug(t, extra30 > 25_000 && float64(ref.Compactions)*extra30 > 0.15*ref.ITE,
		"a tool_choice-changing fork costs at least ~%.0f ITE more than the simulator bills (thread 30k); over %d forks that is >= %.0f%% of the layered total", extra30, ref.Compactions, float64(ref.Compactions)*extra30/ref.ITE*100)
}
