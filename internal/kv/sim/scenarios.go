package sim

import (
	"fmt"
	"strings"
	"time"
)

// Scenario is a named variation of the workload used to find where layering
// wins, where it ties, and where it loses.
type Scenario struct {
	Name string
	Note string
	Make func(agents int) Workload
}

// Scenarios are the standard sensitivity cases. The first is the reference.
func Scenarios() []Scenario {
	with := func(f func(*Workload)) func(int) Workload {
		return func(n int) Workload { w := DefaultWorkload(n); f(&w); return w }
	}
	return []Scenario{
		{"typical", "8k shared + 10k role pin; a cold agent reads ~30k tokens over 10 calls to orient",
			DefaultWorkload},
		{"short tasks", "~8 work steps per task: pins and orientation dominate",
			with(func(w *Workload) { w.StepsMean, w.StepsStd = 8, 3 })},
		{"long tasks", "~90 work steps per task: compaction dominates",
			with(func(w *Workload) { w.StepsMean, w.StepsStd = 90, 25 })},
		{"cold launches", "three swarm launches separated by 12 idle minutes (every launch starts cold)",
			with(func(w *Workload) { w.Waves = 3 })},
		{"small repo", "a cold agent needs only ~6k tokens / 3 calls to orient: little for the pin to replace",
			with(func(w *Workload) { w.ExploreTokens, w.ExploreSteps = 6000, 3 })},
		{"bloated pins", "40k shared + 20k role pin (a sloppy, undense pin) against the same 30k of exploration",
			with(func(w *Workload) { w.SharedTokens, w.RoleTokens = 40000, 20000 })},
		{"huge exploration", "large monorepo: a cold agent reads ~90k tokens over 25 calls",
			with(func(w *Workload) { w.ExploreTokens, w.ExploreSteps = 90000, 25 })},
	}
}

// Row is one line of a scenario table.
type Row struct {
	Scenario      string
	Agents        int
	NaiveITE      float64
	SummaryITE    float64
	LayeredITE    float64
	VsNaive       float64 // percent
	VsSummary     float64
	AvgCtxNaive   float64
	AvgCtxLayered float64
	WallNaive     time.Duration
	WallLayered   time.Duration
}

func pct(a, b float64) float64 { return (a/b - 1) * 100 }

// RunScenario compares the layered policy with both baselines on one workload.
func RunScenario(name string, w Workload, p Provider) Row {
	n := Naive(w, p, NaiveOptions{})
	s := Naive(w, p, NaiveOptions{CompactAt: 100_000})
	l := Layered(w, p, DefaultLayered())
	return Row{
		Scenario: name, Agents: w.Agents,
		NaiveITE: n.ITE, SummaryITE: s.ITE, LayeredITE: l.ITE,
		VsNaive: pct(l.ITE, n.ITE), VsSummary: pct(l.ITE, s.ITE),
		AvgCtxNaive: n.AvgContext, AvgCtxLayered: l.AvgContext,
		WallNaive: durSec(n.WallSeconds), WallLayered: durSec(l.WallSeconds),
	}
}

// ScenarioTable runs every scenario at the given agent count.
func ScenarioTable(agents int, p Provider) (string, []Row) {
	var rows []Row
	var b strings.Builder
	fmt.Fprintf(&b, "provider: %s\n%d workers, %d tasks\n\n", p.Name, agents, agents*2)
	fmt.Fprintf(&b, "%-16s %10s %10s %10s %9s %9s %8s %8s\n", "scenario", "naive", "naive+sum", "sleipnir", "vs naive", "vs n+sum", "ctx n/s", "wall n/s")
	for _, sc := range Scenarios() {
		r := RunScenario(sc.Name, sc.Make(agents), p)
		rows = append(rows, r)
		fmt.Fprintf(&b, "%-16s %9.1fM %9.1fM %9.1fM %+8.0f%% %+8.0f%% %3.0f/%-3.0fk %3.0f/%-3.0fm\n", r.Scenario,
			r.NaiveITE/1e6, r.SummaryITE/1e6, r.LayeredITE/1e6, r.VsNaive, r.VsSummary,
			r.AvgCtxNaive/1000, r.AvgCtxLayered/1000, r.WallNaive.Minutes(), r.WallLayered.Minutes())
	}
	return b.String(), rows
}

// PinPoint is one point of the pin-size sweep.
type PinPoint struct {
	SharedTokens, RoleTokens int
	VsNaive, VsSummary       float64
}

// PinSweep varies the total size of the shared and role pins (split 45/55)
// against a fixed exploration budget, to find the density at which pinning stops
// paying: the rule of thumb the design states is that the pins must not be much
// larger than the exploration they replace.
func PinSweep(agents int, p Provider, totals []int) (string, []PinPoint) {
	var pts []PinPoint
	var b strings.Builder
	base := DefaultWorkload(agents)
	fmt.Fprintf(&b, "provider: %s\n%d workers, cold agents read %dk tokens to orient\n\n", p.Name, agents, base.ExploreTokens/1000)
	fmt.Fprintf(&b, "%-10s %-10s %-8s %10s %10s\n", "pins(k)", "shared/role", "vs pin/explore", "vs naive", "vs n+sum")
	for _, tot := range totals {
		w := base
		w.SharedTokens = tot * 45 / 100
		w.RoleTokens = tot - w.SharedTokens
		r := RunScenario("pins", w, p)
		pts = append(pts, PinPoint{w.SharedTokens, w.RoleTokens, r.VsNaive, r.VsSummary})
		fmt.Fprintf(&b, "%-10d %4d/%-5d %-14.2f %+9.0f%% %+9.0f%%\n", tot/1000, w.SharedTokens/1000, w.RoleTokens/1000,
			float64(tot)/float64(base.ExploreTokens), r.VsNaive, r.VsSummary)
	}
	return b.String(), pts
}

// AgentSweep shows how the advantage scales with swarm size.
func AgentSweep(p Provider, counts []int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "provider: %s\n\n%-8s %10s %10s %10s %9s %9s\n", p.Name, "workers", "naive", "naive+sum", "sleipnir", "vs naive", "vs n+sum")
	for _, n := range counts {
		r := RunScenario("agents", DefaultWorkload(n), p)
		fmt.Fprintf(&b, "%-8d %9.1fM %9.1fM %9.1fM %+8.0f%% %+8.0f%%\n", n, r.NaiveITE/1e6, r.SummaryITE/1e6, r.LayeredITE/1e6, r.VsNaive, r.VsSummary)
	}
	return b.String()
}
