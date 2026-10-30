---
name: sleipnir-plugin
description: Make or change a Sleipnir plugin and load it live (sleipnir tool).
license: MIT
---

# Writing a Sleipnir plugin

A plugin is a directory. Sleipnir reads it when the session starts, so the loop is: write it, check it, install it, reload, try it.
This page is the whole contract: do not search for Sleipnir's source or documentation, and let `plugins op check` tell you what is
wrong.

## The loop

1. Write the plugin in a directory of its own: `$TMPDIR/<id>/` for a plugin of the person's, or `plugins/<id>/` inside the project
   when it belongs to the project's repository. Never write into `~/.sleipnir`: install copies it there.
2. `sleipnir` tool, action `plugins`, op `check`, source = that directory. Fix every error; read every warning.
3. Op `install` with the same source (and `replace: true` when you are updating a plugin that is already installed). Install
   turns it on in the person's own settings.
4. Action `reload` with `why` saying what it adds. The session starts again with this conversation when your turn ends, and you
   are told to go on: then check it is there (`sleipnir` action `help`, `run /skills`, or the command itself) and tell the person.

## The directory

```
<id>/
  plugin.json             the manifest (required)
  skills/<name>/SKILL.md  skills: front matter with name and description, then the instructions
  commands/<name>.md      slash commands: optional front matter (description, argument-hint), then the prompt; $ARGUMENTS is what follows the command
  agents/<name>.md        roles for the team: front matter (name, description, tools), then the role's instructions
  hooks.json              hooks: {"EventName": [{"matcher": "tool", "hooks": [{"type": "command", "command": "..."}]}]}
  mcp.json                tool servers: {"mcpServers": {"name": {"command": "...", "args": [...]}}}
  web/card.js             a card for the web page (optional web/card.css)
```

Only `plugin.json` is required; add only what the request needs. A skill is the cheapest way to teach the agents something; a slash
command gives the person a shortcut; a hook runs a command around tool calls or session events; a tool server gives the agents new
tools.

`plugin.json`:

```json
{
  "id": "standup",
  "api": 1,
  "title": "Standup",
  "description": "a /standup command that summarises yesterday's commits",
  "version": "1.0.0",
  "requires": ["commands"],
  "config": {"type": "object", "properties": {
    "since": {"type": "string", "title": "Since", "description": "how far back", "default": "yesterday"}
  }}
}
```

- `id`: lower-case letters, digits and `- _ . :`; not `core:`, `view:` or `role:`, and not a built-in plugin's id.
- `api`: the plugin API it is written for. Always write it: the current one is what `plugins op check` reports. Sleipnir reads
  every API version it supports the same way across updates, so a plugin that names its version keeps working when Sleipnir is
  updated; a plugin of a newer API than the installed Sleipnir is refused with a reason instead of half working.
- `version`: raise it on every change you install with `replace`.
- `requires`: plugin ids that must be on (`skills` for skills, `commands` for commands, `agents` for roles, `hooks` for hooks,
  `mcp` for tool servers).
- `config`: settings the Plugins page shows a form for (`string`, `number`, `integer`, `boolean`, `enum`).
- `needs`: leave it out unless the plugin runs a program the person may not have installed; check says what is wrong with one.

In `hooks.json` and `mcp.json`, `${SLEIPNIR_PLUGIN_ROOT}` is the plugin's installed directory and `${setting:NAME}` a setting's
value. Commands of a plugin are named `<id>:<name>` (`/standup:today`), skills `<id>:<name>`.

## Keep it working across updates

- Use only what this page describes. Fields the API does not know are ignored, and check warns about them.
- Scripts a hook or tool server runs live inside the plugin and are referenced through `${SLEIPNIR_PLUGIN_ROOT}`; pin the versions
  of anything they install.
- After a Sleipnir update, `plugins op list` shows each installed plugin's state and API; one in error says why.

## Safety

A hook or tool server runs commands on the person's machine in every later session. Write only what the request needs, say in your
reply what each hook runs, and never put a secret in a plugin's files: use a setting or an environment variable.
