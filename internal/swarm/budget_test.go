package swarm

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/events"
)

// promptSays reports whether any text block of the prompt contains s.
func promptSays(p *core.Prompt, s string) bool {
	for _, m := range p.Messages {
		for _, b := range m.Blocks {
			if b.Kind == core.BlockText && strings.Contains(b.Text, s) {
				return true
			}
		}
	}
	return false
}

// Reaching the budget's reserve winds the swarm down before the line: the running worker is asked to hand in and stops on its
// own with money left, nothing new starts, and the manager is told once.
func TestSwarmBudgetReserveWindsDown(t *testing.T) {
	r := newRVRig(t, Config{MaxWriters: 4, BudgetUSD: 10}, func(ctx context.Context, c *rvCall) rvReply {
		if c.Role != "backend" {
			return rvReply{Text: "ok"}
		}
		u := core.Usage{InputTokens: 62_500} // $0.25 per request at 4$/M
		if promptSays(c.Prompt, "budget is nearly spent") {
			return rvReply{Text: "handed in what works", Usage: u}
		}
		return rvReply{Tools: []rvToolCall{{"read", map[string]any{"path": "a.go"}}}, Usage: u}
	})
	r.sw.StartManager()
	id, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "long work", By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	rvWait(t, "worker to hand in", func() bool { return r.idle(id) })
	if spent := r.sw.TotalCost(); spent < 9 || spent >= 10 {
		t.Fatalf("spent $%.2f, want the worker to stop on its own between the reserve ($9) and the budget ($10)", spent)
	}
	lows := r.log.OfType(eventBudgetLow)
	if len(lows) != 1 {
		t.Fatalf("%d %s events, want 1", len(lows), eventBudgetLow)
	}
	var p struct {
		Workers int `json:"workers"`
	}
	if err := json.Unmarshal(lows[0].Data, &p); err != nil || p.Workers != 1 {
		t.Fatalf("%s payload %s: want workers=1 (%v)", eventBudgetLow, lows[0].Data, err)
	}
	if len(r.log.OfType("swarm.budget")) != 0 {
		t.Fatal("the hard budget stop fired although the worker handed in before the line")
	}
	if _, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "more work", By: "mgr"}); !errors.Is(err, errBudgetLow) {
		t.Fatalf("Spawn inside the reserve = %v, want a refusal that wraps errBudgetLow", err)
	}
	rvWait(t, "the manager to be told", func() bool {
		for _, e := range r.log.OfType(events.TypeMailSend) {
			if strings.Contains(string(e.Data), "nearly spent") && strings.Contains(string(e.Data), "report to the user") {
				return true
			}
		}
		return false
	})
}

// Without a budget there is no reserve: startErr never refuses.
func TestStartErrWithoutBudget(t *testing.T) {
	r := newRVRig(t, Config{MaxWriters: 4}, func(ctx context.Context, c *rvCall) rvReply {
		return rvReply{Text: "ok", Usage: core.Usage{InputTokens: 10_000_000}}
	})
	if err := r.sw.startErr(); err != nil {
		t.Fatalf("startErr without a budget = %v", err)
	}
}
