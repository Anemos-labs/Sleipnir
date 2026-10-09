package export_test

import (
	"testing"

	"github.com/anemos-labs/sleipnir/internal/rl"
	"github.com/anemos-labs/sleipnir/internal/rl/export"
	"github.com/anemos-labs/sleipnir/internal/rl/traj/trajtest"
)

// bestOfFour is one task run four times: a cache-hostile pass with the highest reward, a
// cache-friendly pass, a pass as cheap as the friendly one that re-reads its file, and a run the
// repetition guard stopped.
func bestOfFour(t *testing.T) []export.Source {
	t.Helper()
	reread := func(a *trajtest.Agent) {
		for i := 0; i < 2; i++ {
			a.Step(trajtest.Call{Text: "again", Tool: "read", Input: `{"path":"api/a.go"}`, Result: "package api"})
		}
	}
	loop := func(a *trajtest.Agent) {
		for i := 0; i < 8; i++ {
			a.Step(trajtest.Call{Text: "retry", Tool: "bash", Input: `{"command":"make"}`, Result: "make: no rule", IsError: true})
		}
	}
	return []export.Source{
		rollout(t, variant{sample: 0, plan: "plan hostile", reward: 1, pass: true, ite: 90_000}),
		rollout(t, variant{sample: 1, plan: "plan friendly", reward: 0.9, pass: true, ite: 40_000}),
		rollout(t, variant{sample: 2, plan: "plan rereads", reward: 1, pass: true, ite: 40_000, extra: reread}),
		rollout(t, variant{sample: 3, plan: "plan loops", reward: 0, pass: false, ite: 20_000, extra: loop, stuck: true}),
	}
}

func episodesOf(recs []map[string]any, field string) map[string]int {
	out := map[string]int{}
	for _, r := range recs {
		if id, ok := r[field].(string); ok {
			out[id]++
		}
	}
	return out
}

func TestSelectBestPicksTheCacheFriendlyVerifiedRun(t *testing.T) {
	srcs := bestOfFour(t)
	if !srcs[3].Episode.Has(rl.FlagLooped) || srcs[2].Episode.Signals[rl.SigRepeatedReads] != 2 {
		t.Fatalf("fixture: looped=%v repeated_reads=%v", srcs[3].Episode.Flags, srcs[2].Episode.Signals[rl.SigRepeatedReads])
	}

	o := export.DefaultOptions(export.FormatSFT)
	o.Select = export.SelectBest
	out, st := run(t, srcs, o)
	recs := decode(t, out)
	if len(recs) == 0 {
		t.Fatalf("no records; stats %+v", st)
	}
	for _, r := range recs {
		if r["sample"].(float64) != 1 {
			t.Fatalf("select best kept sample %v, want only the cache-friendly sample 1", r["sample"])
		}
		rank := r["rank"].(map[string]any)
		key := rank["key"].(map[string]any)
		if rank["position"].(float64) != 1 || rank["group_size"].(float64) != 4 || key["ite"].(float64) != 40_000 || key["verified"] != true {
			t.Fatalf("rank = %v", rank)
		}
	}
	if st.Drops["episode:not_best_of_group"] != 2 || st.Drops["episode:not_verified"] != 1 {
		t.Errorf("drops = %v", st.Drops)
	}

	// The default selection is unchanged: the best reward per task, which is the cache-hostile run.
	o = export.DefaultOptions(export.FormatSFT)
	o.TopK = 1
	out, _ = run(t, srcs, o)
	for _, r := range decode(t, out) {
		if r["sample"].(float64) != 0 {
			t.Fatalf("top-k by reward kept sample %v, want 0", r["sample"])
		}
	}
}

func TestPairBestWorstPairsRankOneWithRankLastOfTheGroup(t *testing.T) {
	o := export.DefaultOptions(export.FormatDPO)
	o.Pair = export.PairBestWorst
	out, st := run(t, bestOfFour(t), o)
	recs := decode(t, out)
	units := map[string]int{}
	for _, r := range recs {
		units[r["unit"].(string)]++
		if r["chosen_episode"] != "T/1" || r["rejected_episode"] != "T/3" {
			t.Errorf("%s pair %v vs %v, want T/1 vs T/3", r["unit"], r["chosen_episode"], r["rejected_episode"])
		}
		cr, rr := r["chosen_rank"].(map[string]any), r["rejected_rank"].(map[string]any)
		if cr["position"].(float64) != 1 || rr["position"].(float64) != 4 {
			t.Errorf("ranks %v / %v", cr, rr)
		}
	}
	if units["episode"] != 1 || units["step"] != 1 {
		t.Fatalf("units = %v (stats %+v)", units, st)
	}

	// Rank 1 must be verified: a group that only failed gives no pair.
	var failed []export.Source
	for i := 0; i < 3; i++ {
		failed = append(failed, rollout(t, variant{sample: i, plan: "plan " + string(rune('A'+i)), reward: float64(i) / 10, ite: float64(1000 * (i + 1))}))
	}
	out, st = run(t, failed, o)
	if len(lines(out)) != 0 || st.Drops["pair:best_not_verified"] != 2 {
		t.Fatalf("records %d, drops %v", len(lines(out)), st.Drops)
	}

	// Two passes alike in every rank level give no episode pair.
	twins := []export.Source{
		rollout(t, variant{sample: 0, plan: "plan A", reward: 1, pass: true, ite: 500}),
		rollout(t, variant{sample: 1, plan: "plan B", reward: 0.5, pass: true, ite: 500}),
	}
	twins[1].Episode.Signals[rl.SigFinalAnswerChars] = twins[0].Episode.Signals[rl.SigFinalAnswerChars]
	out, st = run(t, twins, o)
	if len(lines(out)) != 0 || st.Drops["pair:no_rank_gap"] != 1 {
		t.Fatalf("records %d, drops %v", len(lines(out)), st.Drops)
	}
}

func TestEpisodePairsWithEqualSidesAreDropped(t *testing.T) {
	// The verdicts differ but the runs said and did the same: a flaky check, not a preference.
	same := []export.Source{
		rollout(t, variant{sample: 0, plan: "plan A", reward: 1, pass: true, ite: 500}),
		rollout(t, variant{sample: 1, plan: "plan A", reward: 0, pass: false, ite: 500}),
	}
	for _, pair := range []string{export.PairReward, export.PairBestWorst} {
		o := export.DefaultOptions(export.FormatDPO)
		o.Pair = pair
		out, st := run(t, same, o)
		if len(lines(out)) != 0 || st.Drops["pair:same_continuation"] != 1 {
			t.Errorf("%s: records %d, drops %v", pair, len(lines(out)), st.Drops)
		}
	}
}

func TestGroupsCarryRankAndEfficiencySignals(t *testing.T) {
	o := export.DefaultOptions(export.FormatGroups)
	out, _ := run(t, bestOfFour(t), o)
	recs := decode(t, out)
	if len(recs) != 1 {
		t.Fatalf("%d group records", len(recs))
	}
	trajs := recs[0]["trajectories"].([]any)
	if len(trajs) != 4 {
		t.Fatalf("%d trajectories: the looped run must stay in the group", len(trajs))
	}
	pos := map[string]float64{}
	for _, tr := range trajs {
		m := tr.(map[string]any)
		pos[m["episode"].(string)] = m["rank"].(map[string]any)["position"].(float64)
		metrics := m["metrics"].(map[string]any)
		for _, k := range []string{rl.SigToolCalls, rl.SigStuckWarnings, rl.SigStuckStops, rl.SigRepeatedReads, rl.SigFinalAnswerChars} {
			if _, ok := metrics["signal/"+k]; !ok {
				t.Errorf("%s has no signal/%s", m["episode"], k)
			}
		}
	}
	want := map[string]float64{"T/1": 1, "T/2": 2, "T/0": 3, "T/3": 4}
	for id, p := range want {
		if pos[id] != p {
			t.Errorf("%s rank %v, want %v (all: %v)", id, pos[id], p, pos)
		}
	}
}

func TestSelectAndPairAreValidated(t *testing.T) {
	for _, o := range []export.Options{
		{Format: export.FormatSFT, Select: "first"},
		{Format: export.FormatDPO, Pair: "random"},
	} {
		if _, err := export.Export(nil, nil, o); err == nil {
			t.Errorf("%+v: no error", o)
		}
	}
}

// Rank 1 is the chosen side of a best-worst pair. When it has no candidate for a prompt (it asked something else) or no usable
// chain (its model is not an allowed teacher), the best of those that remain is rank 2: that is not a rank 1 vs rank last pair.
func TestPairBestWorstNeedsRankOneAmongTheCandidates(t *testing.T) {
	o := export.DefaultOptions(export.FormatDPO)
	o.Pair = export.PairBestWorst
	for _, tc := range []struct {
		name  string
		first variant
	}{
		{"another prompt", variant{sample: 0, plan: "plan A", reward: 1, pass: true, ite: 100, user: "a different request"}},
		{"not a usable teacher", variant{sample: 0, plan: "plan A", reward: 1, pass: true, ite: 100, model: "other-model"}},
	} {
		srcs := []export.Source{
			rollout(t, tc.first),
			rollout(t, variant{sample: 1, plan: "plan B", reward: 0.9, pass: true, ite: 500}),
			rollout(t, variant{sample: 2, plan: "plan C", reward: 0, pass: false, ite: 900}),
		}
		out, st := run(t, srcs, o)
		for _, r := range decode(t, out) {
			if cr := r["chosen_rank"].(map[string]any); cr["position"].(float64) != 1 {
				t.Errorf("%s: chosen side is rank %v: %v", tc.name, cr["position"], r["unit"])
			}
		}
		if st.Drops["pair:best_not_rank_1"] == 0 {
			t.Errorf("%s: no pair was refused for lacking rank 1 (drops %v, %d records)", tc.name, st.Drops, len(lines(out)))
		}
	}
}
