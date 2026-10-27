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
