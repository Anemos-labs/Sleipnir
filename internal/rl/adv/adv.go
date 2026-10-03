// Package adv turns episode and role rewards into per-step advantages for
// group-relative policy optimisation (docs/TRAINING-DATA.md section 6).
//
// The unit of comparison is a *rollout group*: the episodes of one task under
// one policy snapshot. Inside a group the estimator compares like with like:
//
//   - rewards are normalised per role. A manager, a worker and a compactor earn
//     rewards on different scales and have different variances, so one global
//     baseline would push every worker up or down with the manager's luck;
//   - an agent is one sample: all trainable steps of its segment chain, every
//     segment included, receive the agent's advantage (terminal-reward
//     broadcast, so compaction is trained end to end with the steps it served);
//   - every compactor (or mailman) call is its own sample of its own role when
//     reward.Score scored it individually (Step.Reward, with a "role/<name>"
//     aggregate on the episode); otherwise it inherits the reward of the chain it
//     served and shares that chain's advantage;
//   - episodes carrying a hard flag (rl.HardFlag: infra errors, hacks, replay
//     mismatches, ...) and steps the policy did not sample (Trainable false) never
//     contribute to a baseline and never receive an advantage.
//
// Everything is deterministic and independent of the order episodes are
// passed in: samples are sorted before any sum is taken and sums are
// compensated.
package adv

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/rl"
	"github.com/anemos-labs/sleipnir/internal/rl/reward"
)

// Methods.
const (
	MethodGRPO      = "grpo"
	MethodRLOO      = "rloo"
	MethodBroadcast = "broadcast"
	MethodAnchor    = "anchor"
	MethodNone      = "none"
)

// Group-by values.
const (
	GroupByTask       = "task"
	GroupByTaskPolicy = "task+policy"
	GroupByGroup      = "group" // use Episode.Group, falling back to task+policy
)

// Anchor-state definitions.
const (
	AnchorByPrompt = "prompt" // steps whose prompts are byte-identical (same wire hash)
	AnchorByDepth  = "depth"  // steps at the same depth of rollouts from the same starting tree
)

// DefaultEps is the epsilon added to the standard deviation when Spec.Eps is 0.
const DefaultEps = 1e-6

// flatTol is the reward spread at or below which a group counts as having no
// learning signal. It is an absolute tolerance on rewards that live in about
// [-2, 2]; it exists so float noise cannot masquerade as signal.
const flatTol = 1e-9

// Spec selects and parameterises an advantage estimator.
type Spec struct {
	// Method is one of grpo, rloo, broadcast, anchor, none.
	//
	//	grpo       A = (r - mean) / (std + Eps) within the group (sample std, n-1)
	//	rloo       A = r - mean of the other samples
	//	broadcast  A = r (no baseline; the trainer normalises)
	//	anchor     grpo, plus a step-level correction from anchor states (below)
	//	none       clear every advantage
	Method string
	// PerRole computes baselines separately for each role. It should be true: a
	// global baseline destabilises heterogeneous agents. It is a plain bool so the
	// zero Spec is valid, which means the zero value is the *global* baseline;
	// start from DefaultSpec.
	PerRole bool
	// Eps is added to the standard deviation (grpo, anchor); 0 means DefaultEps.
	Eps float64
	// DropFlat lists, in Report.Dropped, episodes none of whose samples has
	// learning signal (every role group they belong to is flat). Apply cannot
	// remove episodes from the slice it is given; exporters drop the listed ones.
	DropFlat bool
	// GroupBy is task, task+policy (the default) or group.
	GroupBy string

	// Anchor options (Method anchor only).
	//
	// GiGPO-style step credit. Steps of different rollouts of the same task that
	// start from the same *anchor state* are grouped, and each step's discounted
	// return is standardised inside its anchor group:
	//
	//	R_t   = Gamma^(T-t) * r      (t: the step's position in its sample, T: the
	//	                              last position; r: the sample's reward)
	//	z_t   = (R_t - mean_anchor(R)) / (std_anchor(R) + Eps)
	//	A_t   = A_sample + AnchorWeight * z_t
	//
	// Anchor groups need AnchorMin (default 2) steps from at least two different
	// episodes; smaller groups add nothing. With terminal-only rewards and
	// Gamma = 1 the correction compares how rollouts that reached an identical
	// state fared afterwards.
	AnchorBy     string  // prompt (default) or depth
	Gamma        float64 // discount in (0, 1]; 0 means 1
	AnchorWeight float64 // weight of the step-level term; 0 means 1
	AnchorMin    int     // smallest anchor group that counts; 0 means 2
}

// DefaultSpec is grpo with per-role baselines, grouped by task and policy.
func DefaultSpec() Spec {
	return Spec{Method: MethodGRPO, PerRole: true, Eps: DefaultEps, GroupBy: GroupByTaskPolicy}
}

// SpecFor is DefaultSpec with the given method name, for command line flags
// ("--advantage grpo|rloo|broadcast|anchor|none").
func SpecFor(method string) (Spec, error) {
	s := DefaultSpec()
	s.Method = strings.ToLower(strings.TrimSpace(method))
	if err := s.validate(); err != nil {
		return Spec{}, err
	}
	return s, nil
}

func (s Spec) validate() error {
	switch s.Method {
	case MethodGRPO, MethodRLOO, MethodBroadcast, MethodAnchor, MethodNone:
	default:
		return fmt.Errorf("adv: unknown method %q (want grpo, rloo, broadcast, anchor or none)", s.Method)
	}
	switch s.GroupBy {
	case "", GroupByTask, GroupByTaskPolicy, GroupByGroup:
	default:
		return fmt.Errorf("adv: unknown GroupBy %q (want task, task+policy or group)", s.GroupBy)
	}
	if math.IsNaN(s.Eps) || math.IsInf(s.Eps, 0) || s.Eps < 0 {
		return fmt.Errorf("adv: Eps must be a finite number >= 0, got %v", s.Eps)
	}
	switch s.AnchorBy {
	case "", AnchorByPrompt, AnchorByDepth:
	default:
		return fmt.Errorf("adv: unknown AnchorBy %q (want prompt or depth)", s.AnchorBy)
	}
	if s.Gamma != 0 && !(s.Gamma > 0 && s.Gamma <= 1) {
		return fmt.Errorf("adv: Gamma must be in (0, 1], got %v", s.Gamma)
	}
	if math.IsNaN(s.AnchorWeight) || math.IsInf(s.AnchorWeight, 0) || s.AnchorWeight < 0 {
		return fmt.Errorf("adv: AnchorWeight must be a finite number >= 0, got %v", s.AnchorWeight)
	}
	if s.AnchorMin < 0 {
		return fmt.Errorf("adv: AnchorMin must be >= 0, got %d", s.AnchorMin)
	}
	return nil
}

// anchorWeight substitutes a weight of one for an unset zero value.
func (s Spec) anchorWeight() float64 {
	if s.AnchorWeight == 0 {
		return 1
	}
	return s.AnchorWeight
}

// gamma substitutes a discount factor of one for an unset zero value.
func (s Spec) gamma() float64 {
	if s.Gamma == 0 {
		return 1
	}
	return s.Gamma
}

// anchorMin substitutes a minimum anchor group size of two for an unset zero value.
func (s Spec) anchorMin() int {
	if s.AnchorMin == 0 {
		return 2
	}
	return s.AnchorMin
}

// eps returns the configured nonzero numerical tolerance or DefaultEps.
func (s Spec) eps() float64 {
	if s.Eps == 0 {
		return DefaultEps
	}
	return s.Eps
}

// GroupStat describes one baseline group: the samples of one role (or of all
// roles, when PerRole is off) among the episodes of one rollout group.
type GroupStat struct {
	Key  string // "<group>|<role>"
	Role string
	N    int
	Mean float64
	// Std is the sample standard deviation (n-1); 0 when N < 2.
	Std, Min, Max float64
	// Flat groups have no learning signal: a single sample or all rewards equal.
	Flat     bool
	Episodes []string // ids of the contributing episodes, sorted
}

// AnchorStat summarises the step-level correction of the anchor method.
type AnchorStat struct {
	Groups    int // anchor groups that were large enough
	Steps     int // steps that received a correction
	MaxSize   int
	MeanSize  float64
	Singleton int // anchor states seen by one sample only
}

// Skip is an episode the estimator ignored.
type Skip struct {
	Episode string
	Reason  string
}

// Report is what Apply did.
type Report struct {
	Method string
	Groups []GroupStat // sorted by key
	// Flat lists the keys of groups with no learning signal.
	Flat []string
	// Dropped lists episodes with no learning signal anywhere (Spec.DropFlat).
	Dropped []string
	// Skipped lists ignored episodes: hard-flagged, or without trainable samples.
	Skipped []Skip
	// Invalid counts samples whose reward was NaN or infinite; they receive 0.
	Invalid int
	// Steps is how many steps received an advantage.
	Steps  int
	Anchor AnchorStat
}

// Groups splits episodes into rollout groups keyed by task ("task"), task and
// policy snapshot ("task+policy", the default) or the recorded Episode.Group
// ("group"). Nil episodes are ignored; duplicates of one pointer count once.
func Groups(eps []*rl.Episode, by string) map[string][]*rl.Episode {
	out := map[string][]*rl.Episode{}
	seen := map[*rl.Episode]bool{}
	for _, ep := range eps {
		if ep == nil || seen[ep] {
			continue
		}
		seen[ep] = true
		k := groupKey(ep, by)
		out[k] = append(out[k], ep)
	}
	return out
}

// taskKey prefers explicit task identity, otherwise deriving it from the episode ID before the
// final slash.
func taskKey(ep *rl.Episode) string {
	switch {
	case ep.TaskID != "":
		return ep.TaskID
	case strings.Contains(ep.ID, "/"):
		return ep.ID[:strings.LastIndex(ep.ID, "/")]
	}
	return ep.ID
}

// policyKey identifies the policy snapshot: model, checkpoint and any role
// overrides (training the manager against fixed workers is a different policy
// setup than training everything).
func policyKey(ep *rl.Episode) string {
	p := ep.Policy
	var b strings.Builder
	b.WriteString(p.Model)
	b.WriteByte('#')
	b.WriteString(p.Checkpoint)
	roles := make([]string, 0, len(p.RoleModels))
	for r := range p.RoleModels {
		roles = append(roles, r)
	}
	sort.Strings(roles)
	for _, r := range roles {
		b.WriteByte(';')
		b.WriteString(r + "=" + p.RoleModels[r])
	}
	return b.String()
}

// groupKey groups episodes by task or explicit group when requested, otherwise combining task and
// policy identity.
func groupKey(ep *rl.Episode, by string) string {
	switch by {
	case GroupByTask:
		return taskKey(ep)
	case GroupByGroup:
		if ep.Group != "" {
			return ep.Group
		}
	}
	return taskKey(ep) + "@" + policyKey(ep)
}

// ---- samples -------------------------------------------------------------------------

// sample is one thing that receives an advantage: an agent's main-role step
// chain, or one compactor or mailman call.
type sample struct {
	ep     *rl.Episode
	ai     int
	si     int // step index for single-call samples, -1 for a whole agent
	role   string
	steps  []*rl.Step // trainable steps, in order
	reward float64
	valid  bool

	group string // rollout group key
	adv   float64
	flat  bool      // its baseline group has no learning signal
	zs    []float64 // per-step anchor correction (anchor method)
}

// sortKey orders samples by episode ID, then zero-padded agent and step indices separated by NUL
// bytes.
func (s *sample) sortKey() string { return fmt.Sprintf("%s\x00%06d\x00%06d", s.ep.ID, s.ai, s.si) }

// singleCall roles are trained per call, each with its own reward.
func singleCall(role string) bool { return role == rl.RoleCompactor || role == rl.RoleMailman }

// callReward returns the reward of a compactor or mailman call when it was
// scored on its own, and false when it should inherit its chain's. reward.Score
// leaves a per-call reward in Step.Reward, the mean over an agent's calls under
// the agent's "role/<name>" component, and the mean over the episode under the
// episode's; a call whose own reward is zero falls back to those aggregates, so
// hand-made episodes that only carry the aggregate work too.
func callReward(ep *rl.Episode, a *rl.Agent, st *rl.Step, role string) (float64, bool) {
	if st.Reward != 0 {
		return st.Reward, true
	}
	if v, ok := a.Reward.Components["role/"+role]; ok {
		return v, true
	}
	if v, ok := ep.Reward.Components["role/"+role]; ok {
		return v, true
	}
	return 0, false
}

// eligible rejects episodes carrying any hard flag and reports the first such flag.
func eligible(ep *rl.Episode) (bool, string) {
	for _, f := range ep.Flags {
		if rl.HardFlag(f) {
			return false, "hard flag " + f
		}
	}
	return true, ""
}

func collectSamples(eps []*rl.Episode, spec Spec, rep *Report) []*sample {
	var out []*sample
	seen := map[*rl.Episode]bool{}
	for _, ep := range eps {
		if ep == nil || seen[ep] {
			continue
		}
		seen[ep] = true
		if ok, why := eligible(ep); !ok {
			rep.Skipped = append(rep.Skipped, Skip{ep.ID, why})
			continue
		}
		gk := groupKey(ep, spec.GroupBy)
		before := len(out)
		for ai := range ep.Agents {
			a := &ep.Agents[ai]
			perRole := map[string]*sample{}
			var roles []string
			for si := range a.Steps {
				st := &a.Steps[si]
				if !st.Trainable {
					continue
				}
				role := reward.StepRole(a, st)
				if singleCall(role) {
					if r, ok := callReward(ep, a, st, role); ok {
						// Scored individually: one sample per call, in the call's own role.
						out = append(out, &sample{ep: ep, ai: ai, si: si, role: role, steps: []*rl.Step{st},
							reward: r, group: gk})
						continue
					}
					// Unscored: the call inherits the reward of the chain it served, i.e.
					// it is part of its agent's sample and shares its advantage.
					role = reward.RoleClass(a.Role)
				}
				smp := perRole[role]
				if smp == nil {
					smp = &sample{ep: ep, ai: ai, si: -1, role: role, reward: a.Reward.Total, group: gk}
					perRole[role] = smp
					roles = append(roles, role)
				}
				smp.steps = append(smp.steps, st)
			}
			sort.Strings(roles)
			for _, r := range roles {
				out = append(out, perRole[r])
			}
		}
		if len(out) == before {
			rep.Skipped = append(rep.Skipped, Skip{ep.ID, "no trainable steps"})
		}
	}
	for _, s := range out {
		s.valid = !math.IsNaN(s.reward) && !math.IsInf(s.reward, 0)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].sortKey() < out[j].sortKey() })
	return out
}

// ---- statistics --------------------------------------------------------------------------

// sum adds with Neumaier compensation: order-independent to within one ulp,
// which is what makes results independent of how episodes were ordered.
func sum(v []float64) float64 {
	s, c := 0.0, 0.0
	for _, x := range v {
		t := s + x
		if math.Abs(s) >= math.Abs(x) {
			c += (s - t) + x
		} else {
			c += (x - t) + s
		}
		s = t
	}
	return s + c
}

// meanStd returns the mean and the sample (n-1) standard deviation.
func meanStd(v []float64) (mean, std float64) {
	n := len(v)
	if n == 0 {
		return 0, 0
	}
	mean = sum(v) / float64(n)
	if n < 2 {
		return mean, 0
	}
	dev := make([]float64, n)
	for i, x := range v {
		d := x - mean
		dev[i] = d * d
	}
	return mean, math.Sqrt(sum(dev) / float64(n-1))
}

// spread returns minimum and maximum values, or positive and negative infinity respectively for
// empty input.
func spread(v []float64) (lo, hi float64) {
	lo, hi = math.Inf(1), math.Inf(-1)
	for _, x := range v {
		lo, hi = math.Min(lo, x), math.Max(hi, x)
	}
	return lo, hi
}

// ---- Apply ---------------------------------------------------------------------------------

// Apply computes advantages and writes them to Step.Advantage.
//
// Every step of every given episode is first reset to 0, so the result never
// mixes runs. Then, for each rollout group and role, the samples' rewards are
// turned into advantages by the chosen method and assigned to the samples'
// trainable steps. Groups of a single sample or with all-equal rewards have no
// baseline to speak of: their advantages are 0 (for grpo, rloo and anchor) and
// they are listed in Report.Flat.
func Apply(eps []*rl.Episode, s Spec) (Report, error) {
	s.Method = strings.ToLower(strings.TrimSpace(s.Method))
	s.GroupBy = strings.ToLower(strings.TrimSpace(s.GroupBy))
	s.AnchorBy = strings.ToLower(strings.TrimSpace(s.AnchorBy))
	rep := Report{Method: s.Method}
	if err := s.validate(); err != nil {
		return rep, err
	}
	if s.Method == "" {
		return rep, errors.New("adv: Spec.Method is required")
	}
	// Reset first: stale advantages from an earlier run must not survive.
	seen := map[*rl.Episode]bool{}
	for _, ep := range eps {
		if ep == nil || seen[ep] {
			continue
		}
		seen[ep] = true
		for ai := range ep.Agents {
			for si := range ep.Agents[ai].Steps {
				ep.Agents[ai].Steps[si].Advantage = 0
			}
		}
	}
	if s.Method == MethodNone {
		return rep, nil
	}

	samples := collectSamples(eps, s, &rep)
	sort.SliceStable(rep.Skipped, func(i, j int) bool { return rep.Skipped[i].Episode < rep.Skipped[j].Episode })

	// Bucket by rollout group and role.
	type bucket struct {
		key, role string
		members   []*sample
	}
	byKey := map[string]*bucket{}
	for _, smp := range samples {
		if !smp.valid {
			rep.Invalid++
			continue
		}
		role := smp.role
		if !s.PerRole {
			role = "*"
		}
		key := smp.group + "|" + role
		b := byKey[key]
		if b == nil {
			b = &bucket{key: key, role: role}
			byKey[key] = b
		}
		b.members = append(b.members, smp)
	}
	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	flatByEpisode := map[string]bool{} // episode id -> has a sample with signal
	for _, k := range keys {
		b := byKey[k]
		rewards := make([]float64, len(b.members))
		for i, m := range b.members {
			rewards[i] = m.reward
		}
		mean, std := meanStd(rewards)
		lo, hi := spread(rewards)
		flat := len(rewards) < 2 || hi-lo <= flatTol
		gs := GroupStat{Key: k, Role: b.role, N: len(rewards), Mean: mean, Std: std, Min: lo, Max: hi, Flat: flat}
		epSet := map[string]bool{}
		for _, m := range b.members {
			m.flat = flat
			epSet[m.ep.ID] = true
			if !flat {
				flatByEpisode[m.ep.ID] = true
			} else if _, ok := flatByEpisode[m.ep.ID]; !ok {
				flatByEpisode[m.ep.ID] = false
			}
		}
		gs.Episodes = sortedSet(epSet)
		rep.Groups = append(rep.Groups, gs)
		if flat {
			rep.Flat = append(rep.Flat, k)
		}

		switch s.Method {
		case MethodBroadcast:
			for _, m := range b.members {
				m.adv = m.reward
			}
		case MethodRLOO:
			if !flat {
				total := sum(rewards)
				n := float64(len(rewards))
				for _, m := range b.members {
					m.adv = m.reward - (total-m.reward)/(n-1)
				}
			}
		case MethodGRPO, MethodAnchor:
			if !flat {
				eps := s.eps()
				for _, m := range b.members {
					m.adv = (m.reward - mean) / (std + eps)
				}
			}
		}
		// Guard: never write a non-finite advantage, whatever the inputs.
		for _, m := range b.members {
			if math.IsNaN(m.adv) || math.IsInf(m.adv, 0) {
				m.adv = 0
			}
		}
	}

	if s.Method == MethodAnchor {
		rep.Anchor = applyAnchor(samples, s)
	}
	w := s.anchorWeight()
	for _, smp := range samples {
		for i, st := range smp.steps {
			a := smp.adv
			if i < len(smp.zs) {
				a += w * smp.zs[i]
			}
			if math.IsNaN(a) || math.IsInf(a, 0) {
				a = 0
			}
			st.Advantage = a
		}
		rep.Steps += len(smp.steps)
	}
	if s.DropFlat {
		for id, hasSignal := range flatByEpisode {
			if !hasSignal {
				rep.Dropped = append(rep.Dropped, id)
			}
		}
		sort.Strings(rep.Dropped)
	}
	return rep, nil
}

// sortedSet returns sorted map keys regardless of their boolean values.
func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// GRPOFunc returns a hook with the signature exporters take, so they can attach
// advantages without importing this package.
func GRPOFunc(spec Spec) func([]*rl.Episode) error {
	return func(eps []*rl.Episode) error {
		_, err := Apply(eps, spec)
		return err
	}
}
