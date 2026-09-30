// Package agentdefs loads markdown subagent definitions, the files Claude Code
// users keep in .claude/agents, and turns them into swarm roles.
//
// # Layout
//
// A definition is a file <agents>/<name>.md: frontmatter, then the role's
// instructions as the body. The directories are searched in this order, and the
// first definition of a name wins (a collision is reported as a Warning):
//
//	<root>/.sleipnir/agents   <root>/.claude/agents      project
//	~/.sleipnir/agents        ~/.claude/agents           user
//	Opts.Extra                                            plugins, namespaced
//
// Frontmatter: name and description (the file name and the first line of the
// body stand in when absent), tools (an allowlist, as a list or a comma
// separated string), disallowedTools, model, permissionMode, maxTurns, skills,
// and the Sleipnir additions readonly, short and priority. Other fields Claude
// Code defines (mcpServers, hooks, memory, ...) are recognised and ignored;
// unknown ones are reported.
//
// # From definition to role
//
// Def.ToRole yields Role, which has exactly the fields of swarm.Role and so
// converts to it directly. The body is the role pin: it is cached once and then
// read on every request of every agent of that role, which is why size is
// policed. A pin over Opts.WarnPinTokens (800 by default) is loaded with a
// warning that says what it costs; over Opts.MaxPinTokens (4000) it is refused.
//
// Role.ReadOnly is computed. It is true when the definition says readonly:
// true, or permissionMode: plan, or has an explicit tool allowlist in which
// every tool is known not to write files or run commands. Anything else in the
// list (a shell, an editor, a delegation tool, an MCP tool whose effects are
// unknown) makes the role a writer, so a definition cannot dodge the swarm's
// writer cap by listing an opaque tool.
//
// The tool allowlist itself cannot be expressed in a Role, and hiding tools
// would fork every agent's cached tool list. Def.Profile gives the runtime
// equivalent: a perm.RoleProfile that denies the tool classes the allowlist
// leaves out and the tools disallowedTools names. Register it with the
// permission engine under Role.Name.
//
// # Security model
//
// Repository-supplied definitions are untrusted until the user trusts the
// project: with Opts.TrustProject false nothing under <root>/.sleipnir or
// <root>/.claude is read, and only the user's own directories are.
//
// A definition can restrict an agent and never widen it. permissionMode values
// that would loosen the session (bypassPermissions, dontAsk, auto) are ignored
// with a warning, Profile can only add denials, and a definition may not take a
// name in Opts.Reserved, so a repository cannot replace the built-in manager or
// reviewer with its own text.
//
// The pin is text that goes into every request of the role, so it is cleaned
// like everything else that reaches a model (hidden Unicode and HTML comments
// removed, and markup that could forge the harness's own section tags
// neutralised) and, like a catalog of skills, is a snapshot taken at Load.
package agentdefs
