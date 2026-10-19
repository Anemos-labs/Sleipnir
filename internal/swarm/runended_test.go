package swarm

import (
	"context"
	"slices"
	"sync"
	"testing"
)

// The session ends what a worker's commands left running when the worker's run ends (Deps.RunEnded), so a server a worker
// started with "server &" does not hold its port for the rest of the session.
func TestRunEndedIsToldWhenAWorkersRunEnds(t *testing.T) {
	var mu sync.Mutex
	var ended []string
	r := newRVRigWith(t, Config{}, func(ctx context.Context, c *rvCall) rvReply {
		return rvReply{Text: "done"}
	}, func(d *Deps) {
		d.RunEnded = func(agent string) {
			mu.Lock()
			ended = append(ended, agent)
			mu.Unlock()
		}
	})
	r.sw.StartManager()
	id, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "a task", By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	rvWait(t, "the end of the worker's run to be told", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return slices.Contains(ended, id)
	})
}
