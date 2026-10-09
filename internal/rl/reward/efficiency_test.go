package reward

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/rl"
)

// paced is a passing single-agent episode of task T whose five calls grow one append-only
// prompt, gap apart: a gap past the cache TTL makes every call write its prefix again.
func paced(id string, gap time.Duration) *rl.Episode {
	var steps []rl.Step
	for i := 0; i < 5; i++ {
		steps = append(steps, mkStep(id+"."+string(rune('1'+i)), withAt(time.Duration(i)*gap), withPrompt(20_000+2_000*i, ""), withOut(200)))
	}
	ep := mkEpisode("T/"+id, mkAgent("solo", "backend", steps...))
	ep.Group = "T@policy"
	ep.Signals = map[string]float64{rl.SigSteps: 5, rl.SigRequests: 5}
	return ep
}

func TestEfficiencyComponentsAreOffByDefault(t *testing.T) {
	ep := paced("a", 10*time.Second)
	ep.Signals[rl.SigStuckStops], ep.Signals[rl.SigRepeatedReads] = 1, 4
	mustScore(t, ep, &rl.Task{}, DefaultConfig(), NoDiffs{})
	near(t, "waste component", ep.Reward.Components[CompWaste], -0.5) // 5 events of a cap of 10, times score 1
	near(t, "group_ite component", ep.Reward.Components[CompGroupITE], 0)

	without := copyComps(ep.Reward.Components)
	delete(without, CompWaste)
	delete(without, CompGroupITE)
	near(t, "default total", ep.Reward.Total, DefaultConfig().Total(without))
	for _, n := range ep.Reward.Notes {
		if strings.Contains(n, "group_ite") {
			t.Errorf("a component with weight 0 should not be noted: %q", n)
		}
	}
}

func TestWasteIsGatedByTheVerifierScore(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Weights[CompWaste] = 0.3
	loop := paced("a", 10*time.Second)
	loop.Signals[rl.SigStuckWarnings], loop.Signals[rl.SigStuckStops], loop.Signals[rl.SigToolErrors] = 2, 1, 30
	clean := paced("b", 10*time.Second)
	mustScore(t, loop, &rl.Task{}, cfg, NoDiffs{})
	mustScore(t, clean, &rl.Task{}, cfg, NoDiffs{})
	near(t, "saturated waste", loop.Reward.Components[CompWaste], -1)
	near(t, "the penalty in the total", clean.Reward.Total-loop.Reward.Total, 0.3)

	failed := paced("c", 10*time.Second)
	failed.Outcome.Verifier = failing()
	failed.Signals[rl.SigStuckStops] = 1
	mustScore(t, failed, &rl.Task{}, cfg, NoDiffs{})
	near(t, "waste of a failed run", failed.Reward.Components[CompWaste], 0)
	b, _ := json.Marshal(map[string]float64{CompWaste: failed.Reward.Components[CompWaste], CompGroupITE: failed.Reward.Components[CompGroupITE]})
	if strings.Contains(string(b), "-0") {
		t.Errorf("components store a negative zero: %s", b)
	}
}

func TestGroupITEPrefersTheCacheFriendlyRunOfTheGroup(t *testing.T) {
	friendly, hostile := paced("friendly", 10*time.Second), paced("hostile", 10*time.Minute)
	other := paced("other", 10*time.Minute)
	other.Group = "U@policy"
	cfg := DefaultConfig()
	cfg.Weights[CompGroupITE] = 0.2
	ranges, err := GroupITE([]*rl.Episode{friendly, hostile, other}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	g := ranges["T@policy"]
	if !(g[0] > 0 && g[1] > g[0]) || len(ranges) != 2 {
		t.Fatalf("ranges = %v: the hostile run must cost more ITE than the friendly one", ranges)
	}
	for _, ep := range []*rl.Episode{friendly, hostile} {
		c := cfg
		c.GroupITE = &g
		mustScore(t, ep, &rl.Task{}, c, NoDiffs{})
	}
	near(t, "friendly group_ite", friendly.Reward.Components[CompGroupITE], 0)
	near(t, "hostile group_ite", hostile.Reward.Components[CompGroupITE], -1)
	near(t, "friendly ITE is the group minimum", friendly.Cost.ITE, g[0])
	if d := friendly.Reward.Total - hostile.Reward.Total; d < 0.2-1e-9 {
		t.Errorf("friendly - hostile = %v, want at least the group_ite weight 0.2", d)
	}

	// Scored alone, the component is 0 and says why.
	alone := paced("alone", 10*time.Minute)
	mustScore(t, alone, &rl.Task{}, cfg, NoDiffs{})
	near(t, "group_ite alone", alone.Reward.Components[CompGroupITE], 0)
	noted := false
	for _, n := range alone.Reward.Notes {
		noted = noted || strings.Contains(n, "group_ite")
	}
	if !noted {
		t.Errorf("notes %q do not explain the missing group", alone.Reward.Notes)
	}
}

func TestEfficiencyKnobsParse(t *testing.T) {
	cfg, err := ParseConfig([]byte(`{"weights":{"waste":0.1,"group_ite":0.2},"caps":{"waste":4}}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Weights[CompWaste] != 0.1 || cfg.Weights[CompGroupITE] != 0.2 || cfg.Caps[CapWaste] != 4 {
		t.Fatalf("parsed %v %v", cfg.Weights, cfg.Caps)
	}
	if d := DefaultConfig(); d.Weights[CompWaste] != 0 || d.Weights[CompGroupITE] != 0 || d.Caps[CapWaste] != 10 {
		t.Fatalf("defaults %v %v", d.Weights, d.Caps)
	}
}

// A failed verdict that scored partial credit still has nothing to say about efficiency: the components tell passing
// runs apart, and a failed run is never penalised for its waste on top of failing.
func TestEfficiencyComponentsIgnoreAFailedVerdictWithPartialCredit(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Weights[CompWaste], cfg.Weights[CompGroupITE] = 0.3, 0.2
	friendly, hostile := paced("friendly", 10*time.Second), paced("hostile", 10*time.Minute)
	ranges, err := GroupITE([]*rl.Episode{friendly, hostile}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	g := ranges["T@policy"]
	cfg.GroupITE = &g
	for _, tc := range []struct {
		name    string
		verdict *rl.Verdict
		want    bool // penalised
	}{
		{"passed", passing(1), true},
		{"failed with partial credit", &rl.Verdict{Kind: "verifier", Pass: false, Score: 0.6}, false},
	} {
		ep := paced("hostile", 10*time.Minute)
		ep.Outcome.Verifier = tc.verdict
		ep.Signals[rl.SigStuckStops], ep.Signals[rl.SigRepeatedReads] = 1, 4
		mustScore(t, ep, &rl.Task{}, cfg, NoDiffs{})
		penalised := ep.Reward.Components[CompWaste] < 0 && ep.Reward.Components[CompGroupITE] < 0
		if penalised != tc.want {
			t.Errorf("%s: waste %v, group_ite %v", tc.name, ep.Reward.Components[CompWaste], ep.Reward.Components[CompGroupITE])
		}
	}
}
