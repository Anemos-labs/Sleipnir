package tools_test

// Adversarial review of the session-wide recall handle table
// (docs/SWARM-PROTOCOL.md). The repro asserted the correct behaviour and failed while
// the finding was open (it was gated behind SLEIPNIR_REVIEW=1); the finding is fixed, so it is an
// ordinary regression test now.

import (
	"fmt"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

// Handles.Add used to derive the model-visible handle from the first 8 hex characters of the blob
// hash (32 bits) and to store it in ONE table shared by every agent. Two different outputs whose
// hashes share that prefix silently aliased: the later Add overwrote the earlier, and
// `recall out_xxxxxxxx` returned the wrong agent's output. Birthday odds: ~1% at 9k truncated
// outputs, ~50% at 77k.
//
// The test still finds a real pair of outputs that share their first 8 hex characters and asserts
// what the finding required: each handle resolves to the blob it was issued for.
//
// Change to the repro (reason): its "setup: handles differ" precondition (Fatalf when the two
// handles are NOT equal) presupposed the vulnerable 32-bit handle format; with 64-bit handles the
// two blobs no longer share one, which is the fix, so the precondition became a failure of the
// fixed behaviour. It is replaced by the assertion that matters (both blobs resolve to
// themselves), generalised to both handles; nothing was weakened. The colliding-prefix case
// itself (a full 64-bit collision, which no search can produce) is covered deterministically in
// handles_test.go.
func TestConc_RecallHandlesDoNotAliasOn32BitPrefixCollisions(t *testing.T) {
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
	if ida == idb {
		t.Fatalf("blobs #%d and #%d share the handle %s: the handle space aliased two distinct outputs after %d outputs (a 50%% chance is expected around 77k with 32 bits)", a, b, ida, b+1)
	}
	if ref, n, ok := h.Resolve(ida); !ok || ref != ha || n != 10 {
		t.Fatalf("handle %s was issued for blob #%d but resolves to %s (len %d, ok=%v)", ida, a, ref.Short(), n, ok)
	}
	if ref, n, ok := h.Resolve(idb); !ok || ref != hb || n != 20 {
		t.Fatalf("handle %s was issued for blob #%d but resolves to %s (len %d, ok=%v)", idb, b, ref.Short(), n, ok)
	}
}
