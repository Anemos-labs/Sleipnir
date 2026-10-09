package export

import (
	"github.com/anemos-labs/sleipnir/internal/rl"
)

// Episode selections for sft (Options.Select).
const (
	SelectAll  = "all"  // every verified episode, best TopK per task by reward
	SelectBest = "best" // the best TopK (default 1) verified episodes per rollout group by rl.CompareRank
)

// Preference pairings for dpo (Options.Pair).
const (
	PairReward    = "reward"     // best vs worst by reward, per task (episodes) or per prompt (steps)
	PairBestWorst = "best-worst" // rank 1 vs rank last by rl.CompareRank, within one rollout group
)

// rankInfo places an episode in its rollout group by the best-of-n ranking. Records carry it so
// a trainer can see, and re-derive, why an episode was chosen.
type rankInfo struct {
	// Position is 1 for the best episode of the group.
	Position  int        `json:"position"`
	GroupSize int        `json:"group_size"`
	Key       rl.RankKey `json:"key"`
}

// rankWork ranks the episodes that reached the format writers within their groups.
func rankWork(work []*workEpisode) {
	byGroup := map[string][]*workEpisode{}
	for _, we := range work {
		we.key = rl.KeyOf(we.ep)
		byGroup[rl.GroupKey(we.ep)] = append(byGroup[rl.GroupKey(we.ep)], we)
	}
	for _, g := range byGroup {
		rl.SortByRank(g, func(we *workEpisode) rl.RankKey { return we.key }, func(we *workEpisode) string { return we.ep.ID })
		for i, we := range g {
			we.rank = rankInfo{Position: i + 1, GroupSize: len(g), Key: we.key}
		}
	}
}

// rankOf returns a copy of an episode's rank for a record.
func rankOf(we *workEpisode) *rankInfo {
	r := we.rank
	return &r
}
