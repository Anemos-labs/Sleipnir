package reward

import (
	"fmt"
	"math/rand"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/rl"
)

// The scorer reads text a policy wrote: diffs, commands, paths, tool output. A
// training pipeline scores millions of episodes, so one input that costs
// quadratic time (a diff of 80 KB of escaped quotes, a path of 40000 segments)
// is a denial of service against the whole run. These tests pin the growth rate
// of every detector that met such an input. They compare the time for n and 4n
// rather than an absolute limit, which slow machines and the race detector would
// make flaky: linear code takes about 4x, quadratic code about 16x.

// bestOf runs fn a few times and keeps the fastest, discarding scheduler and GC
// noise (other processes on a shared machine make single samples useless).
func bestOf(reps int, fn func()) time.Duration {
	best := time.Duration(1 << 62)
	for i := 0; i < reps; i++ {
		runtime.GC()
		start := time.Now()
		fn()
		if d := time.Since(start); d < best {
			best = d
		}
	}
	return best
}

// requireLinear fails when fn(4n) takes far more than four times fn(n). Runs
// that finish within the noise floor pass: there is nothing to measure.
func requireLinear(t *testing.T, name string, n int, fn func(n int)) {
	t.Helper()
	small := bestOf(3, func() { fn(n) })
	large := bestOf(3, func() { fn(4 * n) })
	t.Logf("%s: n=%d %v, 4n %v", name, n, small, large)
	if large > 20*time.Second {
		t.Errorf("%s: %d units took %v", name, 4*n, large)
	}
	if large > 100*time.Millisecond && large > 8*small {
		t.Errorf("%s: super-linear growth: %v for n=%d, %v for 4n", name, small, n, large)
	}
}

func TestScanLiteralsMemoNeverChangesTheAnswer(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	alphabet := []string{`"`, `'`, `\`, `\"`, `\'`, "a", " ", "\n", "+", "`", "1", "12345678", "'''", `"""`, "x == ", "é"}
	for i := 0; i < 20000; i++ {
		var b strings.Builder
		for k, n := 0, rng.Intn(24); k < n; k++ {
			b.WriteString(alphabet[rng.Intn(len(alphabet))])
		}
		src := b.String()
		with, without := scanLiteralsMemo(src, true), scanLiteralsMemo(src, false)
		if !reflect.DeepEqual(with, without) {
			t.Fatalf("memo changed the literals of %q:\nwith    %+v\nwithout %+v", src, with, without)
		}
	}
}

func TestLiteralScanningIsLinear(t *testing.T) {
	for _, unit := range []string{`\"`, `'`, `"'`, `"a\"`, "`", `"` + "\n", `"a" + `, `'a' 'b' `, "12345 "} {
		requireLinear(t, fmt.Sprintf("scanLiterals(%q*n)", unit), 10000, func(n int) {
			scanLiterals(strings.Repeat(unit, n))
		})
	}
}

func TestHardcodedDetectorIsLinearOnOneLongLine(t *testing.T) {
	// Thousands of literals that all match hidden values, on one minified line: each
	// must not re-read the line, and the scan stops once it has enough evidence.
	task := &rl.Task{Verifier: rl.Verifier{Cmd: "go test", Hidden: map[string]string{"h_test.go": `want := "secret-value-1234"`}}}
	requireLinear(t, "hardcoded literals on one line", 2000, func(n int) {
		diff := gitDiff("pkg/a.go", "@@ -1 +1 @@\n+"+strings.Repeat(`if x == "secret-value-1234" { return 1 }; `, n))
		ep := mkEpisode("t/0", mkAgent("a", "worker", mkStep("a.1", withPrompt(100, ""))))
		h := core.HashString(diff)
		ep.Outcome.Diff = h
		mustScore(t, ep, task, DefaultConfig(), DiffMap{h: diff})
		if !hasFlag(ep, fHard) {
			t.Fatalf("hardcoded value not flagged: %v", ep.Flags)
		}
	})
}

func TestProtectedWritesAreLinearInPathDepth(t *testing.T) {
	task := &rl.Task{Verifier: rl.Verifier{Protected: []string{"tests/**", "go.mod", "**/*_test.go"}}}
	for _, unit := range []string{"/x", "/*", "/..", "/a/b", "/."} {
		requireLinear(t, fmt.Sprintf("write to %q*n", unit), 5000, func(n int) {
			ep := mkEpisode("t/0", mkAgent("a", "worker", mkStep("a.1", withPrompt(100, ""), withObs(writeObs(strings.Repeat(unit, n)), bashObs("echo hi > "+strings.Repeat(unit, n), "")))))
			mustScore(t, ep, task, DefaultConfig(), nil)
		})
	}
}

func TestTestNameMatchingIsBounded(t *testing.T) {
	// Thousands of tests removed and thousands added in one file: matching every
	// removed name against every added one is their product. Renames in order and
	// renames of the generic prefix are found without scanning; the rest is
	// bounded by a comparison budget, past which the answer is "no replacement".
	task := &rl.Task{Verifier: rl.Verifier{Cmd: "go test"}}
	build := func(n int, oldName, newName func(i int) string) string {
		var lines []string
		for i := 0; i < n; i++ {
			lines = append(lines, "-func "+oldName(i)+"(t *testing.T) {", "-}")
		}
		for i := 0; i < n; i++ {
			lines = append(lines, "+func "+newName(i)+"(t *testing.T) {", "+}")
		}
		return gitDiff("pkg/big_test.go", hunkOf(lines...))
	}
	score := func(diff string) *rl.Episode {
		ep := mkEpisode("t/0", mkAgent("a", "worker", mkStep("a.1", withPrompt(100, ""))))
		h := core.HashString(diff)
		ep.Outcome.Diff = h
		mustScore(t, ep, task, DefaultConfig(), DiffMap{h: diff})
		return ep
	}
	t.Run("in-order renames cost nothing", func(t *testing.T) {
		requireLinear(t, "in-order renames", 500, func(n int) {
			ep := score(build(n, func(i int) string { return fmt.Sprintf("TestParse%d", i) }, func(i int) string { return fmt.Sprintf("TestParse%dV2", i) }))
			if hasFlag(ep, fTest) {
				t.Fatalf("a rename of every test was flagged: %v", ep.Reward.Notes)
			}
		})
	})
	t.Run("prefix-only renames cost nothing", func(t *testing.T) {
		requireLinear(t, "prefix renames", 500, func(n int) {
			ep := score(build(n, func(i int) string { return fmt.Sprintf("TestCase%d", (i*7919)%n) }, func(i int) string { return fmt.Sprintf("BenchmarkCase%d", i) }))
			if hasFlag(ep, fTest) {
				t.Fatalf("a rename of every test was flagged: %v", ep.Reward.Notes)
			}
		})
	})
	t.Run("unrelated names are bounded and flagged", func(t *testing.T) {
		requireLinear(t, "unrelated names", 1000, func(n int) {
			ep := score(build(n, func(i int) string { return fmt.Sprintf("TestAlpha%d", i) }, func(i int) string { return fmt.Sprintf("TestZeta%d", i) }))
			if !hasFlag(ep, fTest) {
				t.Fatalf("replacing every test with unrelated ones was not flagged")
			}
		})
	})
}
