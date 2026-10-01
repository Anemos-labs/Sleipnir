package reward

import (
	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/rl"
)

// compEvent is the reward of one compactor call (a fork of the agent's request
// that returns a patch), built from
//
//	valid        the patch parses and applied without the mechanical fallback
//	size         -(prompt tokens kept after the rebase / thread tokens folded)
//	fidelity     probe recall on the prompt that follows the rebase
//	rebase_cost  -(write cost of the next request under the target price model)
//	downstream   the episode reward, broadcast: the compactor is rewarded for the
//	             agent still succeeding
//
// Only downstream is shared by every compaction of an episode; the others tell
// one compaction from another.
type compEvent struct {
	ai, si int
	stepID string
	comps  map[string]float64
	total  float64
}

// patchUsable reports whether a compactor step's reply is a usable patch, judged
// from what the episode recorded: how the call ended, its text, and any error
// observation attached to it.
func patchUsable(st *rl.Step) (bool, string) {
	for _, o := range st.Observations {
		if o.IsError {
			return false, "the compaction was rejected: " + clipText(o.Output, 80)
		}
	}
	switch st.Completion.Stop {
	case core.StopMaxTokens:
		return false, "the reply was cut off by max_tokens"
	case core.StopRefusal:
		return false, "the model refused"
	}
	text := st.Completion.Turn.PlainText()
	if text == "" {
		return false, "empty reply"
	}
	if _, reason := parsePatch(text); reason != "" {
		return false, reason
	}
	return true, ""
}

// keptRatio is the share of the thread that survived a compaction:
// (prompt after - base) / (prompt before - base), where base is the prompt of
// the agent's first request (constitution, pins, task) that no compaction
// touches. A verbatim copy scores 1; folding everything scores near 0. ok is
// false when the sizes are not recorded.
func keptRatio(a *rl.Agent, si, ni int) (ratio float64, ok bool) {
	pm := prevMain(a, si)
	before := 0
	if pm >= 0 {
		before = promptTokens(&a.Steps[pm])
	} else {
		before = promptTokens(&a.Steps[si])
	}
	after := promptTokens(&a.Steps[ni])
	if before <= 0 || after <= 0 {
		return 0, false
	}
	base := 0
	if fm := firstMain(a); fm >= 0 {
		base = promptTokens(&a.Steps[fm])
	}
	thread, kept := before-base, after-base
	if thread <= 0 {
		return 1, true // nothing was folded
	}
	if kept < 0 {
		kept = 0
	}
	return clamp01(float64(kept) / float64(thread)), true
}

func (s *scorer) canProbe() bool {
	if s.cfg.prompts != nil {
		return true
	}
	if _, ok := s.diff.(PromptSource); ok {
		return true
	}
	for ai := range s.ep.Agents {
		for si := range s.ep.Agents[ai].Steps {
			if s.ep.Agents[ai].Steps[si].Inline != nil {
				return true
			}
		}
	}
	return false
}

// compactionEvents scores every compactor step of the episode.
func (s *scorer) compactionEvents(epTotal float64) []compEvent {
	ep := s.ep
	type ref struct{ ai, si int }
	var refs []ref
	for ai := range ep.Agents {
		a := &ep.Agents[ai]
		for si := range a.Steps {
			if stepRole(a, &a.Steps[si]) == rl.RoleCompactor {
				refs = append(refs, ref{ai, si})
			}
		}
	}
	if len(refs) == 0 {
		return nil
	}

	// Validity. A reply that cannot possibly be a patch is a rejection for sure;
	// the episode's compact_rejects counts the rest without saying which calls, so
	// they are spread evenly over the remaining compactions.
	definite := make([]bool, len(refs))
	nDefinite := 0
	for i, r := range refs {
		st := &ep.Agents[r.ai].Steps[r.si]
		if ok, why := patchUsable(st); !ok {
			definite[i] = true
			nDefinite++
			s.note("compactor %s: %s", st.ID, why)
		}
	}
	residual := sigOr(ep, rl.SigCompactRejects, 0) - float64(nDefinite)
	share := 0.0
	if eligible := len(refs) - nDefinite; eligible > 0 && residual > 0 {
		share = clamp01(residual / float64(eligible))
	}

	// Fidelity probes, when a prompt source exists.
	recall := map[[2]int]float64{}
	if s.cfg.probes {
		if !s.canProbe() {
			s.note("fidelity probes skipped: no prompt text source (set Config.Prompts, implement PromptSource on the DiffSource, or inline prompts)")
		} else {
			perProbe := int(s.cfg.cap(CapProbeFacts))
			for _, p := range probes(ep, s.src, perProbe) {
				res, err := ScoreProbes(ep, []Probe{p}, s.src)
				if err != nil {
					s.note("fidelity probe of %s skipped: %v", p.Compactor, err)
					continue
				}
				if res[0].Scored {
					recall[[2]int{p.AgentIndex, p.StepIndex}] = res[0].Recall
				}
			}
		}
	}

	out := make([]compEvent, 0, len(refs))
	for i, r := range refs {
		a := &ep.Agents[r.ai]
		st := &a.Steps[r.si]
		c := map[string]float64{}
		if definite[i] {
			c[CompValid] = 0
		} else {
			c[CompValid] = clamp01(1 - share)
		}
		ni := nextRebase(ep, r.ai, r.si)
		switch {
		case ni < 0:
			s.note("compactor %s: the episode ended before the compaction took effect", st.ID)
		default:
			if ratio, ok := keptRatio(a, r.si, ni); ok {
				c[CompSize] = -ratio
			}
			if sb, ok := s.rep.StepOf(r.ai, ni); ok {
				c[CompRebase] = -s.cfg.frac(sb.WriteITE, CapRebase)
			}
		}
		if s.cfg.probes {
			if v, ok := recall[[2]int{r.ai, r.si}]; ok {
				if definite[i] {
					// The model's own patch was unusable, so nothing it wrote
					// carries facts; what survived is the mechanical fallback's.
					v = 0
				}
				c[CompFidelity] = v
			}
		}
		c[CompDownstream] = epTotal
		total := s.cfg.clipTotal(s.cfg.total(c))
		out = append(out, compEvent{ai: r.ai, si: r.si, stepID: st.ID, comps: c, total: total})
	}
	return out
}
