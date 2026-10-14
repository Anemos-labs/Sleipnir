package app

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/tui/input"
	"github.com/anemos-labs/sleipnir/internal/tui/state"
	"github.com/anemos-labs/sleipnir/internal/tui/state/statetest"
	"github.com/anemos-labs/sleipnir/internal/tui/widget"
)

// planEvents is a worker on a ChatGPT plan: two answers that bill nothing (cost_usd 0) and come to $0.0038 each at the API list price
// (list_usd), on a session that recorded the list prices with plan: true.
func planEvents(b *statetest.Builder) []events.Event {
	resp := func(req string) events.Event {
		p := statetest.ResponsePayload(req, "gpt-6-luna", 20_000, 80_000, 0, 2_000, 0)
		p["list_usd"] = 0.0038
		return b.Emit("w-1", events.TypeModelResponse, p)
	}
	return []events.Event{
		b.Emit("", events.TypeSessionStart, map[string]any{"model": "gpt-6-luna", "provider": "chatgpt", "swarm": true,
			"models": map[string]any{"gpt-6-luna": map[string]any{"source": "table", "plan": true, "input_per_m": 0.1, "output_per_m": 0.5, "cache_read_per_m": 0.01, "ttl_s": 300}}}),
		b.Spawn("w-1", "backend", "T1", "mgr"),
		b.Request("w-1", "w-1.1", "gpt-6-luna", "pk", statetest.Sec{Name: "shared", Tokens: 100}),
		resp("w-1.1"),
		b.Request("w-1", "w-1.2", "gpt-6-luna", "pk", statetest.Sec{Name: "shared", Tokens: 100}),
		resp("w-1.2"),
	}
}

// A plan's session shows its usage at the API list price, marked "≈ … plan", in the header, the cockpit's title bar, its governor
// panel (with no budget meter filling for it) and the agent table, and the stats page says what the mark means. Nothing of it is a bill.
func TestAPlansSessionShowsTheListPriceMarked(t *testing.T) {
	st := state.New()
	st.ApplyAll(planEvents(statetest.NewBuilder()))
	sn := st.Snapshot()
	sp := sn.Spend()
	if !sp.Plan || sp.Billed != 0 || math.Abs(sp.List-0.0076) > 1e-12 || sn.Totals.CostUSD != 0 {
		t.Fatalf("the session's spend: %+v (totals %+v)", sp, sn.Totals)
	}
	if a, ok := sn.Agent("w-1"); !ok || !a.Plan || a.CostUSD != 0 || math.Abs(a.ListUSD-0.0076) > 1e-12 {
		t.Fatalf("the agent: %+v", a)
	}
	if !sn.Totals.Savings.Known() || sn.Totals.Savings.SavedUSD <= 0 {
		t.Errorf("savings at the recorded list prices are known: %+v", sn.Totals.Savings)
	}

	head := strings.Join(stats(Scene{Snap: sn}), " | ")
	if !strings.Contains(head, "≈$0.0076 plan") {
		t.Errorf("the header: %s", head)
	}
	d := CockpitData(sn, CockpitOptions{})
	if !d.Plan || d.Billed != 0 || math.Abs(d.Spend-0.0076) > 1e-12 || len(d.Agents) != 1 || !d.Agents[0].Plan {
		t.Fatalf("the cockpit's data: plan %v billed %v spend %v agents %+v", d.Plan, d.Billed, d.Spend, d.Agents)
	}
	screen := plainText(Cockpit(sn, 140, 40, 0, widget.MonoPalette(), CockpitOptions{NoAnim: true}))
	if !strings.Contains(screen, "≈$0.0076 plan") || !strings.Contains(screen, "≈$0.008") {
		t.Errorf("the cockpit does not show the plan's figure:\n%s", screen)
	}
	// the COST column has room for the mark: a cost under a twentieth of a cent has four decimals and is not cut
	small := []widget.AgentRow{{ID: "main", Role: "worker", Cost: 0.0003, Plan: true, Hit: 0.5}}
	if table := plainText(widget.AgentTable(small, 110, 0, widget.MonoPalette())); !strings.Contains(table, "≈$0.0003") {
		t.Errorf("the agent table cuts a plan's cost:\n%s", table)
	}
	d.Budget = 5 // a budget counts what is billed: the plan's figure is not drawn against it
	gov := plainText(widget.Dashboard(d, 140, 40, 0, widget.MonoPalette()))
	if !strings.Contains(gov, "billed $0/$5") || strings.Contains(gov, "$0.0076/$5") {
		t.Errorf("the budget is drawn against the plan's figure:\n%s", gov)
	}
	d.Billed, d.Spend = 2.5, 2.5076 // a session that mixes billed usage with the plan's: the meter is the bill's
	if gov := plainText(widget.Dashboard(d, 140, 40, 0, widget.MonoPalette())); !strings.Contains(gov, "billed $2.50/$5") || !strings.Contains(gov, "≈$2.51 plan") {
		t.Errorf("a mixed session's budget meter:\n%s", gov)
	}

	k := goldenLook(true)
	got := k.turnSummary(2*time.Second, TurnResult{Steps: 2, ListUSD: 0.0076, Plan: true}, 100).Plain()
	if !strings.Contains(got, "≈$0.0076 plan") {
		t.Errorf("the turn's record: %q", got)
	}
	if got := k.turnSummary(2*time.Second, TurnResult{Steps: 2, CostUSD: 0.0076}, 100).Plain(); strings.Contains(got, "≈") || strings.Contains(got, "plan") {
		t.Errorf("a billed turn is marked as a plan's: %q", got)
	}
}

// A plan's model the table does not know answers with list_usd 0: the terminal still marks the session and the agent as a plan's.
func TestAPlanAnswerWithNoListPriceIsStillAPlans(t *testing.T) {
	b := statetest.NewBuilder()
	p := statetest.ResponsePayload("w-1.1", "gpt-next-x", 100, 0, 0, 10, 0)
	p["list_usd"] = 0.0
	st := state.New()
	st.ApplyAll([]events.Event{b.Spawn("w-1", "backend", "T1", "mgr"), b.Request("w-1", "w-1.1", "gpt-next-x", "pk", statetest.Sec{Name: "shared", Tokens: 100}),
		b.Emit("w-1", events.TypeModelResponse, p)})
	sn := st.Snapshot()
	if sp := sn.Spend(); !sp.Plan || sp.Shown() != 0 {
		t.Fatalf("the session's spend: %+v", sp)
	}
	if a, ok := sn.Agent("w-1"); !ok || !a.Plan {
		t.Fatalf("the agent: %+v", a)
	}
	if head := strings.Join(stats(Scene{Snap: sn}), " | "); !strings.Contains(head, "≈$0.00 plan") {
		t.Errorf("the header: %s", head)
	}
}

// The status line marks what this turn has used as a plan's only when this turn's usage was a plan's: a billed turn after a turn on
// the plan says "$0.01", and the plan's usage that follows in the same turn makes it "≈… plan".
func TestTheStatusLineMarksOnlyThisTurnsPlanUsage(t *testing.T) {
	r := startChat(t, rigOpts{cols: 120, rows: 24})
	b := statetest.NewBuilder()
	r.ctrl('t')
	r.shows("nothing has been sent yet")
	r.emit(planEvents(b)...) // an earlier turn, on the plan
	r.shows("≈$0.0076 plan")
	r.special(input.Esc)
	r.shows("Message Sleipnir")

	started, finish := make(chan struct{}), make(chan struct{})
	r.host.turn = func(ctx context.Context, _ string) TurnResult {
		close(started)
		select {
		case <-ctx.Done():
		case <-finish:
		}
		return TurnResult{}
	}
	defer close(finish)
	r.submit("next")
	<-started
	status := func(s string) string {
		for _, line := range strings.Split(s, "\n") {
			if strings.Contains(line, "esc to interrupt") {
				return line
			}
		}
		return ""
	}
	r.emit(b.Request("w-1", "w-1.3", "claude-x", "pk", statetest.Sec{Name: "shared", Tokens: 100}),
		b.Response("w-1", "w-1.3", "claude-x", 1000, 0, 0, 100, 0.0123))
	line := status(r.until("the billed turn's cost", func(s string) bool { return strings.Contains(status(s), "$0.01") }))
	if strings.Contains(line, "plan") || strings.Contains(line, "≈") {
		t.Errorf("a billed turn after a plan's is marked as the plan's: %q", line)
	}
	p := statetest.ResponsePayload("w-1.4", "gpt-6-luna", 20_000, 80_000, 0, 2_000, 0)
	p["list_usd"] = 0.0038
	r.emit(b.Request("w-1", "w-1.4", "gpt-6-luna", "pk", statetest.Sec{Name: "shared", Tokens: 100}), b.Emit("w-1", events.TypeModelResponse, p))
	r.until("the plan's usage in this turn", func(s string) bool { return strings.Contains(status(s), "≈$0.02 plan") })
}

// The chat's stats page and status line say the same: the figure at the list price, its mark, and what the mark means.
func TestTheChatShowsAPlansFigure(t *testing.T) {
	r := startChat(t, rigOpts{cols: 120, rows: 24})
	r.ctrl('t')
	r.shows("nothing has been sent yet")
	r.emit(planEvents(statetest.NewBuilder())...)
	r.shows("≈$0.0076 plan")
	r.shows(state.PlanHelp)
	r.special(input.Esc)
}
