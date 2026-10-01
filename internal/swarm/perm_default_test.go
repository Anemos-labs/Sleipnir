package swarm

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

// A swarm built without a permission Requester gives its agents none, and an agent with none
// is denied everything (S46): forgetting to wire the engine stops the tools, it does not open
// them. The rig's Deps say perm.AllowAll{} on purpose; this test takes it away.
func TestSwarmWithoutARequesterDeniesEveryAgentEverything(t *testing.T) {
	var mu sync.Mutex
	var decision perm.Decision
	asked := false
	r := newRVRigWith(t, Config{}, func(ctx context.Context, c *rvCall) rvReply {
		if !c.Sees("probe-done") {
			return rvReply{Tools: []rvToolCall{{Name: "probe", Args: map[string]any{}}}}
		}
		return rvReply{Text: "finished"}
	}, func(d *Deps) { d.Perm = nil })
	r.sw.deps.Registry.Register(rvFakeTool{name: "probe", ro: true, run: func(ctx context.Context, c *tools.Call) *tools.Result {
		d := c.Env.Perm.Check(ctx, perm.Request{Agent: c.Env.Agent, Tool: "bash", Command: "rm -rf ~", Writes: true})
		mu.Lock()
		decision, asked = d, true
		mu.Unlock()
		return &tools.Result{Text: "probe-done"}
	}})
	if _, err := r.sw.RunManager(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !asked {
		t.Fatal("the tool never ran")
	}
	if decision.Allow || !strings.Contains(decision.Reason, "no permission policy") {
		t.Fatalf("an agent with no Requester was allowed to act: %+v", decision)
	}
}
