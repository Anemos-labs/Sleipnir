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
| Tools | `fs`, `shell`, `web`, `recall`, `skill`, `memory`, `plan`, `mcp` |
| Extensions | `skills`, `builtin-skills`, `commands`, `agents`, `hooks`, `instructions` |
| Team | `swarm`, `mailman`, `auto-docs`, `manager-approves`, `push-progress`, `goal`, `delivery`, `recon`, `update-check`, and `role:architect` … `role:reviewer` (one per worker role) |
| Interface | `view:cockpit`, `view:workspace`, `view:cache`, `view:mail`, `view:board`, `view:replay`, `view:sessions`, `view:tools`, `view:plugins` |
| Core | `core:kernel`, `core:config`, `core:session-log`, `core:agent-loop`, `core:providers`, `core:web-shell`, `checkpoints` |

`sleipnir plugins list` prints each with its one-line description; `--json` gives the full catalogue (the same rows
`GET /api/plugins` returns, docs/WEB-API.md).

Turning `view:plugins` off hides the view from the rail, but a view that is off still opens a note with *Turn it on*, so
the page cannot lock you out. Turning `swarm` off makes every session a single agent; the roles and team plugins then
show as pending.

**Prompt bytes.** Tool plugins change the tool list sent to the model, and `skills`, `instructions` and `recon` change
the shared prompt layer. Both are part of the cache key, so switching one costs one cache write on the next turn; that
is why a change waits for the team to start again rather than landing mid-conversation.

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
- `requires`: plugin ids that must be on; otherwise the plugin is pending.
- `config`: the settings the Plugins page shows a form for (`string`, `number`, `integer`, `boolean`, and `enum` for a
  choice). `sleipnir plugins set hello greeting=Hi` writes one from the terminal.

In `hooks.json` and `mcp.json`, `${SLEIPNIR_PLUGIN_ROOT}` (or `${CLAUDE_PLUGIN_ROOT}`) is the plugin's directory and
`${setting:NAME}` is the value of the setting (or its default). A plugin's MCP servers are named `<id>-<server>`, and
its skills, commands and agents join yours with your own files winning a name collision.

A complete example is in [docs/examples/plugins/hello](examples/plugins/hello/).

Built-in plugins are Go: a manifest in `internal/plugin/builtin.go` and an activator registered with the kernel
(`plugin.NewKernel`), which adds what the plugin contributes and registers, with `Scope.OnDispose`, how to undo it.
