# MCP tool servers

Sleipnir is a client of the Model Context Protocol: it can connect to external tool servers (a local process over
stdio, or a remote server over streamable HTTP or the older HTTP+SSE transport) and give their tools to the model next
to the built-in ones. This page is what you need to use it; the package documentation (`internal/mcp/doc.go`) has the
protocol details and the threat model.

## Configure a server

Servers live under `"mcp"` in your **user** configuration, `~/.sleipnir/config.json`. A server is a name and an object:

```json
{
  "mcp": {
    "github": {
      "command": "github-mcp-server",
      "args": ["stdio"],
      "env": { "GITHUB_TOKEN": "${GITHUB_TOKEN}" }
    },
    "docs": {
      "url": "https://mcp.example.com/v1",
      "headers": { "Authorization": "Bearer ${DOCS_TOKEN}" },
      "allow_tools": ["search_*"]
    },
    "local-dev": {
      "url": "http://localhost:8931/mcp",
      "allow_private": true
    }
  }
}
```

| key | meaning |
|---|---|
| `command`, `args`, `env`, `cwd` | a local server: what to run. The child gets a minimal environment (`PATH`, `HOME`, locale, temp dir, certificate paths) plus what `env` lists; **none of Sleipnir's own API keys**. |
| `url`, `headers`, `type` | a remote server. `type` is inferred (`http` for a URL, `stdio` for a command) and may be `"sse"` for the legacy transport. |
| `allow_tools`, `deny_tools` | globs (`*`, `?`) on the server's own tool names or on the exposed `mcp__server__tool` names; deny beats allow. |
| `timeout`, `startup_timeout` | seconds (a number) or a Go duration (`"90s"`): per call, and for connect + handshake + first listing. |
| `max_output_chars` | lowers how much of a result the model sees (never raises the global limit). |
| `allow_private` | lets a remote server live on a private or loopback address and permits plain `http://`. Ignored in project files. |
| `disabled` | keep the entry, do not start it. |

`${VAR}` and `${VAR:-default}` in strings are expanded from your environment when the server starts. Other clients'
spellings are accepted (`mcpServers`, `includeTools`, `excludeTools`, `workingDirectory`, `serverUrl`, `transport`, ...);
options that would skip permission prompts (`autoApprove`, `alwaysAllow`) are ignored with a warning, because approval
here is decided per call by permission rules, never by the server entry. OAuth flows are not supported: pass a token in
`headers`.

A file in Claude Code's shape also works as a project file: `.mcp.json` at the repository root, `{"mcpServers": {...}}`.

## Who is allowed to start what

A server is a program that runs as you, or a host that receives your prompts and returns text that goes into every
agent's prompt. So where an entry came from decides whether it may start without asking:

* **Your own configuration** (`~/.sleipnir/config.json`) is trusted.
* **A repository's entries** (its `.sleipnir/config.json`, the local override, or `.mcp.json`) are read only when the
  project is trusted (`--trust-project`; without it they are not even looked at), and then **each one needs your
  approval** before it starts. The question shows what would run (the command line with its arguments, or the host),
  the names of the environment variables and headers it uses, and which variables of your environment it asks for.
  Answer `y` to start it this time, `p` to remember that exact entry for this project, `n` to leave it out. The
  approval is keyed by the entry's fingerprint, so a commit that changes the command asks again. Approvals are kept
  in your state directory (`~/.sleipnir/mcp-approvals.json`, owner-only), never in the repository.
* **Headless runs** (`sleipnir run`, CI) cannot ask, so an unapproved project server is left out with a notice that
  says how to approve it. Approve it once interactively, or with `sleipnir mcp approve NAME`, or move the entry into
  your user configuration.

## What the model sees

Tools are named `mcp__<server>__<tool>` (adjusted to the providers' `[a-zA-Z0-9_-]{1,64}` rule, with a short hash
when a name had to change, so two names can never collapse into one). Descriptions and schemas are cleaned of control
characters, terminal escapes and invisible Unicode, capped, and the whole set is held to a size budget shared between
servers; a tool whose description looks like an injection attempt is left out with a warning. A server's `instructions`
and any text in results are **data**, not instructions.

**The tool list is frozen for the session.** Every agent sends the same tools array, byte for byte, so the provider
caches it once for the whole swarm; changing it would rewrite the cached prefix of every agent. The session starts its
servers (waiting up to 20 seconds), takes a snapshot and registers exactly those tools. If a server later announces new
tools, or comes back from a crash with different ones, you are told once and the session keeps its list; a tool the
server no longer offers fails with a visible error instead of running something else. Start a new session to pick
changes up. `session.start` in the event log records how many tools MCP contributed and the hash of the list.

## Permissions

An MCP tool call is an action like any other, and it asks first (unless your mode or a rule allows it). Rules name the
exposed tool, and globs work:

```json
{ "permissions": { "allow": ["mcp__github__list_*", "mcp__docs__*"], "deny": ["mcp__github__delete_*"] } }
```

The request says whether the tool reaches the network, and treats it as writing unless the server marks it read-only
(a hint from the server can only make the question less alarming, never skip it). Plan mode and read-only swarm roles
refuse tools that write. What an MCP tool does to your files bypasses Sleipnir's write leases, staleness checks and
checkpoints, so permission rules are the only barrier around it. Sampling and elicitation requests from servers are
refused.

## Commands

```
sleipnir mcp list [--trust-project]      the servers a session here would consider, where each came from, whether it may start
sleipnir mcp approve NAME [--yes]        remember a project entry for this project (shows what it would run and asks first)
sleipnir mcp revoke NAME                 forget an approval
sleipnir mcp test [NAME...]              start the servers (no model involved) and list the tools they offer
sleipnir chat --no-mcp | sleipnir run --no-mcp     start no servers
```

In chat, `/mcp` shows each server's state and how many tools it contributed to this session, `/mcp reconnect NAME`
restarts one that gave up, and a server's **prompts** appear as slash commands (`/mcp__server__prompt`; arguments as
`name=value` or bare words filled in order). `sleipnir config --json` prints your configuration with MCP credentials
replaced by their names.

RL rollouts (`sleipnir rl ...`) never start MCP servers: an episode is closed and reproducible.

## Limits

* No OAuth, no MCP tasks, no resumable SSE streams, no resource subscriptions.
* A streamable-HTTP server can announce a change only on the GET stream the client opens just after the handshake;
  an announcement sent while it is down is lost. That can delay the `changed its tools` notice; it never changes what the
  model sees.
* Windows and macOS runtime behaviour of stdio servers (process groups, shutdown) has been built for but not exercised;
  the test suite runs on Linux, CI also runs macOS.
