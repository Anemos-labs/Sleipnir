//go:build scratch

package reward

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/rl"
)

var scanUnits = []string{
	"a", " ", "\n", "/", "a/", "../", "./", "\\", "\"", "'", "`", "(", ")", "{", "}", "[", "]", "*", "**", "**/", "?", "$", "$(", "${", "<<", "<<EOF\n", "EOF\n", ">", ">>", "2>&1 ", "|", "||", "&&", ";", "#", "//", "/*", "*/", "'''", "\"\"\"",
	"@@ -1 +1 @@\n", "+", "-", "diff --git a/x b/x\n", "--- a/x\n+++ b/x\n", "rename from a\nrename to b\n", "+t.Skip()\n", "+func TestA(t *testing.T) {\n", "-func TestA(t *testing.T) {\n",
	"https://github.com/o/r ", "git@github.com:o/r ", "github.com/o/r ", "curl -s ", "go test ./... && ", "echo x > f; ", "cat <<'EOF' > f\n", "assert.Equal(t, 1, 1)\n", "expect(a).toBe(1)\n", "def test_x():\n    assert 1\n",
	"x := 0x1234567890\n", "\"quoted string value\" ", "é", "​", "Ａ", "%2F", "\r\n", "\r", "\t", "\x00", "{a,b}", "[ab]", "!", "a=b ", "sh -c '", "bash -c \"", "$(echo ", "`echo ", "\\\n", "t.", "Skip", "if ", "else ",
	"@pytest.mark.skip\n", "it.skip(\"a\", () => {})\n", "+ ", "+\n", " \n", "@@ -1,1 +1,1 @@\n+x\n", "\\ No newline at end of file\n", "Binary files a/x and b/x differ\n", "GIT binary patch\n", "index 1..2 100644\n",
	"new file mode 100644\n", "similarity index 90%\n", "copy from a\ncopy to b\n", "@@@ -1 -1 +1 @@@\n", "\"a\" + ", "'a' + ", "== ", "if x == \"abcdefgh\" {\n", "return \"secret-value-1234\"\n",
	"go test -run TestX ./pkg/... ", "pytest tests/ -k ", "npm test -- ", "make test\n", "FAIL: TestA (0.00s)\n", "[exit code 1]\n", "panic: x\n", "--- FAIL: TestX\n", "FAILED tests/a.py::t - E   assert 1 == 2\n",
	"{\"keep_from\": 3}", "{", "}", "\\\"", "\\u00e9", "\\x41", "0x", "12345 ", "a.b.c.d ", "a::b ", "a-b_c ", "http://", "://", "@", "@@", "@@ ", "..", "...", ".", "~/", "$HOME/", "/etc/", "/tmp/x ",
	"t.Run(\"a\", func(t *testing.T) {\n", "func Test", "def test_", "it(\"", "describe(\"", "assert ", "assert True\n", "assert.True(t, true)\n", "require.True(t, true)\n", "if false {\n", "return\n", "os.Exit(0)\n", "exit 0\n", "|| true\n",
}

func scanFuncs() map[string]func(s string) {
	nosrc := func(s string, lines func(string) []string) []string { return lines(s) }
	_ = nosrc
	prefix := func(op, s string) string {
		var b strings.Builder
		for _, l := range strings.Split(s, "\n") {
			b.WriteString(op)
			b.WriteString(l)
			b.WriteByte('\n')
		}
		return b.String()
	}
	scoreWith := func(task *rl.Task, diff string, obs ...rl.Observation) {
		ep := mkEpisode("t/0", mkAgent("a", "worker", mkStep("a.1", withPrompt(1000, ""), withOut(5), withObs(obs...))))
		src := DiffMap{}
		if diff != "" {
			h := core.HashString(diff)
			src[h] = diff
			ep.Outcome.Diff = h
		}
		if task == nil {
			task = &rl.Task{Verifier: rl.Verifier{Cmd: "go test ./...", Protected: []string{"*_test.go", "tests/**", "go.mod"}}}
		}
		_ = Score(ep, task, DefaultConfig(), src)
	}
	langs := []string{"go", "py", "js", "rs", "rb", "sh", "php", ""}
	m := map[string]func(string){
		"parseDiff": func(s string) {
			for _, fd := range parseDiff(s) {
				_ = fd.touched()
				_ = fd.pathVariants()
				_ = fd.addedText()
			}
		},
		"parseShell":        func(s string) { _ = parseShell(s) },
		"shellWriteTargets": func(s string) { _ = shellWriteTargets(s) },
		"parseVerifier":     func(s string) { _ = parseVerifier(s) },
		"commandCore":       func(s string) { _ = commandCore(s) },
		"splitCommand":      func(s string) { _ = splitCommand(s) },
		"refsIn":            func(s string) { _ = refsIn(s) },
		"scanLiterals":      func(s string) { _ = scanLiterals(s) },
		"unquoteGit":        func(s string) { _, _ = unquoteGit("\"" + s + "\"") },
		"cleanRel":          func(s string) { _, _ = cleanRel(s) },
		"cleanAbs":          func(s string) { _ = cleanAbs("/" + s) },
		"outsideReason": func(s string) {
			_ = outsideReason(s, nil)
			_ = outsideReason("/"+s, []string{"/work"})
		},
		"globCompile": func(s string) { _ = compileGlobs([]string{s}) },
		"globMatch": func(s string) {
			g := compileGlobs([]string{"*_test.go", "tests/**", ".github/**", "a/**/b", "**/x/*.go", "vendor/", "go.{mod,sum}"})
			_, _ = g.match(s)
		},
		"expandBraces":  func(s string) { _ = expandBraces(s, 64) },
		"parsePatch":    func(s string) { _, _ = parsePatch(s) },
		"firstJSON":     func(s string) { _, _ = firstJSONObject(s) },
		"normalizeText": func(s string) { _ = normalizeText(s) },
		"patchPaths":    func(s string) { _ = patchPaths(s) },
		"lexCode": func(s string) {
			for _, l := range langs {
				_ = lexCode(s, l, true)
				_ = lexCode(s, l, false)
			}
		},
		"foldLine": func(s string) { _ = foldLine(s) },
		"tight":    func(s string) { _ = tight(s) },
		"splitCall": func(s string) {
			_, _, _ = splitCall("f("+s, 1)
			_, _, _ = splitCall(s, 0)
		},
		"tautology": func(s string) {
			for _, l := range []string{"go", "py", "js"} {
				_ = tautology(l, s)
			}
		},
		"touchesRef":    func(s string) { _ = touchesRef(s, s); _ = touchesRef("a/b.go", s); _ = touchesRef(s, "a/*.go") },
		"isDirToken":    func(s string) { _ = isDirToken(s); _ = looksLikePath(s) },
		"decodeEscapes": func(s string) { _ = decodeEscapes(s); _ = blankStrings(s); _ = squash(s) },
		"targetNames": func(s string) {
			_ = targetNames(s)
			_ = testNames(kitFor("go"), s, s)
			_ = testNames(kitFor("py"), s, s)
			_ = testNames(kitFor("js"), s, s)
		},
		"countAssertions": func(s string) {
			for _, l := range []string{"go", "py", "js"} {
				_ = countAssertions(kitFor(l), s)
			}
		},
		"analyzeTestChange": func(s string) {
			for _, l := range []string{"go", "py", "js"} {
				for _, body := range []string{prefix("+", s), prefix("-", s), prefix("-", s) + prefix("+", s), prefix(" ", s) + prefix("+", s)} {
					d := gitDiff("pkg/a_test.go", "@@ -1,1 +1,1 @@\n"+body)
					for _, f := range parseDiff(d) {
						_ = analyzeTestChange(f, l, 1)
					}
				}
			}
		},
		"score/diffbody": func(s string) { scoreWith(nil, s) },
		"score/gitdiff-added-test": func(s string) {
			scoreWith(nil, gitDiff("pkg/a_test.go", "@@ -1,1 +1,1 @@\n"+prefix("+", s)))
		},
		"score/gitdiff-removed-added-test": func(s string) {
			scoreWith(nil, gitDiff("pkg/a_test.go", "@@ -1,1 +1,1 @@\n"+prefix("-", s)+prefix("+", s)))
		},
		"score/gitdiff-py-js": func(s string) {
			scoreWith(nil, gitDiff("tests/test_a.py", "@@ -1,1 +1,1 @@\n"+prefix("-", s)+prefix("+", s)))
			scoreWith(nil, gitDiff("src/a.test.js", "@@ -1,1 +1,1 @@\n"+prefix("-", s)+prefix("+", s)))
		},
		"score/newtestfile": func(s string) { scoreWith(nil, newFileDiff("pkg/new_test.go", strings.Split(s, "\n")...)) },
		"score/deletedtest": func(s string) { scoreWith(nil, deletedFileDiff("pkg/old_test.go", strings.Split(s, "\n")...)) },
		"score/gitdiff-src": func(s string) {
			scoreWith(nil, gitDiff("pkg/a.go", "@@ -1,1 +1,1 @@\n"+prefix("+", s)))
			scoreWith(nil, gitDiff("Makefile", "@@ -1,1 +1,1 @@\n"+prefix("-", s)+prefix("+", s)))
			scoreWith(nil, gitDiff("package.json", "@@ -1,1 +1,1 @@\n"+prefix("-", s)+prefix("+", s)))
		},
		"score/cmd":    func(s string) { scoreWith(nil, "", bashObs(s, "")) },
		"score/output": func(s string) { scoreWith(nil, "", bashObs("go test ./...", s)) },
		"score/write":  func(s string) { scoreWith(nil, "", writeObs(s)) },
		"score/web": func(s string) {
			scoreWith(nil, "", obs("web_fetch", map[string]any{"url": s}, s))
		},
		"score/verifiercmd": func(s string) {
			scoreWith(&rl.Task{Verifier: rl.Verifier{Cmd: s, Protected: []string{"*_test.go"}}}, gitDiff("pkg/a.go", "@@ -1 +1 @@\n+x"))
		},
		"score/hidden": func(s string) {
			scoreWith(&rl.Task{Prompt: s, Verifier: rl.Verifier{Cmd: "go test", Hidden: map[string]string{"h_test.go": "text:" + s}}}, gitDiff("pkg/a.go", "@@ -1 +1 @@\n"+prefix("+", s)))
		},
		"score/repo": func(s string) {
			scoreWith(&rl.Task{Repo: rl.RepoSpec{URL: s}}, "", bashObs("git clone "+s, ""), obs("web_fetch", map[string]any{"url": s}, s))
		},
	}
	return m
}

func kitFor(lang string) *testKit { return testKits[lang] }

func TestScratchScaling(t *testing.T) {
	fns := scanFuncs()
	names := make([]string, 0, len(fns))
	for n := range fns {
		names = append(names, n)
	}
	sortStrings(names)
	only := os.Getenv("SCAN_FN")
	skip := map[string]bool{}
	for _, s := range strings.Split(os.Getenv("SCAN_SKIP"), ",") {
		skip[s] = true
	}
	for _, name := range names {
		if only != "" && !strings.HasPrefix(name, only) || skip[name] {
			continue
		}
		worst := time.Duration(0)
		worstUnit := ""
		for _, unit := range scanUnits {
			if u := os.Getenv("SCAN_UNIT"); u != "" && u != unit {
				continue
			}
			if n := os.Getenv("SCAN_N"); n != "" {
				var k int
				fmt.Sscan(n, &k)
				s := strings.Repeat(unit, k/len(unit)+1)[:k]
				start := time.Now()
				fns[name](s)
				fmt.Printf("%s unit=%q n=%d: %v\n", name, unit, k, time.Since(start))
				continue
			}
			run := func(n int) time.Duration {
				s := strings.Repeat(unit, n/len(unit)+1)[:n]
				best := time.Duration(1 << 62)
				for rep := 0; rep < 2; rep++ {
					runtime.GC()
					done := make(chan time.Duration, 1)
					go func() {
						start := time.Now()
						fns[name](s)
						done <- time.Since(start)
					}()
					select {
					case d := <-done:
						if d < best {
							best = d
						}
					case <-time.After(6 * time.Second):
						fmt.Printf("SLOW %s unit=%q n=%d (>6s)\n", name, unit, n)
						os.Exit(2)
					}
					if best > 50*time.Millisecond {
						break
					}
				}
				return best
			}
			t20 := run(20000)
			t80 := run(80000)
			if t80 > worst {
				worst, worstUnit = t80, unit
			}
			if t80 > 300*time.Millisecond && t80 > 7*t20 {
				fmt.Printf("SUPERLINEAR %s unit=%q 20k=%v 80k=%v\n", name, unit, t20, t80)
			}
		}
		fmt.Printf("ok %-34s worst 80k = %v (unit %q)\n", name, worst, worstUnit)
	}
}

func sortStrings(a []string) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}

func TestScratchMemoOff(t *testing.T) {
	for _, n := range []int{10000, 40000} {
		s := strings.Repeat(`\"`, n)
		start := time.Now()
		scanLiteralsMemo(s, false)
		t.Logf("memo off, n=%d units: %v", n, time.Since(start))
	}
}
