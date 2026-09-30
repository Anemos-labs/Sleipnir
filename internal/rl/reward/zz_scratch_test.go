package reward

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestScratchScaling(t *testing.T) {
	units := []string{"a", "/", "*", "{", "}", "[", "]", `\`, `"`, `'`, "`", "(", ")", "<<", "<<X\n", ">", "$", " ", "\n", "&", "|", ";", "a/", "**/", "{a,", "https://", "github.com/", "@", "::", "%", "​", "é", "0", "1.", "0x", "-", "+", "#", "//", "/*", "'''", `"""`, "=", "*/", "a b ", "-e ", "--", "..", "./"}
	funcs := map[string]func(string){
		"parseDiff":         func(s string) { parseDiff(s) },
		"parseShell":        func(s string) { parseShell(s) },
		"shellWriteTargets": func(s string) { shellWriteTargets(s) },
		"parseVerifier":     func(s string) { parseVerifier(s) },
		"commandCore":       func(s string) { commandCore(s) },
		"refsIn":            func(s string) { refsIn(s) },
		"scanLiterals":      func(s string) { scanLiterals(s) },
		"unquoteGit":        func(s string) { unquoteGit(s) },
		"cleanRel":          func(s string) { cleanRel(s) },
		"outsideReason":     func(s string) { outsideReason(s, []string{"/work"}) },
		"globs":             func(s string) { g := compileGlobs([]string{s, "*.go", "a/**/b"}); g.match(s) },
		"parsePatch":        func(s string) { parsePatch(s) },
		"normalizeText":     func(s string) { normalizeText(s) },
		"patchPaths":        func(s string) { patchPaths(s) },
		"lexgo":             func(s string) { lexCode(s, "go", true) },
		"lexpy":             func(s string) { lexCode(s, "py", false) },
		"tight":             func(s string) { tight(s) },
		"tautology":         func(s string) { tautology("go", s); tautology("py", s); tautology("js", s) },
		"analyze": func(s string) {
			for _, f := range parseDiff("diff --git a/x_test.go b/x_test.go\n--- a/x_test.go\n+++ b/x_test.go\n@@ -1 +1 @@\n" + s) {
				analyzeTestChange(f, "go", 1)
				analyzeTestChange(f, "py", 1)
				analyzeTestChange(f, "js", 1)
			}
		},
		"factsExtract": func(s string) {
			ep := mkEpisode("t/0", mkAgent("a", "worker", mkStep("a.1", withObs(bashObs(s, s))), mkStep("a.2"), mkStep("a.3"), mkStep("a.4"), mkStep("a.c", withKind("compactor")), mkStep("a.n", withSeg(1, 1))))
			probes(ep, nil, 8)
		},
	}
	for name, fn := range funcs {
		for _, u := range units {
			var d [2]time.Duration
			for i, n := range []int{20000, 80000} {
				s := strings.Repeat(u, n/max(1, len(u)))
				start := time.Now()
				fn(s)
				d[i] = time.Since(start)
			}
			if d[1] > 300*time.Millisecond && d[1] > 8*d[0] {
				fmt.Printf("SUPERLINEAR %-18s unit=%-12q 20k=%-12v 80k=%v\n", name, u, d[0], d[1])
			}
		}
	}
}
