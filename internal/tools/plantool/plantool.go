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

// Tool sets the calling agent's plan in a store.
type Tool struct{ store *plan.Store }

// New returns the tool over a store.
func New(s *plan.Store) *Tool { return &Tool{store: s} }

const schema = `{"type":"object","properties":{"items":{"type":"array","description":"the whole plan, in order","items":{"type":"object","properties":{` +
	`"step":{"type":"string","description":"one concrete step, in one short sentence"},` +
	`"status":{"type":"string","enum":["pending","doing","done"]}},"required":["step"]}}},"required":["items"]}`

// Spec implements tools.Tool. The tool changes nothing but the plan, so it is read-only for the permission engine.
func (*Tool) Spec() core.ToolSpec {
	return core.ToolSpec{
		Name: "plan",
		Description: "Your plan for the task: send the whole list each time you change it. For any task of three steps or more, make the plan first: concrete steps, " +
			"the last one always checking the result (run the tests, run the program). Mark the step you are on doing and finished steps done; one step doing at a time. " +
			"The plan is shown to you again at the end of every request, so you keep to it; a task is not finished while a step is open.",
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
	items, err := plan.Normalize(in.Items)
	if err != nil {
		return tools.Errorf("%v", err), nil
	}
	t.store.Set(c.Env.Agent, items)
	return &tools.Result{Text: fmt.Sprintf("plan saved (%d of %d done):\n%s", len(items)-plan.Open(items), len(items), plan.Lines(items))}, nil
}
