package agent

import "strings"

// Kinds of tool failure recorded on tool.result events (meta["error_kind"]). They
// are the vocabulary the training pipeline counts protocol mistakes with: an
// invalid call, an edit to a file that changed under the agent, a write another
// agent holds or that lies outside the task's scope, a refused permission. They
// are attached where the harness knows the cause, and otherwise recognised from
// the wording of the harness's own messages, which are stable and written in one
// place each.
const (
	ErrKindUnknownTool  = "unknown_tool"
	ErrKindInvalidInput = "invalid_input"
	ErrKindStale        = "stale"
	ErrKindLease        = "lease"
	ErrKindScope        = "scope"
	ErrKindPermission   = "permission"
	ErrKindHook         = "hook"
	// ErrKindTooManyCalls marks a call that was not run because its turn asked for
	// more calls than the per-turn cap (Config.MaxToolCallsPerTurn).
	ErrKindTooManyCalls = "too_many_calls"
	// ErrKindTimeout marks a call that was stopped because it ran longer than the
	// per-call deadline (Config.ToolTimeout).
	ErrKindTimeout = "timeout"
)

// withErrorKind sets meta["error_kind"], creating the map when needed. An empty
// kind leaves meta alone.
func withErrorKind(meta map[string]any, kind string) map[string]any {
	if kind == "" {
		return meta
	}
	if meta == nil {
		meta = map[string]any{}
	}
	meta["error_kind"] = kind
	return meta
}

// classifyToolError names the kind of a failed tool result from its text, or ""
// when it is an ordinary failure (a command that exited non-zero, a missing file).
func classifyToolError(text string) string {
	switch {
	case strings.Contains(text, "changed since you last read it"):
		return ErrKindStale
	case strings.Contains(text, "is outside the scope of your task"):
		return ErrKindScope
	case strings.Contains(text, "is being edited by"):
		return ErrKindLease
	case strings.HasPrefix(text, "permission denied") || strings.Contains(text, "approval required") || strings.Contains(text, "not allowed by the permission policy"):
		return ErrKindPermission
	case strings.HasPrefix(text, "A hook blocked this call"):
		return ErrKindHook
	}
	return ""
}
