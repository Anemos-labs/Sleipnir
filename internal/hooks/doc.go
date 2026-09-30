// Package hooks runs user-defined commands at points of the agent's life, the
// way Claude Code does, so that existing hooks keep working.
//
// # Configuration
//
// Hooks are the "hooks" object of a settings file (config.Config.Hooks), which
// Parse turns into a Set:
//
//	{"PreToolUse": [{"matcher": "Bash|Edit",
//	                 "hooks": [{"type": "command", "command": "./check.sh", "timeout": 30}]}]}
//
// The events are SessionStart, SessionEnd, UserPromptSubmit, PreToolUse,
// PostToolUse, PostToolUseFailure, PermissionRequest, Notification, Stop,
// SubagentStart, SubagentStop, PreCompact and PostCompact. Spellings such as
// pre_tool_use or pretool are accepted too. Events Claude Code has and Sleipnir
// does not fire are ignored with a warning, not rejected, so one settings file
// can serve both.
//
// A matcher is "" or "*" (everything), an exact list ("Bash|Edit", ignoring case
// and "_"/"-": WebFetch matches web_fetch) or a regular expression
// ("^Notebook", "mcp__memory__.*"). It is tried against the tool name on tool
// events (and against its Claude Code spelling: the patch tool also answers to
// Edit and Write), against the role on SubagentStart and SubagentStop, and
// against Extra["source"], ["reason"], ["notification_type"] and ["trigger"] on
// SessionStart, SessionEnd, Notification and the compaction events.
// UserPromptSubmit and Stop have no matcher.
//
// A hook's "if" is a permission rule ("Bash(git commit:*)", "Edit(src/**)")
// evaluated by the permission engine's own matching; the hook runs only for
// tool calls the rule matches. Hook types "command" and "http" are supported;
// "prompt", "agent" and "mcp_tool" are skipped with a warning.
//
// # What a hook sees and can say
//
// A command hook is run by /bin/sh -c in the project (or the agent's) directory.
// It reads a JSON object on stdin: session_id, cwd, hook_event_name, tool_name,
// tool_input, tool_response, agent_id and agent_type (also agent and role), and
// whatever Event.Extra adds. tool_name is the Claude Code name of the tool
// ("Bash" for bash) and tool_input carries file_path where a tool says path, so
// hooks written for Claude Code see the fields they test; a hook that reads a
// field that is not there would allow everything without anyone noticing.
//
// Exit status 0 is success: stdout is parsed as JSON if it is JSON (below),
// and on SessionStart and UserPromptSubmit plain text on stdout is context for
// the model. Exit status 2 is a blocking error whose stderr is the reason, on
// the events where something can be blocked (PreToolUse, PermissionRequest,
// UserPromptSubmit, Stop, SubagentStop, PreCompact; on PostToolUse and
// PostToolUseFailure it means "tell the model"). Any other status is an error
// shown to the user that blocks nothing, and so is a timeout, which Claude Code
// treats the same way. Set failClosed on a hook, or Runner.FailClosed, to make
// such failures block instead.
//
// JSON on stdout: "decision" ("approve", "block") with "reason";
// "hookSpecificOutput" with "permissionDecision" (allow, deny, ask),
// "permissionDecisionReason", "updatedInput" and "additionalContext"; and
// "continue": false with "stopReason", "systemMessage" and "suppressOutput".
//
// # Combining
//
// All matching hooks of an event run in parallel, each with its own timeout,
// and their answers are folded in configuration order (never completion order):
// Deny beats Ask beats Allow, reasons and additional context are concatenated
// in order and capped, the first hook to rewrite the tool input wins, and any
// hook may stop the run. The result is Result.
//
// # Security model
//
// Hooks are commands: whoever writes one runs code with the user's rights. A
// hook that arrives with a repository is therefore as untrusted as the
// repository. Every hook carries an Origin; hooks of the user's own
// configuration are OriginUser (ParseAs) and run, everything else runs only if
// the Runner is Trusted, that is, the caller has established that the project is
// trusted, or its Approve callback approves that hook. The zero Runner runs
// nothing it cannot vouch for. A hook that is not run is reported, not silently
// dropped.
//
// Hooks do not inherit the harness's secrets. The environment is scrubbed of
// variables that look like credentials (API keys, tokens, passwords, cloud
// access keys, the SSH agent socket, database URLs and any value that is a URL
// with a password in it), and Runner.DenyEnv names the providers' key variables
// exactly. Name matching cannot be complete; a same-user process can still read
// /proc/<pid>/environ of the harness, so the harness should also stop being
// dumpable (prctl PR_SET_DUMPABLE) after it has read its keys.
//
// A hook cannot hang or flood the harness. Each runs in its own process group
// with a timeout, and the whole group is killed (SIGTERM, then SIGKILL) when the
// timeout, the caller's context or an output limit ends it; background children
// that keep its output open are ended when it is done. Output is capped, cleaned
// of terminal escapes, invalid UTF-8 and hidden Unicode, and capped again when it
// becomes a reason or context, because both end up in front of a model. stdin is
// written concurrently, so a hook that never reads it is harmless.
//
// A hook's answer is advice with limits: an Allow answers a question a human
// would have been asked and never lifts a denial (see Runner.Prompter, which
// plugs PermissionRequest hooks into the permission engine at exactly that
// point), and UpdatedInput is honoured only when nothing denied the call.
//
// http hooks send tool inputs and results to a URL and are off unless
// Runner.AllowHTTP is set; they refuse plain http except to loopback, do not
// follow redirects, and cap the response.
package hooks
