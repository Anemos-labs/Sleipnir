# Plugins

Everything Sleipnir does beyond a small core is a plugin you can switch on and off: each tool family, skills, custom
commands, hooks, instruction files, the team and each of its roles, standing goals, GitHub delivery, the project survey,
the update check, and every view of the web page. The core (the agent loop, the session log, the configuration, the
providers, the page's shell and the checkpoints every edit is recorded through) is listed too, with the reason it stays on.

The model comes from the dsh harness's design: a plugin is a manifest that says what it is, what it needs and what it
adds; a small kernel turns plugins on in dependency order and turns them off again by undoing exactly what each added.
Nothing outside the kernel decides whether a feature exists.

## Switching plugins

Three ways, all writing the same configuration key:

| Where | How |
|---|---|
| Terminal | `sleipnir plugins` lists them; `sleipnir plugins disable web`, `enable mailman`, `reset web`, `info goal` |
| Any chat | `/plugins` lists what is off, pending or changed; `/plugins disable web` |
| Web page | the **Plugins** view (`g u`): a switch per plugin, a profile picker, and *Just me* or *This project* |

A running session keeps the plugins it started with. A change applies when its team starts again (`/restart`, or *Apply*
on the Plugins page); until then the page shows the row as *applies on restart*.

```json
// ~/.sleipnir/config.json, .sleipnir/config.json or .sleipnir/config.local.json
{
  "plugins": {
    "profile": "standard",
    "enabled": {"web": false, "mailman": true},
    "settings": {"hello": {"greeting": "Hi"}}
  }
}
```

Writes are minimal: a choice equal to what the profile already gives removes the row instead of writing it, so the file
holds only what you changed. Switching a plugin on in the project's file needs the project to be trusted
(`--trust-project`); switching one off never does.

### Layers

Each plugin's state is decided by the highest layer that has a say, lowest first:

`default` < `profile` < `user` < `project` < `local` < `env` < `flag`

`sleipnir plugins info ID` and the Plugins page say which layer decided. Older settings keep working as layers of the
plugin that now owns them:

| Older setting or flag | Plugin |
|---|---|
| `swarm.mailman`, `--mailman` | `mailman` |
| `swarm.auto_docs`, `--auto-docs` | `auto-docs` |
| `swarm.push_progress` | `push-progress` |
| `permissions.manager_approves` | `manager-approves` |
| `tools.builtin_skills` | `builtin-skills` |
| `--no-web` | `web` |
| `--no-mcp` | `mcp` |
| `--no-recon` | `recon` |
| `--no-memory` | `lessons` (the `memory` tool stays in the list and answers that the memory is off) |

Writing a plugin's row removes the older setting from the same file, so the file holds one answer.

### Profiles

A profile is a starting point; your own choices sit on top of it.

| Profile | What it is |
|---|---|
| `standard` | everything Sleipnir does by default (the mailman is off) |
| `minimal` | one agent with the file, shell and plan tools, and the Workspace, Sessions, Tools and Plugins views: nothing else in the prompt |
| `team` | standard, with the mailman digesting worker mail |
| `headless` | standard without the web views, for scripts and CI |

`sleipnir plugins profile minimal`, or the picker on the Plugins page.

### States

| State | Meaning |
|---|---|
| `on` | running |
| `off` | switched off, or off by default |
| `pending` | switched on, but something it needs is off (`needs swarm, which is off`) |
| `error` | it failed to start; plugins that need it are pending. The session goes on without it |
| `restart` | the configuration changed and the running session has not taken it yet |

A slash command that belongs to a plugin that is off answers with the plugin and how to turn it on, instead of running.

## The built-in plugins

| Category | Plugins |
|---|---|
| Tools | `fs`, `shell`, `web`, `recall`, `skill`, `memory`, `plan`, `harness`, `mcp` |
| Extensions | `skills`, `builtin-skills`, `commands`, `agents`, `hooks`, `instructions` |
| Team | `swarm`, `mailman`, `auto-docs`, `manager-approves`, `push-progress`, `goal`, `lessons`, `delivery`, `recon`, `research`, `update-check`, and `role:architect` … `role:reviewer` (one per worker role) |
| Interface | `view:cockpit`, `view:workspace`, `view:cache`, `view:mail`, `view:board`, `view:replay`, `view:sessions`, `view:tools`, `view:plugins`, `view:memory` |
| Core | `core:kernel`, `core:config`, `core:session-log`, `core:agent-loop`, `core:providers`, `core:web-shell`, `checkpoints` |

`sleipnir plugins list` prints each with its one-line description; `--json` gives the full catalogue (the same rows
`GET /api/plugins` returns, docs/WEB-API.md).

Turning `view:plugins` off hides the view from the rail, but a view that is off still opens a note with *Turn it on*, so
the page cannot lock you out. Turning `swarm` off makes every session a single agent; the roles and team plugins then
show as pending.

**Prompt bytes.** Tool plugins change the tool list sent to the model, and `skills`, `instructions`, `recon` and
`research` change the shared prompt layer. Both are part of the cache key, so switching one costs one cache write on the next turn; that
is why a change waits for the team to start again rather than landing mid-conversation.

## Programs a plugin needs

Some plugins use programs or tool servers that are not part of Sleipnir: the built-in skills use Blender, uv, Node.js,
REA and the Blender and REA tool servers, and GitHub delivery uses the GitHub CLI. A plugin declares each one (a *need*)
with the program's name, where its installers usually put it, and the commands that install it. A need is found, in this
order, at a file you located, on `PATH`, or in one of its usual places; a tool server is found when your configuration
lists it. Nothing is run to find one.

For a need that is not found there are two ways out, on the Plugins page (under the plugin, *Programs it uses*) and in a
terminal:

```
sleipnir plugins needs                     # every need of the plugins that are on, found or not, with its install commands
sleipnir plugins locate blender /opt/blender-4.5/blender
sleipnir plugins setup blender-mcp addon   # shows the command and asks before it runs
sleipnir plugins forget blender
```

- **Locate** takes a program you installed yourself. The file must be an executable (on macOS an `.app` bundle is
  followed to its program) and its version command must succeed. It is remembered in `~/.sleipnir/needs.json`.
- **Install** runs one of the need's declared install methods for this operating system (`brew`, `winget`, `flatpak`,
  `snap`, `npm`, `uvx` ...), as the exact command shown, never through a shell, and only when you ask for that method: the page
  shows the command and needs a confirmation, the terminal asks unless you pass `--yes`. A method whose own program is
  missing says which (`needs Node.js first`). A tool server's method then adds the server to `mcp` in your
  `~/.sleipnir/config.json`, pinned to the version it installed; it starts with your next session.

A program found outside `PATH` (located, or in its usual place) gets a small launcher in `~/.sleipnir/bin`, which every
shell command of a session has at the front of its `PATH`, so agents call it by name (`blender --background ...`); a
program located or installed while a session runs is on that session's `PATH` at once. A
plugin whose *required* need is missing says so on its row; optional ones only limit what it can do.

A plugin that a flag of the running session decided (`--mailman`, `--no-web`) shows the session's state and the `flag`
layer on the Plugins page: a restart parses the same flags again, so a file's row alone does not change it.

## Writing a plugin

An external plugin is a directory. Install one with `sleipnir plugins add <dir or git URL>` (it goes to
`~/.sleipnir/plugins/<id>/`) and turn it on: installed plugins are off until you do. A trusted project may also carry
plugins in `<project>/.sleipnir/plugins/<id>/`.

```
hello/
  plugin.json             the manifest (or .claude-plugin/plugin.json, the Claude Code layout)
  skills/<name>/SKILL.md  skills, as in docs/EXTENDING.md
  commands/<name>.md      custom slash commands
  agents/<name>.md        agent (role) definitions
  hooks.json              hooks, in the shape of the "hooks" key (or hooks/hooks.json)
  mcp.json                MCP servers: {"mcpServers": {...}} (or .mcp.json)
```

`plugin.json`:

```json
{
  "id": "hello",
  "title": "Hello",
  "description": "an example plugin: a skill, a command and a hook",
  "version": "1.0.0",
  "api": 1,
  "requires": ["skills"],
  "config": {
    "type": "object",
    "properties": {
      "greeting": {"type": "string", "title": "Greeting", "description": "what the session-start hook says", "default": "Hello"}
    }
  }
}
```

- `id` (or `name`): lower-case letters, digits and `- _ . :`, at most 64 characters. `core:`, `view:` and `role:` are
  reserved, and a built-in id cannot be taken.
- `api`: the plugin API the plugin is written for ([below](#updates-and-the-plugin-api)); 1 when it is left out.
- `requires`: plugin ids that must be on; otherwise the plugin is pending.
- `config`: the settings the Plugins page shows a form for (`string`, `number`, `integer`, `boolean`, and `enum` for a
  choice). `sleipnir plugins set hello greeting=Hi` writes one from the terminal.
- `needs`: the programs and tool servers the plugin uses ([above](#programs-a-plugin-needs)), each
  `{"id", "title", "description", "homepage", "required", "commands": ["prog", "sibling"], "paths": {"darwin"|"linux"|"windows"|"unix": ["~/bin/prog", "/opt/prog-*/prog"]},
  "version_args": ["--version"], "mcp": {"name", "command", "args"}, "install": [{"id", "title", "os": ["darwin"], "env": {"NAME": ["cmd", "arg"]}, "setup": [["cmd", "arg"]], "run": ["brew", "install", "prog"], "connect": bool, "note"}]}`. `setup` are commands run in order before `run`, such as adding the package source it installs from; each must be safe to run again (`flatpak remote-add --if-not-exists`). `env` gives the method's commands environment variables, each read from the last `NAME=value` line its command prints; one already set in the environment is kept and its command not run.
  A need has `commands` (a program) or `mcp` (a tool server); `paths` expand `~` and `${VAR}` and match `*`; a method
  with `connect` adds the need's `mcp` entry to the user's configuration after its `run` (which may be empty). Pin versions
  in `run` and `args`.

In `hooks.json` and `mcp.json`, `${SLEIPNIR_PLUGIN_ROOT}` (or `${CLAUDE_PLUGIN_ROOT}`) is the plugin's directory and
`${setting:NAME}` is the value of the setting (or its default). A plugin's MCP servers are named `<id>-<server>`, and
its skills, commands and agents join yours with your own files winning a name collision.

A complete example is in [docs/examples/plugins/hello](examples/plugins/hello/).

## Updates and the plugin API

The plugin API is everything above: the manifest's fields, the directory's layout, the placeholders and the built-in plugin ids
a plugin may require. It has a version, and a plugin says which one it was written for with `"api"`. Sleipnir reads every
version from `plugin.MinAPIVersion` to `plugin.APIVersion` (today, 1 to 1) the same way in every release:

- A change that would make a plugin of a supported version load differently gets a new API version; the older one keeps its
  behaviour. `internal/plugin/testdata/contract/v<N>/` holds plugins that use every part of version N with golden files of what
  Sleipnir reads from them, and `TestPluginContract` (with a session-level twin in `internal/session`) fails such a change. The
  documented example plugin is held to the contract too.
- A built-in plugin that is renamed keeps its old id working in `requires` (`plugin.RenamedPlugins`).
- A plugin written for a newer API than the installed Sleipnir does not start: it is in error with "written for plugin API N;
  this Sleipnir reads API M and older: update Sleipnir", and the plugins that need it are pending, rather than half working.
- Fields the API does not know are ignored when a plugin loads, so a plugin may carry the fields of a newer API.

Before installing, `plugins check` of the `sleipnir` tool (below) reads a plugin the way Sleipnir will and reports errors (a
manifest that does not load, an API this Sleipnir does not read, a `requires` that names no plugin, a setting of an unknown type,
a hooks or MCP file that does not parse, a skill without a description) and warnings (unknown fields, no `api`).

## Plugins the agents write

The `sleipnir` tool (plugin `harness`, on by default) lets the agent the person talks to (a single agent, or a team's manager)
do what the person would do with slash commands, the Plugins page and the schedule, when the person asks for it:

| Action | What it does |
|---|---|
| `help` | what the harness can do in this session, and the plugin changes a reload would apply |
| `run` | a slash command: `/scan`, `/research`, `/goal`, `/delivery`, `/jobs stop ID` run when the agent's turn ends, as a line the person typed ahead; `/status`, `/cost`, `/skills`, `/mcp`, `/jobs`, `/agents` answer at once |
| `schedule` | `list`, `add` (cron, goal, and optionally model, mode, budget and folder), `pause`, `resume`, `remove`: the jobs of `sleipnir schedule` |
| `plugins` | `list`, `check`, `install` (a directory or git URL; `replace` installs a new version over the old), `enable`, `disable`, `reset`, `remove` |
| `reload` | the session starts again when the turn ends, with its conversation, so plugin and configuration changes apply |

The built-in skill `sleipnir-plugin` gives the agent the loop: write the plugin in its own directory, check it, install it,
reload, try it. On the web page and in the terminal chat the reload starts the session again and sends the agent on, naming any
plugin that did not start. Every change is a change of state to the permission
engine: it asks in the default mode, passes in bypass and yolo, and is refused in plan mode. The permission mode and rules,
models and roles, the team's shape, the budget, sign-in, project trust and starting the conversation over stay the person's. A
scheduled job the tool adds runs in `default`, `accept-edits` or `plan` mode, never bypass or yolo. A headless run has no chat,
so `run` and `reload` are refused there.

Built-in plugins are Go: a manifest in `internal/plugin/builtin.go` and an activator registered with the kernel
(`plugin.NewKernel`), which adds what the plugin contributes and registers, with `Scope.OnDispose`, how to undo it.
