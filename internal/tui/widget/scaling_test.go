package widget

import (
	"runtime"
	"runtime/debug"
	"testing"
	"time"
)

// Growth checks compare process work for n and 8n input units. Process CPU time
// on Unix and CPU cycles on Windows exclude time waiting for a core. Other
// systems fall back to monotonic elapsed time. The collector is disabled during
// each measurement so a collection triggered by the larger input cannot skew
// the ratio. Tests using this helper must not run in parallel.
const (
	growthScale = 8
	growthLimit = 32
	// Very short samples cannot establish excessive growth reliably.
	growthFloor = 20 * time.Millisecond
)

type workSample struct {
	work uint64
	wall time.Duration
}

// measureCost returns the sample with the least process work. Counter failures
// fail the test instead of mixing CPU work and wall time in the same comparison.
func measureCost(t *testing.T, reps int, fn func()) workSample {
	t.Helper()
	old := debug.SetGCPercent(-1)
	defer debug.SetGCPercent(old)
	best := workSample{work: ^uint64(0)}
	for i := 0; i < reps; i++ {
		runtime.GC()
		before, err := processWork()
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		fn()
		wall := time.Since(start)
		after, err := processWork()
		if err != nil {
			t.Fatal(err)
		}
		if after < before {
			t.Fatal("process work counter went backwards")
		}
		if wall > 2*time.Minute {
			t.Fatalf("scaling sample exceeded the hang guard: %s", wall)
		}
		if cost := after - before; cost < best.work {
			best = workSample{work: cost, wall: wall}
		}
	}
	return best
}

// growsTooFast preserves the same 8x-input / 32x-work bound for each clock.
// With a process counter, wall time only excludes tiny samples and does not
// enter the work ratio.
func growsTooFast(small, large workSample) bool {
	return large.wall > growthFloor && float64(large.work) > growthLimit*float64(max(small.work, workResolution))
}

// requireLinear checks input growth, using a smaller workload under race builds.
func requireLinear(t *testing.T, name string, n int, fn func(n int)) {
	t.Helper()
	requireLinearFrom(t, name, n, max(n/4, 1), fn)
}

// requireLinearFrom accepts a separate race workload for algorithms with size
// dependent caps. Suspect ratios receive up to four rounds of measurements.
func requireLinearFrom(t *testing.T, name string, n, raceN int, fn func(n int)) {
	t.Helper()
	reps := 3
	if widgetUnderRace() {
		n, reps = raceN, 2
	}
	small, large := workSample{work: ^uint64(0)}, workSample{work: ^uint64(0)}
	for attempt := 0; attempt < 4 && (attempt == 0 || growsTooFast(small, large)); attempt++ {
		if sample := measureCost(t, reps, func() { fn(n) }); sample.work < small.work {
			small = sample
		}
		if sample := measureCost(t, reps, func() { fn(growthScale * n) }); sample.work < large.work {
			large = sample
		}
	}
	t.Logf("%s: n=%d costs %d %s, %dn costs %d %s", name, n, small.work, workUnit, growthScale, large.work, workUnit)
	if growsTooFast(small, large) {
		t.Errorf("%s: super-linear growth: %d %s for n=%d, %d %s for %dn", name, small.work, workUnit, n, large.work, workUnit, growthScale)
	}
}

// scalingWork keeps a serial arithmetic dependency that cannot be elided.
func scalingWork(n int) {
	x := uint64(1)
	for i := 0; i < n; i++ {
		x = x*1664525 + uint64(i)
	}
	runtime.KeepAlive(x)
}

func TestScalingClockIgnoresSchedulerWait(t *testing.T) {
	if workUnit == "wall ns" {
		t.Skip("this platform has no process work counter")
	}
	const n = 500_000
	small := measureCost(t, 3, func() { scalingWork(n) })
	large := measureCost(t, 3, func() {
		scalingWork(growthScale * n)
		time.Sleep(50 * time.Millisecond) // delay only the larger sample, without CPU work
	})
	if small.work == 0 || growsTooFast(small, large) {
		t.Fatalf("scheduler wait distorted linear work: small=%+v, large=%+v (%s)", small, large, workUnit)
	}
}

func TestScalingClockRejectsQuadraticWork(t *testing.T) {
	const n = 1500
	small := measureCost(t, 3, func() { scalingWork(n * n) })
	large := measureCost(t, 3, func() { scalingWork(growthScale * n * growthScale * n) })
	if !growsTooFast(small, large) {
		t.Fatalf("quadratic work escaped the growth check: small=%+v, large=%+v (%s)", small, large, workUnit)
	}
}
