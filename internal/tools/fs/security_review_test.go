package fs

// Security review repro for docs/reviews/security-robustness.md. It asserted the SECURE
// behaviour while finding S50 was open; S50 is fixed (matchSegs is now a prefix/suffix compare or
// a DP over (pattern segment, path segment)), so the gate is removed and this is a regression test.

import (
	"strings"
	"testing"
	"time"
)

// S50: matchSegs memoised failed states only at "**" boundaries, so a pattern with P "**" groups
// against a path of N segments cost O(P*N^2), not the O(P*N) its comment claimed. Patterns come from
// the repository's own .gitignore files (up to 20,000 rules) and from the model's glob arguments;
// paths come from the repository tree. A repo with a 1,000-deep directory and one hostile
// .gitignore line made every grep/glob/ls that walks it cost ~0.6 s per entry at that depth, and
// runTools waits for read-only tools with no per-call deadline.
//
// This is a hang guard, not a stopwatch (a stopwatch at 100 ms failed on a machine under load 37). The path is 20,000
// segments deep, far beyond any real tree: the DP does about three million steps (well under a second, a few seconds
// on a loaded machine) while the quadratic matcher needs about 2*10^10 (minutes), so only the complexity class can trip
// the one-minute guard.
func TestSec_S50_GlobMatcherCostIsLinearInPathDepth(t *testing.T) {
	segs := strings.Split(strings.Repeat("a/", 20000)+"b", "/")
	pat := strings.Split(strings.Repeat("**/a/", 50)+"c", "/") // one hostile gitignore line
	done := make(chan bool, 1)
	go func() { done <- matchSegs(pat, segs) }()
	select {
	case matched := <-done:
		if matched {
			t.Error("the pattern ends in c and the path in b: it must not match")
		}
	case <-time.After(time.Minute):
		t.Fatal("S50: one glob match is still running after a minute (O(P*N^2) memoisation); a hostile .gitignore can stall the walker for minutes")
	}
}
