package adv

import (
	"math"
	"sort"
	"strconv"

	"github.com/anemos-labs/sleipnir/internal/rl"
)

// Anchor-state credit (GiGPO-style; docs/TRAINING-DATA.md).
//
// A sample's reward is terminal, so within a rollout every step shares one
// advantage. That is too coarse when several rollouts of the same task pass
// through the *same state*: how each of them fared from there on says something
// about the action taken there. Steps whose starting state is identical are
// grouped across rollouts and their discounted returns standardised inside the
// group; the result is added to the sample-level advantage.
//
// What "the same state" means is Spec.AnchorBy:
//
//	prompt  the steps' prompts are byte-identical (same wire hash). This is the
//	        strict definition: the policy saw exactly the same thing. Rollouts of
//	        one task typically share their first prompts; deeper steps coincide
//	        only when the rollouts did (deterministic tools, low temperature).
//	depth   the steps sit at the same position of their samples, in rollouts that
//	        start from the same tree hash and shared prefix. Looser: it conditions
//	        the baseline on depth, which removes the "late steps look worse" bias.
//
// Steps in flat baseline groups take no part: with no signal in the rewards, a
// discount factor alone would invent some.

type anchorRef struct {
	smp *sample
	idx int
	ret float64
}

func anchorKey(smp *sample, st *rl.Step, ordinal int, by string) string {
	if by != AnchorByDepth && st.Prompt.WireHash != "" {
		return "w|" + smp.group + "|" + smp.role + "|" + string(st.Prompt.WireHash)
	}
	return "d|" + smp.group + "|" + smp.role + "|" + smp.ep.Env.TreeHash + "|" + st.Prompt.SharedPrefix + "|" + strconv.Itoa(ordinal)
}

func applyAnchor(samples []*sample, s Spec) AnchorStat {
	gamma, minSize, eps := s.gamma(), s.anchorMin(), s.eps()
	groups := map[string][]anchorRef{}
	for _, smp := range samples {
		if !smp.valid || smp.flat || len(smp.steps) == 0 {
			continue
		}
		smp.zs = make([]float64, len(smp.steps))
		last := len(smp.steps) - 1
		for t, st := range smp.steps {
			ret := smp.reward * math.Pow(gamma, float64(last-t))
			k := anchorKey(smp, st, t, s.AnchorBy)
			groups[k] = append(groups[k], anchorRef{smp, t, ret})
		}
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var st AnchorStat
	sizes := 0
	for _, k := range keys {
		refs := groups[k]
		distinct := map[*rl.Episode]bool{}
		for _, r := range refs {
			distinct[r.smp.ep] = true
		}
		if len(distinct) < 2 {
			st.Singleton++
			continue
		}
		if len(refs) < minSize {
			continue
		}
		rets := make([]float64, len(refs))
		for i, r := range refs {
			rets[i] = r.ret
		}
		mean, std := meanStd(rets)
		lo, hi := spread(rets)
		st.Groups++
		sizes += len(refs)
		if len(refs) > st.MaxSize {
			st.MaxSize = len(refs)
		}
		if hi-lo <= flatTol {
			continue // identical outcomes from this state: nothing to credit
		}
		for _, r := range refs {
			r.smp.zs[r.idx] = (r.ret - mean) / (std + eps)
			st.Steps++
		}
	}
	if st.Groups > 0 {
		st.MeanSize = float64(sizes) / float64(st.Groups)
	}
	return st
}
