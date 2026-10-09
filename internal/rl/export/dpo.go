package export

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"sort"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/rl"
)

// dpoRecord is one preference pair. Unit "step" compares two completions of the
// same exact prompt (same wire hash) from different episodes, ranked by episode
// reward; unit "episode" compares the whole continuation of the best and the worst
// episode of a task that started from the same prompt.
type dpoRecord struct {
	Schema          string            `json:"schema"`
	ID              string            `json:"id"`
	Unit            string            `json:"unit"`
	TaskID          string            `json:"task_id"`
	Role            string            `json:"role"`
	Prompt          []json.RawMessage `json:"prompt"`
	Chosen          []json.RawMessage `json:"chosen"`
	Rejected        []json.RawMessage `json:"rejected"`
	Tools           json.RawMessage   `json:"tools,omitempty"`
	ChosenReward    float64           `json:"chosen_reward"`
	RejectedReward  float64           `json:"rejected_reward"`
	ChosenEpisode   string            `json:"chosen_episode"`
	RejectedEpisode string            `json:"rejected_episode"`
	ChosenRank      *rankInfo         `json:"chosen_rank,omitempty"`
	RejectedRank    *rankInfo         `json:"rejected_rank,omitempty"`
	WireHash        core.Hash         `json:"wire_hash,omitempty"`
	Split           string            `json:"split,omitempty"`
}

// dpoCand is a step considered for a step-level pair.
type dpoCand struct {
	c          stepCtx
	reward     float64
	completion json.RawMessage
	prompt     wirePrompt
}

// rewardGap is the smallest reward difference that makes a pair worth training on.
const rewardGap = 1e-9

// writeDPO writes step preferences before episode preferences and stops on the first export error.
func (x *exporter) writeDPO(work []*workEpisode) error {
	if err := x.dpoSteps(work); err != nil {
		return err
	}
	return x.dpoEpisodes(work)
}

// dpoSteps pairs steps that saw the identical prompt. Prompts repeat across
// episodes of a group (every rollout of a task starts from the same first prompt;
// compactor forks of identical threads coincide), so the same prompt with different
// completions and different outcomes is a controlled comparison.
func (x *exporter) dpoSteps(work []*workEpisode) error {
	byHash := map[core.Hash][]stepCtx{}
	var hashes []core.Hash
	err := eachStep(work, func(c stepCtx) error {
		if c.st.Prompt.WireHash == "" {
			return nil
		}
		if r := x.stepVerdict(c); r != "" {
			x.noteDrop(c, r)
			return nil
		}
		h := c.st.Prompt.WireHash
		if x.o.Pair == PairBestWorst {
			h += core.Hash("\x00" + rl.GroupKey(c.we.ep)) // compare within one rollout group only
		}
		if _, ok := byHash[h]; !ok {
			hashes = append(hashes, h)
		}
		byHash[h] = append(byHash[h], c)
		return nil
	})
	if err != nil {
		return err
	}
	sort.Slice(hashes, func(i, j int) bool { return hashes[i] < hashes[j] })
	for _, h := range hashes {
		group := byHash[h]
		if len(group) < 2 {
			continue
		}
		var cands []dpoCand
		seenEpisode := map[string]bool{}
		for _, c := range group {
			if seenEpisode[c.we.ep.ID+"|"+c.ag.ID] {
				continue // one step per agent of an episode per prompt
			}
			seenEpisode[c.we.ep.ID+"|"+c.ag.ID] = true
			r, err := x.renderStep(c, true)
			if err != nil {
				x.stats.drop("step:no_prompt")
				x.stats.warn(c.st.ID + ": " + err.Error())
				continue
			}
			rw, _ := rewardOf(c.we.ep, c.ag, c.st)
			cands = append(cands, dpoCand{c: c, reward: rw, completion: r.completion, prompt: r.prompt})
		}
		best, worst, why := x.pickPair(cands)
		if best == nil {
			x.stats.drop(why)
			continue
		}
		if best.reward < x.o.MinReward {
			x.stats.drop("pair:below_min_reward")
			continue
		}
		split := x.splitOf(best.c.we.ep)
		rec := dpoRecord{
			Schema: SchemaDPO, ID: "dpo:step:" + shortHash(best.c.st.Prompt.WireHash) + stepPairSuffix(x.o.Pair, best.c.we.ep), Unit: "step", TaskID: best.c.we.ep.TaskID, Role: best.c.st.Role,
			Prompt: best.prompt.Messages, Chosen: []json.RawMessage{best.completion}, Rejected: []json.RawMessage{worst.completion},
			Tools: best.prompt.Tools, ChosenReward: best.reward, RejectedReward: worst.reward,
			ChosenEpisode: best.c.we.ep.ID, RejectedEpisode: worst.c.we.ep.ID, ChosenRank: rankOf(best.c.we), RejectedRank: rankOf(worst.c.we),
			WireHash: best.c.st.Prompt.WireHash, Split: split,
		}
		if err := x.emit(rec, best.c.st.Role, "pair", split); err != nil {
			return err
		}
		for _, cd := range []*dpoCand{best, worst} {
			cd.c.we.contributed = true
			x.countTeacher(cd.c.st)
		}
		x.stats.PromptTokens += best.c.st.Prompt.Tokens
		x.stats.ResponseTokens += best.c.st.Usage.OutputTokens + worst.c.st.Usage.OutputTokens
	}
	return nil
}

// pickPair returns the chosen and rejected candidate of one prompt, or why there
// is no pair. With Pair "reward" the chosen side is the highest-reward candidate
// and the rejected side the lowest-reward one whose reward is strictly lower;
// with "best-worst" they are rank 1 and the lowest-ranked candidate whose rank
// key is strictly worse, and rank 1 must be verified. Either way the completions
// must differ, and ties break on episode id so the result is deterministic.
func (x *exporter) pickPair(cands []dpoCand) (best, worst *dpoCand, why string) {
	if len(cands) < 2 {
		return nil, nil, "pair:no_reward_gap_or_same_completion"
	}
	ranked := x.o.Pair == PairBestWorst
	sort.SliceStable(cands, func(i, j int) bool {
		a, b := &cands[i], &cands[j]
		if ranked {
			if c := rl.CompareRank(a.c.we.key, b.c.we.key); c != 0 {
				return c < 0
			}
		} else if a.reward != b.reward {
			return a.reward > b.reward
		}
		return a.c.we.ep.ID < b.c.we.ep.ID
	})
	best = &cands[0]
	if ranked && best.c.we.rank.Position != 1 {
		return nil, nil, "pair:best_not_rank_1" // rank 1 of the group has no candidate for this prompt
	}
	if ranked && !best.c.we.key.Verified {
		return nil, nil, "pair:best_not_verified"
	}
	for i := len(cands) - 1; i > 0; i-- {
		w := &cands[i]
		gap := best.reward-w.reward > rewardGap
		if ranked {
			gap = rl.CompareRank(best.c.we.key, w.c.we.key) < 0
		}
		if gap && !sameJSON(best.completion, w.completion) {
			return best, w, ""
		}
	}
	if ranked {
		return nil, nil, "pair:no_rank_gap_or_same_completion"
	}
	return nil, nil, "pair:no_reward_gap_or_same_completion"
}

// stepPairSuffix distinguishes best-worst step pairs of the same prompt in
// different rollout groups.
func stepPairSuffix(pair string, ep *rl.Episode) string {
	if pair != PairBestWorst {
		return ""
	}
	sum := sha256.Sum256([]byte(rl.GroupKey(ep)))
	return ":" + hex.EncodeToString(sum[:4])
}

// shortHash returns the abbreviated content hash used in DPO output.
func shortHash(h core.Hash) string { return h.Short() }

// dpoEpisodes pairs, per task, the best and the worst episode by reward, comparing
// what the root agent did after the shared first prompt. It needs a root agent that
// never rebased (one segment), so that its last prompt still contains the whole
// conversation, and a first prompt common to both episodes.
func (x *exporter) dpoEpisodes(work []*workEpisode) error {
	type epView struct {
		we     *workEpisode
		cv     *chainView
		first  wirePrompt
		reward float64
	}
	byTask := map[string][]*epView{}
	var tasks []string
	for _, we := range work {
		root := rootAgent(we.ep)
		if root < 0 {
			continue
		}
		ag := &we.ep.Agents[root]
		var chain []stepCtx
		segments := map[int]bool{}
		for si := range ag.Steps {
			if ag.Steps[si].Kind != rl.KindMain {
				continue
			}
			segments[ag.Steps[si].Segment] = true
			chain = append(chain, stepCtx{we: we, ag: ag, ai: root, st: &ag.Steps[si], si: si})
		}
		if len(chain) == 0 {
			continue
		}
		if len(segments) != 1 {
			x.stats.drop("pair:multi_segment")
			continue
		}
		okAll := true
		for _, c := range chain {
			if r := x.stepVerdict(c); r != "" {
				x.noteDrop(c, r)
				okAll = false
				break
			}
		}
		if !okAll {
			continue
		}
		cv, err := x.renderChain(chain, true)
		if err != nil {
			x.stats.drop("step:no_prompt")
			x.stats.warn(chain[0].st.ID + ": " + err.Error())
			continue
		}
		p, err := x.promptFor(we, chain[0].st)
		if err != nil {
			continue
		}
		rp, _ := x.redactPrompt(p)
		first, err := x.renderPrompt(rp, true)
		if err != nil {
			continue
		}
		v := &epView{we: we, cv: cv, first: first, reward: we.ep.Reward.Total}
		bucket := we.ep.TaskID
		if x.o.Pair == PairBestWorst {
			bucket = rl.GroupKey(we.ep)
		}
		if _, ok := byTask[bucket]; !ok {
			tasks = append(tasks, bucket)
		}
		byTask[bucket] = append(byTask[bucket], v)
	}
	sort.Strings(tasks)
	for _, t := range tasks {
		views := byTask[t]
		if len(views) < 2 {
			continue
		}
		ranked := x.o.Pair == PairBestWorst
		sort.SliceStable(views, func(i, j int) bool {
			if ranked {
				if c := rl.CompareRank(views[i].we.key, views[j].we.key); c != 0 {
					return c < 0
				}
			} else if views[i].reward != views[j].reward {
				return views[i].reward > views[j].reward
			}
			return views[i].we.ep.ID < views[j].we.ep.ID
		})
		best, worst := views[0], views[len(views)-1]
		switch {
		case ranked && best.we.rank.Position != 1:
			x.stats.drop("pair:best_not_rank_1") // rank 1 of the group has no usable root chain
			continue
		case ranked && !best.we.key.Verified:
			x.stats.drop("pair:best_not_verified")
			continue
		case ranked && rl.CompareRank(best.we.key, worst.we.key) >= 0:
			x.stats.drop("pair:no_rank_gap")
			continue
		case !ranked && best.reward-worst.reward <= rewardGap:
			x.stats.drop("pair:no_reward_gap")
			continue
		}
		if best.reward < x.o.MinReward {
			x.stats.drop("pair:below_min_reward")
			continue
		}
		if !samePrompt(best.first, worst.first) {
			x.stats.drop("pair:prompt_differs")
			continue
		}
		n := len(best.first.Messages)
		if len(best.cv.msgs) <= n || len(worst.cv.msgs) <= n {
			continue
		}
		if sameMessages(best.cv.msgs[n:], worst.cv.msgs[n:]) {
			x.stats.drop("pair:same_continuation") // a pair of equal sides teaches nothing
			continue
		}
		split := x.splitOf(best.we.ep)
		sum := sha256.Sum256([]byte(best.we.ep.ID + "|" + worst.we.ep.ID))
		rec := dpoRecord{
			Schema: SchemaDPO, ID: "dpo:episode:" + hex.EncodeToString(sum[:6]), Unit: "episode", TaskID: best.we.ep.TaskID,
			Role: best.cv.steps[0].st.Role, Prompt: best.first.Messages,
			Chosen: best.cv.msgs[n:], Rejected: worst.cv.msgs[n:], Tools: best.first.Tools,
			ChosenReward: best.reward, RejectedReward: worst.reward, ChosenEpisode: best.we.ep.ID, RejectedEpisode: worst.we.ep.ID,
			ChosenRank: rankOf(best.we), RejectedRank: rankOf(worst.we),
			WireHash: best.cv.steps[0].st.Prompt.WireHash, Split: split,
		}
		if err := x.emit(rec, rec.Role, "pair", split); err != nil {
			return err
		}
		best.we.contributed, worst.we.contributed = true, true
		for _, c := range append(append([]stepCtx(nil), best.cv.steps...), worst.cv.steps...) {
			x.countTeacher(c.st)
		}
	}
	return nil
}

// samePrompt compares tools and ordered messages using whitespace-insensitive JSON comparison.
func samePrompt(a, b wirePrompt) bool {
	return sameJSON(a.Tools, b.Tools) && sameMessages(a.Messages, b.Messages)
}

// sameMessages reports whether two message lists are equal, message by message, as JSON.
func sameMessages(a, b []json.RawMessage) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !sameJSON(a[i], b[i]) {
			return false
		}
	}
	return true
}

// rootAgent is the index of the manager, or of the first agent without a parent.
func rootAgent(ep *rl.Episode) int {
	for i, a := range ep.Agents {
		if a.Role == rl.RoleManager {
			return i
		}
	}
	for i, a := range ep.Agents {
		if a.Parent == "" {
			return i
		}
	}
	return -1
}

var _ = math.Abs
