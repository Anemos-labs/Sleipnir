package agent

import (
	"context"
	"encoding/json"

	"github.com/reee344/sleipnir/internal/tools"
)

// Hooks lets the harness run user-configured commands around an agent's steps
// without the agent knowing what a hook is: the session supplies an
// implementation (internal/hooks does the running), the agent only asks and
// obeys. A nil Hooks makes every call a no-op.
//
// Hooks are advice with limits. A hook can veto a tool call, rewrite its input,
// or add text for the model to read; it can never lift a denial by the
// permission engine, which the tool consults itself.
type Hooks interface {
	// BeforeTool runs before a tool call. A veto turns the call into an error
	// result carrying Reason; UpdatedInput replaces the input; Context is text the
	// model sees appended to the result.
	BeforeTool(ctx context.Context, call ToolHookCall) ToolHookOutcome
	// AfterTool runs after a call with its result (a failure counts as one). A
	// non-empty Reason or Context is appended to what the model sees; neither can
	// undo what the tool did.
	AfterTool(ctx context.Context, call ToolHookCall, res *tools.Result) ToolHookOutcome
	// BeforeStop runs when the agent is about to end its turn with a final answer.
	// continuing is true when an earlier hook already sent the agent back to work
	// in this run (the hook's way to avoid blocking forever). A veto sends Reason
	// to the agent as a new message and it keeps working, at most maxStopVetoes
	// times per run.
	BeforeStop(ctx context.Context, agentID, role, final string, continuing bool) StopOutcome
}

// ToolHookCall identifies a tool call to a hook.
type ToolHookCall struct {
	Agent, Role string
	Tool        string
	Input       json.RawMessage
}

// ToolHookOutcome is what the hooks of a tool event decided.
type ToolHookOutcome struct {
	// Veto stops the call (BeforeTool); Reason says why, for the model.
	Veto   bool
	Reason string
	// UpdatedInput replaces the call's input (BeforeTool), when set and not vetoed.
	UpdatedInput json.RawMessage
	// Context is extra text for the model.
	Context string
}

// StopOutcome is what the Stop hooks decided.
type StopOutcome struct {
	Veto   bool
	Reason string
}

// maxStopVetoes bounds how often Stop hooks may send an agent back to work in
// one run: a hook that always objects must not keep an agent (and the bill)
// running forever.
const maxStopVetoes = 3
