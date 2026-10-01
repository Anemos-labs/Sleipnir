package agent_test

// The agent tells a gate that can use it how urgent its request is (agent.PriorityGate),
// and still works with a gate that cannot.

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/provider"
)

// recGate records how it was entered. It offers EnterPrio as well as Enter.
type recGate struct {
	mu    sync.Mutex
	via   []string // "Enter" or "EnterPrio"
	keys  []string
	prios []int
	ended []bool
}

func (g *recGate) note(via, key string, prio int) func(bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.via, g.keys, g.prios = append(g.via, via), append(g.keys, key), append(g.prios, prio)
	i := len(g.ended)
	g.ended = append(g.ended, false)
	return func(ok bool) {
		g.mu.Lock()
		g.ended[i] = ok
		g.mu.Unlock()
	}
}

func (g *recGate) Enter(_ context.Context, key string) (func(bool), error) {
	return g.note("Enter", key, -1), nil
}

func (g *recGate) EnterPrio(_ context.Context, key string, prio int) (func(bool), error) {
	return g.note("EnterPrio", key, prio), nil
}

// plainGate has Enter only, like a gate written before priorities existed.
type plainGate struct {
	mu   sync.Mutex
	keys []string
}

func (g *plainGate) Enter(_ context.Context, key string) (func(bool), error) {
	g.mu.Lock()
	g.keys = append(g.keys, key)
	g.mu.Unlock()
	return func(bool) {}, nil
}

func answeringProvider() *arvProvider {
	prov := &arvProvider{}
	prov.fn = func(ctx context.Context, req *provider.Request) (core.Turn, core.Usage) {
		return arvText("done"), core.Usage{}
	}
	return prov
}

func TestTheAgentTellsAPriorityGateHowUrgentItsRequestIs(t *testing.T) {
	for _, prio := range []int{agent.PrioInteractive, agent.PrioWorker, agent.PrioBackground} {
		g := &recGate{}
		a, _, _, _ := newARVCfg(t, answeringProvider(), aggressivePlanner(), func(c *agent.Config) {
			c.Gate, c.Priority, c.NoCompaction = g, prio, true
		})
		if _, err := a.Run(context.Background(), "hello"); err != nil {
			t.Fatal(err)
		}
		g.mu.Lock()
		if len(g.via) != 1 || g.via[0] != "EnterPrio" || g.prios[0] != prio {
			t.Errorf("priority %d: the gate was entered %v with priorities %v, want one EnterPrio(%d)", prio, g.via, g.prios, prio)
		}
		if len(g.keys) == 1 && strings.TrimSpace(g.keys[0]) == "" {
			t.Errorf("priority %d: the gate was given an empty key", prio)
		}
		if len(g.ended) == 1 && !g.ended[0] {
			t.Errorf("priority %d: the request succeeded but the gate was told it failed", prio)
		}
		g.mu.Unlock()
	}
}

func TestAGateWithoutPrioritiesStillWorks(t *testing.T) {
	g := &plainGate{}
	a, _, _, _ := newARVCfg(t, answeringProvider(), aggressivePlanner(), func(c *agent.Config) {
		c.Gate, c.Priority, c.NoCompaction = g, agent.PrioInteractive, true
	})
	if _, err := a.Run(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.keys) != 1 {
		t.Fatalf("a gate that only has Enter must still be used once per request, got %d entries", len(g.keys))
	}
}
