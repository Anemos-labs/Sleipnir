package stats

import (
	"math"
	"testing"
)

func near(a, b, eps float64) bool { return math.Abs(a-b) <= eps }

func TestWilsonKnownValues(t *testing.T) {
	cases := []struct {
		k, n   int
		lo, hi float64
		why    string
	}{
		{7, 10, 0.3968, 0.8922, "the textbook example"},
		{0, 10, 0, 0.2775, "no successes still allow a few percent"},
		{10, 10, 0.7225, 1, "all successes are not certainty"},
		{50, 100, 0.4038, 0.5962, "a fair coin, a hundred flips"},
		{1, 1, 0.2065, 1, "one trial says little"},
	}
	for _, c := range cases {
		lo, hi := Wilson(c.k, c.n, Z95)
		if !near(lo, c.lo, 0.0005) || !near(hi, c.hi, 0.0005) {
			t.Errorf("Wilson(%d, %d) = [%.4f, %.4f], want [%.4f, %.4f] (%s)", c.k, c.n, lo, hi, c.lo, c.hi, c.why)
		}
	}
	if lo, hi := Wilson(0, 0, Z95); lo != 0 || hi != 1 {
		t.Errorf("no trials: [%v, %v], want [0, 1]", lo, hi)
	}
	// Out-of-range counts are clamped, never NaN.
	if lo, hi := Wilson(12, 10, Z95); math.IsNaN(lo) || math.IsNaN(hi) || hi > 1 {
		t.Errorf("clamping: [%v, %v]", lo, hi)
	}
}

func TestWilsonNarrowsWithMoreTrials(t *testing.T) {
	prev := 2.0
	for _, n := range []int{10, 40, 160, 640} {
		lo, hi := Wilson(n/2, n, Z95)
		if w := hi - lo; w >= prev {
			t.Errorf("n=%d: width %.4f did not shrink from %.4f", n, w, prev)
		} else {
			prev = w
		}
		if !(lo < 0.5 && 0.5 < hi) {
			t.Errorf("n=%d: [%v, %v] must contain the observed rate", n, lo, hi)
		}
	}
}

func TestQuantileMeanSD(t *testing.T) {
	xs := []float64{9, 1, 5, 3, 7}
	if got := Quantile(xs, 0.5); got != 5 {
		t.Errorf("median = %v", got)
	}
	if got := Quantile(xs, 0.9); !near(got, 8.2, 1e-12) {
		t.Errorf("p90 = %v, want 8.2", got)
	}
	if Quantile(xs, 0) != 1 || Quantile(xs, 1) != 9 || Quantile(xs, -3) != 1 || Quantile(xs, 7) != 9 {
		t.Error("the ends and out-of-range q")
	}
	if xs[0] != 9 || xs[1] != 1 {
		t.Errorf("Quantile sorted its input: %v", xs)
	}
	if got := Median([]float64{1, 2, 3, 4}); got != 2.5 {
		t.Errorf("even median = %v", got)
	}
	if Mean(xs) != 5 || Sum(xs) != 25 {
		t.Errorf("mean %v sum %v", Mean(xs), Sum(xs))
	}
	if got := SD([]float64{2, 4, 4, 4, 5, 5, 7, 9}); !near(got, 2.13809, 1e-4) {
		t.Errorf("SD = %v, want the sample SD 2.13809", got)
	}
	for name, f := range map[string]func() float64{
		"Mean":     func() float64 { return Mean(nil) },
		"Quantile": func() float64 { return Quantile(nil, 0.5) },
		"SD one":   func() float64 { return SD([]float64{3}) },
		"SD none":  func() float64 { return SD(nil) },
	} {
		if got := f(); got != 0 {
			t.Errorf("%s of nothing = %v, want 0", name, got)
		}
	}
}
