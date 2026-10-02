package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/goal"
	"github.com/anemos-labs/sleipnir/internal/kv"
	"github.com/anemos-labs/sleipnir/internal/plan"
	"github.com/anemos-labs/sleipnir/internal/provider"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

// GoalPlan is the plan of the agent the person talks to: the requirements of a standing goal, as that agent wrote them.
func (s *Session) GoalPlan() []plan.Item {
	a := s.Main()
	if a == nil || s.plans == nil {
		return nil
	}
	return s.plans.Get(a.ID())
}

// JudgeGoal asks the model whether the turn that just ended met the goal. It sees the goal, the plan, the last answer and the results of the
// tool calls of the turn (not the whole thread: a judge that reads everything is as slow as the work). worked says whether the turn called
// any tool. A judge that fails or cannot be read says "continue": it must not end work that is not done, and the limits of the goal still hold.
func (s *Session) JudgeGoal(ctx context.Context, g *goal.State) (v goal.Verdict, worked bool, err error) {
	a := s.Main()
	if a == nil {
		return goal.Verdict{Kind: goal.Continue}, false, fmt.Errorf("no agent to judge")
	}
	answer, evidence := turnEvidence(a.Stack().Thread.Turns)
	prompt := &core.Prompt{
		Model:    s.Model.ID,
		System:   []core.Block{core.Text(goal.JudgeSystem)},
		Params:   core.Params{MaxTokens: 600},
		Messages: []core.Message{{Role: core.RoleUser, Blocks: []core.Block{core.Text(goal.JudgePrompt(g, s.GoalPlan(), answer, evidence))}}},
	}
	resp, err := s.Provider.Do(ctx, &provider.Request{Prompt: prompt, Label: "goal:judge", NoStream: true}, nil)
	if err != nil {
		return goal.Verdict{Kind: goal.Continue}, len(evidence) > 0, err
	}
	usd := s.Model.Price.USD(resp.Usage)
	if resp.CostUSD != nil {
		usd = *resp.CostUSD // the gateway's own figure, when it gives one
	}
	s.mu.Lock()
	s.judgeUSD += usd
	s.mu.Unlock()
	v, _ = goal.ParseVerdict(resp.Turn.PlainText())
	return v, len(evidence) > 0, nil
}

// turnEvidence is the last answer and a line for each tool call since the person's last message, newest last: the tool, what it was asked
// and how it ended.
func turnEvidence(turns []core.Turn) (answer string, evidence []string) {
	start := 0
	for i := len(turns) - 1; i >= 0; i-- {
		if turns[i].Role == core.RoleUser && (turns[i].Origin == "" || turns[i].Origin == core.OriginUser) && strings.TrimSpace(kv.AnswerText(turns[i])) != "" {
			start = i + 1
			break
		}
	}
	calls := map[string]string{}
	for _, t := range turns[start:] {
		for _, b := range t.Blocks {
			switch {
			case b.ToolID != "" && b.ToolName != "":
				calls[b.ToolID] = b.ToolName + " " + inputGist(b.Input)
			case b.Kind == core.BlockToolResult:
				out := strings.Join(strings.Fields(b.PlainText()), " ")
				if r := []rune(out); len(r) > 280 {
					out = string(r[:140]) + " … " + string(r[len(r)-120:])
				}
				state := "ok"
				if b.IsError {
					state = "ERROR"
				}
				evidence = append(evidence, fmt.Sprintf("%s → %s: %s", calls[b.ToolID], state, out))
			}
		}
		if t.Role == core.RoleAssistant {
			if txt := strings.TrimSpace(kv.AnswerText(t)); txt != "" {
				answer = txt
			}
		}
	}
	if len(evidence) > 8 {
		evidence = evidence[len(evidence)-8:]
	}
	return answer, evidence
}

// inputGist is a tool call's arguments in one short line.
func inputGist(in json.RawMessage) string {
	s := strings.Join(strings.Fields(string(in)), " ")
	if r := []rune(s); len(r) > 120 {
		s = string(r[:119]) + "…"
	}
	return s
}

// GoalTurn is what a standing goal does after a turn ended with turnErr: it judges the turn and returns a note for the person and, when the goal
// is not met, the message that sends the agent on. A turn that was cancelled or failed pauses the goal, and so does a judge that cannot be
// asked (the person resumes it or drops it). g is changed.
func (s *Session) GoalTurn(ctx context.Context, g *goal.State, turnErr error) (note, next string) {
	if g == nil || g.Paused != "" || g.Done {
		return "", ""
	}
	pause := func(why string) (string, string) {
		g.Paused = why
		return "goal paused: " + why + ". /goal resume goes on, /goal clear drops it", ""
	}
	switch {
	case errors.Is(turnErr, context.Canceled):
		return pause("you interrupted it")
	case turnErr != nil:
		return pause("the turn failed")
	}
	v, worked, err := s.JudgeGoal(ctx, g)
	if err != nil {
		return pause("the judge could not be asked (" + short(tools.SanitizeForTerminal(err.Error()), 100) + ")")
	}
	reason := tools.SanitizeForTerminal(v.Reason)
	switch g.Apply(v, worked) {
	case goal.Stop:
		return "goal met: " + reason, ""
	case goal.Pause:
		return "goal paused: " + g.Paused + ". /goal resume goes on, /goal clear drops it", ""
	}
	if reason == "" {
		reason = "the judge's answer could not be read"
	}
	return fmt.Sprintf("goal not met yet: %s (continuation %d of %d)", reason, g.Turns, g.Max), goal.Continuation(g, s.GoalPlan(), v)
}

func short(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// goalEvent is the log record of a standing goal; a nil goal says it was cleared or met.
const goalEvent = "goal.state"

// SaveGoal records the goal in the session log, so that a chat that is resumed (after /model, /login, a restart) still has it.
func (s *Session) SaveGoal(g *goal.State) {
	s.Log.Emit("", goalEvent, map[string]any{"goal": g})
}

// LoadGoal is the goal this session had when its log last said so, made paused ("the chat was restarted"): nothing runs until the person
// says /goal resume. nil when there was none, or it was cleared or met.
func (s *Session) LoadGoal() *goal.State {
	if !s.Resumed() {
		return nil
	}
	_ = s.Log.Flush()
	var last *goal.State
	_ = events.Scan(filepath.Join(s.Dir, "events.jsonl"), func(e events.Event) error {
		if e.Type != goalEvent {
			return nil
		}
		var d struct {
			Goal *goal.State `json:"goal"`
		}
		if json.Unmarshal(e.Data, &d) == nil {
			last = d.Goal
		}
		return nil
	})
	if last != nil && last.Objective != "" && !last.Done {
		if last.Paused == "" {
			last.Paused = "the chat was restarted"
		}
		return last
	}
	return nil
}
