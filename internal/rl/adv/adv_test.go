package adv

import (
	"fmt"
	"math"
	"math/rand"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/rl"
	"github.com/anemos-labs/sleipnir/internal/rl/reward"
)

// ---- builders ----------------------------------------------------------------------

func mkAgent(id, role string, r float64, steps int) rl.Agent {
	a := rl.Agent{ID: id, Role: role, Reward: rl.Reward{Total: r}}
	for i := 0; i < steps; i++ {
		a.Steps = append(a.Steps, rl.Step{ID: fmt.Sprintf("%s.%d", id, i), Kind: rl.KindMain, Role: role, Trainable: true})
	}
	return a
}

func mkEp(id, task string, agents ...rl.Agent) *rl.Episode {
	return &rl.Episode{Schema: rl.SchemaEpisode, ID: id, TaskID: task, Group: task + "@p", Policy: rl.PolicyRef{Model: "m"}, Agents: agents}
}

// workers builds one episode per reward, each with one worker agent of 3 steps.
func workers(task string, rewards ...float64) []*rl.Episode {
	var eps []*rl.Episode
	for i, r := range rewards {
		eps = append(eps, mkEp(fmt.Sprintf("%s/%d", task, i), task, mkAgent("w", "worker", r, 3)))
	}
	return eps
}

func advOf(ep *rl.Episode, agent int) []float64 {
	var out []float64
	for _, s := range ep.Agents[agent].Steps {
		out = append(out, s.Advantage)
	}
	return out
}

func near(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.IsNaN(got) || math.Abs(got-want) > 1e-9*math.Max(1, math.Abs(want)) {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}

func mustApply(t *testing.T, eps []*rl.Episode, s Spec) Report {
	t.Helper()
	rep, err := Apply(eps, s)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return rep
}

func meanStdRef(v []float64) (float64, float64) {
	m := 0.0
	for _, x := range v {
		m += x
	}
	m /= float64(len(v))
	ss := 0.0
	for _, x := range v {
		ss += (x - m) * (x - m)
	}
	return m, math.Sqrt(ss / float64(len(v)-1))
}

func allSteps(eps []*rl.Episode) map[string]float64 {
	out := map[string]float64{}
	for _, ep := range eps {
		for _, a := range ep.Agents {
			for _, s := range a.Steps {
				out[ep.ID+"/"+s.ID] = s.Advantage
			}
		}
	}
	return out
}

// ---- estimators ------------------------------------------------------------------------

func TestGRPOFormula(t *testing.T) {
	rewards := []float64{1, 0, 0.5, 0.5}
	eps := workers("t", rewards...)
	rep := mustApply(t, eps, DefaultSpec())
	mean, std := meanStdRef(rewards)
	for i, ep := range eps {
		want := (rewards[i] - mean) / (std + DefaultEps)
		for _, a := range advOf(ep, 0) {
			near(t, fmt.Sprintf("episode %d advantage", i), a, want)
		}
	}
	if len(rep.Groups) != 1 || rep.Groups[0].N != 4 || rep.Groups[0].Flat || len(rep.Flat) != 0 {
		t.Errorf("report: %+v", rep)
	}
	near(t, "group mean", rep.Groups[0].Mean, mean)
	near(t, "group std", rep.Groups[0].Std, std)
	if rep.Steps != 12 {
		t.Errorf("steps = %d", rep.Steps)
	}
	// A custom epsilon is used.
	eps = workers("t", rewards...)
	s := DefaultSpec()
	s.Eps = 0.5
	mustApply(t, eps, s)
	near(t, "custom eps", eps[0].Agents[0].Steps[0].Advantage, (1-mean)/(std+0.5))
}

func TestRLOOFormula(t *testing.T) {
	rewards := []float64{1, 0, 0.5, 0.25}
	eps := workers("t", rewards...)
	s := DefaultSpec()
	s.Method = MethodRLOO
	mustApply(t, eps, s)
	total := 1.75
	for i, ep := range eps {
		want := rewards[i] - (total-rewards[i])/3
		near(t, fmt.Sprintf("rloo %d", i), ep.Agents[0].Steps[0].Advantage, want)
	}
}

func TestBroadcastIsTheReward(t *testing.T) {
	eps := workers("t", 0.3, -1.2, 5)
	s := DefaultSpec()
	s.Method = MethodBroadcast
	mustApply(t, eps, s)
	for i, r := range []float64{0.3, -1.2, 5} {
		near(t, "broadcast", eps[i].Agents[0].Steps[2].Advantage, r)
	}
}

func TestNoneClearsEverything(t *testing.T) {
	eps := workers("t", 1, 0)
	for _, ep := range eps {
		for i := range ep.Agents[0].Steps {
			ep.Agents[0].Steps[i].Advantage = 9
		}
	}
	s := DefaultSpec()
	s.Method = MethodNone
	rep := mustApply(t, eps, s)
	for _, ep := range eps {
		for _, a := range advOf(ep, 0) {
			if a != 0 {
				t.Errorf("none must clear advantages, got %v", a)
			}
		}
	}
	if len(rep.Groups) != 0 {
		t.Errorf("none computes nothing: %+v", rep)
	}
}

func TestApplyResetsStaleAdvantages(t *testing.T) {
	eps := workers("t", 1, 0)
	mustApply(t, eps, DefaultSpec())
	// Re-run on a subset: the omitted episode keeps nothing from before only if
	// it is part of the call; episodes in the call are always reset first.
	eps[0].Agents[0].Steps[1].Trainable = false
	eps[0].Agents[0].Steps[1].Advantage = 7 // stale value on a step that stopped being trainable
	mustApply(t, eps, DefaultSpec())
	if eps[0].Agents[0].Steps[1].Advantage != 0 {
		t.Errorf("non-trainable step kept an advantage: %v", eps[0].Agents[0].Steps[1].Advantage)
	}
}

func TestAdvantagesSumToZeroWithinRoleGroups(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	for _, method := range []string{MethodGRPO, MethodRLOO} {
		for trial := 0; trial < 60; trial++ {
			n := 2 + rng.Intn(10)
			var eps []*rl.Episode
			for i := 0; i < n; i++ {
				eps = append(eps, mkEp(fmt.Sprintf("t/%d", i), "t",
					mkAgent("m", "manager", 3+rng.NormFloat64(), 2),
					mkAgent("w1", "worker", rng.Float64(), 3),
					mkAgent("w2", "worker", rng.Float64()*10, 4)))
			}
			s := DefaultSpec()
			s.Method = method
			rep := mustApply(t, eps, s)
			byRole := map[string]float64{}
			for _, ep := range eps {
				for _, a := range ep.Agents {
					byRole[a.Role] += a.Steps[0].Advantage // one advantage per agent
				}
			}
			for role, sum := range byRole {
				if math.Abs(sum) > 1e-7 {
					t.Fatalf("%s trial %d: advantages of role %s sum to %v", method, trial, role, sum)
				}
			}
			if len(rep.Groups) != 2 { // manager, worker
				t.Fatalf("groups: %d", len(rep.Groups))
			}
		}
	}
}

func TestShiftAndScaleInvariance(t *testing.T) {
	rng := rand.New(rand.NewSource(5))
	for trial := 0; trial < 40; trial++ {
		n := 3 + rng.Intn(8)
		base := make([]float64, n)
		for i := range base {
			base[i] = rng.NormFloat64()
		}
		ref := workers("t", base...)
		mustApply(t, ref, DefaultSpec())
		shift, scale := rng.NormFloat64()*100, 0.5+rng.Float64()*200
		tr := make([]float64, n)
		for i, r := range base {
			tr[i] = scale*r + shift
		}
		got := workers("t", tr...)
		mustApply(t, got, DefaultSpec())
		for i := range ref {
			a, b := ref[i].Agents[0].Steps[0].Advantage, got[i].Agents[0].Steps[0].Advantage
			if math.Abs(a-b) > 1e-4*math.Max(1, math.Abs(a)) {
				t.Fatalf("trial %d episode %d: %v vs %v under shift %v scale %v", trial, i, a, b, shift, scale)
			}
		}
	}
	// A pure shift is exactly invariant for RLOO and GRPO (up to float rounding).
	base := []float64{0.1, 0.9, 0.4, 0.7}
	for _, method := range []string{MethodRLOO, MethodGRPO} {
		s := DefaultSpec()
		s.Method = method
		a, b := workers("t", base...), workers("t", 100.1, 100.9, 100.4, 100.7)
		mustApply(t, a, s)
		mustApply(t, b, s)
		for i := range a {
			near(t, method+" shift", b[i].Agents[0].Steps[0].Advantage, a[i].Agents[0].Steps[0].Advantage)
		}
	}
}

func TestOrderIndependence(t *testing.T) {
	rng := rand.New(rand.NewSource(9))
	build := func() []*rl.Episode {
		var eps []*rl.Episode
		for task := 0; task < 3; task++ {
			for i := 0; i < 6; i++ {
				eps = append(eps, mkEp(fmt.Sprintf("task%d/%d", task, i), fmt.Sprintf("task%d", task),
					mkAgent("m", "manager", float64(i*task)+0.5*rng.Float64(), 2),
					mkAgent("w", "worker", rng.Float64(), 3)))
			}
		}
		return eps
	}
	rng = rand.New(rand.NewSource(9))
	ref := build()
	for _, method := range []string{MethodGRPO, MethodRLOO, MethodBroadcast} {
		s := DefaultSpec()
		s.Method = method
		mustApply(t, ref, s)
		want := allSteps(ref)
		for shuffle := 0; shuffle < 10; shuffle++ {
			perm := append([]*rl.Episode(nil), ref...)
			rng2 := rand.New(rand.NewSource(int64(shuffle)))
			rng2.Shuffle(len(perm), func(i, j int) { perm[i], perm[j] = perm[j], perm[i] })
			mustApply(t, perm, s)
			if got := allSteps(perm); !reflect.DeepEqual(got, want) {
				t.Fatalf("%s: advantages depend on episode order (shuffle %d)", method, shuffle)
			}
		}
	}
}

func TestDeterminismAndIdempotence(t *testing.T) {
	eps := workers("t", 0.2, 0.8, 0.5, 0.5, 1)
	s := DefaultSpec()
	rep1 := mustApply(t, eps, s)
	first := allSteps(eps)
	rep2 := mustApply(t, eps, s)
	if !reflect.DeepEqual(first, allSteps(eps)) || !reflect.DeepEqual(rep1, rep2) {
		t.Error("Apply twice must give the same advantages and report")
	}
}

func TestFlatGroupsSingletonsAndTinyVariance(t *testing.T) {
	// all equal
	eps := workers("t", 0.5, 0.5, 0.5)
	rep := mustApply(t, eps, DefaultSpec())
	for _, ep := range eps {
		for _, a := range advOf(ep, 0) {
			if a != 0 {
				t.Errorf("flat group must give 0, got %v", a)
			}
		}
	}
	if len(rep.Flat) != 1 || !rep.Groups[0].Flat {
		t.Errorf("flat group not reported: %+v", rep)
	}
	// single sample
	eps = workers("t", 3)
	rep = mustApply(t, eps, DefaultSpec())
	if eps[0].Agents[0].Steps[0].Advantage != 0 || len(rep.Flat) != 1 {
		t.Errorf("singleton: %v %+v", eps[0].Agents[0].Steps[0].Advantage, rep)
	}
	// Float-noise level differences are flat, not amplified into unit advantages.
	eps = workers("t", 1, 1+1e-13, 1-1e-13, 1)
	rep = mustApply(t, eps, DefaultSpec())
	for _, ep := range eps {
		if a := ep.Agents[0].Steps[0].Advantage; a != 0 {
			t.Errorf("noise-level spread produced advantage %v", a)
		}
	}
	if len(rep.Flat) != 1 {
		t.Errorf("noise-level spread should be flat: %+v", rep.Flat)
	}
	// Tiny but real variance stays finite and bounded by the epsilon.
	eps = workers("t", 1, 1+1e-7, 1-1e-7, 1)
	mustApply(t, eps, DefaultSpec())
	for _, ep := range eps {
		a := ep.Agents[0].Steps[0].Advantage
		if math.IsNaN(a) || math.IsInf(a, 0) || math.Abs(a) > 2 {
			t.Errorf("tiny variance gave %v", a)
		}
	}
	// rloo on a singleton and broadcast on a flat group.
	s := DefaultSpec()
	s.Method = MethodRLOO
	eps = workers("t", 3)
	mustApply(t, eps, s)
	if eps[0].Agents[0].Steps[0].Advantage != 0 {
		t.Error("rloo singleton must be 0")
	}
	s.Method = MethodBroadcast
	eps = workers("t", 2, 2)
	rep = mustApply(t, eps, s)
	near(t, "broadcast keeps the reward", eps[0].Agents[0].Steps[0].Advantage, 2)
	if len(rep.Flat) != 1 {
		t.Error("a flat group is reported for every method")
	}
}

func TestPerRoleBaselinesKeepRolesApart(t *testing.T) {
	// The manager's rewards live near 10 with tiny spread; workers near 0. A global
	// baseline would mark every manager hugely positive and every worker negative.
	var eps []*rl.Episode
	for i := 0; i < 6; i++ {
		eps = append(eps, mkEp(fmt.Sprintf("t/%d", i), "t",
			mkAgent("m", "manager", 10+float64(i)*0.01, 2),
			mkAgent("w", "worker", float64(i)*0.2, 3)))
	}
	per := DefaultSpec()
	mustApply(t, eps, per)
	for _, ep := range eps {
		if a := ep.Agents[0].Steps[0].Advantage; math.Abs(a) > 2 {
			t.Errorf("per-role manager advantage %v should be O(1)", a)
		}
	}
	// Both roles' best episodes are the last ones and both are positive.
	if eps[5].Agents[0].Steps[0].Advantage <= 0 || eps[5].Agents[1].Steps[0].Advantage <= 0 {
		t.Error("the best manager and worker must both get positive advantages")
	}
	glob := DefaultSpec()
	glob.PerRole = false
	rep := mustApply(t, eps, glob)
	if len(rep.Groups) != 1 || rep.Groups[0].Role != "*" || rep.Groups[0].N != 12 {
		t.Errorf("global baseline should be one group: %+v", rep.Groups)
	}
	if a := eps[0].Agents[0].Steps[0].Advantage; a < 0.5 {
		t.Errorf("a global baseline lets the manager's scale dominate: %v", a)
	}
}

func TestCustomWorkerRolesShareABaseline(t *testing.T) {
	// Worker specialisations ("backend", "tests") are one role tag.
	var eps []*rl.Episode
	for i := 0; i < 4; i++ {
		eps = append(eps, mkEp(fmt.Sprintf("t/%d", i), "t",
			mkAgent("b", "backend", float64(i), 2), mkAgent("q", "tests", float64(i)+0.1, 2)))
	}
	rep := mustApply(t, eps, DefaultSpec())
	if len(rep.Groups) != 1 || rep.Groups[0].Role != rl.RoleWorker || rep.Groups[0].N != 8 {
		t.Errorf("groups: %+v", rep.Groups)
	}
}

func TestOnlyTrainableStepsInEligibleEpisodesGetAdvantages(t *testing.T) {
	eps := workers("t", 1, 0, 0.5)
	eps[0].Agents[0].Steps[1].Trainable = false
	// A teacher agent: none of its steps are trainable, so it is not a sample.
	teacher := mkAgent("teacher", "worker", 100, 2)
	for i := range teacher.Steps {
		teacher.Steps[i].Trainable = false
	}
	eps[1].Agents = append(eps[1].Agents, teacher)
	// A hard-flagged episode is ignored entirely, whatever its reward.
	flagged := mkEp("t/flagged", "t", mkAgent("w", "worker", 999, 3))
	flagged.Flags = []string{rl.FlagHackProtected}
	infra := mkEp("t/infra", "t", mkAgent("w", "worker", -999, 3))
	infra.Flags = []string{rl.FlagInfraError}
	all := append(append([]*rl.Episode{}, eps...), flagged, infra)
	rep := mustApply(t, all, DefaultSpec())

	if eps[0].Agents[0].Steps[1].Advantage != 0 {
		t.Error("a non-trainable step got an advantage")
	}
	if eps[0].Agents[0].Steps[0].Advantage == 0 || eps[0].Agents[0].Steps[2].Advantage == 0 {
		t.Error("trainable steps of the same agent must get one")
	}
	for _, s := range teacher.Steps {
		if s.Advantage != 0 {
			t.Error("teacher step got an advantage")
		}
	}
	for _, e := range []*rl.Episode{flagged, infra} {
		for _, s := range e.Agents[0].Steps {
			if s.Advantage != 0 {
				t.Errorf("%s: hard-flagged episode got an advantage", e.ID)
			}
		}
	}
	if len(rep.Skipped) != 2 || rep.Skipped[0].Episode != "t/flagged" || !strings.Contains(rep.Skipped[0].Reason, "hack:protected_edit") {
		t.Errorf("skipped: %+v", rep.Skipped)
	}
	// The flagged outliers did not move the baseline of the others.
	clean := workers("t", 1, 0, 0.5)
	clean[0].Agents[0].Steps[1].Trainable = false
	mustApply(t, clean, DefaultSpec())
	for i := range clean {
		near(t, "unaffected baseline", eps[i].Agents[0].Steps[0].Advantage, clean[i].Agents[0].Steps[0].Advantage)
	}
	// Non-hard flags do not exclude.
	weak := workers("t", 1, 0)
	weak[0].Flags = []string{rl.FlagWeakLabel, rl.FlagBudgetExceeded}
	mustApply(t, weak, DefaultSpec())
	if weak[0].Agents[0].Steps[0].Advantage == 0 {
		t.Error("weak_label and budget_exceeded episodes stay in the baseline")
	}
}

func TestSegmentChainsShareTheTerminalAdvantage(t *testing.T) {
	eps := workers("t", 1, 0)
	for _, ep := range eps {
		for i := range ep.Agents[0].Steps {
			ep.Agents[0].Steps[i].Segment = i / 2 // steps 0-1 in segment 0, step 2 in segment 1
		}
	}
	mustApply(t, eps, DefaultSpec())
	a := advOf(eps[0], 0)
	if a[0] == 0 || a[0] != a[1] || a[1] != a[2] {
		t.Errorf("all steps of every segment get the terminal advantage: %v", a)
	}
}

func TestCompactorStepsAreSeparateSamples(t *testing.T) {
	build := func(scored bool) []*rl.Episode {
		var eps []*rl.Episode
		for i := 0; i < 4; i++ {
			w := mkAgent("w", "worker", float64(i), 4)
			// steps 1 and 3 are compactor calls of this worker
			for _, k := range []int{1, 3} {
				w.Steps[k].Kind = rl.KindCompactor
				w.Steps[k].Role = rl.RoleCompactor
			}
			if scored {
				w.Steps[1].Reward, w.Steps[3].Reward = 10+float64(i), 20-float64(i)
			}
			ep := mkEp(fmt.Sprintf("t/%d", i), "t", w)
			if scored {
				ep.Reward.Components = map[string]float64{"role/compactor": 15}
			}
			eps = append(eps, ep)
		}
		return eps
	}
	// Scored episodes: each compactor call is scored on its own reward, in the
	// compactor role's baseline (8 samples), apart from the workers' (4 samples).
	eps := build(true)
	rep := mustApply(t, eps, DefaultSpec())
	roles := map[string]int{}
	for _, g := range rep.Groups {
		roles[g.Role] = g.N
	}
	if roles[rl.RoleWorker] != 4 || roles[rl.RoleCompactor] != 8 {
		t.Errorf("groups: %v", roles)
	}
	rew := []float64{10, 11, 12, 13, 20, 19, 18, 17}
	mean, std := meanStdRef(rew)
	near(t, "compactor step advantage", eps[0].Agents[0].Steps[1].Advantage, (10-mean)/(std+DefaultEps))
	near(t, "second compactor step", eps[3].Agents[0].Steps[3].Advantage, (17-mean)/(std+DefaultEps))
	// The main steps of the same agent are normalised among workers.
	wr := []float64{0, 1, 2, 3}
	wm, ws := meanStdRef(wr)
	near(t, "worker steps", eps[0].Agents[0].Steps[0].Advantage, (0-wm)/(ws+DefaultEps))
	near(t, "worker steps share it", eps[0].Agents[0].Steps[2].Advantage, eps[0].Agents[0].Steps[0].Advantage)

	// Unscored episodes: compactor calls inherit the reward of the chain they
	// served, so they simply share that chain's advantage (no separate baseline,
	// no double counting of the agent's reward).
	eps = build(false)
	rep = mustApply(t, eps, DefaultSpec())
	if len(rep.Groups) != 1 || rep.Groups[0].Role != rl.RoleWorker || rep.Groups[0].N != 4 {
		t.Errorf("unscored groups: %+v", rep.Groups)
	}
	for i, ep := range eps {
		for _, a := range advOf(ep, 0) {
			near(t, fmt.Sprintf("inherited advantage %d", i), a, ep.Agents[0].Steps[0].Advantage)
		}
		if ep.Agents[0].Steps[1].Advantage == 0 && i > 0 {
			t.Errorf("episode %d: compactor call got no advantage", i)
		}
	}
}

func TestInvalidRewardsAreExcluded(t *testing.T) {
	eps := workers("t", 1, 0, math.NaN(), math.Inf(1), 0.5)
	rep := mustApply(t, eps, DefaultSpec())
	if rep.Invalid != 2 {
		t.Errorf("invalid = %d", rep.Invalid)
	}
	for _, i := range []int{2, 3} {
		for _, a := range advOf(eps[i], 0) {
			if a != 0 {
				t.Errorf("episode %d with a non-finite reward got %v", i, a)
			}
		}
	}
	clean := workers("t", 1, 0, 0.5)
	mustApply(t, clean, DefaultSpec())
	near(t, "baseline ignores invalid samples", eps[0].Agents[0].Steps[0].Advantage, clean[0].Agents[0].Steps[0].Advantage)
	for _, ep := range eps {
		for _, s := range ep.Agents[0].Steps {
			if math.IsNaN(s.Advantage) || math.IsInf(s.Advantage, 0) {
				t.Fatal("non-finite advantage written")
			}
		}
	}
	// Huge rewards do not overflow.
	eps = workers("t", 1e300, -1e300, 0)
	mustApply(t, eps, DefaultSpec())
	for _, ep := range eps {
		if a := ep.Agents[0].Steps[0].Advantage; math.IsNaN(a) || math.IsInf(a, 0) {
			t.Errorf("overflow: %v", a)
		}
	}
}

func TestGroupsAreNormalisedSeparately(t *testing.T) {
	eps := append(workers("taskA", 1, 0), workers("taskB", 100, 90, 95)...)
	rep := mustApply(t, eps, DefaultSpec())
	if len(rep.Groups) != 2 {
		t.Fatalf("groups: %+v", rep.Groups)
	}
	a0 := eps[0].Agents[0].Steps[0].Advantage
	m, s := meanStdRef([]float64{1, 0})
	near(t, "task A independent of task B", a0, (1-m)/(s+DefaultEps))
	// Policy snapshots are separate groups under task+policy but one under task.
	a := workers("t", 1, 0)
	b := workers("t", 5, 4)
	for _, ep := range b {
		ep.Policy.Checkpoint = "step-100"
		ep.ID += "-b"
	}
	all := append(append([]*rl.Episode{}, a...), b...)
	rep = mustApply(t, all, DefaultSpec())
	if len(rep.Groups) != 2 {
		t.Errorf("task+policy: %d groups", len(rep.Groups))
	}
	s2 := DefaultSpec()
	s2.GroupBy = GroupByTask
	rep = mustApply(t, all, s2)
	if len(rep.Groups) != 1 || rep.Groups[0].N != 4 {
		t.Errorf("task: %+v", rep.Groups)
	}
}

func TestGroupsFunction(t *testing.T) {
	a := mkEp("a/0", "a", mkAgent("w", "worker", 1, 1))
	b := mkEp("a/1", "a", mkAgent("w", "worker", 1, 1))
	c := mkEp("b/0", "b", mkAgent("w", "worker", 1, 1))
	d := mkEp("a/2", "a", mkAgent("w", "worker", 1, 1))
	d.Policy.Checkpoint = "ckpt"
	e := mkEp("x/0", "", mkAgent("w", "worker", 1, 1)) // no task id: derived from the episode id
	e.Group = ""
	tests := []struct {
		by   string
		want map[string]int
	}{
		{"task", map[string]int{"a": 3, "b": 1, "x": 1}},
		{"task+policy", map[string]int{"a@m#": 2, "a@m#ckpt": 1, "b@m#": 1, "x@m#": 1}},
		{"", map[string]int{"a@m#": 2, "a@m#ckpt": 1, "b@m#": 1, "x@m#": 1}},
		{"group", map[string]int{"a@p": 3, "b@p": 1, "x@m#": 1}},
	}
	for _, tc := range tests {
		g := Groups([]*rl.Episode{a, b, nil, c, d, a, e}, tc.by) // nil and a duplicate pointer
		got := map[string]int{}
		for k, v := range g {
			got[k] = len(v)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("Groups(%q) = %v, want %v", tc.by, got, tc.want)
		}
	}
	// Role overrides make a different policy setup.
	x := mkEp("t/0", "t")
	y := mkEp("t/1", "t")
	y.Policy.RoleModels = map[string]string{"worker": "fixed"}
	if g := Groups([]*rl.Episode{x, y}, "task+policy"); len(g) != 2 {
		t.Errorf("role models split the group: %v", g)
	}
	if g := Groups(nil, "task"); len(g) != 0 {
		t.Errorf("nil: %v", g)
	}
}

func TestDropFlat(t *testing.T) {
	flat := workers("flat", 0.5, 0.5, 0.5)
	live := workers("live", 1, 0)
	// An episode with a flat worker group but a live manager group has signal.
	mixed := []*rl.Episode{
		mkEp("mix/0", "mix", mkAgent("m", "manager", 1, 1), mkAgent("w", "worker", 0.5, 1)),
		mkEp("mix/1", "mix", mkAgent("m", "manager", 0, 1), mkAgent("w", "worker", 0.5, 1)),
	}
	all := append(append(append([]*rl.Episode{}, flat...), live...), mixed...)
	s := DefaultSpec()
	s.DropFlat = true
	rep := mustApply(t, all, s)
	want := []string{"flat/0", "flat/1", "flat/2"}
	if !reflect.DeepEqual(rep.Dropped, want) {
		t.Errorf("dropped = %v, want %v", rep.Dropped, want)
	}
	if len(rep.Flat) < 4 { // flat|worker, mix|worker plus singletons? at least these
		t.Logf("flat groups: %v", rep.Flat)
	}
	// Without the option nothing is dropped, but flat groups are still reported.
	rep = mustApply(t, all, DefaultSpec())
	if len(rep.Dropped) != 0 || len(rep.Flat) == 0 {
		t.Errorf("default: dropped %v flat %v", rep.Dropped, rep.Flat)
	}
}

func TestEmptyAndDegenerateInput(t *testing.T) {
	for _, eps := range [][]*rl.Episode{nil, {}, {nil}, {mkEp("t/0", "t")}, {{}}, {mkEp("t/0", "t", mkAgent("w", "worker", 1, 0))}} {
		rep, err := Apply(eps, DefaultSpec())
		if err != nil {
			t.Fatalf("Apply(%v): %v", eps, err)
		}
		if rep.Steps != 0 {
			t.Errorf("steps = %d", rep.Steps)
		}
	}
	// An episode with agents but no trainable steps is reported as skipped.
	ep := mkEp("t/0", "t", mkAgent("w", "worker", 1, 2))
	for i := range ep.Agents[0].Steps {
		ep.Agents[0].Steps[i].Trainable = false
	}
	rep := mustApply(t, []*rl.Episode{ep}, DefaultSpec())
	if len(rep.Skipped) != 1 || rep.Skipped[0].Reason != "no trainable steps" {
		t.Errorf("skipped: %+v", rep.Skipped)
	}
}

func TestSpecValidation(t *testing.T) {
	bad := []Spec{
		{Method: "ppo"},
		{Method: ""},
		{Method: MethodGRPO, GroupBy: "everything"},
		{Method: MethodGRPO, Eps: math.NaN()},
		{Method: MethodGRPO, Eps: -1},
		{Method: MethodGRPO, Eps: math.Inf(1)},
		{Method: MethodAnchor, Gamma: 1.5},
		{Method: MethodAnchor, Gamma: -0.5},
		{Method: MethodAnchor, AnchorWeight: -1},
		{Method: MethodAnchor, AnchorWeight: math.NaN()},
		{Method: MethodAnchor, AnchorMin: -1},
		{Method: MethodAnchor, AnchorBy: "vibes"},
	}
	for _, s := range bad {
		if _, err := Apply(workers("t", 1, 0), s); err == nil {
			t.Errorf("spec %+v accepted", s)
		}
	}
	// A rejected spec leaves advantages alone.
	eps := workers("t", 1, 0)
	eps[0].Agents[0].Steps[0].Advantage = 3
	if _, err := Apply(eps, Spec{Method: "nope"}); err == nil {
		t.Fatal("expected an error")
	}
	if eps[0].Agents[0].Steps[0].Advantage != 3 {
		t.Error("an invalid spec must not touch the episodes")
	}
	if s, err := SpecFor("  RLOO "); err != nil || s.Method != MethodRLOO || !s.PerRole {
		t.Errorf("SpecFor: %+v %v", s, err)
	}
	if _, err := SpecFor("nope"); err == nil {
		t.Error("SpecFor accepted a bad method")
	}
	if d := DefaultSpec(); !d.PerRole || d.Method != MethodGRPO || d.GroupBy != GroupByTaskPolicy {
		t.Errorf("default spec: %+v", d)
	}
}

func TestGRPOFuncHook(t *testing.T) {
	hook := GRPOFunc(DefaultSpec())
	eps := workers("t", 1, 0, 0.5)
	if err := hook(eps); err != nil {
		t.Fatal(err)
	}
	if eps[0].Agents[0].Steps[0].Advantage <= 0 || eps[1].Agents[0].Steps[0].Advantage >= 0 {
		t.Error("hook did not apply advantages")
	}
	if err := GRPOFunc(Spec{Method: "x"})(eps); err == nil {
		t.Error("hook must surface spec errors")
	}
}

func TestApplyScalesToLargeRuns(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	var eps []*rl.Episode
	for task := 0; task < 40; task++ {
		for i := 0; i < 16; i++ {
			var agents []rl.Agent
			agents = append(agents, mkAgent("m", "manager", rng.Float64(), 20))
			for w := 0; w < 5; w++ {
				agents = append(agents, mkAgent(fmt.Sprintf("w%d", w), "worker", rng.Float64(), 60))
			}
			eps = append(eps, mkEp(fmt.Sprintf("task%d/%d", task, i), fmt.Sprintf("task%d", task), agents...))
		}
	}
	start := time.Now()
	rep := mustApply(t, eps, DefaultSpec())
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("40x16 episodes took %v", d)
	}
	if rep.Steps != 40*16*(20+5*60) {
		t.Errorf("steps = %d", rep.Steps)
	}
	if !sort.SliceIsSorted(rep.Groups, func(i, j int) bool { return rep.Groups[i].Key < rep.Groups[j].Key }) {
		t.Error("groups must be sorted")
	}
}

// ---- integration with the reward package -------------------------------------------

func TestApplyOnScoredEpisodes(t *testing.T) {
	// Score real episodes, then estimate: compactor calls carry their own rewards.
	mk := func(i int, pass bool) *rl.Episode {
		st := func(id string, opts ...func(*rl.Step)) rl.Step {
			s := rl.Step{ID: id, Kind: rl.KindMain, Role: "worker", Trainable: true, At: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(len(id)) * time.Second)}
			for _, o := range opts {
				o(&s)
			}
			return s
		}
		patch := func(s *rl.Step) {
			s.Kind, s.Role = rl.KindCompactor, rl.RoleCompactor
			s.Completion.Turn = core.Turn{Blocks: []core.Block{core.Text(`{"keep_from":"t3"}`)}}
		}
		size := func(n int) func(*rl.Step) { return func(s *rl.Step) { s.Prompt.Tokens = n } }
		a := rl.Agent{ID: "w", Role: "worker", Steps: []rl.Step{
			st("w.1", size(3000)), st("w.2", size(9000)),
			st("w.c", patch, size(10000)),
			st("w.3", size(4000), func(s *rl.Step) { s.Segment, s.Epoch = 1, 1 }),
		}}
		ep := &rl.Episode{ID: fmt.Sprintf("t/%d", i), TaskID: "t", Group: "t@p", Policy: rl.PolicyRef{Model: "m"}, Agents: []rl.Agent{a}}
		ep.Outcome = rl.Outcome{Verifier: &rl.Verdict{Pass: pass, Score: map[bool]float64{true: 1, false: 0}[pass]}, Claimed: "done"}
		cfg := reward.DefaultConfig()
		cfg.Probes = false
		if err := reward.Score(ep, &rl.Task{}, cfg, nil); err != nil {
			t.Fatal(err)
		}
		return ep
	}
	eps := []*rl.Episode{mk(0, true), mk(1, false), mk(2, true), mk(3, false)}
	rep := mustApply(t, eps, DefaultSpec())
	if len(rep.Groups) != 2 {
		t.Fatalf("groups: %+v", rep.Groups)
	}
	for _, ep := range eps {
		w, c := ep.Agents[0].Steps[0].Advantage, ep.Agents[0].Steps[2].Advantage
		if ep.Outcome.Verifier.Pass && (w <= 0 || c <= 0) {
			t.Errorf("%s passed: worker advantage %v, compactor advantage %v", ep.ID, w, c)
		}
		if !ep.Outcome.Verifier.Pass && (w >= 0 || c >= 0) {
			t.Errorf("%s failed: worker advantage %v, compactor advantage %v", ep.ID, w, c)
		}
		if ep.Agents[0].Steps[0].Advantage != ep.Agents[0].Steps[1].Advantage || ep.Agents[0].Steps[0].Advantage != ep.Agents[0].Steps[3].Advantage {
			t.Errorf("%s: every main step of the segment chain shares one advantage", ep.ID)
		}
	}
}

func TestCompactorRewardFromAggregatesOnly(t *testing.T) {
	// Episodes that carry only the "role/compactor" aggregate (no per-call
	// rewards): every compactor call of the episode uses it.
	var eps []*rl.Episode
	for i, agg := range []float64{0.2, 0.9, 0.5} {
		w := mkAgent("w", "worker", float64(i), 3)
		w.Steps[1].Kind, w.Steps[1].Role = rl.KindCompactor, rl.RoleCompactor
		ep := mkEp(fmt.Sprintf("t/%d", i), "t", w)
		ep.Reward.Components = map[string]float64{"role/compactor": agg}
		eps = append(eps, ep)
	}
	rep := mustApply(t, eps, DefaultSpec())
	roles := map[string]int{}
	for _, g := range rep.Groups {
		roles[g.Role] = g.N
	}
	if roles[rl.RoleCompactor] != 3 || roles[rl.RoleWorker] != 3 {
		t.Fatalf("groups: %v", roles)
	}
	m, s := meanStdRef([]float64{0.2, 0.9, 0.5})
	for i, agg := range []float64{0.2, 0.9, 0.5} {
		near(t, "compactor advantage from the aggregate", eps[i].Agents[0].Steps[1].Advantage, (agg-m)/(s+DefaultEps))
	}
	// The agent-level aggregate outranks the episode's.
	for i, ep := range eps {
		ep.Agents[0].Reward.Components = map[string]float64{"role/compactor": []float64{5, 1, 3}[i]}
	}
	mustApply(t, eps, DefaultSpec())
	m2, s2 := meanStdRef([]float64{5, 1, 3})
	near(t, "agent aggregate", eps[0].Agents[0].Steps[1].Advantage, (5-m2)/(s2+DefaultEps))
}

func TestSpecStringsAreNormalised(t *testing.T) {
	eps := workers("t", 1, 0, 0.5)
	rep := mustApply(t, eps, Spec{Method: "  GRPO ", PerRole: true, GroupBy: "Task+Policy"})
	if rep.Method != MethodGRPO || eps[0].Agents[0].Steps[0].Advantage <= 0 {
		t.Errorf("spec strings should be case and space insensitive: %+v", rep)
	}
}
