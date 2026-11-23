// Package acpserve exposes Sleipnir as an Agent Client Protocol (ACP) agent,
// so that an editor or another coding harness can drive Sleipnir agents and
// teams as a normal ACP session.
//
// The server speaks ACP v1 over stdio: newline-delimited JSON-RPC 2.0 on
// stdin and stdout, with progress sent as session/update notifications. It
// implements initialize, session/new, session/list, session/load,
// session/resume, session/close, session/prompt, session/cancel,
// session/set_config_option and session/set_mode. Each prompt turn starts one
// child `sleipnir run --json --delegated`; the events of that stream become
// streamed messages, thought chunks, tool calls, usage and a final stop
// reason. A turn can run one agent or a manager with workers, selected by the
// session's "swarm" option, and can resume the Sleipnir session of the last
// turn after session/load.
//
// The security boundary is the same as for `sleipnir mcp serve`: the person
// who starts the server fixes the permission-mode ceiling, --allow rules,
// project trust, roots and maximum number of runs. The ACP client picks the
// folder, goal, model, mode and team size, but cannot widen any of those
// limits. A client's MCP servers are passed to each run with --mcp-config
// from a private temporary file; their credentials are never logged or sent
// back in a notification. Provider keys are handed to a run on an inherited
// pipe (sched.PassKeys), never in its environment.
//
// Session metadata and a bounded replay of updates are kept under the
// Sleipnir state directory so that session/list, session/load and
// session/resume work across a client reconnect. Closing a connection
// cancels the turns still running and waits for them to end.
package acpserve
