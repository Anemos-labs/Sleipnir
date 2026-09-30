package session

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/reee344/sleipnir/internal/agent"
	"github.com/reee344/sleipnir/internal/config"
	"github.com/reee344/sleipnir/internal/hooks"
	"github.com/reee344/sleipnir/internal/perm"
	"github.com/reee344/sleipnir/internal/tools"
)

// ErrPromptBlocked is returned by Run when a UserPromptSubmit hook blocked the
// prompt. The error text is the hook's reason.
var ErrPromptBlocked = errors.New("blocked by a hook")

// newHookRunner builds the hook runner from the configuration. Only settings
// the user controls reach it: config.Load drops a project's hooks unless the
// project is trusted, so everything in cfg.Hooks is vouched for. A configuration
// that does not parse disables hooks and says why; it never stops the session.
func newHookRunner(cfg *config.Config, sessionID, root string) (*hooks.Runner, []string) {
	if len(cfg.Hooks) == 0 {
		return nil, nil
	}
	set, err := hooks.ParseAs(hooks.OriginUser, "config", cfg.Hooks)
	if err != nil {
		return nil, []string{"hooks: not used: " + err.Error()}
	}
	var warns []string
	for _, w := range set.Warnings() {
		warns = append(warns, "hooks: "+w)
	}
	if set.Empty() {
		return nil, warns
	}
	// Hooks must not see the providers' keys: names cannot all be guessed, so the
	// runner is told exactly which variables hold them.
	var deny []string
	for name := range builtinProviders {
		if p, ok := lookupProvider(cfg, name); ok && p.APIKeyEnv != "" {
			deny = append(deny, p.APIKeyEnv)
		}
	}
	for _, p := range cfg.Providers {
		if p.APIKeyEnv != "" {
			deny = append(deny, p.APIKeyEnv)
		}
	}
	return &hooks.Runner{Set: set, Dir: root, SessionID: sessionID, DenyEnv: deny, MaxConcurrent: 8}, warns
}

// hookAdapter implements agent.Hooks over a hooks.Runner, and records what hooks
// did (hook.run events) and what they said to the user (notices).
type hookAdapter struct {
	s   *Session
	run *hooks.Runner
}

var _ agent.Hooks = (*hookAdapter)(nil)

func (h *hookAdapter) fire(ctx context.Context, ev hooks.Event) hooks.Result {
	ev.SessionID, ev.Cwd = h.s.ID, h.s.opts.Cwd
	res, err := h.run.Run(ctx, ev)
	if err != nil {
		h.s.notice(ev.Agent, "hook error: "+err.Error())
		return hooks.Result{}
	}
	for _, r := range res.Runs {
		h.s.Log.Emit(ev.Agent, "hook.run", map[string]any{
			"event": ev.Name, "tool": ev.Tool, "hook": r.Hook, "ms": r.Duration.Milliseconds(), "exit": r.ExitCode,
			"timed_out": r.TimedOut, "skipped": r.Skipped, "blocked": res.Blocked, "reason": clip(res.Reason, 300),
		})
	}
	for _, e := range res.Errors {
		h.s.notice(ev.Agent, "hook: "+e.Error())
	}
	for _, m := range res.Messages {
		h.s.notice(ev.Agent, m)
	}
	return res
}

// BeforeTool implements agent.Hooks.
func (h *hookAdapter) BeforeTool(ctx context.Context, c agent.ToolHookCall) agent.ToolHookOutcome {
	res := h.fire(ctx, hooks.Event{Name: hooks.PreToolUse, Tool: c.Tool, Input: c.Input, Agent: c.Agent, Role: c.Role})
	out := agent.ToolHookOutcome{Context: res.AdditionalContext}
	switch {
	case res.Blocked || res.Decision == hooks.Deny || res.Stop:
		out.Veto = true
		out.Reason = firstNonEmpty(res.Reason, res.StopReason, "a hook blocked this call")
	case len(res.UpdatedInput) > 0:
		out.UpdatedInput = res.UpdatedInput
	}
	return out
}

// AfterTool implements agent.Hooks.
func (h *hookAdapter) AfterTool(ctx context.Context, c agent.ToolHookCall, r *tools.Result) agent.ToolHookOutcome {
	name := hooks.PostToolUse
	if r != nil && r.IsError {
		name = hooks.PostToolUseFailure
	}
	text := ""
	if r != nil {
		text = r.Text
	}
	res := h.fire(ctx, hooks.Event{Name: name, Tool: c.Tool, Input: c.Input, Output: text, Agent: c.Agent, Role: c.Role})
	out := agent.ToolHookOutcome{Context: res.AdditionalContext}
	if res.Blocked {
		out.Reason = res.Reason
	}
	return out
}

// BeforeStop implements agent.Hooks.
func (h *hookAdapter) BeforeStop(ctx context.Context, agentID, role, final string, continuing bool) agent.StopOutcome {
	res := h.fire(ctx, hooks.Event{Name: hooks.Stop, Agent: agentID, Role: role, Extra: map[string]any{"stop_hook_active": continuing}})
	if res.Blocked {
		return agent.StopOutcome{Veto: true, Reason: firstNonEmpty(res.Reason, "a Stop hook asked you to keep working")}
	}
	return agent.StopOutcome{}
}

// promptHook runs UserPromptSubmit and SessionStart hooks around a goal. It
// returns the goal to send (hook context is appended as text of the message, never
// written into a cached prompt layer) or ErrPromptBlocked.
func (s *Session) promptHook(ctx context.Context, first bool, goal string) (string, error) {
	if s.hooks == nil {
		return goal, nil
	}
	var extra []string
	if first {
		res := s.hookAdapter.fire(ctx, hooks.Event{Name: hooks.SessionStart, Agent: s.mainAgent(), Extra: map[string]any{"source": "startup"}})
		if res.AdditionalContext != "" {
			extra = append(extra, res.AdditionalContext)
		}
	}
	res := s.hookAdapter.fire(ctx, hooks.Event{Name: hooks.UserPromptSubmit, Agent: s.mainAgent(), Extra: map[string]any{"prompt": goal}})
	if res.Blocked || res.Stop {
		return "", fmt.Errorf("%w: %s", ErrPromptBlocked, firstNonEmpty(res.Reason, res.StopReason, "the prompt was not sent"))
	}
	if res.AdditionalContext != "" {
		extra = append(extra, res.AdditionalContext)
	}
	if len(extra) == 0 {
		return goal, nil
	}
	return goal + "\n\n[context from your hooks]\n" + strings.Join(extra, "\n"), nil
}

func (s *Session) mainAgent() string {
	if s.opts.Swarm {
		return "mgr"
	}
	return "main"
}

// notice reports something to the user and the log.
func (s *Session) notice(agentID, msg string) {
	s.Log.Emit(agentID, "notice", map[string]any{"level": "warn", "msg": msg})
	if s.opts.Sink != nil {
		s.opts.Sink.Notice(agentID, "warn", msg)
	}
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// hookPrompter puts PermissionRequest hooks in front of the human: a hook may
// answer a question the permission engine would have asked, and never overrides a
// refusal by the engine itself.
func (s *Session) hookPrompter(next perm.Prompter) perm.Prompter {
	if s.hooks == nil {
		return next
	}
	return s.hooks.Prompter(next)
}
