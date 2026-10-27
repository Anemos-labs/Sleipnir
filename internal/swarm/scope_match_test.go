package swarm

import (
	"strings"
	"testing"
	"time"
)

// Separated globstars over a long path that does not match end at once: each pair of
// pattern and path positions is decided only once.
func TestSeparatedGlobstarsOverAFailingPathAreBounded(t *testing.T) {
	p := strings.Split(strings.Repeat("**/a/", 15)+"b", "/")
	s := strings.Split(strings.TrimSuffix(strings.Repeat("a/", maxScopeSegs*2), "/"), "/")
	start := time.Now()
	if matchSegs(p, s) {
		t.Fatal("matched a path with no b")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("took %v", d)
	}
	if !matchSegs(p, append(s, "b")) {
		t.Fatal("did not match the path ending in b")
	}
}

// At the limits, a pattern of globstars alone between its segments over the longest path that fails still ends at once:
// each position moves to at most two others.
func TestGlobstarsAtTheLimitsOverAFailingPathAreBounded(t *testing.T) {
	p := strings.Split(strings.Repeat("**/a/", maxScopeSegs/2)+"**/b", "/")
	s := strings.Split(strings.TrimSuffix(strings.Repeat("a/", maxScopeSegs*2), "/"), "/")
	start := time.Now()
	for i := 0; i < 100; i++ {
		if matchSegs(p, s) {
			t.Fatal("matched a path with no b")
		}
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("took %v", d)
	}
	if !matchSegs(p, append(s, "b")) || !matchSegs([]string{"**"}, s) || !matchSegs([]string{"a", "**"}, []string{"a"}) {
		t.Fatal("a matching path did not match")
	}
}
