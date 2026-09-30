package export

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/rl"
)

// ATIFVersion is the schema_version written by the atif format. The format is a
// subset of Harbor's Agent Trajectory Interchange Format as summarised in
// docs/research/03-swarm-and-training.md (steps with tool_calls and observation,
// metrics with token ids and logprobs, subagent_trajectories, context_management);
// it has not been validated against Harbor's RFC and should be before it is
// published as ATIF.
const ATIFVersion = "ATIF-v1.8"

type atifTrajectory struct {
	SchemaVersion string           `json:"schema_version"`
	SessionID     string           `json:"session_id"`
	Agent         atifAgent        `json:"agent"`
	Steps         []atifStep       `json:"steps"`
	FinalMetrics  *atifFinal       `json:"final_metrics,omitempty"`
	Subagents     []atifTrajectory `json:"subagent_trajectories,omitempty"`
	Extra         map[string]any   `json:"extra,omitempty"`
}

type atifAgent struct {
	Name      string         `json:"name"`
	Version   string         `json:"version,omitempty"`
	ModelName string         `json:"model_name,omitempty"`
	Extra     map[string]any `json:"extra,omitempty"`
}

type atifStep struct {
	StepID            int              `json:"step_id"`
	Timestamp         string           `json:"timestamp,omitempty"`
	Source            string           `json:"source"`
	ModelName         string           `json:"model_name,omitempty"`
	Message           string           `json:"message"`
	ReasoningContent  string           `json:"reasoning_content,omitempty"`
	ToolCalls         []atifToolCall   `json:"tool_calls,omitempty"`
	Observation       *atifObservation `json:"observation,omitempty"`
	Metrics           *atifMetrics     `json:"metrics,omitempty"`
	ContextManagement *atifContext     `json:"context_management,omitempty"`
	Extra             map[string]any   `json:"extra,omitempty"`
}

type atifToolCall struct {
	ToolCallID   string          `json:"tool_call_id"`
	FunctionName string          `json:"function_name"`
	Arguments    json.RawMessage `json:"arguments"`
}

type atifObservation struct {
	Results []atifResult `json:"results"`
}

type atifResult struct {
	SourceCallID          string   `json:"source_call_id"`
	Content               string   `json:"content"`
	SubagentTrajectoryRef []string `json:"subagent_trajectory_ref,omitempty"`
}

type atifMetrics struct {
	PromptTokens       int            `json:"prompt_tokens"`
	CompletionTokens   int            `json:"completion_tokens"`
	CachedTokens       int            `json:"cached_tokens"`
	PromptTokenIDs     []int32        `json:"prompt_token_ids,omitempty"`
	CompletionTokenIDs []int32        `json:"completion_token_ids,omitempty"`
	Logprobs           []float32      `json:"logprobs,omitempty"`
	Extra              map[string]any `json:"extra,omitempty"`
}

type atifContext struct {
	Type     string `json:"type"`
	Boundary int    `json:"boundary"`
}

type atifFinal struct {
	TotalPromptTokens     int     `json:"total_prompt_tokens"`
	TotalCompletionTokens int     `json:"total_completion_tokens"`
	TotalCachedTokens     int     `json:"total_cached_tokens"`
	TotalCostUSD          float64 `json:"total_cost_usd,omitempty"`
	TotalSteps            int     `json:"total_steps"`
}

func (x *exporter) writeATIF(work []*workEpisode) error {
	for _, we := range work {
		root := rootAgent(we.ep)
		if root < 0 {
			continue
		}
		traj, err := x.atifOf(we, root)
		if err != nil {
			return err
		}
		if err := x.emit(traj, "", "episode", x.splitOf(we.ep)); err != nil {
			return err
		}
		we.contributed = true
	}
	return nil
}

// atifOf builds the trajectory of the root agent with every other agent nested as
// a subagent trajectory. Spawn edges become subagent_trajectory_ref on the
// observation of the spawning call.
func (x *exporter) atifOf(we *workEpisode, root int) (atifTrajectory, error) {
	ep := we.ep
	children := map[string][]string{} // spawning step id -> child session ids
	for _, e := range ep.Edges {
		if e.Kind == rl.EdgeSpawn {
			children[e.From] = append(children[e.From], sessionID(ep, e.To))
		}
	}
	t, err := x.atifAgent(we, root, children)
	if err != nil {
		return t, err
	}
	for ai := range ep.Agents {
		if ai == root {
			continue
		}
		sub, err := x.atifAgent(we, ai, children)
		if err != nil {
			return t, err
		}
		sub.Extra = map[string]any{"parent": ep.Agents[ai].Parent, "role": ep.Agents[ai].Role, "status": ep.Agents[ai].Status}
		t.Subagents = append(t.Subagents, sub)
	}
	comps := map[string]float64{}
	for k, v := range ep.Reward.Components {
		comps[k] = v
	}
	t.Extra = map[string]any{"sleipnir": map[string]any{
		"schema": ep.Schema, "episode": ep.ID, "task_id": ep.TaskID, "group": ep.Group, "sample": ep.Sample,
		"reward": ep.Reward.Total, "reward_components": comps, "flags": ep.Flags, "signals": ep.Signals,
		"outcome": ep.Outcome, "license": ep.Provenance.License,
	}}
	if t.FinalMetrics != nil {
		t.FinalMetrics.TotalCostUSD = ep.Cost.USD
	}
	return t, nil
}

func sessionID(ep *rl.Episode, agent string) string { return ep.ID + "/" + agent }

func (x *exporter) atifAgent(we *workEpisode, ai int, children map[string][]string) (atifTrajectory, error) {
	ep := we.ep
	ag := &ep.Agents[ai]
	sid := ep.ID
	if ai != rootAgent(ep) {
		sid = sessionID(ep, ag.ID)
	}
	t := atifTrajectory{
		SchemaVersion: ATIFVersion, SessionID: sid,
		Agent: atifAgent{Name: "sleipnir", Version: ep.Harness.Version, ModelName: ag.Model, Extra: map[string]any{"role": ag.Role, "renderer": ep.Harness.Renderer}},
		Steps: []atifStep{},
	}
	final := atifFinal{}
	next := 1
	boundaries := map[int]rl.Segment{}
	for _, s := range ag.Segments {
		if s.Index > 0 {
			boundaries[s.From] = s
		}
	}
	for si := range ag.Steps {
		c := stepCtx{we: we, ag: ag, ai: ai, st: &ag.Steps[si], si: si}
		st := c.st
		if si == 0 {
			if msg, ok := x.instruction(c); ok {
				t.Steps = append(t.Steps, atifStep{StepID: next, Timestamp: stamp(st.At), Source: "user", Message: msg})
				next++
			}
		}
		step := atifStep{StepID: next, Timestamp: stamp(st.At), Source: "agent", ModelName: st.Model}
		turn, tchanged := x.redactTurn(st.Completion.Turn)
		var text, reasoning strings.Builder
		for _, b := range turn.Blocks {
			switch b.Kind {
			case core.BlockText:
				text.WriteString(b.Text)
			case core.BlockThinking:
				if reasoning.Len() > 0 {
					reasoning.WriteString("\n")
				}
				reasoning.WriteString(b.Text)
			case core.BlockToolUse:
				args := b.Input
				if len(args) == 0 {
					args = json.RawMessage(`{}`)
				}
				step.ToolCalls = append(step.ToolCalls, atifToolCall{ToolCallID: b.ToolID, FunctionName: b.ToolName, Arguments: args})
			}
		}
		step.Message, step.ReasoningContent = text.String(), reasoning.String()
		if len(st.Observations) > 0 {
			obs := &atifObservation{}
			for _, o := range st.Observations {
				content, _ := x.redactText(o.Output)
				obs.Results = append(obs.Results, atifResult{SourceCallID: o.ToolID, Content: content})
			}
			if refs := children[st.ID]; len(refs) > 0 {
				k := 0
				for i, o := range st.Observations {
					if o.Name == "spawn" {
						k = i
						break
					}
				}
				obs.Results[k].SubagentTrajectoryRef = append([]string(nil), refs...)
			}
			step.Observation = obs
		}
		m := &atifMetrics{PromptTokens: st.Usage.TotalInput(), CompletionTokens: st.Usage.OutputTokens, CachedTokens: st.Usage.CacheReadTokens}
		if tr, reason := x.traceUsable(c); reason == "" && !tchanged {
			m.PromptTokenIDs, m.CompletionTokenIDs, m.Logprobs = tr.PromptIDs, tr.CompletionIDs, tr.Logprobs
		}
		m.Extra = map[string]any{"cache_hit_ratio": st.Cache.HitRatio, "latency_ms": st.LatencyMs, "reasoning_tokens": st.Usage.ReasoningTokens}
		step.Metrics = m
		if b, ok := boundaries[si]; ok {
			step.ContextManagement = &atifContext{Type: b.Reason, Boundary: b.Index}
		}
		step.Extra = map[string]any{"request": st.ID, "kind": st.Kind, "role": st.Role, "segment": st.Segment, "epoch": st.Epoch, "trainable": st.Trainable, "stop": st.Completion.Stop}
		t.Steps = append(t.Steps, step)
		next++
		final.TotalPromptTokens += m.PromptTokens
		final.TotalCompletionTokens += m.CompletionTokens
		final.TotalCachedTokens += m.CachedTokens
	}
	final.TotalSteps = len(t.Steps)
	t.FinalMetrics = &final
	return t, nil
}

// instruction extracts the text the agent was asked to do from its first prompt:
// the last text block of the first user message (the thread's first user turn
// follows the pinned layers in that message).
func (x *exporter) instruction(c stepCtx) (string, bool) {
	p, err := x.promptFor(c.we, c.st)
	if err != nil {
		return "", false
	}
	for _, m := range p.Messages {
		if m.Role != core.RoleUser {
			continue
		}
		for i := len(m.Blocks) - 1; i >= 0; i-- {
			if m.Blocks[i].Kind == core.BlockText && !m.Blocks[i].Ephemeral {
				s, _ := x.redactText(m.Blocks[i].Text)
				return s, true
			}
		}
		break
	}
	return "", false
}

func stamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}
