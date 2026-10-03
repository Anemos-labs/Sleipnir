package swarm

import (
	"context"
	"sync"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/provider"
)

func TestEffortUpdatesExistingAndNewWorkersPerModel(t *testing.T) {
	var effort provider.EffortSetting
	effort.Set("medium")
	var mu sync.Mutex
	seen := map[string][]string{}
	r := newRVRigWith(t, Config{MaxWriters: 4}, func(_ context.Context, c *rvCall) rvReply {
		mu.Lock()
		seen[c.Agent] = append(seen[c.Agent], c.Prompt.Params.Effort)
		mu.Unlock()
		return rvReply{Text: "done"}
	}, func(d *Deps) {
		d.Effort = &effort
		d.Model.ID = "deepseek-v4.1-flash"
		model := d.Model
		model.ID = "gpt-6-astra"
		d.RoleModels = map[string]RoleModel{"reviewer": {Provider: d.Provider, Model: model}}
	})
	if _, err := r.sw.StartManager(); err != nil {
		t.Fatal(err)
	}
	first, err := r.sw.Spawn(SpawnReq{Role: "reviewer", Title: "review", By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	rvWait(t, "reviewer idle", func() bool { return r.idle(first) })
	effort.Set("max")
	if _, err := r.sw.Router.Send("mgr", first, "request", "review again"); err != nil {
		t.Fatal(err)
	}
	rvWait(t, "reviewer restarted", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(seen[first]) >= 2
	})
	second, err := r.sw.Spawn(SpawnReq{Role: "scout", Title: "inspect", By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	rvWait(t, "scout idle", func() bool { return r.idle(second) })
	mu.Lock()
	defer mu.Unlock()
	if len(seen[first]) < 2 || seen[first][0] != "medium" || seen[first][1] != "max" || len(seen[second]) == 0 || seen[second][0] != "max" {
		t.Fatalf("worker effort requests: %v", seen)
	}
}
