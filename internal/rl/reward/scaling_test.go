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
// that finish within the noise floor pass: there is nothing to measure. A
// failure is measured again, up to five times, before it counts: on a busy machine a
// sample says more about the neighbours than about the code (three suites at once
// gave ratios of nine and more, twice in a row, for code that is linear), while an
// algorithm that really is quadratic gives sixteen every time.
func requireLinear(t *testing.T, name string, n int, fn func(n int)) {
	t.Helper()
	reps := 3
	if underRace {
		n, reps = max(n/4, 1), 2
	}
	var small, large time.Duration
	for attempt := 0; attempt < 5; attempt++ {
		small = bestOf(reps, func() { fn(n) })
		large = bestOf(reps, func() { fn(4 * n) })
		t.Logf("%s: n=%d %v, 4n %v", name, n, small, large)
		if large <= 100*time.Millisecond || large <= 8*small {
			break
		}
	}
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

// bigEpisode builds a long swarm run: a manager that spawns the workers, workers
// that run tests and write files, a compaction (a compactor fork and a rebase)
// every 40 steps, mail between neighbours. Inline prompts on the compactor steps
// and the steps after them give the fidelity probes something to read.
func bigEpisode(agents, steps int) *rl.Episode {
	var as []rl.Agent
	var edges []rl.Edge
	for a := 0; a < agents; a++ {
		role := "worker"
		if a == 0 {
			role = "manager"
		}
		id := fmt.Sprintf("ag%d", a)
		var sts []rl.Step
		seg, prompt, turn := 0, 4000, int64(1)
		for s := 0; s < steps; s++ {
			st := mkStep(fmt.Sprintf("%s.%d", id, s), withPrompt(prompt, "sp"), withOut(80), withAt(time.Duration(a*steps+s)*time.Second),
				withTurnID(turn), withSeg(seg, 0), withUsage(prompt/2, prompt/2, 300, 80),
				withObs(bashObs(fmt.Sprintf("go test ./pkg%d/...", s%17), fmt.Sprintf("--- FAIL: TestCase%d (0.01s)\n    x_test.go:%d: got \"value %d here\"", s, s, s)),
					writeObs(fmt.Sprintf("pkg%d/file%d.go", s%17, s))),
				withText(fmt.Sprintf("step %d", s)))
			turn++
			prompt += 300
			if seg > 0 && s%40 == 0 {
				st.Inline = &core.Prompt{Messages: []core.Message{{Role: core.RoleUser, Blocks: []core.Block{core.Text(fmt.Sprintf("summary keeps pkg%d/file%d.go and TestCase%d", (s-1)%17, s-1, s-1))}}}}
			}
			sts = append(sts, st)
			if s%40 == 39 {
				c := mkStep(fmt.Sprintf("%s.c%d", id, s), withKind(rl.KindCompactor), withPrompt(prompt+500, "sp"), withOut(200), withAt(time.Duration(a*steps+s)*time.Second),
					withSeg(seg, 0), withText(fmt.Sprintf(`{"keep_from":"t%d"}`, turn-8)))
				c.Inline = &core.Prompt{Messages: []core.Message{{Role: core.RoleUser, Blocks: []core.Block{core.Text(strings.Repeat(fmt.Sprintf("context of go test ./pkg%d/... and TestCase%d in pkg%d/file%d.go ", s%17, s, s%17, s), 60))}}}}
				sts = append(sts, c)
				edges = append(edges, rl.Edge{Kind: rl.EdgeCompact, From: c.ID, To: fmt.Sprintf("%s.%d", id, s+1)})
				seg++
				prompt = 4000 + 3000
			}
		}
		as = append(as, mkAgent(id, role, sts...))
		if a > 0 {
			edges = append(edges, rl.Edge{Kind: rl.EdgeSpawn, From: "ag0.0", To: id})
			edges = append(edges, rl.Edge{Kind: rl.EdgeMail, From: fmt.Sprintf("ag%d.1", a-1), To: fmt.Sprintf("%s.2", id)})
		}
	}
	ep := mkEpisode("big/0", as...)
	ep.Edges = edges
	ep.Signals = map[string]float64{rl.SigRequests: float64(agents * steps), rl.SigMailSent: float64(agents), rl.SigCriticalPath: float64(steps)}
	return ep
}

func TestScoringScalesWithEpisodeSize(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Probes = true
	task := &rl.Task{Budget: rl.Budget{ITE: 5e7, Steps: 100000, Requests: 100000}, Verifier: rl.Verifier{Cmd: "go test ./...", Protected: []string{"*_test.go"}}}
	run := func(agents, steps int) {
		ep := bigEpisode(agents, steps)
		if err := Score(ep, task, cfg, nil); err != nil {
			t.Fatal(err)
		}
		if !finite(ep.Reward.Total) {
			t.Fatalf("total %v", ep.Reward.Total)
		}
	}
	t.Run("one long agent", func(t *testing.T) {
		requireLinear(t, "1 agent, n steps", 500, func(n int) { run(1, n) })
	})
	t.Run("many agents", func(t *testing.T) {
		requireLinear(t, "n agents, 40 steps", 50, func(n int) { run(n, 40) })
	})
}

func TestLazySamplingEqualsFilterThenSample(t *testing.T) {
	rng := rand.New(rand.NewSource(5))
	kinds := []string{FactFile, FactCommand, FactTest, FactFailure, FactLiteral}
	keep := func(f Fact) bool { return len(f.Text)%3 != 0 || strings.HasSuffix(f.Text, "7") }
	for trial := 0; trial < 500; trial++ {
		var cands, filtered []Fact
		for i, n := 0, rng.Intn(60); i < n; i++ {
			f := Fact{Kind: kinds[rng.Intn(len(kinds))], Text: fmt.Sprintf("fact-%d-%d", trial%7, rng.Intn(100)), Step: "s1"}
			cands = append(cands, f)
			if keep(f) {
				filtered = append(filtered, f)
			}
		}
		k := 1 + rng.Intn(10)
		want := sampleFacts(filtered, "e", "s", k)
		got := sampleFactsIf(cands, "e", "s", k, keep)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("trial %d: lazy sampling differs from filtering first:\n got %+v\nwant %+v", trial, got, want)
		}
	}
}

func TestFactChecksAreBounded(t *testing.T) {
	// Facts the compactor could not see all fail the test; the sampler must not
	// search the prompt for thousands of them.
	var cands []Fact
	for i := 0; i < 5000; i++ {
		cands = append(cands, Fact{Kind: FactFile, Text: fmt.Sprintf("pkg/file%d.go", i)})
	}
	calls := 0
	got := sampleFactsIf(cands, "e", "s", 8, func(Fact) bool { calls++; return false })
	if len(got) != 0 || calls != maxFactChecks {
		t.Errorf("got %d facts after %d checks, want none after %d", len(got), calls, maxFactChecks)
	}
	// When facts do pass, only about k of them are ever tested.
	calls = 0
	got = sampleFactsIf(cands, "e", "s", 8, func(Fact) bool { calls++; return true })
	if len(got) != 8 || calls > 16 {
		t.Errorf("got %d facts after %d checks", len(got), calls)
	}
}
