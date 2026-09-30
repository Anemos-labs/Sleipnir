package hooks

import "strings"

// Event names, spelled as Claude Code spells them so that settings written for
// it work unchanged.
const (
	SessionStart       = "SessionStart"
	SessionEnd         = "SessionEnd"
	UserPromptSubmit   = "UserPromptSubmit"
	PreToolUse         = "PreToolUse"
	PostToolUse        = "PostToolUse"
	PostToolUseFailure = "PostToolUseFailure"
	PermissionRequest  = "PermissionRequest"
	Notification       = "Notification"
	Stop               = "Stop"
	SubagentStart      = "SubagentStart"
	SubagentStop       = "SubagentStop"
	PreCompact         = "PreCompact"
	PostCompact        = "PostCompact"
)

// eventOrder is the canonical order, used wherever events are listed.
var eventOrder = []string{
	SessionStart, SessionEnd, UserPromptSubmit, PreToolUse, PostToolUse, PostToolUseFailure,
	PermissionRequest, Notification, Stop, SubagentStart, SubagentStop, PreCompact, PostCompact,
}

// Events lists the canonical event names in a fixed order.
func Events() []string { return append([]string(nil), eventOrder...) }

// normEvent is the spelling-insensitive form of an event name: lower case
// without "_", "-" or spaces.
func normEvent(s string) string {
	return strings.NewReplacer("_", "", "-", "", " ", "").Replace(strings.ToLower(strings.TrimSpace(s)))
}

// eventAliases maps normalised spellings to canonical names. Besides every
// canonical name (so "pre_tool_use", "PRE-TOOL-USE" and "pretooluse" all work),
// it holds the short lower-case aliases Sleipnir configuration uses.
var eventAliases = map[string]string{
	"sessionstart": SessionStart, "sessionend": SessionEnd,
	"userpromptsubmit": UserPromptSubmit, "promptsubmit": UserPromptSubmit, "userprompt": UserPromptSubmit,
	"pretooluse": PreToolUse, "pretool": PreToolUse,
	"posttooluse": PostToolUse, "posttool": PostToolUse,
	"posttoolusefailure": PostToolUseFailure, "posttoolfailure": PostToolUseFailure, "toolfailure": PostToolUseFailure,
	"permissionrequest": PermissionRequest, "permission": PermissionRequest,
	"notification": Notification, "notify": Notification,
	"stop":          Stop,
	"subagentstart": SubagentStart, "agentstart": SubagentStart,
	"subagentstop": SubagentStop, "agentstop": SubagentStop,
	"precompact": PreCompact, "postcompact": PostCompact,
}

// Canonical resolves any accepted spelling of an event name ("PreToolUse",
// "pre_tool_use", "pretool") to its canonical form.
func Canonical(name string) (string, bool) {
	c, ok := eventAliases[normEvent(name)]
	return c, ok
}

// isClaudeOnly reports whether name is a Claude Code event that Sleipnir does
// not fire. A settings file that lists one is valid Claude Code configuration,
// so it is ignored with a warning rather than rejected: sharing a file between
// the two harnesses must not break either.
func isClaudeOnly(name string) bool {
	switch normEvent(name) {
	case "setup", "instructionsloaded", "userpromptexpansion", "messagedisplay", "posttoolbatch", "permissiondenied",
		"taskcreated", "taskcompleted", "stopfailure", "teammateidle", "configchange", "cwdchanged", "directoryadded",
		"filechanged", "worktreecreate", "worktreeremove", "premodelswitch", "postmodelswitch", "elicitation", "elicitationresult":
		return true
	}
	return false
}

// exit2Blocks reports whether a hook exiting with status 2 stops something on
// this event: the tool call, the prompt, the stop, the compaction. For
// PostToolUse and PostToolUseFailure the tool has already run, so blocking means
// "feed the reason back to the model". On the other events status 2 is only an
// error to show the user.
func exit2Blocks(event string) bool {
	switch event {
	case PreToolUse, PermissionRequest, UserPromptSubmit, Stop, SubagentStop, PreCompact, PostToolUse, PostToolUseFailure:
		return true
	}
	return false
}

// hasDecision reports whether the event is a permission decision point.
func hasDecision(event string) bool { return event == PreToolUse || event == PermissionRequest }

// stdoutIsContext reports whether plain text on stdout (exit 0) is context for
// the model on this event.
func stdoutIsContext(event string) bool { return event == SessionStart || event == UserPromptSubmit }
