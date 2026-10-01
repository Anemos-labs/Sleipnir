package adv

import (
	"fmt"
	"math"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/rl"
)

// anchorEps builds len(rewards) episodes, each with one worker agent whose
// steps carry the given wire hashes: hashes[i][t] is episode i's step t.
func anchorEps(rewards []float64, hashes [][]string) []*rl.Episode {
	var eps []*rl.Episode
	for i, r := range rewards {
		a := mkAgent("w", "worker", r, len(hashes[i]))
		for t, h := range hashes[i] {
			a.Steps[t].Prompt.WireHash = core.Hash(h)
		}
		ep := mkEp(fmt.Sprintf("t/%d", i), "t", a)
		ep.Env.TreeHash = "tree1"
		eps = append(eps, ep)
	}
	return eps
}

func TestAnchorFormula(t *testing.T) {
	rewards := []float64{1, 0, 1, 0}
	hashes := [][]string{{"S0", "A"}, {"S0", "A"}, {"S0", "B"}, {"S0", "C"}}
	eps := anchorEps(rewards, hashes)
	s := DefaultSpec()
	s.Method = MethodAnchor
	rep := mustApply(t, eps, s)

	mean, std := meanStdRef(rewards)
	unit := func(i int) float64 { return (rewards[i] - mean) / (std + DefaultEps) }
	// S0 is seen by all four rollouts: returns equal the rewards (gamma 1).
	z0 := func(i int) float64 { return (rewards[i] - mean) / (std + DefaultEps) }
	// A is seen by rollouts 0 and 1: returns [1, 0].
	am, as := meanStdRef([]float64{1, 0})
	zA := func(r float64) float64 { return (r - am) / (as + DefaultEps) }

	near(t, "ep0 step0", eps[0].Agents[0].Steps[0].Advantage, unit(0)+z0(0))
	near(t, "ep1 step0", eps[1].Agents[0].Steps[0].Advantage, unit(1)+z0(1))
	near(t, "ep0 step1 (shared anchor A)", eps[0].Agents[0].Steps[1].Advantage, unit(0)+zA(1))
	near(t, "ep1 step1 (shared anchor A)", eps[1].Agents[0].Steps[1].Advantage, unit(1)+zA(0))
	// B and C are reached by one rollout each: no correction.
	near(t, "ep2 step1 (private state)", eps[2].Agents[0].Steps[1].Advantage, unit(2))
	near(t, "ep3 step1 (private state)", eps[3].Agents[0].Steps[1].Advantage, unit(3))

	if rep.Anchor.Groups != 2 || rep.Anchor.MaxSize != 4 || rep.Anchor.Steps != 6 || rep.Anchor.Singleton != 2 {
		t.Errorf("anchor stats: %+v", rep.Anchor)
	}
	near(t, "mean anchor size", rep.Anchor.MeanSize, 3)
}

func TestAnchorWeightAndGamma(t *testing.T) {
	rewards := []float64{1, 0}
	hashes := [][]string{{"S0", "S1", "S2"}, {"S0", "S1", "S2"}}
	// Gamma < 1: earlier steps see a discounted return, so states shared at every
	// depth give per-step corrections of the same sign but different scale.
	eps := anchorEps(rewards, hashes)
	s := DefaultSpec()
	s.Method = MethodAnchor
	s.Gamma = 0.5
	s.AnchorWeight = 2
	mustApply(t, eps, s)
	mean, std := meanStdRef(rewards)
	unit0 := (1 - mean) / (std + DefaultEps)
	for step, disc := range []float64{0.25, 0.5, 1} { // gamma^(T-t) for T=2
		rets := []float64{1 * disc, 0}
		m, sd := meanStdRef(rets)
		z := (rets[0] - m) / (sd + DefaultEps)
		near(t, fmt.Sprintf("step %d", step), eps[0].Agents[0].Steps[step].Advantage, unit0+2*z)
	}
	// AnchorWeight is a plain multiplier; tiny weight reduces to grpo.
	eps = anchorEps(rewards, hashes)
	s.AnchorWeight, s.Gamma = 1e-12, 0
	mustApply(t, eps, s)
	near(t, "weight ~0", eps[0].Agents[0].Steps[1].Advantage, unit0)
}

func TestAnchorRequiresDistinctEpisodesAndSize(t *testing.T) {
	// The same state seen twice by ONE rollout is not an anchor group.
	rewards := []float64{1, 0}
	hashes := [][]string{{"S", "S", "S"}, {"X", "Y", "Z"}}
	eps := anchorEps(rewards, hashes)
	s := DefaultSpec()
	s.Method = MethodAnchor
	rep := mustApply(t, eps, s)
	mean, std := meanStdRef(rewards)
	for i := range eps[0].Agents[0].Steps {
		near(t, "no correction", eps[0].Agents[0].Steps[i].Advantage, (1-mean)/(std+DefaultEps))
	}
	if rep.Anchor.Groups != 0 || rep.Anchor.Singleton != 4 {
		t.Errorf("stats: %+v", rep.Anchor)
	}
	// AnchorMin above the group size disables it.
	eps = anchorEps([]float64{1, 0}, [][]string{{"S"}, {"S"}})
	s.AnchorMin = 3
	rep = mustApply(t, eps, s)
	if rep.Anchor.Steps != 0 {
		t.Errorf("AnchorMin ignored: %+v", rep.Anchor)
	}
}

func TestAnchorSkipsFlatBaselineGroups(t *testing.T) {
	// All rewards equal: there is no signal, and a discount factor must not invent
	// one out of the different remaining horizons.
	eps := anchorEps([]float64{1, 1, 1}, [][]string{{"S0", "S1"}, {"S0", "S1"}, {"S0", "S1"}})
	s := DefaultSpec()
	s.Method = MethodAnchor
	s.Gamma = 0.5
	rep := mustApply(t, eps, s)
	for _, ep := range eps {
		for _, a := range advOf(ep, 0) {
			if a != 0 {
				t.Errorf("flat group produced advantage %v", a)
			}
		}
	}
	if rep.Anchor.Groups != 0 {
		t.Errorf("stats: %+v", rep.Anchor)
	}
	// Identical outcomes from a state add nothing even in a live group: rollouts 0
	// and 2 both pass from state P.
	eps = anchorEps([]float64{1, 0, 1}, [][]string{{"S", "P"}, {"S", "Q"}, {"S", "P"}})
	s.Gamma = 0
	mustApply(t, eps, s)
	mean, std := meanStdRef([]float64{1, 0, 1})
	unit := func(r float64) float64 { return (r - mean) / (std + DefaultEps) }
	near(t, "identical outcomes: no step credit", eps[0].Agents[0].Steps[1].Advantage, unit(1))
}

func TestAnchorByDepth(t *testing.T) {
	// Every prompt is unique (nothing shares a wire hash), but the rollouts start
	// from one tree and the steps line up by position.
	rewards := []float64{1, 0, 0.5}
	var hashes [][]string
	for i := range rewards {
		hashes = append(hashes, []string{fmt.Sprintf("a%d", i), fmt.Sprintf("b%d", i)})
	}
	eps := anchorEps(rewards, hashes)
	s := DefaultSpec()
	s.Method = MethodAnchor
	rep := mustApply(t, eps, s)
	if rep.Anchor.Groups != 0 {
		t.Errorf("prompt anchors need identical prompts: %+v", rep.Anchor)
	}
	s.AnchorBy = AnchorByDepth
	eps = anchorEps(rewards, hashes)
	rep = mustApply(t, eps, s)
	if rep.Anchor.Groups != 2 || rep.Anchor.Steps != 6 {
		t.Errorf("depth anchors: %+v", rep.Anchor)
	}
	// Different starting trees are different anchor families.
	eps = anchorEps(rewards, hashes)
	eps[2].Env.TreeHash = "tree2"
	rep = mustApply(t, eps, s)
	if rep.Anchor.Steps != 4 {
		t.Errorf("trees must not mix: %+v", rep.Anchor)
	}
	// Steps with no wire hash fall back to depth anchoring in prompt mode.
	eps = anchorEps(rewards, [][]string{{"", ""}, {"", ""}, {"", ""}})
	s.AnchorBy = ""
	rep = mustApply(t, eps, s)
	if rep.Anchor.Groups != 2 {
		t.Errorf("fallback to depth: %+v", rep.Anchor)
	}
}

func TestAnchorOnlyComparesWithinGroupsAndRoles(t *testing.T) {
	// Two tasks share a prompt hash by accident; they must not be one anchor group.
	a := anchorEps([]float64{1, 0}, [][]string{{"S"}, {"S"}})
	b := anchorEps([]float64{5, 0}, [][]string{{"S"}, {"S"}})
	for i, ep := range b {
		ep.TaskID, ep.Group, ep.ID = "other", "other@p", fmt.Sprintf("other/%d", i)
	}
	s := DefaultSpec()
	s.Method = MethodAnchor
	rep := mustApply(t, append(a, b...), s)
	if rep.Anchor.Groups != 2 || rep.Anchor.MaxSize != 2 {
		t.Errorf("groups leaked into each other: %+v", rep.Anchor)
	}
	// A manager step and a worker step with the same prompt are different states
	// for different roles.
	m := mkEp("t/0", "t", mkAgent("m", "manager", 1, 1), mkAgent("w", "worker", 1, 1))
	n := mkEp("t/1", "t", mkAgent("m", "manager", 0, 1), mkAgent("w", "worker", 0, 1))
	for _, ep := range []*rl.Episode{m, n} {
		for ai := range ep.Agents {
			ep.Agents[ai].Steps[0].Prompt.WireHash = "same"
		}
	}
	rep = mustApply(t, []*rl.Episode{m, n}, s)
	if rep.Anchor.Groups != 2 {
		t.Errorf("roles are separate anchor families: %+v", rep.Anchor)
	}
}

func TestAnchorIsOrderIndependentAndDeterministic(t *testing.T) {
	rewards := []float64{1, 0, 0.3, 0.9, 0.2}
	hashes := [][]string{{"S", "A", "X"}, {"S", "A", "Y"}, {"S", "B", "Z"}, {"S", "B", "W"}, {"S", "A", "V"}}
	s := DefaultSpec()
	s.Method = MethodAnchor
	s.Gamma = 0.9
	ref := anchorEps(rewards, hashes)
	mustApply(t, ref, s)
	want := allSteps(ref)
	rev := anchorEps(rewards, hashes)
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	mustApply(t, rev, s)
	got := allSteps(rev)
	for k, v := range want {
		if math.Abs(got[k]-v) > 1e-12 {
			t.Fatalf("%s: %v vs %v", k, got[k], v)
		}
	}
	// A rerun replaces, never accumulates.
	mustApply(t, ref, s)
	for k, v := range allSteps(ref) {
		if math.Abs(want[k]-v) > 1e-15 {
			t.Fatalf("rerun changed %s", k)
		}
	}
}

func TestAnchorWithCompactorCalls(t *testing.T) {
	// Scored compactor calls are samples of their own role and anchor separately.
	var eps []*rl.Episode
	for i, r := range []float64{1, 0} {
		w := mkAgent("w", "worker", r, 2)
		w.Steps[1].Kind, w.Steps[1].Role = rl.KindCompactor, rl.RoleCompactor
		w.Steps[1].Reward = 5 + float64(i)
		w.Steps[1].Prompt.WireHash = "fork-of-same-state"
		w.Steps[0].Prompt.WireHash = "S"
		ep := mkEp(fmt.Sprintf("t/%d", i), "t", w)
		ep.Reward.Components = map[string]float64{"role/compactor": 5.5}
		eps = append(eps, ep)
	}
	s := DefaultSpec()
	s.Method = MethodAnchor
	rep := mustApply(t, eps, s)
	if rep.Anchor.Groups != 2 { // worker step S, compactor fork
		t.Errorf("anchor stats: %+v", rep.Anchor)
	}
	for _, ep := range eps {
		for _, st := range ep.Agents[0].Steps {
			if math.IsNaN(st.Advantage) || math.IsInf(st.Advantage, 0) {
				t.Fatal("non-finite advantage")
			}
		}
	}
}
