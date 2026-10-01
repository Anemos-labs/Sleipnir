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
	"github.com/reee344/sleipnir/internal/swarm"
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

var (
	_ agent.Hooks           = (*hookAdapter)(nil)
	_ agent.CompactionHooks = (*hookAdapter)(nil)
	_ agent.WorkerHooks     = (*hookAdapter)(nil)
)

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

// BeforeStop implements agent.Hooks. The agent the person talks to (the single
// agent, or a swarm's manager) fires Stop; a swarm's workers fire SubagentStop, as in
// Claude Code, so a hook written for one of them is not run for every worker. The
// mailman is a service of the harness, nobody's subagent: it fires neither.
func (h *hookAdapter) BeforeStop(ctx context.Context, agentID, role, final string, continuing bool) agent.StopOutcome {
	name := hooks.Stop
	if agentID != h.s.mainAgent() {
		if role == swarm.MailmanRoleName {
			return agent.StopOutcome{}
		}
		name = hooks.SubagentStop
	}
	res := h.fire(ctx, hooks.Event{Name: name, Agent: agentID, Role: role, Extra: map[string]any{
		"stop_hook_active": continuing, "last_assistant_message": clip(final, 4000),
	}})
	if res.Blocked {
		return agent.StopOutcome{Veto: true, Reason: firstNonEmpty(res.Reason, "a "+name+" hook asked you to keep working")}
	}
	return agent.StopOutcome{}
}

// WorkerStarted implements agent.WorkerHooks: the SubagentStart hooks of a new worker.
// What they print is added to the worker's first task message (see swarm.startedBrief).
func (h *hookAdapter) WorkerStarted(ctx context.Context, agentID, role, task string) string {
	res := h.fire(ctx, hooks.Event{Name: hooks.SubagentStart, Agent: agentID, Role: role, Extra: map[string]any{"task": task}})
	return res.AdditionalContext
}

// BeforeCompact and AfterCompact implement agent.CompactionHooks: PreCompact and
// PostCompact around the compactions the agent decides on itself (trigger "auto").
// A person's /compact is Session.Compact (trigger "manual"), where a PreCompact hook
// may refuse; an automatic one cannot be refused, so a hook that tries is told so.
func (h *hookAdapter) BeforeCompact(ctx context.Context, agentID, role, reason string) {
	res := h.fire(ctx, hooks.Event{Name: hooks.PreCompact, Agent: agentID, Role: role, Extra: map[string]any{"trigger": "auto", "reason": clip(reason, 200)}})
	if res.Blocked {
		h.s.notice(agentID, "a PreCompact hook asked to skip an automatic compaction: it goes ahead, because the prompt would only grow ("+clip(firstNonEmpty(res.Reason, "no reason given"), 120)+")")
	}
}

func (h *hookAdapter) AfterCompact(ctx context.Context, agentID, role, reason string) {
	h.fire(ctx, hooks.Event{Name: hooks.PostCompact, Agent: agentID, Role: role, Extra: map[string]any{"trigger": "auto", "reason": clip(reason, 200)}})
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
		source := "startup"
		if s.Resumed() {
			source = "resume"
		}
		res := s.hookAdapter.fire(ctx, hooks.Event{Name: hooks.SessionStart, Agent: s.mainAgent(), Extra: map[string]any{"source": source}})
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

// agentHooks is the hook adapter as an agent.Hooks, or a true nil when no hooks
// are configured (a nil *hookAdapter inside an interface would not be nil).
func (s *Session) agentHooks() agent.Hooks {
	if s.hookAdapter == nil {
		return nil
	}
	return s.hookAdapter
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

// trackAsks lets the swarm know which of its workers are waiting for the person's answer to a question (Swarm.Asking), for as long as
// the question is open, so that a worker whose question nobody answered is reported as that and not as hung. Without a prompter there
// is nobody to ask and the engine refuses; that stays so. The swarm is read when the question is put, not when the engine is built:
// the engine is made first.
func (s *Session) trackAsks(next perm.Prompter) perm.Prompter {
	if next == nil {
		return nil
	}
	return func(ctx context.Context, r perm.Request) perm.Decision {
		if sw := s.Swarm; sw != nil {
			defer sw.Asking(r.Agent, r.Summary)()
		}
		return next(ctx, r)
	}
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
