package core_test

import (
	"math"
	"math/rand"
	"strings"
	"sync"
	"testing"

	"github.com/reee344/sleipnir/internal/core"
)

var _ core.Estimator = (*core.BytesEstimator)(nil)

func TestBytesEstimatorTokens(t *testing.T) {
	bytesOf := func(n int) string { return strings.Repeat("a", n) }
	four := func() *core.BytesEstimator { return core.NewBytesEstimator().WithRatio(4) }
	cases := []struct {
		name string
		e    *core.BytesEstimator
		in   string
		want int
	}{
		{"ratio 4: empty", four(), "", 0},
		{"ratio 4: one byte", four(), bytesOf(1), 1},
		{"ratio 4: a full token", four(), bytesOf(4), 1},
		{"ratio 4: one over rounds up", four(), bytesOf(5), 2},
		{"ratio 4: two full tokens", four(), bytesOf(8), 2},
		{"ratio 4: 1000 bytes", four(), bytesOf(1000), 250},
		{"ratio 4: 1001 bytes", four(), bytesOf(1001), 251},
		{"ratio 4: counts bytes, not characters (3 characters, 9 bytes)", four(), "日本語", 3},
		{"ratio 4: one emoji is 4 bytes", four(), "😀", 1},
		{"ratio 4: a NUL byte is a byte", four(), "\x00", 1},
		{"default: empty", core.NewBytesEstimator(), "", 0},
		{"default: one byte", core.NewBytesEstimator(), bytesOf(1), 1},
		{"default: 3 bytes", core.NewBytesEstimator(), bytesOf(3), 1},
		{"default: 4 bytes is over one token of 3.6", core.NewBytesEstimator(), bytesOf(4), 2},
		{"default: 7 bytes", core.NewBytesEstimator(), bytesOf(7), 2},
		{"default: 8 bytes", core.NewBytesEstimator(), bytesOf(8), 3},
		{"default: 18 bytes is exactly five tokens", core.NewBytesEstimator(), bytesOf(18), 5},
		{"default: 36 bytes is exactly ten tokens", core.NewBytesEstimator(), bytesOf(36), 10},
		{"default: 100 bytes", core.NewBytesEstimator(), bytesOf(100), 28},
		{"default: 1000 bytes", core.NewBytesEstimator(), bytesOf(1000), 278},
	}
	for _, c := range cases {
		if got := c.e.Tokens(c.in); got != c.want {
			t.Errorf("%s: Tokens = %d, want %d", c.name, got, c.want)
		}
	}
	if r := core.NewBytesEstimator().Ratio(); r != 3.6 {
		t.Errorf("a new estimator starts at 3.6 bytes per token, got %v", r)
	}
}

// TestBytesEstimatorTokensGrowByAtMostOnePerByte: the estimate never goes down as text
// grows and never jumps by more than one token per byte, whatever the ratio.
func TestBytesEstimatorTokensGrowByAtMostOnePerByte(t *testing.T) {
	for _, ratio := range []float64{1, 1.5, 2.75, 3.6, 4, 8} {
		e := core.NewBytesEstimator().WithRatio(ratio)
		prev := 0
		for n := 0; n <= 2500; n++ {
			got := e.Tokens(strings.Repeat("x", n))
			if got < prev || got > prev+1 {
				t.Fatalf("ratio %v: %d bytes -> %d tokens after %d tokens for %d bytes", ratio, n, got, prev, n-1)
			}
			if want := int(math.Ceil(float64(n) / ratio)); got != want {
				t.Fatalf("ratio %v: %d bytes -> %d tokens, want %d", ratio, n, got, want)
			}
			prev = got
		}
	}
}

func TestBytesEstimatorWithRatio(t *testing.T) {
	cases := []struct {
		name string
		in   float64
		want float64
	}{
		{"accepted", 4, 4},
		{"just above the floor", 0.51, 0.51},
		{"the floor itself is refused", 0.5, 3.6},
		{"zero is refused", 0, 3.6},
		{"negative is refused", -2, 3.6},
		{"NaN is refused", math.NaN(), 3.6},
	}
	for _, c := range cases {
		e := core.NewBytesEstimator()
		if got := e.WithRatio(c.in); got != e {
			t.Errorf("%s: WithRatio must return its receiver so calls chain", c.name)
		}
		if got := e.Ratio(); got != c.want {
			t.Errorf("%s: ratio = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestBytesEstimatorObserve(t *testing.T) {
	const tol = 1e-12
	type obs struct{ bytes, tokens int }

	t.Run("samples that would skew the ratio are ignored", func(t *testing.T) {
		for _, c := range []struct {
			name string
			o    obs
		}{
			{"under 2048 bytes (dominated by per-message overhead)", obs{2047, 500}},
			{"no tokens", obs{4096, 0}},
			{"negative tokens", obs{4096, -5}},
			{"under one byte per token", obs{4096, 4097}},
			{"over eight bytes per token", obs{8193, 1024}},
			{"far over", obs{1 << 20, 1}},
			{"negative bytes", obs{-4096, 1000}},
		} {
			e := core.NewBytesEstimator()
			e.Observe(c.o.bytes, c.o.tokens)
			if e.Ratio() != 3.6 {
				t.Errorf("%s: the ratio moved to %v", c.name, e.Ratio())
			}
		}
	})
	t.Run("the bounds themselves are accepted", func(t *testing.T) {
		for _, c := range []struct {
			o    obs
			want float64
		}{
			{obs{2048, 1024}, 2.0}, // the smallest accepted sample size
			{obs{4096, 4096}, 1.0}, // one byte per token
			{obs{8192, 1024}, 8.0}, // eight bytes per token
		} {
			e := core.NewBytesEstimator()
			e.Observe(c.o.bytes, c.o.tokens)
			if e.Ratio() != c.want {
				t.Errorf("Observe(%d, %d): ratio = %v, want %v", c.o.bytes, c.o.tokens, e.Ratio(), c.want)
			}
		}
	})
	t.Run("the first sample sets the ratio, later ones move it a fifth of the way", func(t *testing.T) {
		e := core.NewBytesEstimator()
		steps := []struct {
			o    obs
			want float64
		}{
			{obs{4000, 1000}, 4.0},   // first: taken as it is
			{obs{6000, 1000}, 4.4},   // 0.8*4.0 + 0.2*6.0
			{obs{3000, 1000}, 4.12},  // 0.8*4.4 + 0.2*3.0
			{obs{100, 10}, 4.12},     // ignored: too small
			{obs{4120, 1000}, 4.12},  // 0.8*4.12 + 0.2*4.12
			{obs{8000, 1000}, 4.896}, // 0.8*4.12 + 0.2*8.0
		}
		for i, s := range steps {
			e.Observe(s.o.bytes, s.o.tokens)
			if math.Abs(e.Ratio()-s.want) > tol {
				t.Fatalf("step %d (Observe(%d, %d)): ratio = %v, want %v", i, s.o.bytes, s.o.tokens, e.Ratio(), s.want)
			}
		}
		// The estimate follows the ratio: 4.896 bytes per token.
		if got, want := e.Tokens(strings.Repeat("x", 4896)), 1000; got != want && got != want+1 {
			t.Errorf("Tokens(4896 bytes) = %d, want %d (or one more for float rounding)", got, want)
		}
	})
	t.Run("a steady reading wins in the end", func(t *testing.T) {
		e := core.NewBytesEstimator().WithRatio(6)
		e.Observe(8000, 1000) // first accepted sample replaces the starting ratio outright
		for i := 0; i < 200; i++ {
			e.Observe(4000, 1000)
		}
		if math.Abs(e.Ratio()-4.0) > 1e-6 {
			t.Fatalf("ratio = %v after 200 samples of 4.0 bytes per token", e.Ratio())
		}
	})
	t.Run("whatever is observed, the ratio stays between the bounds", func(t *testing.T) {
		rng := rand.New(rand.NewSource(7))
		e := core.NewBytesEstimator()
		for i := 0; i < 5000; i++ {
			e.Observe(rng.Intn(20000)-1000, rng.Intn(6000)-100)
			if r := e.Ratio(); r < 1.0 || r > 8.0 {
				t.Fatalf("after %d observations the ratio is %v", i+1, r)
			}
		}
	})
	t.Run("the same observations give the same estimator", func(t *testing.T) {
		a, b := core.NewBytesEstimator(), core.NewBytesEstimator()
		rng := rand.New(rand.NewSource(11))
		for i := 0; i < 500; i++ {
			by, tok := 2048+rng.Intn(30000), 300+rng.Intn(9000)
			a.Observe(by, tok)
			b.Observe(by, tok)
		}
		if a.Ratio() != b.Ratio() {
			t.Fatalf("ratios differ: %v %v", a.Ratio(), b.Ratio())
		}
		s := strings.Repeat("deterministic ", 500)
		if a.Tokens(s) != b.Tokens(s) {
			t.Fatal("estimates differ")
		}
	})
}

// TestBytesEstimatorIsSafeForConcurrentUse: one estimator is shared by every agent of a
// swarm. Under the race detector this finds an unguarded field; the assertions hold
// whatever the interleaving.
func TestBytesEstimatorIsSafeForConcurrentUse(t *testing.T) {
	e := core.NewBytesEstimator()
	text := strings.Repeat("x", 1000)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(g)))
			for i := 0; i < 500; i++ {
				switch rng.Intn(3) {
				case 0:
					e.Observe(2048+rng.Intn(10000), 400+rng.Intn(2000))
				case 1:
					if n := e.Tokens(text); n < 125 || n > 1000 { // between 8 and 1 bytes per token
						t.Errorf("Tokens = %d", n)
						return
					}
				default:
					if r := e.Ratio(); r < 1.0 || r > 8.0 {
						t.Errorf("Ratio = %v", r)
						return
					}
				}
			}
		}(g)
	}
	wg.Wait()
}
