package reward

import (
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/rl"
)

// The detectors read text a policy wrote. None of them may panic, loop or go
// quadratic on any input, and none may crash the scorer. The fuzz targets run
// their seed corpus in a normal `go test`; `go test -fuzz FuzzX` explores further.

var fuzzSeeds = []string{
	"",
	"diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n",
	"diff --git a/a b/b\nrename from a\nrename to b\n",
	"diff --git \"a/\\303\\251\" \"b/\\303\\251\"\nBinary files differ\n",
	"@@@ -1,2 -1,2 +1,3 @@@\n  x\n+ y\n",
	"--- a\n+++ b\n@@ -1,99999999999999999999 +1 @@\n-x\n",
	"@@ -0,0 +0,0 @@\n",
	"GIT binary patch\nliteral 3\nabc\n\ndiff --git a/y b/y\n",
	"echo a > /etc/x <<EOF\n$(rm -rf /)\nEOF\n`cat` 'unterminated \"",
	"func TestA(t *testing.T) { t.Skip(\"x\") /* unterminated",
	"@pytest.mark.skip\ndef test_a():\n    '''doc\n",
	"it.skip(`x ${y} \nz`, () => {})",
	"\x00\x01\xff\xfe ‮ ​ Ａ",
	strings.Repeat("(", 200) + strings.Repeat(")", 200),
	strings.Repeat("\"", 999),
	"{\"keep_from\": \"t\\ud800\"}",
	"https://github.com/o/r git@x:y/z github:a/b " + strings.Repeat("%", 50),
	"[exit code 1]\n--- FAIL: TestX (0.00s)\nFAILED tests/a.py::t - E   assert 1 == 2\npanic: boom\n\"quoted value here\"",
}

// within fails the fuzz run when one input takes long: the inputs are small, so
// anything slow is super-linear behaviour that a crash-only fuzzer would miss.
func within(t *testing.T, limit time.Duration, s string, fn func()) {
	t.Helper()
	start := time.Now()
	fn()
	if d := time.Since(start); d > limit {
		t.Fatalf("%d-byte input took %v (limit %v): %q", len(s), d, limit, clipText(s, 200))
	}
}

func FuzzParseDiff(f *testing.F) {
	for _, s := range fuzzSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		within(t, 5*time.Second, s, func() {
			for _, fd := range parseDiff(s) {
				_ = fd.touched()
				_ = fd.path()
				_ = fd.pathVariants()
				_ = fd.addedText()
			}
		})
	})
}

func FuzzLexAndTight(f *testing.F) {
	for _, s := range fuzzSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		within(t, 5*time.Second, s, func() {
			for _, lang := range []string{"go", "py", "js", "rs", "rb", "sh", "php", ""} {
				a := lexCode(s, lang, true)
				b := lexCode(s, lang, false)
				_ = tight(a)
				_ = foldLine(b)
				// Only an unterminated triple quote can add closing quotes: a few bytes.
				if len(a) > len(s)+8 || len(b) > len(s)+8 {
					t.Fatalf("lexCode grew the input: %d -> %d/%d", len(s), len(a), len(b))
				}
			}
		})
	})
}

func FuzzShellAndPaths(f *testing.F) {
	for _, s := range fuzzSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		within(t, 5*time.Second, s, func() {
			_ = parseShell(s)
			_ = shellWriteTargets(s)
			_ = parseVerifier(s)
			_ = commandCore(s)
			_ = refsIn(s)
			_ = scanLiterals(s)
			_, _ = unquoteGit(s)
			_, _ = cleanRel(s)
			_ = cleanAbs(s)
			_ = outsideReason(s, nil)
			_ = outsideReason(s, []string{"/work"})
			g := compileGlobs([]string{s, "*.go", "a/**/b"})
			_, _ = g.match(s)
			_, _ = g.matchAnywhere(s)
			_, _, _ = protectedTarget(g, s, nil)
			_, _ = parsePatch(s)
			_ = normalizeText(s)
			_ = patchPaths(s)
		})
	})
}

func FuzzScoreDiff(f *testing.F) {
	for _, s := range fuzzSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		start := time.Now()
		defer func() {
			if d := time.Since(start); d > 10*time.Second {
				t.Errorf("scoring a %d-byte input took %v: %q", len(s), d, clipText(s, 200))
			}
		}()
		ep := mkEpisode("t/0", mkAgent("a", "worker", mkStep("a.1", withPrompt(100, ""), withObs(bashObs(s, s), writeObs(s)))))
		h := core.HashString(s)
		ep.Outcome.Diff = h
		task := &rl.Task{Prompt: s, Verifier: rl.Verifier{Cmd: s, Protected: []string{"*_test.go", s}, Hidden: map[string]string{"h_test.go": "text:" + s}}, Repo: rl.RepoSpec{URL: s}}
		err := Score(ep, task, DefaultConfig(), DiffMap{h: s})
		if len(s) > maxPatternBytes {
			// An over-long protected pattern is refused, not silently dropped.
			if err == nil || !strings.Contains(err.Error(), "task.verifier.protected[1]") {
				t.Fatalf("over-long protected pattern (%d bytes) was not refused: %v", len(s), err)
			}
			return
		}
		if err != nil {
			t.Fatalf("Score failed on fuzz input: %v", err)
		}
		if !finite(ep.Reward.Total) {
			t.Fatalf("non-finite total %v", ep.Reward.Total)
		}
	})
}

// TestRandomInputsNeverPanic is the deterministic cousin of the fuzz targets: a
// fixed-seed pass over random and structured garbage, so plain `go test` covers
// the same ground on every run.
func TestRandomInputsNeverPanic(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	alphabet := []string{"diff --git ", "a/", "b/", " ", "\n", "\r\n", "@@ ", "-1,2 +1,2", "+", "-", "\\", "\"", "'", "`", "(", ")", "{", "}",
		"t.Skip()", "it.skip(", "def test_", "func Test", "#", "//", "/*", "*/", "&&", "||", ";", "|", ">", "<<EOF", "\x00", "é", "​", "%2F", "../", "/etc/", "~/", "$HOME", "github.com/o/r", "https://", "0x", "12345"}
	gen := func() string {
		var b strings.Builder
		for i, n := 0, rng.Intn(60); i < n; i++ {
			if rng.Intn(4) == 0 {
				b.WriteByte(byte(rng.Intn(256)))
			} else {
				b.WriteString(alphabet[rng.Intn(len(alphabet))])
			}
		}
		return b.String()
	}
	start := time.Now()
	for i := 0; i < 4000; i++ {
		s := gen()
		for _, fd := range parseDiff(s) {
			_ = fd.pathVariants()
		}
		for _, lang := range []string{"go", "py", "js"} {
			_ = tight(lexCode(s, lang, i%2 == 0))
		}
		_ = parseShell(s)
		_ = shellWriteTargets(s)
		_ = parseVerifier(s)
		_ = refsIn(s)
		_ = scanLiterals(s)
		g := compileGlobs([]string{s})
		_, _ = g.match(gen())
		_, _ = parsePatch(s)

		ep := mkEpisode("t/0", mkAgent("a", "worker", mkStep("a.1", withPrompt(100, ""), withObs(bashObs(s, s)))))
		h := core.HashString(s)
		ep.Outcome.Diff = h
		task := &rl.Task{Prompt: gen(), Verifier: rl.Verifier{Cmd: gen(), Protected: []string{gen(), "*_test.go"}, Hidden: map[string]string{"h": "text:" + gen()}}}
		if err := Score(ep, task, DefaultConfig(), DiffMap{h: s}); err != nil {
			t.Fatalf("Score(%q): %v", s, err)
		}
	}
	if d := time.Since(start); d > 60*time.Second {
		t.Errorf("4000 random inputs took %v", d)
	}
}

func TestRandomEpisodesNeverBreakScoring(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	roles := []string{"worker", "manager", "compactor", "mailman", "reviewer", "backend", ""}
	kinds := []string{rl.KindMain, rl.KindCompactor, rl.KindMailman, rl.KindRecon, ""}
	for trial := 0; trial < 300; trial++ {
		var agents []rl.Agent
		for a := 0; a < rng.Intn(5); a++ {
			var steps []rl.Step
			for s := 0; s < rng.Intn(12); s++ {
				st := mkStep("s"+string(rune('a'+a))+string(rune('a'+s)),
					withKind(kinds[rng.Intn(len(kinds))]), withSeg(rng.Intn(3), rng.Intn(2)),
					withPrompt(rng.Intn(20000)-1000, []string{"", "g", "h"}[rng.Intn(3)]), withOut(rng.Intn(500)-10),
					withText([]string{"", "{}", `{"keep_from":"t3"}`, "junk"}[rng.Intn(4)]))
				if rng.Intn(3) == 0 {
					st.At = t0.Add(time.Duration(rng.Intn(3600)) * time.Second)
				}
				st.Usage.CacheReadTokens = rng.Intn(5000) - 100
				st.Observations = []rl.Observation{bashObs("go test ./...", "FAIL x"), writeObs("pkg/a.go")}
				steps = append(steps, st)
			}
			agents = append(agents, mkAgent("ag"+string(rune('a'+a)), roles[rng.Intn(len(roles))], steps...))
		}
		ep := mkEpisode("t/0", agents...)
		for i := 0; i < rng.Intn(4); i++ {
			if len(agents) > 1 {
				ep.Edges = append(ep.Edges, rl.Edge{Kind: []string{rl.EdgeSpawn, rl.EdgeMail, rl.EdgeCompact}[rng.Intn(3)],
					From: agents[rng.Intn(len(agents))].ID, To: agents[rng.Intn(len(agents))].ID})
			}
		}
		if rng.Intn(3) == 0 {
			ep.Signals = map[string]float64{rl.SigRequests: float64(rng.Intn(50)), rl.SigCompactRejects: float64(rng.Intn(5)), rl.SigMailSent: float64(rng.Intn(9)), rl.SigCriticalPath: float64(rng.Intn(9))}
		}
		task := &rl.Task{Budget: rl.Budget{ITE: float64(rng.Intn(3) * 100000), Steps: rng.Intn(3) * 30, Requests: rng.Intn(3) * 40}}
		if err := Score(ep, task, DefaultConfig(), nil); err != nil {
			t.Fatalf("trial %d: %v", trial, err)
		}
		if !finite(ep.Reward.Total) {
			t.Fatalf("trial %d: total %v", trial, ep.Reward.Total)
		}
		for _, a := range ep.Agents {
			if !finite(a.Reward.Total) {
				t.Fatalf("trial %d agent %s: %v", trial, a.ID, a.Reward.Total)
			}
			for _, st := range a.Steps {
				if !finite(st.Reward) {
					t.Fatalf("trial %d step %s: %v", trial, st.ID, st.Reward)
				}
			}
		}
		// A second pass is identical.
		before := epJSON(t, ep)
		if err := Score(ep, task, DefaultConfig(), nil); err != nil {
			t.Fatal(err)
		}
		if epJSON(t, ep) != before {
			t.Fatalf("trial %d: not idempotent", trial)
		}
	}
}
