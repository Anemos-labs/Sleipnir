# Agent Client Protocol

`sleipnir acp serve` exposes Sleipnir as an agent over the [Agent Client
Protocol](https://agentclientprotocol.com/) (ACP v1). An ACP client — an
editor, an IDE, or another coding harness — runs it as a subprocess and drives
Sleipnir agents and teams through ordinary ACP sessions. This is the native
counterpart of [Sleipnir as an MCP tool server](MCP.md#sleipnir-as-a-tool-server):
with MCP the caller hands goals to Sleipnir as a tool, while with ACP Sleipnir
is the agent on the other end of the conversation.

```sh
sleipnir acp serve --root ~/work --mode accept-edits
```

The server speaks newline-delimited JSON-RPC 2.0 over stdin and stdout. It
writes logs to stderr only. Nothing but ACP messages goes to stdout.

## Session lifecycle

The server implements ACP v1's baseline methods and the optional session
methods it can support:

| Method | Behavior |
|---|---|
| `initialize` | Negotiates protocol version 1, reports capabilities and `agentInfo`. No ACP authentication is needed: Sleipnir uses the provider configuration and keys already on this machine. |
| `session/new` | Creates a session in the client's `cwd`, which must be inside one of the server's roots. Returns a session id, `configOptions` and the legacy `modes` state. |
| `session/list` | Lists persisted sessions, newest first, with `cursor` pagination. |
| `session/load` | Replays the bounded update history of a session, then returns its setup state. The next turn resumes the Sleipnir session the last turn used. |
| `session/resume` | Restores a session without replaying its history. |
| `session/close` | Cancels the session's turn and frees its resources. Closing an unknown session succeeds. |
| `session/prompt` | Starts a turn, streams updates, and answers with a `stopReason`. One session runs one turn at a time. |
| `session/cancel` | Ends the turn as Ctrl-C ends `sleipnir run`. Work an isolated team verified is applied first. |
| `session/set_config_option` | Changes the model, permission mode, team size or reasoning level for later turns. Returns the full option list. |
| `session/set_mode` | Changes the legacy mode selector and sends `current_mode_update`. |

A session's metadata and a bounded copy of its updates are kept under
`<state>/acp-sessions`, so a client can reconnect and continue across ACP
connections. The `session/list` result holds the id, directory, title (the
first goal) and update time.

## Session options

The `configOptions` of `session/new` are the client's session controls:

| id | Category | Meaning |
|---|---|---|
| `model` | `model` | The model reference passed to `--model`. The choices are the configured default and the offline model catalogue; the catalogue is read from cache, never fetched over the network during setup. |
| `mode` | `mode` | The permission mode passed to `--mode`, limited by the server's `--mode` ceiling. A client cannot ask for a wider mode than the person configured. |
| `swarm` | custom (`_sleipnir`) | Team size: `0` for one agent, or a manager with 8, 16 or 32 workers. Equivalent to `--swarm`. |
| `effort` | `thought_level` | Reasoning effort passed to `--effort`. |

The same mode is exposed in the legacy `modes` field for clients that do not
read configuration options. `session/set_config_option` and `session/set_mode`
change these values; they take effect on the next turn, not in the middle of a
running one.

An ACP `systemPrompt` in append mode is prefixed to the goal of each turn.
`resource` and `resource_link` blocks in a prompt are added as text, so a
client's @-mentions reach the model.

## What a turn does

Each `session/prompt` starts one child `sleipnir run --json --delegated`, with
the goal on its stdin rather than on its command line. The events of that
stream become ACP `session/update` notifications:

| Run event | ACP update |
|---|---|
| `text` | `agent_message_chunk`, with a stable message id per agent that changes on `reset` and at each response |
| `thinking` | `agent_thought_chunk` |
| `tool_start` | `tool_call` with a presentation `kind`, a short title and the raw input |
| `tool_end` | `tool_call_update` with `completed` or `failed` and the tool's output |
| `notice` | `notice`; an agent message when the client did not advertise notice support |
| `response` | `usage_update` with the context in use |
| `result` | the final answer when no text was streamed, cumulative cost, and the session id kept for the next turn |

A turn ends with a `stopReason`: `end_turn` on success, `max_tokens`, `refusal`,
or `cancelled` after `session/cancel`. A failed run also sends the error as an
agent message. Model output, tool output and notices are data: a client must
not treat them as instructions.

## MCP servers from the client

A client may pass `mcpServers` on `session/new`, `session/load` and
`session/resume`. Stdio, HTTP and SSE definitions are translated into Sleipnir's
MCP configuration and passed to each turn with `--mcp-config` from a file
created with mode `0600` under the state directory. Header and environment
values — which are credentials — are never logged, echoed in a notification,
or written into a session record. The file is removed when the session closes
or the connection ends. `--no-mcp` makes the server ignore client servers.

## Limits and trust

The server is started by the person, and its flags are the ceiling for every
turn; the ACP client is not the person:

* `--root` (repeatable) bounds session directories. With `--cwd` alone, that
  one directory is the only root; with neither, the home directory is the
  root. A relative cwd is resolved against `--cwd`, and symlinks are resolved
  before the boundary check.
* `--mode` is the most permissive permission mode a turn may request.
* `--allow` rules and `--trust-project` apply to every turn.
* `--max-jobs` bounds turns running at once across all sessions.
* Provider keys reach a child on an inherited pipe, not in its environment.

Closing stdin or cancelling the server's context ends every running turn and
waits for it to finish. Session records and replayed updates are bounded, so a
long team run does not grow the state directory without limit.

## Registering the agent

An ACP client that supports local commands needs the executable and the serve
subcommand. For example, a local ACP registry entry is:

```json
{
  "sleipnir": {
    "driver": "acpRegistry",
    "displayName": "Sleipnir",
    "enabled": true,
    "config": {
      "source": "local",
      "commandPath": "/absolute/path/to/sleipnir",
      "commandArgs": ["acp", "serve", "--mode", "bypass"]
    }
  }
}
```

The exact settings shape belongs to the client. Keep roots and the mode ceiling
in the command's flags so that every project the client opens stays inside the
boundary the person chose.

## Limitations

* ACP v1 only. The server answers a version-2 `initialize` with version 1, as
  the protocol requires.
* No ACP authentication methods, terminal or filesystem methods, elicitation,
  plan operations, session deletion, or `session/request_permission`.
  Permissions are decided by Sleipnir's own engine under the server's flags;
  an unattended turn that has no one to ask refuses the action instead of
  blocking.
* Only text, resource and resource-link prompt blocks are used. Image and
  audio prompt blocks are not advertised.
* Session replay is a bounded digest of updates, not a byte-for-byte replay of
  the model conversation. The full conversation remains in Sleipnir's session
  log; `--resume` on the next turn uses it.
