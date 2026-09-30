package fs

// Security review repro for docs/reviews/security-robustness.md. It asserted the SECURE
// behaviour while finding S50 was open; S50 is fixed (matchSegs is now a prefix/suffix compare or
// a DP over (pattern segment, path segment)), so the gate is removed and this is a regression test.

import (
	"strings"
	"testing"
	"time"
)

// S50: matchSegs memoises failed states only at "**" boundaries, so a pattern with P "**" groups
// against a path of N segments costs O(P*N^2), not the O(P*N) its comment claims. Patterns come from
// the repository's own .gitignore files (up to 20,000 rules) and from the model's glob arguments;
// paths come from the repository tree. A repo with a 1,000-deep directory and one hostile
// .gitignore line makes every grep/glob/ls that walks it cost ~0.6 s per entry at that depth, and
// runTools waits for read-only tools with no per-call deadline.
func TestSec_S50_GlobMatcherCostIsLinearInPathDepth(t *testing.T) {
	segs := strings.Split(strings.Repeat("a/", 1000)+"b", "/") // 2 KB path, well under PATH_MAX
	pat := strings.Split(strings.Repeat("**/a/", 50)+"c", "/") // one hostile gitignore line
	start := time.Now()
	matchSegs(pat, segs)
	d := time.Since(start)
	t.Logf("50 '**' groups vs a 1000-segment path: %v per match", d)
	if d > 100*time.Millisecond {
		t.Errorf("S50: one glob match takes %v (O(P*N^2) memoisation); a hostile .gitignore can stall the walker for minutes", d)
	}
}
