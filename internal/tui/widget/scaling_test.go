package widget

import (
	"runtime"
	"runtime/debug"
	"testing"
	"time"
)

// Growth checks. A widget's work is linear in its input (or bounded by a cap), and the input that breaks that is hostile: a scan
// for a closer from every opener, a prefix parsed again at every nesting level, a line built by concatenation. requireLinear pins
// it without an absolute time limit, which a slow machine, the race detector or three suites at once would make flaky
// (docs/BUILDING.md): it compares what fn costs for n units with what it costs for growthScale times as many.
//
// Three things make that comparison steady enough to fail only for code that is not linear. They were found by running these
// tests as eight processes at once on four cores, where the first version (the wall clock, four times the size, a limit of
// eight) failed in 20 of 32 runs of the three growth tests, and the version below in none of 56 (with and without the race
// detector):
//
//   - The cost is the CPU time of the process, where the operating system counts it (see costStart), and not the time on the
//     wall. A process that has to wait for a core still waits, and a run that is long enough to be descheduled waits more often
//     than a short one, so the wall-clock ratio of linear code went up to 30 at four times the size (quadratic code gives 16)
//     while its CPU-time ratio stayed where it is on an idle machine.
//   - The garbage collector is off while fn runs. A run that allocates enough to start a collection pays for it and a smaller
//     run does not, so linear code costs six to twelve times as much for four times the input with the collector on, and four
//     to eight times with it off.
//   - The larger input is growthScale times the smaller, not four times, and the limit is between what linear code costs (8 to
//     18 times as much: the bigger run no longer fits in the CPU caches) and what quadratic code costs (40 to 64 times). At four
//     times the size the two are 4 to 8 and 16, too close to tell apart on a machine that is not quiet.
const (
	growthScale = 8
	growthLimit = 32

	// A run that costs less than growthFloor is too short to say anything about (timer granularity, fixed costs), and a clock
	// that cannot resolve growthResolution cannot tell the smaller run's cost from zero: it counts as this much.
	growthFloor      = 20 * time.Millisecond
	growthResolution = 500 * time.Microsecond
)

// costMark is where a measurement began.
type costMark struct {
	wall  time.Time
	cpu   time.Duration
	cpuOK bool
}

// costStart marks the start of a measurement. What is measured is the CPU time of the process where the system reports it
// (Linux, macOS and the other unix systems: processCPUTime) and the wall clock where it does not. Nothing else is running in
// the process while fn runs (these tests are not parallel, and with the collector off no background worker is), so the CPU time
// of the process is the CPU time of fn.
func costStart() costMark {
	cpu, ok := processCPUTime()
	return costMark{wall: time.Now(), cpu: cpu, cpuOK: ok}
}

// elapsed is the cost since the mark.
func (m costMark) elapsed() time.Duration {
	if m.cpuOK {
		if cpu, ok := processCPUTime(); ok {
			return cpu - m.cpu
		}
	}
	return time.Since(m.wall)
}

// measureCost runs fn reps times and returns the least cost of one run (see costStart). The garbage collector runs before every
// run and is off during it. The least of several runs is the best estimate of what the code costs: noise only ever adds.
func measureCost(reps int, fn func()) time.Duration {
	old := debug.SetGCPercent(-1)
	defer debug.SetGCPercent(old)
	best := time.Duration(1 << 62)
	for i := 0; i < reps; i++ {
		runtime.GC()
		start := costStart()
		fn()
		best = min(best, start.elapsed())
	}
	return best
}

// requireLinear fails when fn(growthScale*n) costs far more than growthScale times fn(n), and runs fn on a quarter of n under
// the race detector, which makes everything several times slower.
func requireLinear(t *testing.T, name string, n int, fn func(n int)) {
	t.Helper()
	requireLinearFrom(t, name, n, max(n/4, 1), fn)
}

// requireLinearFrom is requireLinear for a workload whose shape changes with its size (the diff search has a cap, and "a change
// at every line" is a different case below it than above it): raceN is the n to use under the race detector, which must be in
// the same regime as n. A verdict that is not yet a pass is measured again, up to four times, and the verdict is about the
// least cost seen at each size, which can only come down with every measurement.
func requireLinearFrom(t *testing.T, name string, n, raceN int, fn func(n int)) {
	t.Helper()
	reps := 3
	if widgetUnderRace() {
		n, reps = raceN, 2
	}
	small, large := time.Duration(1<<62), time.Duration(1<<62)
	tooBig := func() bool { return large > growthFloor && large > growthLimit*max(small, growthResolution) }
	for attempt := 0; attempt < 4 && (attempt == 0 || tooBig()); attempt++ {
		small = min(small, measureCost(reps, func() { fn(n) }))
		large = min(large, measureCost(reps, func() { fn(growthScale * n) }))
	}
	t.Logf("%s: n=%d costs %v, %dn costs %v", name, n, small, growthScale, large)
	if large > 2*time.Minute { // a hang guard, not a timing: what a complexity bomb does to a test that has no other limit
		t.Errorf("%s: %d units cost %v", name, growthScale*n, large)
	}
	if tooBig() {
		t.Errorf("%s: super-linear growth: %v for n=%d, %v for %dn", name, small, n, large, growthScale)
	}
}
