// Package mcpserve exposes Sleipnir as a Model Context Protocol server, so that
// another harness or an agentic development environment (Claude Code, Codex,
// an editor) can hand a goal to a Sleipnir agent or team and follow it.
//
// The server speaks MCP over stdio (newline-delimited JSON-RPC 2.0) and offers
// five tools:
//
//	sleipnir_start   start a goal: one agent, or a manager and N workers
//	sleipnir_status  a job's state, its recent activity and, once it ended, its result
//	sleipnir_wait    block until a job ends or a timeout passes, then report as status does
//	sleipnir_cancel  end a running job the way Ctrl-C ends `sleipnir run`
//	sleipnir_jobs    the jobs of this server
//
// A job is a child `sleipnir run --json` process. The server reads its event
// stream and keeps a bounded digest of it (one line per tool call, message and
// notice), never the whole stream, so a long team run costs the caller only
// what it asks to see. The goal is written to the child's stdin, never put on
// its command line, so no goal can be read as a flag.
//
// # What a caller may do
//
// The server is started by the person's own MCP configuration, so it runs with
// the person's rights, but the model that calls it is not the person. Every
// choice that widens what a job may do is the server's, fixed by the flags it
// was started with, never the caller's: the permission mode is a ceiling (a
// call may ask for a stricter one), the --allow rules and project trust are the
// server's own, and a job's directory must be inside one of the server's roots. A
// caller picks the job's directory (and may have it made, for a new project), the goal, the model, the team size or saved team, a verify
// command, the isolation, a budget and a session to resume.
//
// The provider keys reach a child on an inherited pipe (sched.PassKeys), as
// they do for the web runner's commands, never in its environment.
//
// When the client closes the connection, running jobs are ended as a cancel
// ends them: work an isolated team verified is still applied first.
package mcpserve
