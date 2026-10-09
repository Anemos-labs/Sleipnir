package env

import (
	"strconv"

	"github.com/anemos-labs/sleipnir/internal/rl"
)

// Efficiency is the per-episode mean, over completed rollouts, of the signals the best-of-n
// ranking reads beyond the verdict and the cost.
type Efficiency struct {
	ToolCalls        float64 `json:"tool_calls"`
	StuckWarnings    float64 `json:"stuck_warnings"`
	StuckStops       float64 `json:"stuck_stops"`
	RepeatedReads    float64 `json:"repeated_reads"`
	FinalAnswerChars float64 `json:"final_answer_chars"`
	// Waste is the mean of rl.Waste: guard warnings and stops, repeated reads, tool errors and
	// invalid tool calls.
	Waste float64 `json:"waste"`
	// LoopRate is the share of completed rollouts the repetition guard stopped (flag looped).
	LoopRate float64 `json:"loop_rate"`
}

// BestOfGroup is the best completed rollout of one task by the best-of-n ranking (rl.CompareRank,
// ties to the lower episode id).
type BestOfGroup struct {
	Sample int        `json:"sample"`
	Key    rl.RankKey `json:"key"`
}

// efficiencyFields copies an episode's efficiency signals and rank key into its result.
func efficiencyFields(res *RolloutResult, ep *rl.Episode) {
	res.ToolCalls = int(ep.Signals[rl.SigToolCalls])
	res.StuckWarnings = int(ep.Signals[rl.SigStuckWarnings])
	res.StuckStops = int(ep.Signals[rl.SigStuckStops])
	res.RepeatedReads = int(ep.Signals[rl.SigRepeatedReads])
	res.FinalAnswerChars = int(ep.Signals[rl.SigFinalAnswerChars])
	k := rl.KeyOf(ep)
	res.Rank = &k
}

// efficiencyOf averages the efficiency fields of the completed results.
func efficiencyOf(results []RolloutResult) Efficiency {
	var e Efficiency
	n, loops := 0, 0
	for _, r := range results {
		if r.Status != StatusOK {
			continue
		}
		n++
		e.ToolCalls += float64(r.ToolCalls)
		e.StuckWarnings += float64(r.StuckWarnings)
		e.StuckStops += float64(r.StuckStops)
		e.RepeatedReads += float64(r.RepeatedReads)
		e.FinalAnswerChars += float64(r.FinalAnswerChars)
		if r.Rank != nil {
			e.Waste += r.Rank.Waste
		}
		if r.StuckStops > 0 {
			loops++
		}
	}
	if n == 0 {
		return Efficiency{}
	}
	f := float64(n)
	e.ToolCalls, e.StuckWarnings, e.StuckStops = e.ToolCalls/f, e.StuckWarnings/f, e.StuckStops/f
	e.RepeatedReads, e.FinalAnswerChars, e.Waste = e.RepeatedReads/f, e.FinalAnswerChars/f, e.Waste/f
	e.LoopRate = float64(loops) / f
	return e
}

// bestOf returns the best completed result of one task's results, or nil when none has a rank key.
func bestOf(results []RolloutResult) *BestOfGroup {
	var ranked []RolloutResult
	for _, r := range results {
		if r.Status == StatusOK && r.Rank != nil {
			ranked = append(ranked, r)
		}
	}
	if len(ranked) == 0 {
		return nil
	}
	// Ties break on the episode id "<task>/<sample>", as they do in rl export.
	rl.SortByRank(ranked, func(r RolloutResult) rl.RankKey { return *r.Rank }, func(r RolloutResult) string {
		return r.Task + "/" + strconv.Itoa(r.Sample)
	})
	return &BestOfGroup{Sample: ranked[0].Sample, Key: *ranked[0].Rank}
}
