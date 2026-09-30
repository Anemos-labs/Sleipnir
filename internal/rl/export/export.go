package export

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/provider/openaichat"
	"github.com/reee344/sleipnir/internal/rl"
	"github.com/reee344/sleipnir/internal/rl/redact"
)

// Format names an export format.
type Format string

// The export formats. See the package documentation for each record's fields.
const (
	FormatSteps     Format = "steps"
	FormatTokens    Format = "tokens"
	FormatGroups    Format = "groups"
	FormatSFT       Format = "sft"
	FormatDPO       Format = "dpo"
	FormatKTO       Format = "kto"
	FormatATIF      Format = "atif"
	FormatCanonical Format = "canonical"
)

// Formats lists every valid format.
func Formats() []Format {
	return []Format{FormatSteps, FormatTokens, FormatGroups, FormatSFT, FormatDPO, FormatKTO, FormatATIF, FormatCanonical}
}

// rlFormat reports the formats whose learning signal is the group's reward
// spread: an all-equal group teaches nothing and is dropped unless KeepFlat.
func (f Format) rl() bool { return f == FormatSteps || f == FormatTokens || f == FormatGroups }

// training reports the formats that use completions as training targets, and so
// apply the provider-terms and weak-label filters.
func (f Format) training() bool {
	switch f {
	case FormatSteps, FormatTokens, FormatGroups, FormatSFT, FormatDPO, FormatKTO:
		return true
	}
	return false
}

// PromptResolver returns the exact prompt of a step. A traj.Resolver is the usual
// implementation; a step that already carries its prompt inline needs none.
type PromptResolver interface {
	Prompt(ep *rl.Episode, st *rl.Step) (*core.Prompt, error)
}

// Source is one episode and the means to read its prompts.
type Source struct {
	Episode *rl.Episode
	Prompts PromptResolver
}

// Options controls an export. The zero value exports nothing useful: Format is
// required.
type Options struct {
	Format Format
	// Roles keeps only steps of these roles (step roles as recorded, plus "worker"
	// as an alias for any role that is not manager, reviewer, compactor or
	// mailman). Empty keeps all. canonical and atif are episode archives and ignore it.
	Roles []string
	// MinReward is the smallest episode reward an sft record, a dpo "chosen" side
	// or a positive kto label may have. It is not applied to the RL formats, where
	// low rewards are the point.
	MinReward float64
	// TopK keeps the K best episodes per task for sft (0 keeps all that qualify).
	TopK int
	// DropFlagged drops episodes carrying a hard flag (rl.HardFlag: infra_error,
	// truncated, replay_mismatch, contaminated, hack:*) and, for the training
	// formats, weak-label episodes unless KeepWeak. token_mismatch counts only for
	// the tokens format. Set it false to export everything.
	DropFlagged bool
	// KeepFlat keeps groups whose rewards are all equal (RL formats).
	KeepFlat bool
	// KeepWeak keeps weak_label episodes (no verifier verdict) in the training
	// formats; without it they are dropped when DropFlagged is set.
	KeepWeak bool
	// Advantage fills Step.Advantage (and may fill rewards) on clones of the kept
	// episodes, grouped as the caller sees fit. nil exports without advantages.
	Advantage func([]*rl.Episode) error
	// Redactor scrubs every string that leaves the exporter: prompts, tools,
	// completions, observations, tool arguments. nil exports text as recorded.
	Redactor *redact.Redactor
	// TeacherOK lists the non-policy models whose completions may be used as
	// targets (sft, dpo, kto). A step by any other non-policy model is never
	// exported as a training target. The RL formats only ever export the policy's own steps.
	TeacherOK []string
	// Licenses is an allow-list of SPDX ids (case-insensitive); empty allows any.
	// An episode with no recorded licence is dropped when the list is set.
	Licenses []string
	// Inline embeds the full prompt in every step of the canonical format; without
	// it the format writes a segment table once and steps reference it by hash.
	Inline bool
	// Table receives the canonical segment table when Inline is false. nil writes
	// the table at the head of the main stream instead.
	Table io.Writer
	// Reasoning is what to do with reasoning in completions: "drop" (default),
	// "field" (a reasoning_content string) or "keep" (provider-native
	// reasoning_details exactly as replayed on the wire).
	Reasoning string
	// MaxPromptTokens drops steps whose prompt is longer (0: no limit).
	MaxPromptTokens int
	// MaxSamples stops after this many records (0: no limit).
	MaxSamples int
	// Split assigns whole repositories to named splits, e.g. "train:0.9,val:0.1".
	// Every record gets a "split" field.
	Split string
	// Seed salts the split assignment.
	Seed int64
	// PackSegments packs an (agent, segment) into one token sequence when its
	// traces chain; otherwise it falls back to per-step records and counts why.
	PackSegments bool
	// Wire selects the rendering of prompts (see openaichat.Options).
	Wire openaichat.Options
}

// DefaultOptions returns the options a caller should start from: the format with
// DropFlagged on. A bool cannot default to true in Go, so leaving DropFlagged to
// the zero value would silently export infra-failed, truncated and reward-hacked
// episodes; a caller that wants everything must say so by clearing it.
func DefaultOptions(f Format) Options {
	return Options{Format: f, DropFlagged: true}
}

// Stats reports what an export did and why it dropped what it dropped. All maps
// are keyed by stable names and are sorted when marshalled.
type Stats struct {
	Format   Format `json:"format"`
	Episodes int    `json:"episodes"`      // episodes offered
	Kept     int    `json:"episodes_kept"` // episodes that contributed at least one record
	Records  int    `json:"records"`
	// ByRole counts records per training role; ByUnit per unit (step, segment,
	// episode, pair); BySplit per split.
	ByRole  map[string]int `json:"by_role,omitempty"`
	ByUnit  map[string]int `json:"by_unit,omitempty"`
	BySplit map[string]int `json:"by_split,omitempty"`
	// Drops counts what was left out, by reason. Episode reasons start with
	// "episode:", step reasons with "step:", pair reasons with "pair:".
	Drops map[string]int `json:"drops,omitempty"`
	// PromptTokens and ResponseTokens total the exported samples (token ids for
	// the token formats, the recorded usage otherwise); TrainedTokens counts the
	// loss-bearing response tokens.
	PromptTokens   int `json:"prompt_tokens"`
	ResponseTokens int `json:"response_tokens"`
	TrainedTokens  int `json:"trained_tokens"`
	// Packed counts packed (agent, segment) records, PackedSteps the steps in
	// them; PackFailed counts segments that fell back to per-step records, by reason.
	Packed      int            `json:"packed"`
	PackedSteps int            `json:"packed_steps"`
	PackFailed  map[string]int `json:"pack_failed,omitempty"`
	// Redactions counts replacements made by this export, by kind.
	Redactions map[string]int `json:"redactions,omitempty"`
	// TeacherSkipped counts steps not exported because their non-policy model is
	// not in TeacherOK (by model); TeacherUsed counts steps exported because it is.
	// Teacher completions are never exported as trainable otherwise.
	TeacherSkipped map[string]int `json:"teacher_skipped,omitempty"`
	TeacherUsed    map[string]int `json:"teacher_used,omitempty"`
	// Warnings holds the first few unexpected conditions (unresolvable prompts...).
	Warnings []string `json:"warnings,omitempty"`
}

func (s *Stats) drop(reason string) {
	if s.Drops == nil {
		s.Drops = map[string]int{}
	}
	s.Drops[reason]++
}

func (s *Stats) warn(msg string) {
	if len(s.Warnings) < 20 {
		s.Warnings = append(s.Warnings, msg)
	}
}

func bump(m *map[string]int, k string, n int) {
	if *m == nil {
		*m = map[string]int{}
	}
	(*m)[k] += n
}

// ErrBadOptions wraps option validation failures.
var ErrBadOptions = errors.New("export: bad options")

// errStop ends an export early when MaxSamples is reached.
var errStop = errors.New("export: sample cap reached")

// exporter carries the state of one Export call.
type exporter struct {
	o         Options
	out       *bufio.Writer
	stats     Stats
	red       *redact.Redactor
	redBefore map[string]int

	teacherOK map[string]bool
	licenses  map[string]bool
	roles     []string
	splits    []splitBin

	// work is the set of episodes that reached the format writers, for the
	// episodes-kept count.
	work []*workEpisode
}

// Export writes the selected records of srcs to w as JSON lines and returns what it
// did. The pipeline, in order: drop infra and hard-flagged episodes (and weak
// labels, for training formats), run the Advantage hook on clones of the kept
// episodes, drop zero-variance groups (RL formats, unless KeepFlat), filter by
// role, enforce the provider-terms (TeacherOK) and licence filters, redact all
// text, assign splits and apply the caps. Output is deterministic: sources are
// ordered by (group, task, sample, id) and every map is written in sorted order.
func Export(w io.Writer, srcs []Source, o Options) (Stats, error) {
	x, err := newExporter(w, o)
	if err != nil {
		return Stats{}, err
	}
	err = x.run(srcs)
	if ferr := x.out.Flush(); err == nil {
		err = ferr
	}
	if errors.Is(err, errStop) {
		err = nil
	}
	x.finishStats()
	return x.stats, err
}

func newExporter(w io.Writer, o Options) (*exporter, error) {
	valid := false
	for _, f := range Formats() {
		if o.Format == f {
			valid = true
		}
	}
	if !valid {
		return nil, fmt.Errorf("%w: unknown format %q (want one of %v)", ErrBadOptions, o.Format, Formats())
	}
	switch o.Reasoning {
	case "":
		o.Reasoning = "drop"
	case "drop", "field", "keep":
	default:
		return nil, fmt.Errorf("%w: reasoning must be drop, field or keep, not %q", ErrBadOptions, o.Reasoning)
	}
	if o.MinReward != o.MinReward || math.IsInf(o.MinReward, 0) {
		return nil, fmt.Errorf("%w: min reward must be a finite number", ErrBadOptions)
	}
	if o.TopK < 0 || o.MaxSamples < 0 || o.MaxPromptTokens < 0 {
		return nil, fmt.Errorf("%w: TopK, MaxSamples and MaxPromptTokens cannot be negative", ErrBadOptions)
	}
	splits, err := parseSplit(o.Split)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadOptions, err)
	}
	x := &exporter{o: o, out: bufio.NewWriterSize(w, 1<<20), red: o.Redactor, splits: splits,
		teacherOK: map[string]bool{}, licenses: map[string]bool{}}
	x.stats.Format = o.Format
	for _, m := range o.TeacherOK {
		x.teacherOK[m] = true
	}
	for _, l := range o.Licenses {
		x.licenses[strings.ToLower(strings.TrimSpace(l))] = true
	}
	for _, r := range o.Roles {
		if r = strings.TrimSpace(r); r != "" {
			x.roles = append(x.roles, r)
		}
	}
	if x.red != nil {
		x.redBefore = x.red.Stats()
	}
	return x, nil
}

func (x *exporter) finishStats() {
	for _, we := range x.work {
		if we.contributed {
			x.stats.Kept++
		}
	}
	if x.red != nil {
		after := x.red.Stats()
		for k, v := range after {
			if d := v - x.redBefore[k]; d > 0 {
				bump(&x.stats.Redactions, k, d)
			}
		}
	}
}

// run executes the pipeline.
func (x *exporter) run(srcs []Source) error {
	// Deterministic order, whatever order the caller listed the episodes in.
	list := make([]Source, 0, len(srcs))
	for _, s := range srcs {
		if s.Episode != nil {
			list = append(list, s)
		}
	}
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i].Episode, list[j].Episode
		if a.Group != b.Group {
			return a.Group < b.Group
		}
		if a.TaskID != b.TaskID {
			return a.TaskID < b.TaskID
		}
		if a.Sample != b.Sample {
			return a.Sample < b.Sample
		}
		return a.ID < b.ID
	})
	x.stats.Episodes = len(list)

	// 1. infra / hard flags / weak labels.
	kept := list[:0:0]
	for _, s := range list {
		if reason := x.episodeDrop(s.Episode); reason != "" {
			x.stats.drop("episode:" + reason)
			continue
		}
		kept = append(kept, s)
	}

	// 2. Advantage hook on clones, so the caller's episodes are never mutated.
	work := make([]*workEpisode, len(kept))
	eps := make([]*rl.Episode, len(kept))
	for i, s := range kept {
		work[i] = &workEpisode{src: s, ep: cloneEpisode(s.Episode)}
		eps[i] = work[i].ep
	}
	if x.o.Advantage != nil && len(eps) > 0 {
		if err := x.o.Advantage(eps); err != nil {
			return fmt.Errorf("export: advantage hook: %w", err)
		}
	}

	// 3. zero-variance groups (RL formats).
	if x.o.Format.rl() && !x.o.KeepFlat {
		work = x.dropFlatGroups(work)
	}

	// 4-5. licence filter (episode level); role and teacher filters are per step and
	// live in the format writers, which know their unit.
	if len(x.licenses) > 0 {
		out := work[:0:0]
		for _, we := range work {
			lic := strings.ToLower(strings.TrimSpace(we.ep.Provenance.License))
			if !x.licenses[lic] {
				if lic == "" {
					x.stats.drop("episode:license_unknown")
				} else {
					x.stats.drop("episode:license")
				}
				continue
			}
			out = append(out, we)
		}
		work = out
	}

	x.work = work
	switch x.o.Format {
	case FormatSteps:
		return x.writeSteps(work)
	case FormatKTO:
		return x.writeKTO(work)
	case FormatTokens:
		return x.writeTokens(work)
	case FormatGroups:
		return x.writeGroups(work)
	case FormatSFT:
		return x.writeSFT(work)
	case FormatDPO:
		return x.writeDPO(work)
	case FormatATIF:
		return x.writeATIF(work)
	case FormatCanonical:
		return x.writeCanonical(work)
	}
	return nil
}

// workEpisode is an episode being exported: the caller's source and the clone the
// pipeline works on.
type workEpisode struct {
	src Source
	ep  *rl.Episode
	// contributed is set when the episode produced at least one record.
	contributed bool
}

// episodeDrop names why an episode is excluded outright, or "".
func (x *exporter) episodeDrop(ep *rl.Episode) string {
	if !x.o.DropFlagged {
		return ""
	}
	for _, f := range ep.Flags {
		if !rl.HardFlag(f) {
			continue
		}
		if f == rl.FlagTokenMismatch && x.o.Format != FormatTokens {
			continue // a token trace problem is irrelevant to text formats
		}
		return strings.Replace(f, "hack:", "hack_", 1)
	}
	if x.o.Format.training() && !x.o.KeepWeak && ep.Has(rl.FlagWeakLabel) {
		return rl.FlagWeakLabel
	}
	return ""
}

// cloneEpisode copies what hooks may write: agents, steps, segments, maps and
// slices. Immutable payloads (token traces, inline prompts, turns) are shared.
func cloneEpisode(ep *rl.Episode) *rl.Episode {
	c := *ep
	c.Agents = make([]rl.Agent, len(ep.Agents))
	for i, a := range ep.Agents {
		a.Steps = append([]rl.Step(nil), a.Steps...)
		a.Segments = append([]rl.Segment(nil), a.Segments...)
		a.Reward = cloneReward(a.Reward)
		c.Agents[i] = a
	}
	c.Edges = append([]rl.Edge(nil), ep.Edges...)
	c.Flags = append([]string(nil), ep.Flags...)
	c.Reward = cloneReward(ep.Reward)
	if ep.Signals != nil {
		c.Signals = make(map[string]float64, len(ep.Signals))
		for k, v := range ep.Signals {
			c.Signals[k] = v
		}
	}
	c.Provenance.TeacherModels = append([]string(nil), ep.Provenance.TeacherModels...)
	if ep.Outcome.Verifier != nil {
		v := *ep.Outcome.Verifier
		c.Outcome.Verifier = &v
	}
	c.Outcome.Reviews = append([]rl.Verdict(nil), ep.Outcome.Reviews...)
	c.Outcome.Labels = append([]string(nil), ep.Outcome.Labels...)
	return &c
}

func cloneReward(r rl.Reward) rl.Reward {
	if r.Components != nil {
		m := make(map[string]float64, len(r.Components))
		for k, v := range r.Components {
			m[k] = v
		}
		r.Components = m
	}
	r.Notes = append([]string(nil), r.Notes...)
	return r
}

// dropFlatGroups removes groups with no learning signal: every role's rewards are
// constant across the group and no step carries a non-zero advantage.
func (x *exporter) dropFlatGroups(work []*workEpisode) []*workEpisode {
	byGroup := map[string][]*workEpisode{}
	var order []string
	for _, we := range work {
		g := we.ep.Group
		if _, ok := byGroup[g]; !ok {
			order = append(order, g)
		}
		byGroup[g] = append(byGroup[g], we)
	}
	var out []*workEpisode
	for _, g := range order {
		members := byGroup[g]
		if groupHasSignal(members) {
			out = append(out, members...)
			continue
		}
		x.stats.Drops = addDrop(x.stats.Drops, "episode:flat_group", len(members))
	}
	return out
}

func addDrop(m map[string]int, k string, n int) map[string]int {
	if m == nil {
		m = map[string]int{}
	}
	m[k] += n
	return m
}

const flatEps = 1e-12

func groupHasSignal(members []*workEpisode) bool {
	type roleReward struct {
		role string
		v    float64
	}
	seen := map[roleReward]bool{}
	perRole := map[string][]float64{}
	nonZeroAdv := false
	for _, we := range members {
		for ai := range we.ep.Agents {
			ag := &we.ep.Agents[ai]
			for si := range ag.Steps {
				st := &ag.Steps[si]
				if math.Abs(st.Advantage) > flatEps {
					nonZeroAdv = true
				}
				r, _ := rewardOf(we.ep, ag, st)
				if k := (roleReward{st.Role, r}); !seen[k] {
					seen[k] = true
					perRole[st.Role] = append(perRole[st.Role], r)
				}
			}
		}
		// An episode with no steps still has a reward.
		if len(we.ep.Agents) == 0 {
			perRole[""] = append(perRole[""], we.ep.Reward.Total)
		}
	}
	if nonZeroAdv {
		return true
	}
	if len(members) < 2 {
		return false
	}
	for _, rs := range perRole {
		lo, hi := rs[0], rs[0]
		for _, r := range rs[1:] {
			lo, hi = math.Min(lo, r), math.Max(hi, r)
		}
		if hi-lo > flatEps {
			return true
		}
	}
	return false
}

// rewardOf is the reward a step trains against and the components it was built
// from. The most specific value wins: a non-zero step-level reward, else the
// agent's role reward when the reward package filled one, else the episode's.
func rewardOf(ep *rl.Episode, ag *rl.Agent, st *rl.Step) (float64, map[string]float64) {
	if st.Reward != 0 {
		return st.Reward, nil
	}
	if ag != nil && (ag.Reward.Total != 0 || len(ag.Reward.Components) > 0) {
		return ag.Reward.Total, ag.Reward.Components
	}
	return ep.Reward.Total, ep.Reward.Components
}

// roleMatches reports whether a step role passes the role filter.
func (x *exporter) roleMatches(role string) bool {
	if len(x.roles) == 0 {
		return true
	}
	for _, f := range x.roles {
		if f == role {
			return true
		}
		if f == rl.RoleWorker {
			switch role {
			case rl.RoleManager, rl.RoleReviewer, rl.RoleCompactor, rl.RoleMailman:
			default:
				return true
			}
		}
	}
	return false
}

// emit writes one record as a JSON line and enforces the sample cap.
func (x *exporter) emit(rec any, role, unit, split string) error {
	if x.o.MaxSamples > 0 && x.stats.Records >= x.o.MaxSamples {
		return errStop
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(rec); err != nil {
		return fmt.Errorf("export: encode %s record: %w", unit, err)
	}
	if _, err := x.out.Write(buf.Bytes()); err != nil {
		return err
	}
	x.stats.Records++
	if role != "" {
		bump(&x.stats.ByRole, role, 1)
	}
	bump(&x.stats.ByUnit, unit, 1)
	if split != "" {
		bump(&x.stats.BySplit, split, 1)
	}
	if x.o.MaxSamples > 0 && x.stats.Records >= x.o.MaxSamples {
		return errStop
	}
	return nil
}

// marshalStable marshals without HTML escaping.
func marshalStable(v any) ([]byte, error) { return core.MarshalStable(v) }
