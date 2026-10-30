package agent

import (
	"testing"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/cost"
)

// A compactor model of its own is asked for no more than its maximum; the agent's own forks keep the agent's limit.
func TestCompactorParamsCapTheOutputLimit(t *testing.T) {
	p := core.Params{MaxTokens: 32000, Effort: "high"}
	small := cost.Model{ID: "compactor", MaxOutput: 8192}
	if got := compactorParams(p, small, true); got.MaxTokens != 8192 || got.Effort != "high" {
		t.Fatalf("compactor request %+v", got)
	}
	if got := compactorParams(p, small, false); got.MaxTokens != 32000 {
		t.Fatalf("the agent's own fork asked for %d", got.MaxTokens)
	}
	if got := compactorParams(p, cost.Model{ID: "compactor"}, true); got.MaxTokens != 32000 {
		t.Fatalf("a compactor with no reported maximum asked for %d", got.MaxTokens)
	}
}
