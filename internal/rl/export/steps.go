package export

import (
	"encoding/json"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/rl"
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
// it can: role, provider terms, prompt size. Nothing is recorded; the caller that
// leaves the step out calls noteDrop, so a step is counted once.
//
// RL formats only ever export the policy's own steps. sft, dpo and kto may also
// use a teacher's completions, but only for models listed in TeacherOK; every
// other non-policy step is skipped and counted, never exported as a target.
func (x *exporter) stepVerdict(c stepCtx) string {
	st := c.st
	if !x.roleMatches(st.Role) {
		return "role_filtered"
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
	if x.o.MaxPromptTokens > 0 && st.Prompt.Tokens > x.o.MaxPromptTokens {
		return "prompt_too_long"
	}
	return ""
}

// noteDrop records that a step was left out and why.
func (x *exporter) noteDrop(c stepCtx, reason string) {
	x.stats.drop("step:" + reason)
	if reason == "teacher_not_ok" {
		bump(&x.stats.TeacherSkipped, c.st.Model, 1)
	}
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
			x.stats.warn(c.st.ID + ": " + err.Error())
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
			x.stats.warn(c.st.ID + ": " + err.Error())
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
