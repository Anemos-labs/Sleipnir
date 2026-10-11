package rl

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/core"
)

func TestEpisodeRoundTrip(t *testing.T) {
	ep := Episode{
		Schema: SchemaEpisode, ID: "t/0", TaskID: "t", Group: "t@p", Sample: 0,
		Policy: PolicyRef{Model: "m", RoleModels: map[string]string{"worker": "w"}},
		Agents: []Agent{{ID: "mgr", Role: RoleManager, Steps: []Step{{
			ID: "mgr.1", Kind: KindMain, Role: RoleManager, Model: "m", Trainable: true,
			Prompt:     PromptRef{Req: "mgr.1", WireHash: core.Hash("abc"), SharedPrefix: "g1", SharedMessages: 1},
			Completion: Completion{Turn: core.Turn{ID: 3, Role: core.RoleAssistant, Blocks: []core.Block{core.Text("ok")}}, Stop: core.StopEnd},
			Tokens:     &TokenTrace{CompletionIDs: []int32{1, 2}, Logprobs: []float32{-0.1, -0.2}},
		}}}},
		Edges: []Edge{{Kind: EdgeSpawn, From: "mgr.1", To: "w1"}},
		Outcome: Outcome{Verifier: &Verdict{Kind: "verifier", Pass: true, Score: 1,
			Units: map[string]UnitScore{"parser": {Passed: 4, Total: 4, Weight: 2}}, Guards: &GuardScore{Kept: 9, Total: 9}}},
		Reward:  Reward{Total: 0.9, Components: map[string]float64{"outcome": 1, "cost": -0.1}},
		Signals: map[string]float64{SigRequests: 4},
		Flags:   []string{FlagWeakLabel},
	}
	b, err := json.Marshal(ep)
	if err != nil {
		t.Fatal(err)
	}
	var back Episode
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ep, back) {
		t.Fatalf("round trip changed the episode:\n%s", b)
	}
}

// TestVerdictCloneSharesNothing: a cloned verdict's breakdown can be edited without touching the original's.
func TestVerdictCloneSharesNothing(t *testing.T) {
	v := Verdict{Kind: "verifier", Score: 0.5, Units: map[string]UnitScore{"a": {Passed: 1, Total: 2, Weight: 1}}, Guards: &GuardScore{Kept: 1, Total: 2}}
	c := v.Clone()
	c.Units["a"] = UnitScore{}
	c.Units["b"] = UnitScore{}
	c.Guards.Kept = 0
	if len(v.Units) != 1 || v.Units["a"].Passed != 1 || v.Guards.Kept != 1 {
		t.Fatalf("the clone shares state with the original: %+v %+v", v.Units, *v.Guards)
	}
	if e := (Verdict{Kind: "review"}).Clone(); e.Units != nil || e.Guards != nil {
		t.Fatalf("a verdict without a breakdown gains one: %+v", e)
	}
	b, _ := json.Marshal(Verdict{Kind: "verifier", Score: 1})
	if string(b) != `{"kind":"verifier","pass":false,"score":1}` {
		t.Fatalf("a verdict without a breakdown changed its JSON: %s", b)
	}
}

func TestHardFlags(t *testing.T) {
	for _, f := range []string{FlagInfraError, FlagHackProtected, FlagHackNetwork, FlagReplayMismatch} {
		if !HardFlag(f) {
			t.Errorf("%s should be a hard flag", f)
		}
	}
	for _, f := range []string{FlagBudgetExceeded, FlagWeakLabel} {
		if HardFlag(f) {
			t.Errorf("%s should not be a hard flag", f)
		}
	}
	var e Episode
	e.AddFlag(FlagWeakLabel)
	e.AddFlag(FlagWeakLabel)
	if len(e.Flags) != 1 || !e.Has(FlagWeakLabel) {
		t.Fatalf("flags: %v", e.Flags)
	}
}
