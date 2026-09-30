package tools_test

// Adversarial review of the session-wide recall handle table
// (docs/reviews/swarm-concurrency.md). Gated like the other repros:
//
//	SLEIPNIR_REVIEW=1 go test -race -count=1 -run 'TestConc_' ./internal/tools

import (
	"fmt"
	"os"
	"testing"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/tools"
)

// Handles.Add derives the model-visible handle from the first 8 hex characters of
// the blob hash (32 bits) and stores it in ONE table shared by every agent of the
// session. Two different outputs whose hashes share that prefix silently alias:
// the later Add overwrites the earlier, and `recall out_xxxxxxxx` returns the wrong
// agent's output. Birthday odds: ~1% at 9k truncated outputs, ~50% at 77k.
func TestConc_RecallHandlesAreOnly32BitsAndAliasSilently(t *testing.T) {
	if os.Getenv("SLEIPNIR_REVIEW") == "" {
		t.Skip("concurrency-review repro: set SLEIPNIR_REVIEW=1 (asserts the correct behaviour, fails while the finding is open)")
	}
	seen := map[string]int{} // 8-hex prefix -> i of the first blob that had it
	var a, b int = -1, -1
	for i := 0; i < 400_000 && a < 0; i++ {
		h := core.HashString(fmt.Sprintf("tool output number %d", i))
		p := string(h)[:8]
		if j, ok := seen[p]; ok {
			a, b = j, i
			break
		}
		seen[p] = i
	}
	if a < 0 {
		t.Skip("no 8-hex prefix collision within 400k blobs (expected around 77k)")
	}
	ha := core.HashString(fmt.Sprintf("tool output number %d", a))
	hb := core.HashString(fmt.Sprintf("tool output number %d", b))
	h := tools.NewHandles()
	ida := h.Add(ha, 10)
	idb := h.Add(hb, 20)
	if ida != idb {
		t.Fatalf("setup: handles differ (%s vs %s)", ida, idb)
	}
	if ref, _, _ := h.Resolve(ida); ref != ha {
		t.Fatalf("handle %s was issued for blob #%d but resolves to blob #%d (another agent's output): the 32-bit handle space aliased two distinct outputs after %d outputs (a 50%% chance is expected around 77k)", ida, a, b, b+1)
	}
}
