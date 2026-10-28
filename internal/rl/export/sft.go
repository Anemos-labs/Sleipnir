package export

import (
	"encoding/json"
	"sort"
	"time"

	"github.com/anemos-labs/sleipnir/internal/rl"
)

// sftRecord is one line of the sft format, OpenAI's supervised fine-tuning shape:
// chat messages with tool-call arguments as JSON strings, plus the tools. Weights
// is the machine-friendly view of the per-message "weight" the assistant messages
// carry: 1 on the turns to learn from, 0 on everything else, aligned with messages.
type sftRecord struct {
	Schema   string            `json:"schema"`
	ID       string            `json:"id"`
	TaskID   string            `json:"task_id"`
	GroupID  string            `json:"group_id"`
	Sample   int               `json:"sample"`
	Role     string            `json:"role"`
	Agent    string            `json:"agent"`
	Segment  int               `json:"segment"`
	Steps    []string          `json:"steps"`
	Messages []json.RawMessage `json:"messages"`
	Tools    json.RawMessage   `json:"tools,omitempty"`
	Weights  []int             `json:"weights"`
	Reward   float64           `json:"reward"`
	Rank     *rankInfo         `json:"rank,omitempty"`
	Split    string            `json:"split,omitempty"`
	Meta     stepMeta          `json:"meta"`
}

// sftEpisodes selects the episodes rejection sampling keeps: verified (a passing
// verifier verdict when there is one), reward at least MinReward, then the best
// TopK per task by reward or, with Select "best", the best TopK (default 1) per
// rollout group by the best-of-n ranking.
func (x *exporter) sftEpisodes(work []*workEpisode) []*workEpisode {
	var ok []*workEpisode
	for _, we := range work {
		ep := we.ep
		// Best-of-group needs a verdict: a weak label cannot be the verified best.
		if v := ep.Outcome.Verifier; v != nil && !v.Pass || x.o.Select == SelectBest && !we.key.Verified {
			x.stats.drop("episode:not_verified")
			continue
		}
		if ep.Reward.Total < x.o.MinReward {
			x.stats.drop("episode:below_min_reward")
			continue
		}
		ok = append(ok, we)
	}
	var kept []*workEpisode
	switch {
	case x.o.Select == SelectBest:
		kept = x.topPer(work, ok, x.groupOf, max(x.o.TopK, 1), "episode:not_best_of_group", func(a, b *workEpisode) bool {
			return a.rank.Position < b.rank.Position
		})
	case x.o.TopK <= 0:
		kept = ok
	default:
		kept = x.topPer(work, ok, x.taskOf, x.o.TopK, "episode:below_top_k", func(a, b *workEpisode) bool {
			if a.ep.Reward.Total != b.ep.Reward.Total {
				return a.ep.Reward.Total > b.ep.Reward.Total
			}
			return a.ep.ID < b.ep.ID
		})
	}
	if x.o.SFTPrefix == PrefixBest {
		kept = append(kept, x.bestPrefixes(work)...)
	}
	return kept
}

// PrefixBest is the SFTPrefix that exports failed episodes up to their best
// savepoint.
const PrefixBest = "best"

// bestPrefixes are the failed episodes that scored above 0 at a savepoint, each
// cut at its best-scoring one (the earliest of equal scores): the work that
// went well before the run went wrong. Their until is the savepoint's time.
func (x *exporter) bestPrefixes(work []*workEpisode) []*workEpisode {
	var out []*workEpisode
	for _, we := range work {
		v := we.ep.Outcome.Verifier
		if v == nil || v.Pass || len(we.ep.Progress) == 0 {
			continue
		}
		best := -1
		for i, p := range we.ep.Progress {
			if !p.At.IsZero() && p.Score > 0 && (best < 0 || p.Score > we.ep.Progress[best].Score) {
				best = i
			}
		}
		if best < 0 {
			continue
		}
		cut := *we
		cut.until = we.ep.Progress[best].At
		x.stats.Prefixes++
		out = append(out, &cut)
	}
	return out
}

// beforeCut reports whether a step of a prefix-cut episode completed before its
// cut (always true for an episode without one).
func beforeCut(we *workEpisode, st *rl.Step) bool {
	if we.until.IsZero() {
		return true
	}
	if st.At.IsZero() {
		return false
	}
	return !st.At.Add(time.Duration(st.LatencyMs) * time.Millisecond).After(we.until)
}

// topPer keeps the k first episodes of each bucket in the order less defines, counts the rest
// under reason, and returns the kept ones in work's order.
func (x *exporter) topPer(work, ok []*workEpisode, bucket func(*rl.Episode) string, k int, reason string, less func(a, b *workEpisode) bool) []*workEpisode {
	byKey := map[string][]*workEpisode{}
	var keys []string
	for _, we := range ok {
		key := bucket(we.ep)
		if _, seen := byKey[key]; !seen {
			keys = append(keys, key)
		}
		byKey[key] = append(byKey[key], we)
	}
	sort.Strings(keys)
	var out []*workEpisode
	for _, key := range keys {
		eps := byKey[key]
		sort.SliceStable(eps, func(i, j int) bool { return less(eps[i], eps[j]) })
		for i, we := range eps {
			if i < k {
				out = append(out, we)
			} else {
				x.stats.drop(reason)
			}
		}
	}
	pos := map[*workEpisode]int{}
	for i, we := range work {
		pos[we] = i
	}
	sort.SliceStable(out, func(i, j int) bool { return pos[out[i]] < pos[out[j]] })
	return out
}

// writeSFT exports selected episodes by agent and continuous prompt chain, stopping on the first
// unit failure.
func (x *exporter) writeSFT(work []*workEpisode) error {
	for _, we := range x.sftEpisodes(work) {
		for ai := range we.ep.Agents {
			ag := &we.ep.Agents[ai]
			for _, unit := range splitChains(we, ag, ai) {
				if err := x.sftUnit(unit); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// sftUnit writes one chain as a packed record (PackSegments) or each of its steps
// as its own record.
func (x *exporter) sftUnit(unit []stepCtx) error {
	if len(unit) > 0 && !unit[0].we.until.IsZero() {
		var before []stepCtx
		for _, c := range unit {
			if beforeCut(c.we, c.st) {
				before = append(before, c)
			} else {
				x.stats.drop("step:after_best_savepoint")
			}
		}
		unit = before
	}
	var ok []stepCtx
	for _, c := range unit {
		if x.selectStep(c) {
			ok = append(ok, c)
		}
	}
	if len(ok) == 0 {
		return nil
	}
	if x.o.PackSegments && unit[0].st.Kind == rl.KindMain {
		return x.sftPacked(unit, ok)
	}
	for _, c := range ok {
		if err := x.sftStep(c); err != nil {
			return err
		}
	}
	return nil
}

// sftStep writes one call as prompt plus completion, weighting only the completion.
func (x *exporter) sftStep(c stepCtx) error {
	r, err := x.renderStep(c, false)
	if err != nil {
		x.stats.drop("step:no_prompt")
		x.warn(c.st.ID + ": " + err.Error())
		return nil
	}
	msgs := append(append([]json.RawMessage(nil), r.prompt.Messages...), r.completion)
	weights := make([]int, len(msgs))
	weights[len(msgs)-1] = 1
	return x.emitSFT(c, []stepCtx{c}, msgs, r.prompt.Tools, weights)
}

// sftPacked writes a whole segment as one conversation: every model turn that was
// selected gets weight 1, every other turn (teacher turns not in TeacherOK, steps a
// rule dropped, tool results, mail) weight 0. Trailing steps that exceed a length
// limit are cut off (cutLong), so the conversation fits too.
func (x *exporter) sftPacked(unit, ok []stepCtx) error {
	if unit = x.cutLong(unit); len(unit) == 0 {
		return nil
	}
	cv, err := x.renderChain(unit, false)
	if err != nil {
		x.stats.drop("step:no_prompt")
		x.warn(unit[0].st.ID + ": " + err.Error())
		return nil
	}
	selected := map[string]bool{}
	for _, c := range ok {
		selected[c.st.ID] = true
	}
	weights := make([]int, len(cv.msgs))
	var used []stepCtx
	for j, c := range unit {
		if selected[c.st.ID] && cv.at[j] >= 0 {
			weights[cv.at[j]] = 1
			used = append(used, c)
		}
	}
	if len(used) == 0 {
		return nil
	}
	return x.emitSFT(unit[len(unit)-1], used, cv.msgs, cv.tools, weights)
}

func (x *exporter) emitSFT(c stepCtx, used []stepCtx, msgs []json.RawMessage, tools json.RawMessage, weights []int) error {
	wm, err := withWeight(msgs, weights)
	if err != nil {
		return err
	}
	reward, _ := rewardOf(c.we.ep, c.ag, c.st)
	split := x.splitOf(c.we.ep)
	ids := make([]string, len(used))
	for i, u := range used {
		ids[i] = u.st.ID
	}
	id := recordID(c.we.ep, used[0].st)
	if len(used) > 1 {
		id = recordID(c.we.ep, used[0].st) + "+" + used[len(used)-1].st.ID
	}
	rec := sftRecord{
		Schema: SchemaSFT, ID: id, TaskID: c.we.ep.TaskID, GroupID: c.we.ep.Group, Sample: c.we.ep.Sample, Role: c.st.Role, Agent: c.ag.ID,
		Segment: c.st.Segment, Steps: ids, Messages: wm, Tools: tools, Weights: weights, Reward: reward, Rank: rankOf(c.we), Split: split, Meta: x.metaOf(c),
	}
	unit := "step"
	if len(used) > 1 || x.o.PackSegments && c.st.Kind == rl.KindMain {
		unit = "segment"
	}
	if err := x.emit(rec, c.st.Role, unit, split); err != nil {
		return err
	}
	c.we.contributed = true
	for _, u := range used {
		x.countTeacher(u.st)
		x.stats.PromptTokens += u.st.Prompt.Tokens
		x.stats.ResponseTokens += u.st.Usage.OutputTokens
		x.stats.TrainedTokens += u.st.Usage.OutputTokens
	}
	return nil
}
