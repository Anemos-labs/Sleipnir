package kv

import (
	"testing"

	"github.com/anemos-labs/sleipnir/internal/core"
)

func TestGuardRequiresTheCachedMessageEnding(t *testing.T) {
	for _, boundary := range []bool{false, true} {
		rendered := func(extended bool) *Rendered {
			blocks := []core.Block{core.Text("stable reference material")}
			if extended {
				blocks = append(blocks, core.Text("new live state"))
			}
			return &Rendered{Caps: Caps{MessageBoundaries: boundary}, Prompt: &core.Prompt{
				Model: "test", Messages: []core.Message{{Role: core.RoleUser, Blocks: blocks}},
			}}
		}
		est := cxEst()
		var g Guard
		g.Observe(rendered(false), 0, est)
		unchanged := g.Observe(rendered(false), 0, est)
		if unchanged.ReadableTokens == 0 {
			t.Fatal("an unchanged eligible message should remain readable")
		}
		extended := g.Observe(rendered(true), 0, est)
		if extended.Drift || extended.SharedTokens == 0 {
			t.Fatal("appending a block preserves the earlier internal bytes")
		}
		if boundary && extended.ReadableTokens != 0 {
			t.Fatal("a lost message ending must not be counted as a readable cache entry")
		}
		if !boundary && extended.ReadableTokens != extended.SharedTokens {
			t.Fatal("a token-prefix cache can still reuse the shorter prefix")
		}
	}
}
