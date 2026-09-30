package tools

// Security review repros for docs/reviews/security-robustness.md (S32, S46), fixed and now
// ordinary regression tests.

import (
	"context"
	"strings"
	"testing"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/perm"
)

// S32: recall handles were "out_" + the first 8 hex digits (32 bits) of the blob hash, in a
// session-wide table that silently overwrote on collision: recall(handle) could then return a
// different command's output. With ~50k truncated outputs in a large session the chance of at
// least one collision was ~25%; an insider could also grind a 32-bit prefix. The handle is now
// 64 bits and a colliding newcomer gets a longer one, so two blobs never share a handle.
func TestSec_S32_DistinctBlobsNeverShareAHandle(t *testing.T) {
	h := NewHandles()
	a := core.Hash("deadbeef" + strings.Repeat("1", 56)) // e.g. the real test log
	b := core.Hash("deadbeef" + strings.Repeat("2", 56)) // e.g. attacker-ground content: same first 8 hex digits
	ida := h.Add(a, 100)
	idb := h.Add(b, 200)
	if ida == idb {
		t.Fatalf("S32: two different blobs got the same handle %s", ida)
	}
	for _, c := range []struct {
		id   string
		want core.Hash
		len  int
	}{{ida, a, 100}, {idb, b, 200}} {
		got, n, ok := h.Resolve(c.id)
		if !ok || got != c.want || n != c.len {
			t.Errorf("S32: handle %s resolves to %s (len %d, ok=%v), want %s (len %d)", c.id, got.Short(), n, ok, c.want.Short(), c.len)
		}
	}
}

// S46: every constructor used to default a missing Requester to allow-all (tools.Env.Defaults,
// agent.New, swarm members), so forgetting to wire one silently disabled all permission checks.
// The default is deny now (perm.DenyAll).
func TestSec_S46_MissingRequesterDeniesEverything(t *testing.T) {
	env := (&Env{Agent: "be-1"}).Defaults()
	d := env.Perm.Check(context.Background(), perm.Request{Agent: "be-1", Tool: "bash", Command: "rm -rf ~", Writes: true})
	if d.Allow {
		t.Errorf("S46: an Env without a permission Requester allows %q (%s)", "rm -rf ~", d.Reason)
	}
}
