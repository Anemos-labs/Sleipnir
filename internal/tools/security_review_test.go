package tools

// Security review repro for docs/reviews/security-robustness.md (gated: SLEIPNIR_REVIEW=1,
// asserts the SECURE behaviour and fails while the finding is open).

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/perm"
)

// S32: recall handles are "out_" + the first 8 hex digits (32 bits) of the blob hash, in a
// session-wide table that silently overwrites on collision: recall(handle) can then return a
// different command's output. With ~50k truncated outputs in a large session the chance of at
// least one collision is ~25%; an insider can also grind a 32-bit prefix.
func TestSecReview_S32_HandleCollisionSilentlyOverwrites(t *testing.T) {
	if os.Getenv("SLEIPNIR_REVIEW") == "" {
		t.Skip("security-review repro: set SLEIPNIR_REVIEW=1")
	}
	h := NewHandles()
	a := core.Hash("deadbeef" + strings.Repeat("1", 56)) // e.g. the real test log
	b := core.Hash("deadbeef" + strings.Repeat("2", 56)) // e.g. attacker-ground content
	ida := h.Add(a, 100)
	idb := h.Add(b, 200)
	got, _, _ := h.Resolve(ida)
	t.Logf("handles: %s -> %s, %s -> %s; resolving the first now returns %s", ida, a.Short(), idb, b.Short(), got.Short())
	if ida == idb && got != a {
		t.Errorf("S32: handle %s (first output) now resolves to a different blob; Add overwrote it silently", ida)
	}
}

// S46: the permission engine is an interface with exactly one implementation today (AllowAll) and
// every constructor defaults a missing Requester to it (tools.Env.Defaults, agent.New,
// swarm.buildAgent), so forgetting to wire one silently disables all permission checks.
func TestSecReview_S46_MissingRequesterDefaultsToAllowAll(t *testing.T) {
	if os.Getenv("SLEIPNIR_REVIEW") == "" {
		t.Skip("security-review repro: set SLEIPNIR_REVIEW=1")
	}
	env := (&Env{Agent: "be-1"}).Defaults()
	d := env.Perm.Check(context.Background(), perm.Request{Agent: "be-1", Tool: "bash", Command: "rm -rf ~", Writes: true})
	if d.Allow {
		t.Errorf("S46: an Env without a permission Requester allows %q (%s)", "rm -rf ~", d.Reason)
	}
}
