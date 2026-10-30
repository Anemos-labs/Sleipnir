package swarm

import (
	"context"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

// serviceHooks holds a service agent (the mailman) to its one tool before any tool runs. The permission requester refuses what
// asks it, but a tool that changes only the harness's own records (a research page) asks nobody; this check does not depend on the
// tool asking. The user's hooks still run for the calls it lets through.
type serviceHooks struct {
	inner agent.Hooks
	only  map[string]bool
	role  string
}

var (
	_ agent.Hooks           = (*serviceHooks)(nil)
	_ agent.CompactionHooks = (*serviceHooks)(nil)
)

// BeforeTool refuses a tool outside the service's set, and passes the others to the user's hooks.
func (h *serviceHooks) BeforeTool(ctx context.Context, call agent.ToolHookCall) agent.ToolHookOutcome {
	if !h.only[call.Tool] {
		return agent.ToolHookOutcome{Veto: true, Reason: "the " + h.role + " only delivers mail: its one tool is mail"}
	}
	if h.inner == nil {
		return agent.ToolHookOutcome{}
	}
	return h.inner.BeforeTool(ctx, call)
}

// AfterTool passes the result to the user's hooks.
func (h *serviceHooks) AfterTool(ctx context.Context, call agent.ToolHookCall, res *tools.Result) agent.ToolHookOutcome {
	if h.inner == nil {
		return agent.ToolHookOutcome{}
	}
	return h.inner.AfterTool(ctx, call, res)
}

// BeforeStop passes the end of the turn to the user's hooks.
func (h *serviceHooks) BeforeStop(ctx context.Context, agentID, role, final string, continuing bool) agent.StopOutcome {
	if h.inner == nil {
		return agent.StopOutcome{}
	}
	return h.inner.BeforeStop(ctx, agentID, role, final, continuing)
}

// BeforeCompact passes an automatic compaction on to the user's hooks, when they listen.
func (h *serviceHooks) BeforeCompact(ctx context.Context, agentID, role, reason string) {
	if ch, ok := h.inner.(agent.CompactionHooks); ok {
		ch.BeforeCompact(ctx, agentID, role, reason)
	}
}

// AfterCompact passes the end of an automatic compaction on to the user's hooks, when they listen.
func (h *serviceHooks) AfterCompact(ctx context.Context, agentID, role, reason string) {
	if ch, ok := h.inner.(agent.CompactionHooks); ok {
		ch.AfterCompact(ctx, agentID, role, reason)
	}
}
