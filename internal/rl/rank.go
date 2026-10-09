package rl

import (
	"math"
	"sort"
	"strings"
)

// RankKey is what the best-of-n ranking reads from an episode: the verdict, the repriced cost,
// the waste and the length of the final answer. It is small and plain so that run summaries and
// export records can carry it and a trainer can re-derive the order.
type RankKey struct {
	// Verified is a passing verifier verdict on an episode no hack detector flagged.
	Verified bool `json:"verified"`
	// Score is the verifier score in [0, 1] (0 without a verdict).
	Score float64 `json:"score"`
	// ITE is the input-token-equivalent cost repriced under the scoring target (Cost.ITE). It
	// prices cache reads below fresh input, so a run that reuses its cache costs less. 0 means
	// the episode was not priced; such an episode ranks after every priced one.
	ITE float64 `json:"ite"`
	// Waste is the sum of the WasteSignals.
	Waste float64 `json:"waste"`
	// FinalAnswerChars is the length of the root agent's final answer (SigFinalAnswerChars).
	FinalAnswerChars float64 `json:"final_answer_chars"`
}

// WasteSignals are the signals whose sum is an episode's waste: repetition-guard warnings and
// stops, re-reads of unchanged files, failed tool calls and malformed ones.
var WasteSignals = []string{SigStuckWarnings, SigStuckStops, SigRepeatedReads, SigToolErrors, SigInvalidToolCalls}

// Waste sums the WasteSignals of a signal map; absent signals count 0.
func Waste(signals map[string]float64) float64 {
	sum := 0.0
	for _, k := range WasteSignals {
		if v := signals[k]; v > 0 && !math.IsInf(v, 0) {
			sum += v
		}
	}
	return sum
}

// GroupKey names an episode's rollout group, the set best-of-n compares it within:
// Episode.Group, else its task.
func GroupKey(ep *Episode) string {
	if ep.Group != "" {
		return ep.Group
	}
	return ep.TaskID
}

// KeyOf derives an episode's rank key.
func KeyOf(ep *Episode) RankKey {
	k := RankKey{ITE: ep.Cost.ITE, Waste: Waste(ep.Signals), FinalAnswerChars: ep.Signals[SigFinalAnswerChars]}
	if v := ep.Outcome.Verifier; v != nil {
		if !math.IsNaN(v.Score) {
			k.Score = math.Max(0, math.Min(1, v.Score))
		}
		k.Verified = v.Pass && !hacked(ep.Flags)
	}
	if math.IsNaN(k.ITE) || k.ITE < 0 {
		k.ITE = 0
	}
	return k
}

// hacked reports whether any flag is a hack detector's.
func hacked(flags []string) bool {
	for _, f := range flags {
		if strings.HasPrefix(f, "hack:") {
			return true
		}
	}
	return false
}

// criterion is one level of the lexicographic ranking: the value it reads and its direction.
type criterion struct {
	name   string
	value  func(RankKey) float64
	higher bool // higher values rank first
}

// bestOfN is the ranking, most significant level first.
var bestOfN = []criterion{
	{"verified", func(k RankKey) float64 {
		if k.Verified {
			return 1
		}
		return 0
	}, true},
	{"score", func(k RankKey) float64 { return k.Score }, true},
	{"ite", func(k RankKey) float64 {
		if k.ITE > 0 {
			return k.ITE
		}
		return math.Inf(1)
	}, false},
	{"waste", func(k RankKey) float64 { return k.Waste }, false},
	{"final_answer_chars", func(k RankKey) float64 { return k.FinalAnswerChars }, false},
}

// RankCriteria names the levels of the best-of-n ranking, most significant first.
func RankCriteria() []string {
	out := make([]string, len(bestOfN))
	for i, c := range bestOfN {
		out[i] = c.name
	}
	return out
}

// CompareRank orders two keys by the best-of-n ranking: verified first, then higher verifier
// score, lower ITE, less waste and a shorter final answer. It returns -1 when a ranks before b,
// 1 when after, and 0 when they tie on every level.
func CompareRank(a, b RankKey) int {
	for _, c := range bestOfN {
		x, y := c.value(a), c.value(b)
		if x == y {
			continue
		}
		if (x > y) == c.higher {
			return -1
		}
		return 1
	}
	return 0
}

// SortByRank sorts xs best first by the best-of-n ranking of key, breaking ties by id so the
// order never depends on the input order.
func SortByRank[T any](xs []T, key func(T) RankKey, id func(T) string) {
	sort.SliceStable(xs, func(i, j int) bool {
		if c := CompareRank(key(xs[i]), key(xs[j])); c != 0 {
			return c < 0
		}
		return id(xs[i]) < id(xs[j])
	})
}
