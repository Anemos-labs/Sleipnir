package traj

import (
	"fmt"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/core"
)

// A chain of requests that each add a message holds the square of its length in memoised lists: past the budget the log is
// refused instead of the memory taken; a chain within it resolves.
func TestMessageListsOfALogAreBounded(t *testing.T) {
	defer func(n int) { maxMemoRefs = n }(maxMemoRefs)
	maxMemoRefs = 1000
	r := newRun(nil, nil)
	prev := ""
	for i := 0; i < 100; i++ {
		id := fmt.Sprintf("r%d", i)
		r.reqs[id] = &reqInfo{id: id, man: core.Manifest{Base: prev, Keep: i, Add: []core.Hash{core.HashBytes([]byte(id))}}}
		prev = id
	}
	if h, err := r.msgHashes("r20"); err != nil || len(h) != 21 {
		t.Fatalf("a short chain: %d, %v", len(h), err)
	}
	if _, err := r.msgHashes("r99"); err == nil || !strings.Contains(err.Error(), "references in all") {
		t.Fatalf("a long chain: %v", err)
	}
	if r.memoRefs > maxMemoRefs {
		t.Fatalf("%d references held", r.memoRefs)
	}
}
