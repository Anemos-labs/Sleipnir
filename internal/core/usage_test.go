package core_test

import (
	"math"
	"math/rand"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/core"
)

func TestUsageTotals(t *testing.T) {
	cases := []struct {
		name       string
		u          core.Usage
		wantWrite  int
		wantTotal  int
		wantRatio  float64
		ratioExact bool
	}{
		{"zero", core.Usage{}, 0, 0, 0, true},
		{"uncached only", core.Usage{InputTokens: 100}, 0, 100, 0, true},
		{"all read", core.Usage{CacheReadTokens: 100}, 0, 100, 1, true},
		{"writes add up", core.Usage{CacheWrite5mTokens: 30, CacheWrite1hTokens: 12}, 42, 42, 0, true},
		{"half read", core.Usage{InputTokens: 25, CacheReadTokens: 50, CacheWrite5mTokens: 25}, 25, 100, 0.5, true},
		{"output and reasoning are not prompt", core.Usage{InputTokens: 10, CacheReadTokens: 10, OutputTokens: 999, ReasoningTokens: 500}, 0, 20, 0.5, true},
		{"third", core.Usage{InputTokens: 100, CacheReadTokens: 50}, 0, 150, 1.0 / 3.0, false},
	}
	for _, c := range cases {
		if got := c.u.CacheWriteTokens(); got != c.wantWrite {
			t.Errorf("%s: CacheWriteTokens = %d, want %d", c.name, got, c.wantWrite)
		}
		if got := c.u.TotalInput(); got != c.wantTotal {
			t.Errorf("%s: TotalInput = %d, want %d", c.name, got, c.wantTotal)
		}
		got := c.u.HitRatio()
		if (c.ratioExact && got != c.wantRatio) || math.Abs(got-c.wantRatio) > 1e-12 {
			t.Errorf("%s: HitRatio = %v, want %v", c.name, got, c.wantRatio)
		}
	}
}

// randUsage is a usage with every field set to a different non-negative number
// (adapters clamp counters at zero, so a negative one never reaches this package).
func randUsage(rng *rand.Rand) core.Usage {
	n := func() int { return rng.Intn(100000) }
	return core.Usage{InputTokens: n(), CacheReadTokens: n(), CacheWrite5mTokens: n(), CacheWrite1hTokens: n(), OutputTokens: n(), ReasoningTokens: n()}
}

func TestUsageAdd(t *testing.T) {
	// Each field is added to its own kind and to nothing else.
	one := core.Usage{InputTokens: 1, CacheReadTokens: 2, CacheWrite5mTokens: 3, CacheWrite1hTokens: 4, OutputTokens: 5, ReasoningTokens: 6}
	if got, want := one.Add(one), (core.Usage{InputTokens: 2, CacheReadTokens: 4, CacheWrite5mTokens: 6, CacheWrite1hTokens: 8, OutputTokens: 10, ReasoningTokens: 12}); got != want {
		t.Fatalf("Add = %+v, want %+v", got, want)
	}
	if one.Add(core.Usage{}) != one || (core.Usage{}).Add(one) != one {
		t.Fatal("the zero Usage is not the identity of Add")
	}

	rng := rand.New(rand.NewSource(3))
	for i := 0; i < 500; i++ {
		a, b, c := randUsage(rng), randUsage(rng), randUsage(rng)
		if a.Add(b) != b.Add(a) {
			t.Fatalf("Add is not commutative: %+v %+v", a, b)
		}
		if a.Add(b).Add(c) != a.Add(b.Add(c)) {
			t.Fatalf("Add is not associative: %+v %+v %+v", a, b, c)
		}
		sum := a.Add(b)
		if sum.TotalInput() != a.TotalInput()+b.TotalInput() || sum.CacheWriteTokens() != a.CacheWriteTokens()+b.CacheWriteTokens() {
			t.Fatalf("totals do not add up: %+v + %+v = %+v", a, b, sum)
		}
	}
}

func TestUsageHitRatioStaysInUnitInterval(t *testing.T) {
	rng := rand.New(rand.NewSource(5))
	for i := 0; i < 2000; i++ {
		u := randUsage(rng)
		if i%7 == 0 {
			u.InputTokens = 0
		}
		if i%11 == 0 {
			u.CacheWrite5mTokens, u.CacheWrite1hTokens = 0, 0
		}
		r := u.HitRatio()
		if r < 0 || r > 1 || math.IsNaN(r) {
			t.Fatalf("HitRatio(%+v) = %v", u, r)
		}
		if u.CacheReadTokens == 0 && r != 0 {
			t.Fatalf("no reads, ratio %v: %+v", r, u)
		}
	}
	if r := (core.Usage{OutputTokens: 50}).HitRatio(); r != 0 {
		t.Fatalf("a request with no prompt has ratio %v, want 0 (not NaN)", r)
	}
}
