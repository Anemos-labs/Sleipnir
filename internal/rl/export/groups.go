package export

import (
	"encoding/json"
	"fmt"
	"math"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/rl"
)

// groupRecord is one line of the groups format, in the shape ART and rLLM consume:
// one record per GRPO group (same task and policy snapshot) holding one trajectory
// per (episode, agent, segment).
type groupRecord struct {
	Schema       string       `json:"schema"`
	TaskID       string       `json:"task_id"`
	Group        string       `json:"group"`
	Split        string       `json:"split,omitempty"`
	Trajectories []trajectory `json:"trajectories"`
}

// trajectory is a conversation with the model's own turns marked as choices.
// messages_and_choices holds plain wire messages for everything the model did not
// say and, for each model turn, a choice object {finish_reason, index, message,
// logprobs} in the OpenAI shape. Logprobs are the sampled token ids
// ("token_id:<id>", the vLLM convention that avoids re-tokenising) with their
// logprobs, present only when the step has a consistent trace and redaction left
// its text alone; otherwise null.
type trajectory struct {
	ID                 string             `json:"id"`
	Episode            string             `json:"episode"`
	Sample             int                `json:"sample"`
	Agent              string             `json:"agent"`
	Role               string             `json:"role"`
	Segment            int                `json:"segment"`
	Steps              []string           `json:"steps"`
	MessagesAndChoices []json.RawMessage  `json:"messages_and_choices"`
	Tools              json.RawMessage    `json:"tools,omitempty"`
	Reward             float64            `json:"reward"`
	Advantage          *float64           `json:"advantage,omitempty"`
	Metrics            map[string]float64 `json:"metrics"`
}

type choice struct {
	FinishReason string          `json:"finish_reason"`
	Index        int             `json:"index"`
	Message      json.RawMessage `json:"message"`
	Logprobs     *choiceLogprobs `json:"logprobs"`
}

type choiceLogprobs struct {
	Content []tokenLogprob `json:"content"`
}

type tokenLogprob struct {
	Token       string   `json:"token"`
	Logprob     float64  `json:"logprob"`
	TopLogprobs []string `json:"top_logprobs"`
}

// finishReason maps internal stop reasons to training-export finish labels, defaulting to stop.
func finishReason(s core.StopReason) string {
	switch s {
	case core.StopToolUse:
		return "tool_calls"
	case core.StopMaxTokens:
		return "length"
	case core.StopRefusal:
		return "content_filter"
	}
	return "stop"
}

func (x *exporter) writeGroups(work []*workEpisode) error {
	type bucket struct {
		group string
		task  string
		split string
		trajs []trajectory
	}
	var order []*bucket
	byGroup := map[string]*bucket{}
	for _, we := range work {
		b, ok := byGroup[we.ep.Group]
		if !ok {
			b = &bucket{group: we.ep.Group, task: we.ep.TaskID, split: x.splitOf(we.ep)}
			byGroup[we.ep.Group] = b
			order = append(order, b)
		}
		for ai := range we.ep.Agents {
			ag := &we.ep.Agents[ai]
			for _, unit := range splitChains(we, ag, ai) {
				tr, ok, err := x.trajectoryOf(unit)
				if err != nil {
					x.stats.drop("step:no_prompt")
					x.stats.warn(unit[0].st.ID + ": " + err.Error())
					continue
				}
				if ok {
					b.trajs = append(b.trajs, tr)
					we.contributed = true
				}
			}
		}
	}
	for _, b := range order {
		if len(b.trajs) == 0 {
			continue
		}
		rec := groupRecord{Schema: SchemaGroup, TaskID: b.task, Group: b.group, Split: b.split, Trajectories: b.trajs}
		if err := x.emit(rec, "", "group", b.split); err != nil {
			return err
		}
		for _, t := range b.trajs {
			bump(&x.stats.ByRole, t.Role, 1)
		}
	}
	return nil
}

// trajectoryOf renders one chain as a trajectory. Only steps that pass the step
// filters become choices; the rest of the conversation is context.
func (x *exporter) trajectoryOf(unit []stepCtx) (trajectory, bool, error) {
	var sel []bool
	anySel := false
	for _, c := range unit {
		r := x.stepVerdict(c)
		sel = append(sel, r == "")
		if r == "" {
			anySel = true
		} else {
			x.noteDrop(c, r)
		}
	}
	if !anySel {
		return trajectory{}, false, nil
	}
	cv, err := x.renderChain(unit, false)
	if err != nil {
		return trajectory{}, false, err
	}
	msgs := append([]json.RawMessage(nil), cv.msgs...)
	var ids []string
	var promptTok, compTok int
	for j, c := range unit {
		if !sel[j] || cv.at[j] < 0 {
			continue
		}
		ch := choice{FinishReason: finishReason(c.st.Completion.Stop), Message: cv.msgs[cv.at[j]]}
		if tr := c.st.Tokens; tr != nil && tr.Consistent() && len(tr.Logprobs) == len(tr.CompletionIDs) && !cv.changed {
			lp := &choiceLogprobs{Content: make([]tokenLogprob, len(tr.CompletionIDs))}
			finite := true
			for i, id := range tr.CompletionIDs {
				v := float64(tr.Logprobs[i])
				finite = finite && !math.IsNaN(v) && !math.IsInf(v, 0)
				lp.Content[i] = tokenLogprob{Token: fmt.Sprintf("token_id:%d", id), Logprob: v, TopLogprobs: []string{}}
			}
			if finite {
				ch.Logprobs = lp
			}
		}
		b, err := marshalStable(ch)
		if err != nil {
			return trajectory{}, false, err
		}
		msgs[cv.at[j]] = b
		ids = append(ids, c.st.ID)
		promptTok += c.st.Prompt.Tokens
		compTok += c.st.Usage.OutputTokens
		x.countTeacher(c.st)
	}
	if len(ids) == 0 {
		return trajectory{}, false, nil
	}
	last := unit[len(unit)-1]
	reward, comps := rewardOf(last.we.ep, last.ag, last.st)
	metrics := map[string]float64{"steps": float64(len(ids)), "prompt_tokens": float64(promptTok), "completion_tokens": float64(compTok)}
	for k, v := range comps {
		metrics["reward/"+k] = v
	}
	for k, v := range last.we.ep.Signals {
		metrics["signal/"+k] = v
	}
	x.stats.PromptTokens += promptTok
	x.stats.ResponseTokens += compTok
	x.stats.TrainedTokens += compTok
	return trajectory{
		ID: last.we.ep.ID + "#" + last.ag.ID + ".seg" + fmt.Sprint(last.st.Segment) + firstIDSuffix(unit), Episode: last.we.ep.ID, Sample: last.we.ep.Sample,
		Agent: last.ag.ID, Role: last.st.Role, Segment: last.st.Segment, Steps: ids, MessagesAndChoices: msgs, Tools: cv.tools,
		Reward: reward, Advantage: x.advantageOf(last.st), Metrics: metrics,
	}, true, nil
}

// firstIDSuffix distinguishes side-call trajectories, which share their segment
// with the main chain, from the chain itself.
func firstIDSuffix(unit []stepCtx) string {
	if unit[0].st.Kind == rl.KindMain {
		return ""
	}
	return "." + unit[0].st.ID
}
