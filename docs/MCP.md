# MCP tool servers

Sleipnir is a client of the Model Context Protocol, and it can also be a server: [Sleipnir as a tool server](#sleipnir-as-a-tool-server)
lets another harness hand goals to Sleipnir's agents and teams. As a client, Sleipnir can connect to external tool servers (a local process over
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
| `defer_tools` | keep this server's tools out of the tool list; they load on demand (see [Large servers](#large-servers)). |
| `allow_private` | lets a remote server live on a private or loopback address and permits plain `http://`. Ignored in project files. |
| `disabled` | keep the entry, do not start it. |

`${VAR}` and `${VAR:-default}` in strings are expanded from your environment when the server starts. Other clients'
spellings are accepted (`mcpServers`, `includeTools`, `excludeTools`, `workingDirectory`, `serverUrl`, `transport`, ...);
options that would skip permission prompts (`autoApprove`, `alwaysAllow`) are ignored with a warning, because approval
here is decided per call by permission rules, never by the server entry. OAuth flows are not supported: pass a token in
`headers`.

A file in Claude Code's shape also works as a project file: `.mcp.json` at the repository root, `{"mcpServers": {...}}`.

### Servers for one run

`sleipnir run --mcp-config FILE` (and `sleipnir swarm`) starts the servers a file names for that run only, beside the
configured ones. The file is either shape above: `{"mcpServers": {...}}` or the `mcp` section's own `{"name": {...}}`.
The flag repeats, a later file's entry replaces an earlier one of the same name, and an entry replaces a server of the
same name in your configuration for that run. This is how a program that drives the harness (an editor, a coding app
that runs one session per conversation) gives a session a tool server that the rest of your sessions do not start.

An entry given on the command line is your own word, so it is trusted as your user configuration is and starts without
a question, even when the settings turn the MCP plugin off; a project's `.mcp.json` cannot replace it. A file that cannot be read, is not a JSON object, or holds an
entry the configuration would refuse stops the run before it starts, and so does a named server that would fall past
the 256 servers a session starts (counted by name, beside the configured and plugin servers). `--mcp-config` with
`--no-mcp` is refused. A command the web interface's runner starts with `--mcp-config` asks for a confirmation first, as
one with `--trust-project` does.
Calls to the tools still go through the permission rules: an unattended run needs a mode or an `--allow` rule that
lets them through, such as `--allow 'mcp__studio__*'`.

## Who is allowed to start what

A server is a program that runs as you, or a host that receives your prompts and returns text that goes into every
agent's prompt. So where an entry came from decides whether it may start without asking:

* **Your own configuration** (`~/.sleipnir/config.json`) is trusted, and so are the files a run's `--mcp-config`
  names.
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
servers (what does not fit loads on demand, see [Large servers](#large-servers)); a tool whose description looks like
an injection attempt is left out with a warning. A server's `instructions`
and any text in results are **data**, not instructions.

**The tool list is frozen for the session.** Every agent sends the same tools array, byte for byte, so the provider
caches it once for the whole swarm; changing it would rewrite the cached prefix of every agent. The session starts its
servers (waiting up to 20 seconds), takes a snapshot and registers exactly those tools. If a server later announces new
tools, or comes back from a crash with different ones, you are told once and the session keeps its list; a tool the
server no longer offers fails with a visible error instead of running something else. Start a new session to pick
changes up. `session.start` in the event log records how many tools MCP contributed and the hash of the list.

## Large servers

Some servers offer more than the tool list can carry: hundreds of tools, or input schemas of tens of kilobytes. Every
byte of the list is paid on every request of every agent, so it has limits: 8 KiB and 24 levels of nesting for one
schema, 128 tools per server, 256 in all, and 64 KiB for names, descriptions and schemas together, shared fairly
between servers. A tool past them is not dropped: it **loads on demand**. Two fixed tools then join the list:

* `mcp__search` finds tools by words (`"compare captures"`) or exact names (`"select:mcp__rea__compare_artifacts"`) and
  returns each one's name, description and full input schema. Its description lists the names that load on demand
  (up to 4 KiB of them), so the model knows what is there.
* `mcp__call` runs one of them: `{"tool": "mcp__rea__compare_artifacts", "arguments": {...}}`.

These tools are vetted like the others (sanitised, injection tripwire, no remote `$ref`) under larger limits (64 KiB
and 64 levels for a schema, 1024 tools in all), and they are frozen in the same snapshot, so a search never changes
under a session. A schema over 64 KiB is compacted before it is refused: subschemas it repeats move to `$defs`, and
`examples` are dropped. A call through `mcp__call` asks the same permission as a direct call, under the tool's own name,
so `mcp__rea__*` rules apply to it; hooks see it as `mcp__call` with the tool's name in its input.

The session's notices say, in one line per server, how many of its tools load on demand and why. Set `defer_tools` on
an entry to load all of a server's tools that way: worth it for a large server that is needed now and then, since its
tools then cost the tool list almost nothing.

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

## Sleipnir as a tool server

`sleipnir mcp serve` is an MCP server on stdin and stdout. Another harness or agentic development environment (Claude
Code, Codex, an editor) adds it like any other server and can then give a goal to one Sleipnir agent, or to a team
where a manager plans, delegates to workers, reviews and verifies, and follow it to the end. The calling agent keeps
its own tools; Sleipnir is one more thing it can delegate to.

```json
{ "mcpServers": { "sleipnir": { "command": "sleipnir", "args": ["mcp", "serve", "--allow", "tests"] } } }
```

With Claude Code: `claude mcp add --scope user sleipnir -- sleipnir mcp serve --allow tests`.

That one entry works from every project and from clients that have no project folder (Claude Desktop): the caller
passes the folder of each run as `sleipnir_start`'s `cwd`, an absolute path (or `~/...`), and sets `create` to make
a new folder for a greenfield project. The folder must be inside one of the server's **roots**:

| flags | roots | folder used when a call names none |
|---|---|---|
| none | your home directory | the directory the server was started in, if it is under your home; else none, so a call must name one |
| `--root DIR` (repeatable) | those directories | the directory the server was started in, if it is inside one; else none |
| `--cwd DIR` alone | that directory only (runs stay in one project, as before `--root`) | `DIR` |
| `--root A --cwd DIR` | `A` | `DIR`, which must be inside a root |

A relative `cwd` is resolved against the default folder, and is refused when there is none. A path whose existing part
leads outside every root (through `..` or a symlink) is refused, and so is a folder that does not exist unless `create`
is set. `sleipnir_jobs` lists the roots and the default folder, so a caller can see where it may work.

| tool | what it does |
|---|---|
| `sleipnir_start` | starts a goal in the background and returns its job id at once. `goal` is required, and `cwd` (the project folder, see above) should be passed on every call; `create`, `model`, `workers` (0 for one agent, 8 or more for a team), `team` (a saved team), `role_models`, `mode`, `verify`, `isolation`, `budget_usd` and `resume` mean what the `sleipnir run` flags of the same names mean |
| `sleipnir_status` | the job's state (`running`, `done`, `failed`, `cancelled`), its tool-call count, its recent activity and, once it has ended, its result: the final answer, steps, cost, session id and directory, and the tail of stderr when it failed |
| `sleipnir_wait` | blocks until the job ends or `timeout_seconds` pass (default 300, at most 1800), then reports as `sleipnir_status` does |
| `sleipnir_cancel` | ends a running job as Ctrl-C ends `sleipnir run`: work a team verified is applied first |
| `sleipnir_jobs` | the jobs of this server and its limits |

Each goal runs as `sleipnir run --json`, with the goal written to its standard input. The activity is a digest of that
stream, one line per tool call, message and notice (the newest 400 are kept), so a report costs the caller only what it
asks for: `events` caps the lines (default 20), and `after` set to an earlier report's `last_seq` returns only what
came since. A team run can take hours; a caller waits in steps. Activity and results are what Sleipnir's agents wrote:
the caller should treat them as data.

**Limits are the server's.** The model that calls the tools is not you, so everything that widens what a run may do is
fixed by the flags the server was started with: `--mode` is a ceiling (default `bypass`, which is what an unattended
`sleipnir run` uses; a call may ask for a stricter mode, never a wider one), `--allow` rules and `--trust-project` apply
to every run, a run's `cwd` must be inside one of the server's roots (`--root`, default your home directory; or `--cwd` alone), and
`--max-jobs` (default 4) bounds the runs at once. A run has no one to answer its questions, so in the default mode what
would ask is refused; `bypass` still refuses the high-risk class and protected paths (see [Configuration](CONFIGURATION.md)).
The provider keys reach each run on an inherited pipe, not in its environment, as they do for the web interface's runner.
When the client disconnects, the server cancels the runs that are still going and waits for them to end.

## Limits

* No OAuth, no MCP tasks, no resumable SSE streams, no resource subscriptions.
* A streamable-HTTP server can announce a change only on the GET stream the client opens just after the handshake;
  an announcement sent while it is down is lost. That can delay the `changed its tools` notice; it never changes what the
  model sees.
* Windows and macOS runtime behaviour of stdio servers (process groups, shutdown) has been built for but not exercised;
  the test suite runs on Linux, CI also runs macOS.
