package rl

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/reee344/sleipnir/internal/core"
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
		Edges:   []Edge{{Kind: EdgeSpawn, From: "mgr.1", To: "w1"}},
		Outcome: Outcome{Verifier: &Verdict{Kind: "verifier", Pass: true, Score: 1}},
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
