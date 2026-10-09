package reward

import (
	"github.com/anemos-labs/sleipnir/internal/rl"
)

// penalty stores x in [0, 1] as a penalty component: -x, and 0 rather than -0 so the
// stored JSON reads 0.
func penalty(x float64) float64 {
	if x == 0 {
		return 0
	}
	return -x
}

// groupITEFrac places the episode's repriced ITE in its group's range: 0 for the cheapest,
// 1 for the dearest. It is 0 without a range (scored alone), with a one-ITE group, or for an
// episode that could not be priced.
func (s *scorer) groupITEFrac() float64 {
	g := s.cfg.groupITE
	if g == nil {
		if s.cfg.weights[CompGroupITE] > 0 {
			s.note("group_ite: scored without its rollout group: 0 (rl reward scores whole groups)")
		}
		return 0
	}
	lo, hi := g[0], g[1]
	if !finite(lo) || !finite(hi) || hi <= lo || s.rep.ITE <= 0 {
		return 0
	}
	return clamp01((s.rep.ITE - lo) / (hi - lo))
}

// GroupITE reprices every episode under cfg's target, as Score would, and returns the lowest
// and highest positive ITE of each rollout group, keyed by rl.GroupKey. A caller that scores
// whole groups sets Config.GroupITE from it, so the group_ite component compares each episode
// with the others of its group. Episodes flagged infra_error are left out.
func GroupITE(eps []*rl.Episode, cfg Config) (map[string][2]float64, error) {
	r, err := cfg.resolve()
	if err != nil {
		return nil, err
	}
	out := map[string][2]float64{}
	for _, ep := range eps {
		if ep == nil || ep.Has(rl.FlagInfraError) {
			continue
		}
		rep, err := RepriceWith(ep, r.target, r.reprice)
		if err != nil {
			return nil, err
		}
		if rep.ITE <= 0 || !finite(rep.ITE) {
			continue
		}
		k := rl.GroupKey(ep)
		g, ok := out[k]
		if !ok {
			g = [2]float64{rep.ITE, rep.ITE}
		}
		g[0], g[1] = min(g[0], rep.ITE), max(g[1], rep.ITE)
		out[k] = g
	}
	return out, nil
}
