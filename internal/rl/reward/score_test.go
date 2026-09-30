package reward

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/rl"
)

func cloneEp(t *testing.T, ep *rl.Episode) *rl.Episode {
	t.Helper()
	b, err := json.Marshal(ep)
	if err != nil {
		t.Fatal(err)
	}
	var out rl.Episode
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return &out
}

func epJSON(t *testing.T, ep *rl.Episode) string {
	t.Helper()
	b, err := json.Marshal(ep)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// singleAgent is a one-agent, three-step episode with known sizes.
func singleAgent() *rl.Episode {
	return mkEpisode("task/0", mkAgent("w1", "worker",
		mkStep("w1.1", withAt(0), withPrompt(5000, ""), withOut(100)),
		mkStep("w1.2", withAt(30*time.Second), withPrompt(5500, ""), withOut(100)),
		mkStep("w1.3", withAt(60*time.Second), withPrompt(6000, ""), withOut(100)),
	))
}

func TestScoreEpisodeComponents(t *testing.T) {
	// Costs for singleAgent under the default target (sonnet, anthropic-like):
	// 5000 written 1.25 + 500 written 1.25 + 500 written 1.25 = 7500 tokens at 1.25,
	// reads 5000 + 5500 at 0.1, outputs 300 at 5. (No shared prefix, so everything
	// is a 5m write.)
	const wantITE = 5000*1.25 + 500*1.25 + 500*1.25 + 0.1*5000 + 0.1*5500 + 300*5
	tests := []struct {
		name  string
		mod   func(ep *rl.Episode)
		task  *rl.Task
		want  map[string]float64
		total float64
		note  string
	}{
		{name: "passing, claimed done, no budgets", task: &rl.Task{},
			want:  map[string]float64{CompOutcome: 1, CompHonestDone: 1, CompCost: 0, CompRequests: 0, CompTime: 0, CompProtocol: 0},
			total: 1.1},
		{name: "false done claim", task: &rl.Task{}, mod: func(ep *rl.Episode) { ep.Outcome.Verifier = failing() },
			want: map[string]float64{CompOutcome: 0, CompHonestDone: -1}, total: -0.2},
		{name: "failed without claiming done", task: &rl.Task{}, mod: func(ep *rl.Episode) {
			ep.Outcome.Verifier = failing()
			ep.Outcome.Claimed = "gave_up"
		}, want: map[string]float64{CompOutcome: 0, CompHonestDone: 0}, total: 0},
		{name: "partial credit does not count as pass", task: &rl.Task{}, mod: func(ep *rl.Episode) {
			ep.Outcome.Verifier = passing(0.6)
		}, want: map[string]float64{CompOutcome: 0.6, CompHonestDone: -1}, total: 0.6 - 0.2},
		{name: "no verdict", task: &rl.Task{}, mod: func(ep *rl.Episode) { ep.Outcome.Verifier = nil },
			want: map[string]float64{CompOutcome: 0, CompHonestDone: 0}, total: 0, note: "no verifier verdict"},
		{name: "pass with score zero is scored as one", task: &rl.Task{}, mod: func(ep *rl.Episode) {
			ep.Outcome.Verifier = &rl.Verdict{Pass: true, Score: 0}
		}, want: map[string]float64{CompOutcome: 1}, total: 1.1, note: "scored as 1"},
		{name: "score above one is clamped", task: &rl.Task{}, mod: func(ep *rl.Episode) { ep.Outcome.Verifier = &rl.Verdict{Pass: true, Score: 7} },
			want: map[string]float64{CompOutcome: 1}, total: 1.1, note: "clamped"},
		{name: "negative score is clamped", task: &rl.Task{}, mod: func(ep *rl.Episode) { ep.Outcome.Verifier = &rl.Verdict{Score: -3} },
			want: map[string]float64{CompOutcome: 0}, total: -0.2, note: "clamped"},
		{name: "NaN score", task: &rl.Task{}, mod: func(ep *rl.Episode) { ep.Outcome.Verifier = &rl.Verdict{Score: math.NaN()} },
			want: map[string]float64{CompOutcome: 0}, total: -0.2, note: "not a finite"},
		{name: "Inf score", task: &rl.Task{}, mod: func(ep *rl.Episode) { ep.Outcome.Verifier = &rl.Verdict{Score: math.Inf(1)} },
			want: map[string]float64{CompOutcome: 0}, total: -0.2, note: "not a finite"},
		{name: "cost is relative to the budget", task: &rl.Task{Budget: rl.Budget{ITE: 4 * wantITE}},
			want: map[string]float64{CompCost: -0.25}, total: 1.1 - 0.25*0.15},
		{name: "cost saturates at one budget", task: &rl.Task{Budget: rl.Budget{ITE: wantITE / 10}},
			want: map[string]float64{CompCost: -1}, total: 1.1 - 0.15},
		{name: "requests relative to budget", task: &rl.Task{Budget: rl.Budget{Requests: 12}},
			want: map[string]float64{CompRequests: -0.25}, total: 1.1 - 0.25*0.05},
		{name: "requests prefer the harness signal when larger", task: &rl.Task{Budget: rl.Budget{Requests: 10}},
			mod:  func(ep *rl.Episode) { ep.Signals = map[string]float64{rl.SigRequests: 5} },
			want: map[string]float64{CompRequests: -0.5}, total: 1.1 - 0.5*0.05},
		{name: "time uses the critical path signal", task: &rl.Task{Budget: rl.Budget{Steps: 20}},
			mod:  func(ep *rl.Episode) { ep.Signals = map[string]float64{rl.SigCriticalPath: 5} },
			want: map[string]float64{CompTime: -0.25}, total: 1.1 - 0.25*0.05},
		{name: "time falls back to the DAG", task: &rl.Task{Budget: rl.Budget{Steps: 6}},
			want: map[string]float64{CompTime: -0.5}, total: 1.1 - 0.5*0.05},
		{name: "zero budgets mean no terms", task: &rl.Task{Budget: rl.Budget{}},
			want: map[string]float64{CompCost: 0, CompRequests: 0, CompTime: 0}, total: 1.1},
		{name: "protocol events from signals", task: &rl.Task{},
			mod: func(ep *rl.Episode) {
				ep.Signals = map[string]float64{rl.SigInvalidToolCalls: 2, rl.SigCompactRejects: 1, rl.SigLeaseConflicts: 1,
					rl.SigScopeViolations: 1, rl.SigStaleWrites: 0, rl.SigToolErrors: 50}
			},
			want: map[string]float64{CompProtocol: -0.5}, total: 1.1 - 0.5*0.1},
		{name: "protocol saturates", task: &rl.Task{},
			mod:  func(ep *rl.Episode) { ep.Signals = map[string]float64{rl.SigStaleWrites: 1000} },
			want: map[string]float64{CompProtocol: -1}, total: 1.1 - 0.1},
		{name: "budget_exceeded counts as one breach", task: &rl.Task{},
			mod:  func(ep *rl.Episode) { ep.Flags = append(ep.Flags, rl.FlagBudgetExceeded) },
			want: map[string]float64{CompProtocol: -0.1}, total: 1.1 - 0.1*0.1},
		{name: "invalid tool calls are derived from the completions when no signal exists", task: &rl.Task{},
			mod: func(ep *rl.Episode) {
				ep.Agents[0].Steps[0].Completion.Turn.Blocks = []core.Block{
					{Kind: core.BlockToolUse, ToolName: "edit", Invalid: "truncated"}, core.ToolUse("c", "read", jsonRaw(map[string]any{"path": "a"}))}
			},
			want: map[string]float64{CompProtocol: -0.1}, total: 1.1 - 0.1*0.1},
		{name: "negative and NaN signals are ignored", task: &rl.Task{},
			mod: func(ep *rl.Episode) {
				ep.Signals = map[string]float64{rl.SigStaleWrites: -4, rl.SigLeaseConflicts: math.NaN()}
			},
			want: map[string]float64{CompProtocol: 0}, total: 1.1},
		{name: "done accepted by the gate with no explicit claim", task: &rl.Task{},
			mod: func(ep *rl.Episode) {
				ep.Outcome.Claimed = ""
				ep.Signals = map[string]float64{rl.SigDoneAccepted: 1}
			},
			want: map[string]float64{CompHonestDone: 1}, total: 1.1},
		{name: "blocked is not a done claim", task: &rl.Task{},
			mod: func(ep *rl.Episode) {
				ep.Outcome.Claimed = "blocked"
				ep.Outcome.Verifier = failing()
			},
			want: map[string]float64{CompHonestDone: 0}, total: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ep := singleAgent()
			if tc.mod != nil {
				tc.mod(ep)
			}
			mustScore(t, ep, tc.task, DefaultConfig(), nil)
			for k, v := range tc.want {
				near(t, k, ep.Reward.Components[k], v)
			}
			near(t, "total", ep.Reward.Total, tc.total)
			if tc.note != "" && !strings.Contains(strings.Join(ep.Reward.Notes, "\n"), tc.note) {
				t.Errorf("notes %q lack %q", ep.Reward.Notes, tc.note)
			}
			if !finite(ep.Reward.Total) {
				t.Error("non-finite total")
			}
		})
	}
	// The cost figure itself is the repriced ITE.
	ep := singleAgent()
	mustScore(t, ep, &rl.Task{}, DefaultConfig(), nil)
	near(t, "ep.Cost.ITE", ep.Cost.ITE, wantITE)
	if ep.Cost.Target == "" {
		t.Error("cost target not recorded")
	}
}

func TestScoreTotalIsTheWeightedSumOfStoredComponents(t *testing.T) {
	ep := singleAgent()
	ep.Signals = map[string]float64{rl.SigStaleWrites: 3, rl.SigCriticalPath: 4}
	task := &rl.Task{Budget: rl.Budget{ITE: 100000, Requests: 20, Steps: 40}}
	cfg := DefaultConfig()
	mustScore(t, ep, task, cfg, nil)
	near(t, "episode total", ep.Reward.Total, cfg.Total(ep.Reward.Components))
	near(t, "agent total", ep.Agents[0].Reward.Total, cfg.Total(ep.Agents[0].Reward.Components))
	// Every documented component is present for a worker.
	for _, k := range []string{CompOutcome, CompHonestDone, CompCost, CompRequests, CompTime, CompProtocol} {
		if _, ok := ep.Reward.Components[k]; !ok {
			t.Errorf("component %s missing", k)
		}
	}
	for _, k := range []string{CompEvidence, CompReread, CompScope} {
		if _, ok := ep.Agents[0].Reward.Components[k]; !ok {
			t.Errorf("worker component %s missing", k)
		}
	}
	// Re-weighting from stored components alone matches a fresh Score under the
	// new weights.
	cfg2 := DefaultConfig()
	cfg2.Weights[CompCost] = 0.9
	cfg2.Weights[CompProtocol] = 0.4
	offline := cfg2.Total(ep.Reward.Components)
	mustScore(t, ep, task, cfg2, nil)
	near(t, "offline reweighting", ep.Reward.Total, offline)
}

func TestScoreIsIdempotentAndRecomputesFromScratch(t *testing.T) {
	ep := singleAgent()
	ep.Signals = map[string]float64{rl.SigRequests: 9}
	task := &rl.Task{Budget: rl.Budget{ITE: 50000, Requests: 30, Steps: 10}}
	mustScore(t, ep, task, DefaultConfig(), nil)
	first := epJSON(t, ep)
	mustScore(t, ep, task, DefaultConfig(), nil)
	if second := epJSON(t, ep); second != first {
		t.Fatalf("Score is not idempotent:\n%s\n%s", first, second)
	}
	// Stale content in the owned fields is discarded.
	ep.Reward.Components["stale"] = 99
	ep.Reward.Notes = append(ep.Reward.Notes, "stale note")
	ep.Reward.Total = 1234
	ep.Agents[0].Reward = rl.Reward{Total: 55, Components: map[string]float64{"junk": 1}}
	mustScore(t, ep, task, DefaultConfig(), nil)
	if again := epJSON(t, ep); again != first {
		t.Fatalf("stale reward state leaked into a re-score:\n%s\n%s", first, again)
	}
	// New weights change totals but not components.
	cfg := DefaultConfig()
	cfg.Weights[CompCost] = 1
	compBefore := copyComps(ep.Reward.Components)
	totalBefore := ep.Reward.Total
	mustScore(t, ep, task, cfg, nil)
	if ep.Reward.Total == totalBefore {
		t.Error("a heavier cost weight must change the total")
	}
	// Components are raw; only the informational role aggregates ("role/...", which
	// are reward totals) move with the weights.
	raw := func(m map[string]float64) map[string]float64 {
		out := map[string]float64{}
		for k, v := range m {
			if !strings.HasPrefix(k, "role/") {
				out[k] = v
			}
		}
		return out
	}
	if !reflect.DeepEqual(raw(compBefore), raw(ep.Reward.Components)) {
		t.Error("components are raw: weights must not change them")
	}
}

func TestScoreDoesNotMutateAnythingItDoesNotOwn(t *testing.T) {
	ep, prompts := compactionEpisode()
	ep.Signals = map[string]float64{rl.SigCompactions: 1, rl.SigRequests: 6}
	ep.Agents[0].Steps[0].Inline = &core.Prompt{Messages: []core.Message{{Role: core.RoleUser, Blocks: []core.Block{core.Text("prompt text")}}}}
	ep.Agents[0].Steps[0].Tokens = &rl.TokenTrace{PromptIDs: []int32{1, 2}, CompletionIDs: []int32{3}}
	before := cloneEp(t, ep)
	cfg := DefaultConfig()
	cfg.Prompts = resolverOf(prompts)
	mustScore(t, ep, &rl.Task{}, cfg, nil)
	// Owned fields aside, the episode is untouched.
	after := cloneEp(t, ep)
	after.Reward, before.Reward = rl.Reward{}, rl.Reward{}
	after.Cost.ITE, after.Cost.Target = 0, ""
	for i := range after.Agents {
		after.Agents[i].Reward, before.Agents[i].Reward = rl.Reward{}, rl.Reward{}
		for j := range after.Agents[i].Steps {
			after.Agents[i].Steps[j].Reward, before.Agents[i].Steps[j].Reward = 0, 0
		}
	}
	if !reflect.DeepEqual(before, after) {
		t.Errorf("Score changed data it does not own:\n%s\n%s", epJSON(t, before), epJSON(t, after))
	}
	// Prompt references and inlined prompts in particular.
	for i := range ep.Agents[0].Steps {
		if !reflect.DeepEqual(ep.Agents[0].Steps[i].Prompt, before.Agents[0].Steps[i].Prompt) || !reflect.DeepEqual(ep.Agents[0].Steps[i].Inline, before.Agents[0].Steps[i].Inline) {
			t.Errorf("step %d prompt changed", i)
		}
	}
}

func TestScoreErrorsLeaveTheEpisodeUntouched(t *testing.T) {
	ep := singleAgent()
	ep.Reward = rl.Reward{Total: 7, Components: map[string]float64{"keep": 1}}
	ep.Cost.ITE = 3
	before := epJSON(t, ep)
	bad := DefaultConfig()
	bad.Weights[CompOutcome] = -1
	if err := Score(ep, &rl.Task{}, bad, nil); err == nil {
		t.Error("invalid config accepted")
	}
	badTarget := DefaultConfig()
	badTarget.TargetName = "nonesuch"
	if err := Score(ep, &rl.Task{}, badTarget, nil); err == nil {
		t.Error("unknown target accepted")
	}
	if err := Score(nil, &rl.Task{}, DefaultConfig(), nil); err == nil {
		t.Error("nil episode accepted")
	}
	if after := epJSON(t, ep); after != before {
		t.Errorf("failed Score changed the episode:\n%s\n%s", before, after)
	}
}

func TestScoreNilTaskAndEmptyEpisodes(t *testing.T) {
	for _, ep := range []*rl.Episode{
		{}, mkEpisode("t/0"), mkEpisode("t/0", mkAgent("a", "worker")),
		mkEpisode("t/0", mkAgent("a", "manager", mkStep("a.1")), mkAgent("b", "compactor"), mkAgent("c", "mailman"), mkAgent("d", "reviewer")),
	} {
		if err := Score(ep, nil, DefaultConfig(), nil); err != nil {
			t.Fatalf("Score: %v", err)
		}
		if !finite(ep.Reward.Total) {
			t.Errorf("non-finite total: %v", ep.Reward.Total)
		}
		for _, a := range ep.Agents {
			if !finite(a.Reward.Total) {
				t.Errorf("agent %s: non-finite total", a.ID)
			}
		}
	}
	// A zero Config scores like the defaults.
	a, b := singleAgent(), singleAgent()
	mustScore(t, a, &rl.Task{}, Config{}, nil)
	mustScore(t, b, &rl.Task{}, DefaultConfig(), nil)
	near(t, "zero cfg", a.Reward.Total, b.Reward.Total)
}

func TestScoreClip(t *testing.T) {
	ep := singleAgent()
	cfg := DefaultConfig()
	cfg.Weights[CompOutcome] = 10
	cfg.Clip = [2]float64{-1, 2}
	mustScore(t, ep, &rl.Task{}, cfg, nil)
	near(t, "clipped total", ep.Reward.Total, 2)
	near(t, "clipped agent", ep.Agents[0].Reward.Total, 2)
	near(t, "components stay raw", ep.Reward.Components[CompOutcome], 1)
	ep.Outcome.Verifier = failing()
	cfg.Weights[CompFalseDone] = 50
	mustScore(t, ep, &rl.Task{}, cfg, nil)
	near(t, "lower bound", ep.Reward.Total, -1)
}

func TestScoreWorkerEvidenceFromToolCalls(t *testing.T) {
	done := func() rl.Observation {
		return obs("task", map[string]any{"action": "done", "id": "t1", "text": "ok"}, "in review")
	}
	testRun := func() rl.Observation { return bashObs("go test ./... -count=1", "ok") }
	mk := func(steps ...rl.Step) *rl.Episode {
		return mkEpisode("t/0", mkAgent("w", "worker", steps...))
	}
	tests := []struct {
		name string
		ep   *rl.Episode
		task *rl.Task
		want float64
	}{
		{"ran the checks then claimed", mk(mkStep("w.1", withObs(testRun())), mkStep("w.2", withObs(done()))), &rl.Task{}, 1},
		{"claimed without running anything", mk(mkStep("w.1", withObs(bashObs("ls", "a"))), mkStep("w.2", withObs(done()))), &rl.Task{}, 0},
		{"never claimed", mk(mkStep("w.1", withObs(testRun()))), &rl.Task{}, 0},
		{"first claim backed, re-claim after a failed gate is not", mk(mkStep("w.1", withObs(testRun(), done())), mkStep("w.2", withObs(done()))), &rl.Task{}, 0.5},
		{"each claim needs its own run", mk(mkStep("w.1", withObs(testRun(), done())), mkStep("w.2", withObs(testRun(), done()))), &rl.Task{}, 1},
		{"the task's own verifier command counts", mk(mkStep("w.1", withObs(bashObs("./verify --strict", "pass"))), mkStep("w.2", withObs(done()))),
			&rl.Task{Verifier: rl.Verifier{Cmd: "./verify   --strict"}}, 1},
		{"a run after the claim does not help", mk(mkStep("w.1", withObs(done())), mkStep("w.2", withObs(testRun()))), &rl.Task{}, 0},
		{"claims recorded as tool_use blocks only", mk(mkStep("w.1", func(s *rl.Step) {
			s.Completion.Turn.Blocks = []core.Block{core.ToolUse("a", "bash", jsonRaw(map[string]any{"command": "pytest -q"}))}
		}), mkStep("w.2", func(s *rl.Step) {
			s.Completion.Turn.Blocks = []core.Block{core.ToolUse("b", "task", jsonRaw(map[string]any{"action": "done"}))}
		})), &rl.Task{}, 1},
		{"task calls other than done are not claims", mk(mkStep("w.1", withObs(obs("task", map[string]any{"action": "claim", "id": "t1"}, "yours")))), &rl.Task{}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mustScore(t, tc.ep, tc.task, DefaultConfig(), nil)
			near(t, "evidence", tc.ep.Agents[0].Reward.Components[CompEvidence], tc.want)
		})
	}
	// With no tool calls recorded, the episode-level signals stand in.
	ep := mkEpisode("t/0", mkAgent("w", "worker", mkStep("w.1")))
	ep.Signals = map[string]float64{rl.SigDoneClaims: 2, rl.SigVerifierRuns: 1}
	mustScore(t, ep, &rl.Task{}, DefaultConfig(), nil)
	near(t, "signal evidence", ep.Agents[0].Reward.Components[CompEvidence], 0.5)
	ep.Signals[rl.SigVerifierRuns] = 9
	mustScore(t, ep, &rl.Task{}, DefaultConfig(), nil)
	near(t, "signal evidence capped", ep.Agents[0].Reward.Components[CompEvidence], 1)
	ep.Signals = nil
	mustScore(t, ep, &rl.Task{}, DefaultConfig(), nil)
	near(t, "no signals", ep.Agents[0].Reward.Components[CompEvidence], 0)
}

func TestScoreWorkerTermsAndRoleReward(t *testing.T) {
	ep := singleAgent()
	ep.Signals = map[string]float64{rl.SigReReads: 2, rl.SigScopeViolations: 1}
	mustScore(t, ep, &rl.Task{}, DefaultConfig(), nil)
	c := ep.Agents[0].Reward.Components
	near(t, "reread", c[CompReread], -2.0/5)
	near(t, "scope", c[CompScope], -1.0/3)
	// Role reward = episode components + worker terms.
	want := ep.Reward.Total + 0.05*(-0.4) + 0.1*(-1.0/3) + 0.1*c[CompEvidence] +
		// scope violations also count as protocol events at the episode level
		0
	near(t, "worker reward", ep.Agents[0].Reward.Total, want)
	near(t, "role aggregate", ep.Reward.Components["role/worker"], ep.Agents[0].Reward.Total)
	if _, ok := ep.Agents[0].Reward.Components[CompParallel]; ok {
		t.Error("worker must not carry manager terms")
	}
}

func TestScoreManagerTerms(t *testing.T) {
	mgr := mkAgent("m", "manager", mkStep("m.1", withAt(0)), mkStep("m.2", withAt(time.Minute)))
	w1 := mkAgent("w1", "worker", mkStep("w1.1", withAt(10*time.Second)), mkStep("w1.2", withAt(20*time.Second)), mkStep("w1.3", withAt(30*time.Second)))
	w1.Parent = "m"
	w2 := mkAgent("w2", "worker", mkStep("w2.1", withAt(10*time.Second)), mkStep("w2.2", withAt(20*time.Second)), mkStep("w2.3", withAt(30*time.Second)))
	w2.Parent = "m"
	ep := mkEpisode("t/0", mgr, w1, w2)
	ep.Edges = []rl.Edge{
		{Kind: rl.EdgeSpawn, From: "m.1", To: "w1"}, {Kind: rl.EdgeSpawn, From: "m.1", To: "w2"},
		{Kind: rl.EdgeMail, From: "w1.3", To: "m.2"}, {Kind: rl.EdgeMail, From: "w2.3", To: "m.2"},
	}
	ep.Signals = map[string]float64{rl.SigDuplicateWork: 5, rl.SigLeaseConflicts: 2, rl.SigIdleMs: 60000, rl.SigSpawnNoResult: 1, rl.SigSpawns: 2}
	mustScore(t, ep, &rl.Task{}, DefaultConfig(), nil)
	c := ep.Agents[0].Reward.Components
	// critical path m.1 -> w1.1..3 -> m.2 = 5 steps; worker steps = 6 -> ratio 1.2, cap 4.
	near(t, "parallel_efficiency", c[CompParallel], 1.2/4)
	near(t, "duplicate_work", c[CompDuplicate], -0.5)
	near(t, "conflicts", c[CompConflicts], -0.4)
	near(t, "idle", c[CompIdle], -0.5)
	near(t, "over_spawn", c[CompOverSpawn], -0.5)
	// Workers carry worker terms only.
	if _, ok := ep.Agents[1].Reward.Components[CompDuplicate]; ok {
		t.Error("worker carries manager terms")
	}
	want := ep.Reward.Total + 0.1*(1.2/4) + 0.1*(-0.5) + 0.1*(-0.4) + 0.05*(-0.5) + 0.1*(-0.5)
	near(t, "manager reward", ep.Agents[0].Reward.Total, want)
	// The DAG critical path is used when the signal is absent, and the signal wins when present.
	ep.Signals[rl.SigCriticalPath] = 2
	mustScore(t, ep, &rl.Task{}, DefaultConfig(), nil)
	near(t, "signal critical path", ep.Agents[0].Reward.Components[CompParallel], 3.0/4) // 6/2 = 3
	// Fully sequential work earns little; capped at the cap.
	ep.Signals[rl.SigCriticalPath] = 0.5
	mustScore(t, ep, &rl.Task{}, DefaultConfig(), nil)
	near(t, "capped", ep.Agents[0].Reward.Components[CompParallel], 1)
}

func TestCriticalPath(t *testing.T) {
	steps := func(id string, n int) []rl.Step {
		var out []rl.Step
		for i := 1; i <= n; i++ {
			out = append(out, mkStep(fmt.Sprintf("%s.%d", id, i)))
		}
		return out
	}
	mk := func(edges ...rl.Edge) *rl.Episode {
		ep := mkEpisode("t/0", mkAgent("m", "manager", steps("m", 3)...), mkAgent("a", "worker", steps("a", 4)...), mkAgent("b", "worker", steps("b", 2)...))
		ep.Edges = edges
		return ep
	}
	tests := []struct {
		name  string
		edges []rl.Edge
		want  int
		ok    bool
	}{
		{"no edges: longest single agent", nil, 4, true},
		{"spawn by step and agent id", []rl.Edge{{Kind: rl.EdgeSpawn, From: "m.1", To: "a"}}, 1 + 4 + 0 + 0, true}, // m.1 -> a.1..a.4 = 5; m.2/m.3 parallel
		{"spawn then join by mail", []rl.Edge{{Kind: rl.EdgeSpawn, From: "m.1", To: "a"}, {Kind: rl.EdgeMail, From: "a.4", To: "m.3"}}, 1 + 4 + 1, true},
		{"two workers, the longer decides", []rl.Edge{{Kind: rl.EdgeSpawn, From: "m.1", To: "a"}, {Kind: rl.EdgeSpawn, From: "m.1", To: "b"}, {Kind: rl.EdgeMail, From: "b.2", To: "m.3"}, {Kind: rl.EdgeMail, From: "a.4", To: "m.2"}}, 1 + 4 + 2, true},
		{"unknown endpoints are ignored", []rl.Edge{{Kind: rl.EdgeSpawn, From: "nobody", To: "a"}, {Kind: rl.EdgeMail, From: "a.1", To: "ghost"}}, 4, true},
		{"other edge kinds are not causal for the path", []rl.Edge{{Kind: rl.EdgeLease, From: "a.4", To: "m.3"}}, 4, true},
		{"a cycle falls back to the single-agent chain", []rl.Edge{{Kind: rl.EdgeMail, From: "a.4", To: "m.1"}, {Kind: rl.EdgeSpawn, From: "m.3", To: "a"}}, 4, false},
	}
	for _, tc := range tests {
		got, ok := criticalPath(mk(tc.edges...))
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: critical path = %d,%v want %d,%v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
	if got, ok := criticalPath(&rl.Episode{}); got != 0 || !ok {
		t.Errorf("empty: %d %v", got, ok)
	}
	// Compactor steps are not part of the work path.
	ep := mkEpisode("t/0", mkAgent("a", "worker", mkStep("a.1"), mkStep("a.c", withKind(rl.KindCompactor)), mkStep("a.2")))
	if got, _ := criticalPath(ep); got != 2 {
		t.Errorf("forks must not count: %d", got)
	}
}

func TestScoreMailUse(t *testing.T) {
	ep := mkEpisode("t/0",
		mkAgent("w", "worker", mkStep("w.1")),
		mkAgent("mail", "mailman", mkStep("mail.1", withKind(rl.KindMailman)), mkStep("mail.2", withKind(rl.KindMailman))))
	ep.Signals = map[string]float64{rl.SigMailSent: 10, rl.SigMailIgnored: 3, rl.SigMailDuplicate: 1}
	mustScore(t, ep, &rl.Task{}, DefaultConfig(), nil)
	mc := ep.Agents[1].Reward.Components
	near(t, "mail_useful", mc[CompMailUseful], 0.6) // 6/10 acted on, verifier passed (score 1)
	near(t, "mail_spam", mc[CompMailSpam], -0.4)
	near(t, "downstream", mc[CompDownstream], ep.Reward.Total)
	want := ep.Reward.Total + 0.1*0.6 + 0.1*(-0.4)
	near(t, "mailman reward", ep.Agents[1].Reward.Total, want)
	for i := range ep.Agents[1].Steps {
		near(t, "mail step reward", ep.Agents[1].Steps[i].Reward, want)
	}
	near(t, "aggregate", ep.Reward.Components["role/mailman"], want)
	// Useful mail in a failed episode is worth nothing.
	ep.Outcome.Verifier = failing()
	mustScore(t, ep, &rl.Task{}, DefaultConfig(), nil)
	near(t, "useful only if the team passed", ep.Agents[1].Reward.Components[CompMailUseful], 0)
	// No mail, no terms.
	ep.Signals = nil
	mustScore(t, ep, &rl.Task{}, DefaultConfig(), nil)
	near(t, "no mail", ep.Agents[1].Reward.Components[CompMailSpam], 0)
	// A hacked episode earns no mail credit.
	ep.Outcome.Verifier = passing(1)
	ep.Signals = map[string]float64{rl.SigMailSent: 4}
	ep.Flags = append(ep.Flags, rl.FlagHackProtected)
	mustScore(t, ep, &rl.Task{}, DefaultConfig(), nil)
	near(t, "hacked", ep.Agents[1].Reward.Components[CompMailUseful], 0)
}

func TestScoreCompactorEvents(t *testing.T) {
	ep, prompts := compactionEpisode()
	ep.Signals = map[string]float64{rl.SigCompactions: 1}
	cfg := DefaultConfig()
	cfg.Prompts = resolverOf(prompts)
	mustScore(t, ep, &rl.Task{}, cfg, nil)

	comps := ep.Agents[0].Reward.Components
	get := func(name string) float64 { return comps["compactor/A.c1/"+name] }
	near(t, "valid", get(CompValid), 1)
	// size: before = 4800 (A.4), after = 2600 (A.5), base = 3000 (first request)
	// thread = 1800, kept = -400 -> clamped to 0 kept, ratio 0.
	near(t, "size", get(CompSize), 0)
	fid := get(CompFidelity)
	if fid <= 0 || fid >= 1 {
		t.Errorf("fidelity should be a partial recall, got %v", fid)
	}
	near(t, "downstream", get(CompDownstream), ep.Reward.Total)
	if get(CompRebase) >= 0 {
		t.Errorf("rebase cost should be negative (A.5 writes its rebuilt prefix): %v", get(CompRebase))
	}
	step := ep.Agents[0].Steps[4]
	want := 0.1*get(CompValid) + 0.1*get(CompSize) + 0.2*fid + 0.1*get(CompRebase) + 1.0*ep.Reward.Total
	near(t, "compactor step reward", step.Reward, want)
	near(t, "role aggregate", ep.Reward.Components["role/compactor"], want)
	near(t, "agent-level aggregate", comps["role/compactor"], want)
	// Main steps keep their zero step reward; the agent carries the worker reward.
	if ep.Agents[0].Steps[0].Reward != 0 {
		t.Error("main steps do not get step rewards")
	}
	// The weights combine per-event components offline too.
	sub := map[string]float64{}
	for _, k := range []string{CompValid, CompSize, CompFidelity, CompRebase, CompDownstream} {
		sub[k] = get(k)
	}
	near(t, "offline", cfg.Total(sub), want)
}

func TestScoreCompactorValidity(t *testing.T) {
	build := func(replies ...string) *rl.Episode {
		var steps []rl.Step
		steps = append(steps, mkStep("A.1", withPrompt(3000, ""), withAt(0)))
		for i, r := range replies {
			steps = append(steps, mkStep(fmt.Sprintf("A.c%d", i), withKind(rl.KindCompactor), withPrompt(4000, ""), withText(r), withAt(time.Duration(i+1)*time.Minute)))
		}
		return mkEpisode("t/0", mkAgent("A", "worker", steps...))
	}
	good := `{"keep_from":"t3","spine":[]}`
	tests := []struct {
		name    string
		replies []string
		signals map[string]float64
		want    []float64
	}{
		{"all good", []string{good, good}, nil, []float64{1, 1}},
		{"malformed reply is definitely invalid", []string{good, "sorry, I cannot"}, nil, []float64{1, 0}},
		{"no keep_from", []string{`{"spine":[]}`}, nil, []float64{0}},
		{"rejects spread over the compactions", []string{good, good, good, good}, map[string]float64{rl.SigCompactRejects: 3}, []float64{0.25, 0.25, 0.25, 0.25}},
		{"known invalid steps account for rejects first", []string{good, good, "junk"}, map[string]float64{rl.SigCompactRejects: 1}, []float64{1, 1, 0}},
		{"more rejects than compactions saturate", []string{good}, map[string]float64{rl.SigCompactRejects: 9}, []float64{0}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ep := build(tc.replies...)
			ep.Signals = tc.signals
			mustScore(t, ep, &rl.Task{}, DefaultConfig(), nil)
			for i, want := range tc.want {
				near(t, fmt.Sprintf("valid[%d]", i), ep.Agents[0].Reward.Components[fmt.Sprintf("compactor/A.c%d/valid", i)], want)
			}
		})
	}
	// max_tokens, refusal and an error observation invalidate a reply that would parse.
	for name, mod := range map[string]stepOpt{
		"max tokens": withStop(core.StopMaxTokens), "refusal": withStop(core.StopRefusal),
		"error observation": withObs(obsErr("compact", map[string]any{}, "patch rejected: nothing to compact")),
	} {
		ep := build(good)
		mod(&ep.Agents[0].Steps[1])
		mustScore(t, ep, &rl.Task{}, DefaultConfig(), nil)
		near(t, name, ep.Agents[0].Reward.Components["compactor/A.c0/valid"], 0)
	}
}

func TestScoreCompactorSizeAndRebaseCost(t *testing.T) {
	mk := func(afterTokens int) *rl.Episode {
		return mkEpisode("t/0", mkAgent("A", "worker",
			mkStep("A.1", withAt(0), withPrompt(3000, "")),
			mkStep("A.2", withAt(time.Minute), withPrompt(23000, "")),
			mkStep("A.c", withKind(rl.KindCompactor), withAt(2*time.Minute), withPrompt(24500, ""), withText(`{"keep_from":"t9"}`)),
			mkStep("A.3", withAt(3*time.Minute), withSeg(1, 1), withPrompt(afterTokens, "")),
		))
	}
	// thread = 20000 (23000 - 3000 base). Kept 4000 -> ratio 0.2.
	ep := mk(7000)
	mustScore(t, ep, &rl.Task{}, DefaultConfig(), nil)
	near(t, "size", ep.Agents[0].Reward.Components["compactor/A.c/size"], -0.2)
	// The next request rewrites its prompt: with no shared prefix it is one 5m
	// write of 7000 tokens = 8750 ITE, against a 60000 cap.
	near(t, "rebase", ep.Agents[0].Reward.Components["compactor/A.c/rebase_cost"], -8750.0/60000)
	// A verbatim copy keeps the whole thread: ratio 1.
	ep = mk(23000)
	mustScore(t, ep, &rl.Task{}, DefaultConfig(), nil)
	near(t, "verbatim size", ep.Agents[0].Reward.Components["compactor/A.c/size"], -1)
	// A prompt that grew (held commit) is capped at 1.
	ep = mk(40000)
	mustScore(t, ep, &rl.Task{}, DefaultConfig(), nil)
	near(t, "grown size", ep.Agents[0].Reward.Components["compactor/A.c/size"], -1)
	// The cap comes from the config.
	cfg := DefaultConfig()
	cfg.Caps[CapRebase] = 17500
	ep = mk(7000)
	mustScore(t, ep, &rl.Task{}, cfg, nil)
	near(t, "rebase with cap", ep.Agents[0].Reward.Components["compactor/A.c/rebase_cost"], -0.5)
	// No thread to fold (before <= base): worst size.
	ep = mkEpisode("t/0", mkAgent("A", "worker",
		mkStep("A.1", withAt(0), withPrompt(3000, "")),
		mkStep("A.c", withKind(rl.KindCompactor), withAt(time.Minute), withPrompt(3500, ""), withText(`{"keep_from":"t1"}`)),
		mkStep("A.2", withAt(2*time.Minute), withSeg(1, 1), withPrompt(2900, ""))))
	mustScore(t, ep, &rl.Task{}, DefaultConfig(), nil)
	near(t, "nothing folded", ep.Agents[0].Reward.Components["compactor/A.c/size"], -1)
}

func TestScoreCompactorWithoutNextStepOrProbeSource(t *testing.T) {
	ep, _ := compactionEpisode()
	ep.Agents[0].Steps = ep.Agents[0].Steps[:5]
	mustScore(t, ep, &rl.Task{}, DefaultConfig(), nil)
	c := ep.Agents[0].Reward.Components
	if _, ok := c["compactor/A.c1/size"]; ok {
		t.Error("no next step: size is unknown, not zero")
	}
	if !strings.Contains(strings.Join(ep.Reward.Notes, "\n"), "before the compaction took effect") {
		t.Errorf("notes: %q", ep.Reward.Notes)
	}
	// Probes on, no source anywhere: skipped with a note, never scored as zero.
	ep, _ = compactionEpisode()
	mustScore(t, ep, &rl.Task{}, DefaultConfig(), nil)
	if _, ok := ep.Agents[0].Reward.Components["compactor/A.c1/fidelity"]; ok {
		t.Error("fidelity without a prompt source must be absent")
	}
	if !strings.Contains(strings.Join(ep.Reward.Notes, "\n"), "fidelity probes skipped") {
		t.Errorf("notes: %q", ep.Reward.Notes)
	}
	// Probes off: no note, no component.
	off := DefaultConfig()
	off.Probes = false
	ep, _ = compactionEpisode()
	mustScore(t, ep, &rl.Task{}, off, nil)
	if strings.Contains(strings.Join(ep.Reward.Notes, "\n"), "probes") {
		t.Errorf("notes: %q", ep.Reward.Notes)
	}
	// A DiffSource that also serves prompts is used.
	ep, prompts := compactionEpisode()
	mustScore(t, ep, &rl.Task{}, DefaultConfig(), promptDiffs{DiffMap{}, prompts})
	if _, ok := ep.Agents[0].Reward.Components["compactor/A.c1/fidelity"]; !ok {
		t.Errorf("PromptSource on the DiffSource ignored: %q", ep.Reward.Notes)
	}
	// Inlined prompts work without any resolver.
	ep, prompts = compactionEpisode()
	for i := range ep.Agents[0].Steps {
		ep.Agents[0].Steps[i].Inline = &core.Prompt{Messages: []core.Message{{Role: core.RoleUser, Blocks: []core.Block{core.Text(prompts[ep.Agents[0].Steps[i].ID])}}}}
	}
	mustScore(t, ep, &rl.Task{}, DefaultConfig(), nil)
	if _, ok := ep.Agents[0].Reward.Components["compactor/A.c1/fidelity"]; !ok {
		t.Errorf("inline prompts ignored: %q", ep.Reward.Notes)
	}
	// A resolver that fails does not fail Score; the probe is skipped and noted.
	ep, _ = compactionEpisode()
	cfg := DefaultConfig()
	cfg.Prompts = func(*rl.Episode, *rl.Step) (string, error) { return "", fmt.Errorf("blob store offline") }
	mustScore(t, ep, &rl.Task{}, cfg, nil)
	if !strings.Contains(strings.Join(ep.Reward.Notes, "\n"), "blob store offline") {
		t.Errorf("notes: %q", ep.Reward.Notes)
	}
}

type promptDiffs struct {
	DiffMap
	prompts map[string]string
}

func (p promptDiffs) PromptText(_ *rl.Episode, st *rl.Step) (string, error) {
	if s, ok := p.prompts[st.ID]; ok {
		return s, nil
	}
	return "", fmt.Errorf("no prompt for %s", st.ID)
}

func TestScoreHackedEpisodeGetsNoOutcomeCredit(t *testing.T) {
	ep := singleAgent()
	ep.Flags = []string{rl.FlagHackNetwork}
	mustScore(t, ep, &rl.Task{}, DefaultConfig(), nil)
	near(t, "outcome", ep.Reward.Components[CompOutcome], 0)
	// Claiming done on a hacked pass is dishonest.
	near(t, "honest_done", ep.Reward.Components[CompHonestDone], -1)
	near(t, "total", ep.Reward.Total, -0.2)
	// Non-hack flags do not zero anything.
	ep = singleAgent()
	ep.Flags = []string{rl.FlagWeakLabel, rl.FlagBudgetExceeded}
	mustScore(t, ep, &rl.Task{}, DefaultConfig(), nil)
	near(t, "outcome with weak label", ep.Reward.Components[CompOutcome], 1)
}

func TestScoreNotesAreCappedAndDeterministic(t *testing.T) {
	var lines []string
	for i := 0; i < 60; i++ {
		lines = append(lines, fmt.Sprintf("+\tt.Skip(%d)", i))
	}
	// One file per skip so each becomes its own note.
	var diff strings.Builder
	for i := 0; i < 60; i++ {
		diff.WriteString(gitDiff(fmt.Sprintf("pkg/f%02d_test.go", i), hunkOf(lines[i])))
	}
	ep := scoreDiff(t, nil, diff.String())
	if len(ep.Reward.Notes) > 24 {
		t.Errorf("notes not capped: %d", len(ep.Reward.Notes))
	}
	last := ep.Reward.Notes[len(ep.Reward.Notes)-1]
	if !strings.Contains(last, "more notes") {
		t.Errorf("cap marker missing: %q", last)
	}
	again := scoreDiff(t, nil, diff.String())
	if !reflect.DeepEqual(ep.Reward.Notes, again.Reward.Notes) {
		t.Error("notes differ between runs")
	}
}

func TestScoreIsSafeToRunConcurrently(t *testing.T) {
	var wg sync.WaitGroup
	results := make([]float64, 8)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ep, prompts := compactionEpisode()
			cfg := DefaultConfig()
			cfg.Prompts = resolverOf(prompts)
			if err := Score(ep, &rl.Task{Budget: rl.Budget{ITE: 1e5}}, cfg, nil); err != nil {
				t.Error(err)
				return
			}
			results[i] = ep.Reward.Total
		}(i)
	}
	wg.Wait()
	for _, r := range results {
		if r != results[0] {
			t.Errorf("concurrent scoring differs: %v", results)
			break
		}
	}
}

func TestScoreWithRealisticSwarm(t *testing.T) {
	// Manager + two workers + compactor forks + mail: every role gets a reward and
	// the per-role aggregates are informational only.
	mgr := mkAgent("m", "manager",
		mkStep("m.1", withAt(0), withPrompt(9000, "M"), withOut(300)),
		mkStep("m.2", withAt(2*time.Minute), withPrompt(9800, "M"), withOut(200)))
	mkWorker := func(id string, at time.Duration) rl.Agent {
		w := mkAgent(id, "worker",
			mkStep(id+".1", withAt(at), withPrompt(8000, "W"), withOut(100)),
			mkStep(id+".2", withAt(at+30*time.Second), withPrompt(8600, "W"), withOut(100), withObs(bashObs("go test ./...", "ok"))),
			mkStep(id+".c", withKind(rl.KindCompactor), withAt(at+time.Minute), withPrompt(9500, "W"), withOut(60), withText(`{"keep_from":"t2"}`)),
			mkStep(id+".3", withAt(at+90*time.Second), withSeg(1, 1), withPrompt(7000, "W"), withOut(100), withObs(obs("task", map[string]any{"action": "done"}, "ok"))))
		w.Parent = "m"
		return w
	}
	ep := mkEpisode("swarm/0", mgr, mkWorker("w1", 5*time.Second), mkWorker("w2", 8*time.Second))
	ep.Edges = []rl.Edge{{Kind: rl.EdgeSpawn, From: "m.1", To: "w1"}, {Kind: rl.EdgeSpawn, From: "m.1", To: "w2"},
		{Kind: rl.EdgeMail, From: "w1.3", To: "m.2"}, {Kind: rl.EdgeMail, From: "w2.3", To: "m.2"}}
	ep.Signals = map[string]float64{rl.SigWorkerSteps: 6, rl.SigSpawns: 2, rl.SigCompactions: 2}
	task := &rl.Task{Kind: rl.TaskSwarm, Budget: rl.Budget{ITE: 500000, Steps: 30, Requests: 40}}
	cfg := DefaultConfig()
	cfg.Probes = false
	mustScore(t, ep, task, cfg, nil)
	for _, a := range ep.Agents {
		if !finite(a.Reward.Total) {
			t.Errorf("agent %s total %v", a.ID, a.Reward.Total)
		}
	}
	if ep.Reward.Components["role/manager"] == 0 || ep.Reward.Components["role/worker"] == 0 || ep.Reward.Components["role/compactor"] == 0 {
		t.Errorf("role aggregates: %v", ep.Reward.Components)
	}
	// Both workers have compactor steps rewarded on their own.
	for _, i := range []int{1, 2} {
		if ep.Agents[i].Steps[2].Reward == 0 {
			t.Errorf("worker %d compactor step has no reward", i)
		}
	}
	// The informational keys never enter the weighted sum.
	near(t, "total ignores aggregates", ep.Reward.Total, cfg.Total(ep.Reward.Components))
	// Workers evidenced their claims (tests were run before done in the same agent).
	near(t, "worker evidence", ep.Agents[1].Reward.Components[CompEvidence], 1)
}
