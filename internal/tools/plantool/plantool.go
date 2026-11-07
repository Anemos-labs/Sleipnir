// Package plantool is the model's plan: it sets the list of steps that the harness shows back at the end of every request.
package plantool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/plan"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

// ManagerRole is the role of a team's manager (swarm.ManagerRole), whose plan may have a step doing for each worker on one.
const ManagerRole = "manager"

// Tool sets the calling agent's plan in a store.
type Tool struct {
	store *plan.Store
	check func(agent string, items []plan.Item) string
}

// New returns the tool over a store.
func New(s *plan.Store) *Tool { return &Tool{store: s} }

// WithCheck has the tool add what check says about a plan it saved to its result (a goal's milestones that no step names, say):
// advice to the agent, never a refusal, since a refused plan costs a turn and keeps no plan at all.
func (t *Tool) WithCheck(check func(agent string, items []plan.Item) string) *Tool {
	t.check = check
	return t
}

const schema = `{"type":"object","properties":{"items":{"type":"array","description":"the whole plan, in order","items":{"type":"object","properties":{` +
	`"step":{"type":"string","description":"one concrete step, in one short sentence"},` +
	`"status":{"type":"string","enum":["pending","doing","done"]},` +
	`"effort":{"type":"number","description":"your estimate of the step's work in hours"},` +
	`"milestone":{"type":"string","description":"the id of the goal milestone the step serves, such as M3"}},"required":["step"]}}},"required":["items"]}`

// Spec implements tools.Tool. The tool changes nothing but the plan, so it is read-only for the permission engine.
func (*Tool) Spec() core.ToolSpec {
	return core.ToolSpec{
		Name: "plan",
		Description: "Your plan for the task: send the whole list each time you change it. For any task of three steps or more, make the plan first: concrete steps, " +
			"the last one always checking the result (run the tests, run the program). Mark the step you are on doing and finished steps done; one step doing at a time. " +
			"The plan is shown to you again at the end of every request, so you keep to it; a task is not finished while a step is open. " +
			"Give each step an effort estimate in hours and, for a goal with milestones, the milestone it serves: progress is weighed by the estimates, so cover all of the work, split a step of more than a few hours, " +
			"add steps for work you discover, and mark a step done only when its work is finished and checked, not when it is handed to someone.",
		InputSchema: json.RawMessage(schema),
		ReadOnly:    true,
	}
}

// Run implements tools.Tool.
func (t *Tool) Run(_ context.Context, c *tools.Call) (*tools.Result, error) {
	var in struct {
		Items []plan.Item `json:"items"`
	}
	if err := json.Unmarshal(c.Input, &in); err != nil {
		return tools.Errorf(`invalid arguments (%v): send {"items": [{"step": "...", "status": "pending"}, ...]}`, err), nil
	}
	normalize := plan.Normalize
	if c.Env.Role == ManagerRole {
		normalize = plan.NormalizeDelegated // its steps are carried by its workers, several at once
	}
	items, err := normalize(in.Items)
	if err != nil {
		return tools.Errorf("%v", err), nil
	}
	t.store.Set(c.Env.Agent, items)
	text := fmt.Sprintf("plan saved (%s):\n%s", plan.Summary(items, nil), plan.Lines(items))
	if t.check != nil {
		if note := t.check(c.Env.Agent, items); note != "" {
			text += "\n" + note
		}
	}
	return &tools.Result{Text: text}, nil
}
