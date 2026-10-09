package rl

import (
	"math/rand"
	"reflect"
	"testing"
)

// ep builds an episode with a verdict, a cost and signals.
func rankEp(id string, pass bool, score, ite float64, sig map[string]float64, flags ...string) *Episode {
	e := &Episode{ID: id, Cost: CostRef{ITE: ite}, Signals: sig, Flags: flags}
	e.Outcome.Verifier = &Verdict{Kind: "verifier", Pass: pass, Score: score}
	return e
}

func TestRankCriteriaAreTheApprovedOrder(t *testing.T) {
	want := []string{"verified", "score", "ite", "waste", "final_answer_chars"}
	if got := RankCriteria(); !reflect.DeepEqual(got, want) {
		t.Fatalf("RankCriteria = %v, want %v", got, want)
	}
}

func TestCompareRankIsLexicographic(t *testing.T) {
	base := RankKey{Verified: true, Score: 1, ITE: 1000, Waste: 2, FinalAnswerChars: 100}
	with := func(f func(*RankKey)) RankKey { k := base; f(&k); return k }
	tests := []struct {
		name   string
		better RankKey
		worse  RankKey
	}{
		{"verified beats a cheaper unverified run", base, with(func(k *RankKey) { k.Verified, k.ITE, k.Waste = false, 1, 0 })},
		{"higher score beats lower cost", base, with(func(k *RankKey) { k.Score, k.ITE = 0.5, 10 })},
		{"lower ITE beats less waste", base, with(func(k *RankKey) { k.ITE, k.Waste = 2000, 0 })},
		{"a priced run beats an unpriced one", base, with(func(k *RankKey) { k.ITE = 0 })},
		{"less waste beats a shorter answer", base, with(func(k *RankKey) { k.Waste, k.FinalAnswerChars = 3, 1 })},
		{"shorter answer wins the last tie", base, with(func(k *RankKey) { k.FinalAnswerChars = 101 })},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if c := CompareRank(tc.better, tc.worse); c != -1 {
				t.Errorf("CompareRank(better, worse) = %d, want -1", c)
			}
			if c := CompareRank(tc.worse, tc.better); c != 1 {
				t.Errorf("CompareRank(worse, better) = %d, want 1", c)
			}
		})
	}
	if c := CompareRank(base, base); c != 0 {
		t.Errorf("equal keys compare %d", c)
	}
}

func TestKeyOfReadsVerdictCostAndWaste(t *testing.T) {
	sig := map[string]float64{SigStuckWarnings: 1, SigStuckStops: 1, SigRepeatedReads: 2, SigToolErrors: 3, SigInvalidToolCalls: 1,
		SigFinalAnswerChars: 42, SigRequests: 99}
	k := KeyOf(rankEp("t/0", true, 1, 1234, sig))
	want := RankKey{Verified: true, Score: 1, ITE: 1234, Waste: 8, FinalAnswerChars: 42}
	if k != want {
		t.Fatalf("KeyOf = %+v, want %+v", k, want)
	}
	if KeyOf(rankEp("t/1", true, 1, 1, nil, FlagHackProtected)).Verified {
		t.Error("a hack-flagged pass must not count as verified")
	}
	if k := KeyOf(&Episode{ID: "t/2"}); k != (RankKey{}) {
		t.Errorf("no verdict: %+v", k)
	}
}

func TestSortByRankPicksCacheFriendlyRunAndBreaksTiesByID(t *testing.T) {
	friendly := rankEp("t/2", true, 1, 40_000, map[string]float64{SigToolCalls: 6})
	hostile := rankEp("t/0", true, 1, 90_000, map[string]float64{SigToolCalls: 6})
	looped := rankEp("t/1", false, 0, 20_000, map[string]float64{SigStuckStops: 1}, FlagLooped)
	twinA := rankEp("t/3", true, 1, 90_000, map[string]float64{SigToolCalls: 6})
	eps := []*Episode{hostile, looped, friendly, twinA}
	want := []string{"t/2", "t/0", "t/3", "t/1"}
	for i := 0; i < 20; i++ {
		rand.New(rand.NewSource(int64(i))).Shuffle(len(eps), func(a, b int) { eps[a], eps[b] = eps[b], eps[a] })
		SortByRank(eps, KeyOf, func(e *Episode) string { return e.ID })
		var got []string
		for _, e := range eps {
			got = append(got, e.ID)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}
