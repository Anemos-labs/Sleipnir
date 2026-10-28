package export

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/rl"
)

// stepCtx addresses one step of one work episode.
type stepCtx struct {
	we *workEpisode
	ag *rl.Agent
	ai int // agent index
	st *rl.Step
	si int // step index within the agent
}

// eachStep visits every step of the work episodes in deterministic order.
func eachStep(work []*workEpisode, fn func(c stepCtx) error) error {
	for _, we := range work {
		for ai := range we.ep.Agents {
			ag := &we.ep.Agents[ai]
			for si := range ag.Steps {
				if err := fn(stepCtx{we: we, ag: ag, ai: ai, st: &ag.Steps[si], si: si}); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// stepVerdict says why a step cannot be exported in the current format, or "" if
// it can: role, drop rules, role teachers, provider terms, an empty completion, the
// length limits. Drops are not recorded here; the caller that leaves the step out
// calls noteDrop. A step that reaches the length limits is counted once in
// Stats.CandidatesByRole, whatever the limits decide.
//
// RL formats only ever export the policy's own steps. sft, dpo and kto may also
// use a teacher's completions, but only for models listed in TeacherOK; every
// other non-policy step is skipped and counted, never exported as a target. Drop
// rules and role teachers come first, so they apply to trainable and teacher steps
// alike and their drops are counted under their own names.
func (x *exporter) stepVerdict(c stepCtx) string {
	st := c.st
	if st.Inherited {
		return "inherited"
	}
	if !x.roleMatches(st.Role) {
		return "role_filtered"
	}
	if name := x.ruleMatch(c); name != "" {
		return "rule:" + name
	}
	if !x.roleTeacherOK(c) {
		return "role_teacher"
	}
	if !st.Trainable {
		switch {
		case x.o.Format.rl():
			return "not_trainable"
		case !x.teacherOK[st.Model]:
			return "teacher_not_ok"
		}
	}
	if len(st.Completion.Turn.Blocks) == 0 {
		return "empty_completion"
	}
	if !x.candidate[st] {
		x.candidate[st] = true
		bump(&x.stats.CandidatesByRole, st.Role, 1)
	}
	return x.tooLong(st)
}

// Length drop reasons.
const (
	reasonPromptTooLong = "prompt_too_long"
	reasonSampleTooLong = "sample_too_long"
)

// tooLong names the length limit a step exceeds, or "": MaxPromptTokens on the
// recorded prompt size, then MaxSampleTokens on sampleTokens. A step whose size was
// not recorded (0) is never too long.
func (x *exporter) tooLong(st *rl.Step) string {
	if x.o.MaxPromptTokens > 0 && st.Prompt.Tokens > x.o.MaxPromptTokens {
		return reasonPromptTooLong
	}
	if x.o.MaxSampleTokens > 0 && x.sampleTokens(st) > x.o.MaxSampleTokens {
		return reasonSampleTooLong
	}
	return ""
}

// sampleTokens is the length of the sample a step becomes: in the tokens format,
// the ids of its trace when it has one (prompt and response ids exactly as
// exported); otherwise the recorded prompt size plus the output tokens, less the
// reasoning tokens when Reasoning drops reasoning from the completion.
func (x *exporter) sampleTokens(st *rl.Step) int {
	if tr := st.Tokens; x.o.Format == FormatTokens && tr != nil && len(tr.PromptIDs) > 0 {
		return len(tr.PromptIDs) + len(tr.CompletionIDs)
	}
	n := st.Prompt.Tokens + st.Usage.OutputTokens
	if x.o.Reasoning == "drop" {
		n -= st.Usage.ReasoningTokens
	}
	return n
}

// cutLong drops the trailing steps of a chain that exceed the length limits. A
// conversation rendered from a chain is its last step's prompt and completion, so
// it then fits the limits too. Prompts of one segment only grow, so the steps that
// are too long are normally a tail of the chain and nothing else is lost.
func (x *exporter) cutLong(unit []stepCtx) []stepCtx {
	end := len(unit)
	for end > 0 && x.tooLong(unit[end-1].st) != "" {
		end--
	}
	return unit[:end]
}

// noteDrop records that a step was left out and why: in Drops on every call, and
// in DroppedByRole once per step.
func (x *exporter) noteDrop(c stepCtx, reason string) {
	x.stats.drop("step:" + reason)
	if reason == "teacher_not_ok" {
		bump(&x.stats.TeacherSkipped, c.st.Model, 1)
	}
	if x.noted[c.st] {
		return
	}
	x.noted[c.st] = true
	if x.stats.DroppedByRole == nil {
		x.stats.DroppedByRole = map[string]map[string]int{}
	}
	m := x.stats.DroppedByRole[c.st.Role]
	bump(&m, reason, 1)
	x.stats.DroppedByRole[c.st.Role] = m
}

// LengthDropRates returns, for each role with candidate steps, the share of them
// that the length limits (MaxPromptTokens, MaxSampleTokens) dropped.
func (s Stats) LengthDropRates() map[string]float64 {
	out := map[string]float64{}
	for role, n := range s.CandidatesByRole {
		if n <= 0 {
			continue
		}
		d := s.DroppedByRole[role]
		out[role] = float64(d[reasonPromptTooLong]+d[reasonSampleTooLong]) / float64(n)
	}
	return out
}

// ErrDropRate is returned by CheckLengthDropRate when the length limits dropped
// too large a share of some role's steps.
var ErrDropRate = errors.New("export: too many steps dropped for length")

// CheckLengthDropRate fails, naming every offending role in order, when the
// length limits dropped more than max (a fraction in [0, 1]) of the candidate
// steps of any role. max >= 1 never fails; 0 fails on any length drop.
func (s Stats) CheckLengthDropRate(max float64) error {
	if max >= 1 {
		return nil
	}
	rates := s.LengthDropRates()
	roles := make([]string, 0, len(rates))
	for r := range rates {
		roles = append(roles, r)
	}
	sort.Strings(roles)
	var bad []string
	for _, r := range roles {
		if rates[r] > max {
			d := s.DroppedByRole[r]
			bad = append(bad, fmt.Sprintf("%s: %d of %d steps (%.1f%%)", r, d[reasonPromptTooLong]+d[reasonSampleTooLong], s.CandidatesByRole[r], 100*rates[r]))
		}
	}
	if len(bad) == 0 {
		return nil
	}
	return fmt.Errorf("%w: more than %.1f%% of a role's steps exceed the length limits: %s", ErrDropRate, 100*max, strings.Join(bad, "; "))
}

// selectStep is stepVerdict plus recording, for formats whose unit is one step.
func (x *exporter) selectStep(c stepCtx) bool {
	if reason := x.stepVerdict(c); reason != "" {
		x.noteDrop(c, reason)
		return false
	}
	return true
}

// countTeacher records that a teacher step was used as a target.
func (x *exporter) countTeacher(st *rl.Step) {
	if !st.Trainable {
		bump(&x.stats.TeacherUsed, st.Model, 1)
	}
}

// rendered is a step ready to be written in a chat-message format.
type rendered struct {
	prompt     wirePrompt
	completion json.RawMessage
	changed    bool // redaction altered the prompt or the completion
}

// renderStep resolves, redacts and renders a step's prompt and completion.
func (x *exporter) renderStep(c stepCtx, objectArgs bool) (rendered, error) {
	p, err := x.promptFor(c.we, c.st)
	if err != nil {
		return rendered{}, err
	}
	rp, pc := x.redactPrompt(p)
	wp, err := x.renderPrompt(rp, objectArgs)
	if err != nil {
		return rendered{}, err
	}
	turn, tc := x.redactTurn(c.st.Completion.Turn)
	comp, err := x.renderCompletion(turn, c.st.Model, objectArgs)
	if err != nil {
		return rendered{}, err
	}
	return rendered{prompt: wp, completion: comp, changed: pc || tc}, nil
}

// stepMeta is the provenance block of a step record.
type stepMeta struct {
	Policy         rl.PolicyRef    `json:"policy"`
	Harness        rl.HarnessRef   `json:"harness"`
	Kind           string          `json:"kind"`
	Model          string          `json:"model"`
	Req            string          `json:"req"`
	Epoch          int             `json:"epoch"`
	Stop           core.StopReason `json:"stop"`
	Usage          core.Usage      `json:"usage"`
	Cache          rl.CacheInfo    `json:"cache"`
	WireHash       core.Hash       `json:"wire_hash"`
	SharedPrefix   string          `json:"shared_prefix,omitempty"`
	SharedMessages int             `json:"shared_messages,omitempty"`
	Trainable      bool            `json:"trainable"`
	Flags          []string        `json:"flags,omitempty"`
	License        string          `json:"license,omitempty"`
}

// metaOf collects step and episode provenance, cache, usage, and training metadata for export.
func (x *exporter) metaOf(c stepCtx) stepMeta {
	ep := c.we.ep
	return stepMeta{
		Policy: ep.Policy, Harness: ep.Harness, Kind: c.st.Kind, Model: c.st.Model, Req: c.st.Prompt.Req, Epoch: c.st.Epoch,
		Stop: c.st.Completion.Stop, Usage: c.st.Usage, Cache: c.st.Cache, WireHash: c.st.Prompt.WireHash,
		SharedPrefix: c.st.Prompt.SharedPrefix, SharedMessages: c.st.Prompt.SharedMessages, Trainable: c.st.Trainable,
		Flags: ep.Flags, License: ep.Provenance.License,
	}
}

// stepRecord is one line of the steps format: Hugging Face conversational
// prompt/completion form with the exact wire messages.
type stepRecord struct {
	Schema           string             `json:"schema"`
	ID               string             `json:"id"`
	TaskID           string             `json:"task_id"`
	GroupID          string             `json:"group_id"`
	Sample           int                `json:"sample"`
	Role             string             `json:"role"`
	Agent            string             `json:"agent"`
	Step             int                `json:"step"`
	Segment          int                `json:"segment"`
	Prompt           []json.RawMessage  `json:"prompt"`
	Completion       []json.RawMessage  `json:"completion"`
	Tools            json.RawMessage    `json:"tools,omitempty"`
	Reward           float64            `json:"reward"`
	Advantage        *float64           `json:"advantage,omitempty"`
	RewardComponents map[string]float64 `json:"reward_components,omitempty"`
	Split            string             `json:"split,omitempty"`
	Meta             stepMeta           `json:"meta"`
}

// recordID names a sample: "<episode id>#<step id>".
func recordID(ep *rl.Episode, st *rl.Step) string { return ep.ID + "#" + st.ID }

// advantageOf omits an unconfigured zero advantage and otherwise returns a pointer to a value
// copy.
func (x *exporter) advantageOf(st *rl.Step) *float64 {
	if x.o.Advantage == nil && st.Advantage == 0 {
		return nil
	}
	a := st.Advantage
	return &a
}

func (x *exporter) writeSteps(work []*workEpisode) error {
	return eachStep(work, func(c stepCtx) error {
		if !x.selectStep(c) {
			return nil
		}
		r, err := x.renderStep(c, true)
		if err != nil {
			x.stats.drop("step:no_prompt")
			x.warn(c.st.ID + ": " + err.Error())
			return nil
		}
		reward, comps := rewardOf(c.we.ep, c.ag, c.st)
		split := x.splitOf(c.we.ep)
		rec := stepRecord{
			Schema: rl.SchemaStep, ID: recordID(c.we.ep, c.st), TaskID: c.we.ep.TaskID, GroupID: c.we.ep.Group, Sample: c.we.ep.Sample,
			Role: c.st.Role, Agent: c.ag.ID, Step: c.si, Segment: c.st.Segment,
			Prompt: r.prompt.Messages, Completion: []json.RawMessage{r.completion}, Tools: r.prompt.Tools,
			Reward: reward, Advantage: x.advantageOf(c.st), RewardComponents: comps, Split: split, Meta: x.metaOf(c),
		}
		if err := x.emit(rec, c.st.Role, "step", split); err != nil {
			return err
		}
		c.we.contributed = true
		x.countTeacher(c.st)
		x.stats.PromptTokens += c.st.Prompt.Tokens
		x.stats.ResponseTokens += c.st.Usage.OutputTokens
		x.stats.TrainedTokens += c.st.Usage.OutputTokens
		return nil
	})
}

// ktoRecord is one line of the kto format.
type ktoRecord struct {
	Schema     string            `json:"schema"`
	ID         string            `json:"id"`
	TaskID     string            `json:"task_id"`
	GroupID    string            `json:"group_id"`
	Sample     int               `json:"sample"`
	Role       string            `json:"role"`
	Agent      string            `json:"agent"`
	Prompt     []json.RawMessage `json:"prompt"`
	Completion []json.RawMessage `json:"completion"`
	Tools      json.RawMessage   `json:"tools,omitempty"`
	Label      bool              `json:"label"`
	Reward     float64           `json:"reward"`
	Split      string            `json:"split,omitempty"`
	Meta       stepMeta          `json:"meta"`
}

// SchemaKTO, SchemaSFT, SchemaDPO, SchemaTokens, SchemaGroup and SchemaSegment
// identify the record types of the other formats.
const (
	SchemaKTO     = "sleipnir.rl.kto/1"
	SchemaSFT     = "sleipnir.rl.sft/1"
	SchemaDPO     = "sleipnir.rl.dpo/1"
	SchemaTokens  = "sleipnir.rl.tokens/1"
	SchemaGroup   = "sleipnir.rl.group/1"
	SchemaSegment = "sleipnir.rl.segment/1"
)

// label is the kto label of an episode: its verifier verdict when there is one,
// else whether its reward reaches MinReward.
func (x *exporter) label(ep *rl.Episode, reward float64) bool {
	if ep.Outcome.Verifier != nil {
		return ep.Outcome.Verifier.Pass
	}
	return reward >= x.o.MinReward
}

func (x *exporter) writeKTO(work []*workEpisode) error {
	return eachStep(work, func(c stepCtx) error {
		if !x.selectStep(c) {
			return nil
		}
		r, err := x.renderStep(c, true)
		if err != nil {
			x.stats.drop("step:no_prompt")
			x.warn(c.st.ID + ": " + err.Error())
			return nil
		}
		reward, _ := rewardOf(c.we.ep, c.ag, c.st)
		split := x.splitOf(c.we.ep)
		rec := ktoRecord{
			Schema: SchemaKTO, ID: recordID(c.we.ep, c.st), TaskID: c.we.ep.TaskID, GroupID: c.we.ep.Group, Sample: c.we.ep.Sample,
			Role: c.st.Role, Agent: c.ag.ID, Prompt: r.prompt.Messages, Completion: []json.RawMessage{r.completion}, Tools: r.prompt.Tools,
			Label: x.label(c.we.ep, reward), Reward: reward, Split: split, Meta: x.metaOf(c),
		}
		if err := x.emit(rec, c.st.Role, "step", split); err != nil {
			return err
		}
		c.we.contributed = true
		x.countTeacher(c.st)
		x.stats.PromptTokens += c.st.Prompt.Tokens
		x.stats.ResponseTokens += c.st.Usage.OutputTokens
		return nil
	})
}
