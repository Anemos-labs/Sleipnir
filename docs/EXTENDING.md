# Extending Sleipnir

Six things shape how Sleipnir behaves in your project without touching Go: instruction files, skills, custom slash
commands, agent (role) definitions, hooks, and the session machinery (resume, compaction) that you steer from the
command line. This page describes each as the binary implements it today. Settings and keys are in
`docs/CONFIGURATION.md`; commands and flags are in `docs/CLI.md`.

MCP servers: see the MCP section (coming with the MCP integration).

## 0. The rules every extension follows

**Where files live.** Skills, commands and agent definitions are looked for in four directories, in this order; the
first definition of a name wins and a collision is reported as a warning (the same directory reached twice, say one
brand symlinked to the other, is not a collision):

| Order | Directory | Trust |
|---|---|---|
| 1 | `<project>/.sleipnir/<kind>/` | repository: read only with `--trust-project` |
| 2 | `<project>/.claude/<kind>/` | repository: read only with `--trust-project` |
| 3 | `~/.sleipnir/<kind>/` | yours: always read |
| 4 | `~/.claude/<kind>/` | yours: always read |

`<kind>` is `skills`, `commands` or `agents`. The `.claude` directories exist so files written for Claude Code work
unchanged; `.sleipnir` wins when both have the same name. Instruction files have their own names (section 1), and hooks
live in the `hooks` key of a Sleipnir config file, not in `.claude/settings.json` (that file is never read).

**Trust.** Anything that arrives with a repository is untrusted until you pass `--trust-project` (`chat`, `run`,
`swarm`). Without it Sleipnir reads only your own files. Skills, commands and agent directories that exist in the
repository are named in a start-up warning (`skills: .sleipnir/skills: not loaded: it is part of the repository and the
project is not trusted`; agent directories are only looked at in swarm sessions); project instruction files are skipped
**without any message**. Trust decides who wrote a
file, not what it may do: a trusted skill or command cannot grant itself a permission (`allowed-tools` is parsed and
applied nowhere), and everything it makes the agent do goes through the permission engine.

**Snapshots.** Everything is discovered once, when a session starts. An agent that edits a skill mid-session changes
nothing until the next session, and writes into these directories always ask (section 8).

**Cleaning.** Text that will reach the model is cleaned first: hidden Unicode (tag characters, bidirectional controls,
zero-width runs) is removed because no editor shows it but a model reads it, and HTML comments are removed from skill,
command and agent bodies and from instruction files. A warning says when removed characters had no innocent
explanation.

**Warnings.** Problems in skills, commands, agent definitions and hooks are printed at start-up (`[] warn: ...`) and
recorded in the session log. Problems in instruction files are recorded in the log only (section 1).

**The cache.** Instruction files and the skills listing are part of the shared prompt layer, identical bytes for every
agent and read on every request. Changing them changes the shared layer: the next session (or a resumed one) pays one
cache re-write. That is cheap once and expensive if you edit them constantly; keep them short and stable.

## 1. Instruction files

Plain Markdown that tells the agent how your project works: build and test commands, conventions, things to avoid.
`sleipnir init` writes a starter `AGENTS.md`.

**Which files.** All that exist are included, in this order (later text is more specific and is read last):

1. `~/.sleipnir/SLEIPNIR.md` (scope `user`)
2. for each directory from the project root down to the working directory: `AGENTS.md`, `CLAUDE.md`, `SLEIPNIR.md`,
   `.sleipnir/SLEIPNIR.md` (scope `project` at the root, `dir` below it)
3. for each of those directories: `SLEIPNIR.local.md`, `.sleipnir/SLEIPNIR.local.md` (scope `local`, for personal notes;
   keep them out of git)

`AGENTS.md` and `CLAUDE.md` are both read; if one is a symlink to the other it is read once. `~/.claude/CLAUDE.md` and a
`~/AGENTS.md` are not read.

**Imports.** A line that consists only of `@path` (no spaces) imports another file: relative to the importing file, or
`~/...` from your own `~/.sleipnir/SLEIPNIR.md`. Only `.md`, `.markdown`, `.mdx` and `.txt` files are imported, never
through a hidden directory other than `.sleipnir`, `.claude`, `.github` and `.agents`, never from outside the project
root (for project files) and never from your home directory (for project files). Imports are followed five levels
deep, each file is included once, an imported file appears right after its importer, and lines inside code fences or
indented code are examples, not imports. A refused import is reported and left out.

**Cleaning and size.** BOM removed, line endings unified, HTML comments and hidden characters removed. One file is cut
at 64 KB and all files together at 256 KB (at a line boundary, with a note). Then the whole instruction block, as one
piece, is fitted to about **3,000 tokens (roughly 10 KB)**: it is cut at a line boundary from the **end** and closed
with the line `(survey truncated)`. The end is where the most specific text sits (subdirectory files, then
`SLEIPNIR.local.md`), so with a large `AGENTS.md` your local notes are the first thing to disappear, silently. Put the
important rules first and keep the total under 10 KB. `/recon` prints exactly what the shared layer holds.

**How it is rendered.** In the first user message of every agent, under `<shared-context>`:

```text
## instructions
### ~/.sleipnir/SLEIPNIR.md (user)
I prefer short answers. Never touch the vendor/ directory.

### AGENTS.md (project, unverified)
# Project rules
...
### docs/style.md (import, unverified)
Use tabs. Keep functions short.

### SLEIPNIR.local.md (local, unverified)
My local note: use the staging database.
```

**Why `unverified`.** Anyone who can get a repository cloned can plant an `AGENTS.md` in it, and the model would
otherwise read its text as something you said. Every source that comes out of the repository (scopes `project`, `dir`,
`local` and everything imported from them) is labelled `(scope, unverified)`; your own file is labelled `(user)` and
carries no marker. The text is still put in front of the model (as user-role context, never as system text), so the
label is a signal about who wrote it, not a filter. The loader also refuses symlinks that lead out of the project
root or to something that could not have been imported, so `AGENTS.md -> .env` does not work.

**What `--trust-project` changes.** Without it the project's files are not loaded at all: the agent sees only
`~/.sleipnir/SLEIPNIR.md`, and nothing tells you that `AGENTS.md` was skipped. With it, every file above is loaded and
the repository's ones are labelled `unverified`. Since `sleipnir init` creates an `AGENTS.md` and says Sleipnir pins it,
note that the pinning happens only in trusted runs.

**When something is wrong.** Unreadable or refused files, removed hidden characters and refused imports never stop a
run and are not printed. They are recorded as a `notice` event in the session's `events.jsonl`, for example
`instruction files: memory: AGENTS.md: removed 1 hidden characters ...; import @../outside.md refused: outside the
project root`.

## 2. Skills

A skill is reusable instructions for a kind of task ("run the deploy checklist", "write a migration"): a directory with
a `SKILL.md` and, optionally, supporting files. Only a one-line listing sits in the prompt; the body is loaded when
it is needed.

```text
.sleipnir/skills/deploy/
├── SKILL.md
└── checklist.md          supporting file, read on demand
```

`SKILL.md` starts with frontmatter (a strict YAML subset, or a leading JSON object) and continues with the body:

```markdown
---
name: deploy
description: Run the deploy checklist. Use when the user asks to ship or release.
when_to_use: after the tests pass
argument-hint: [environment]
---
# Deploy

Deploy to $1 (all arguments: $ARGUMENTS).

Skill directory: ${SLEIPNIR_SKILL_DIR}. Follow checklist.md.
```

**Frontmatter keys.** Spelling does not matter (`allowed-tools`, `allowed_tools` and `allowedTools` are one key); two
keys that differ only in spelling are an error.

| Key | Type | Effect |
|---|---|---|
| `name` | string | The skill's name; default the directory name. Starts with a letter or digit, then letters, digits, `-`, `.`, `_`; at most 64 characters. A name that differs from the directory name warns; the name wins |
| `description` | string | What the skill does and when to use it. The only text always in the prompt. One line, at most 1,024 characters (cut with a warning); default: the first paragraph of the body |
| `when_to_use` | string | Extra trigger text, listed after the description |
| `disable-model-invocation` | bool | `true`: the model is not told the skill exists and cannot load it; only you can (`/name`) |
| `user-invocable` | bool | Default `true`. `false`: only the model can use it; `/name` refuses |
| `argument-hint` | string | Parsed and kept; not displayed anywhere yet |
| `allowed-tools` | list or string | Parsed and validated; **not applied**: a skill cannot pre-approve tools |
| `model` | string | Parsed; **not applied** yet |
| `context` | `fork` | Parsed; **not applied** yet (it would run the skill in a subagent) |
| `agent` | string | Parsed; **not applied** yet |

Also accepted without a warning and ignored: `license`, `compatibility`, `metadata`, `version`, `effort`, `paths`,
`shell`, `disallowed-tools`, `arguments`, `background`, `author`, `tags`, `category`. `hooks` warns that it is not
supported; any other key warns `unknown frontmatter field(s) ignored`. Booleans accept `true/false/yes/no/on/off`.

**Body.** `$ARGUMENTS` is everything the caller typed, `$ARGUMENTS[N]` the Nth word counting from 0, `$1` to `$9` the
Nth word counting from 1 (words are split like a shell: quotes group). A missing word is empty; `$$1` keeps a literal
`$1`. `${SLEIPNIR_SKILL_DIR}` (also `${CLAUDE_SKILL_DIR}`) is the skill's absolute directory. If the body has no
placeholder and arguments were given, they are appended as `ARGUMENTS: ...`. Substituted text is never re-interpreted.

**Limits.** `SKILL.md` is read up to 64 KiB (longer is cut, with a warning); at most 256 skills; a directory without a
`SKILL.md` and a stray file are skipped. A `SKILL.md` or supporting file whose real path leaves the skill directory
(a symlink to `~/.ssh/...`) is refused, and so is a project skills directory that resolves outside the project.

**How the listing enters the shared layer.** After the instructions, inside `<shared-context>`:

```text
## skills
<skills>
Load one with the skill tool when its description matches the task.
deploy: Run the deploy checklist. Use when the user asks to ship or release. after the tests pass
</skills>
```

One line per skill, `name: description when_to_use`, in name order. The listing has a budget of 1,500 tokens (about
27 skills with full descriptions): when it does not fit, descriptions are shortened for all skills together
(240, 160, 110, 80, 56, 40, 24 characters, then names only), and if even names do not fit the least relevant skills
(user skills before project skills, later names first) are dropped and a last line says how many. Skills with
`disable-model-invocation` are not listed. The listing is a pure function of the files, so it is byte-stable across
runs and safe to cache.

**How a skill is loaded on demand.** The model calls the `skill` tool with `name` and optional `args`. The result is
the header `Base directory for this skill: <absolute dir>`, the body with arguments substituted, and the list of
supporting files (up to 200, four levels deep). The model then reads supporting files with its normal `read` tool,
under the normal permission rules. The tool is registered in every session, whether or not skills exist, so the tool
list never depends on the project. Asking for a skill that does not exist or is model-hidden gives
`unknown skill "x"; available skills: ...`.

Supporting files of a **user-level** skill live outside the workspace, so in `default` mode reading them asks (and is
refused when unattended). Allow the directory once:

```json
"permissions": { "allow": ["Read(~/.sleipnir/skills/**)"] }
```

**You invoke a skill.** In `chat`, `/deploy prod` loads the skill with `prod` as its arguments and sends the result as
your message. `/skills` lists every skill (`(you only)` marks the model-hidden ones). If a custom command and a skill
share a name, the command wins.

## 3. Custom slash commands

A command is a Markdown file whose body is a prompt template: `<commands>/<name>.md`. They exist in `chat` only.

```markdown
---
description: Fix a numbered issue
argument-hint: [issue-number]
---
Fix issue #$1 in this repository.

Recent history:
!`git log --oneline -5`

Read @docs/style.md first, then follow $ARGUMENTS.
```

`/fix-issue 42 "the flaky test"` expands (in one pass) to:

```text
Fix issue #42 in this repository.

Recent history:
<output of the command>

Read @docs/style.md first, then follow 42 "the flaky test".

Referenced files (contents inserted by the harness):

Contents of docs/style.md:
...
```

**Frontmatter.** `description` (default: the first line of the body; shown by `/help`), and `argument-hint`,
`allowed-tools` and `model`, which are parsed and validated but **not applied** (a command runs on the current model and
grants no permission). `name`, `disable-model-invocation`, `user-invocable`, `version`, `license`, `metadata`,
`effort`, `argument-hint-long`, `arguments` and `when_to_use` are accepted and ignored; `hooks` warns.

**Names and namespaces.** The name is the file name without `.md`; each directory level below `commands/` adds a
`dir:` prefix, up to three deep (`frontend/lint.md` is `/frontend:lint`). Names start with a letter or digit and
continue with letters, digits, `-`, `.`, `_` (64 characters). A command whose name is reserved for a built-in is
skipped with a warning, and a duplicate name is shadowed by the first (section 0). Reserved, ignoring case: `agents`,
`clear`, `compact`, `config`, `context`, `cost`, `diff`, `doctor`, `exit`, `help`, `hooks`, `init`, `login`, `logout`,
`mcp`, `memory`, `mode`, `model`, `permissions`, `plan`, `quit`, `recon`, `resume`, `rewind`, `skill`, `skills`,
`status`, `trust`, `usage` (some are reserved but not implemented yet).

**Arguments.** Same placeholders as skills (`$ARGUMENTS`, `$ARGUMENTS[N]`, `$1`..`$9`, `$$1` for a literal). Without a
placeholder the arguments are appended as `ARGUMENTS: ...`. Arguments are text: an argument that itself contains `$1`
or `@file` is inserted verbatim and never expanded.

**`@path` includes.** A token that starts with `@` at the start of a line or after a space, quote or bracket, and names
a file, appends that file's contents after the prompt in a fenced block (the sentence that mentions the file still reads
as written). The file must be inside the project root (symlinks resolved) and be text; at most 8 files per expansion,
each cut at 32 KiB with a notice (hidden Unicode is removed; HTML comments are kept). A file that cannot be included is
skipped, the mention stays as plain text, and you see a notice:

```text
note: @.env: not included: it is an environment file that may hold secrets
note: @/etc/hostname: not included: it is outside the project
note: @missing.md: not included: no such file
```

Two layers judge a file: a small built-in guard (credential files, private keys, `.git/`, `.env` files other than
`.example`/`.sample`/`.template`) and the permission engine's own `read` check, so your `Read(...)` deny rules and the
built-in protections apply (`@.env.example` is refused by the engine even though the guard allows it). `@alice` in
prose stays text without a notice.

**`!`command`` runs a shell command** and inserts its output. The command goes through the permission engine like any
`bash` call by the agent, and through the same scrubbed environment (credential-looking variables removed); it does not
inherit a special allowance from the command file. In an unattended or refusing setting the **whole command fails**
and nothing is sent:

```text
/danger: command /danger: the shell command "touch danger-file" failed: permission denied: approval required: default mode: touch danger-file modifies files; approve it, or use accept-edits mode to allow this inside the workspace
```

Read-only commands (`git log`, `echo`, `ls`) pass in `default` mode. At most 8 commands per expansion, 30 seconds
each, 16 KiB of output each (longer is cut with a marker). Arguments substituted into a `!` command are single-quoted
word by word, so an argument cannot add an operator to it.

**Literal regions.** Fenced code blocks are left alone: no include and no shell command inside them, so a command that
documents this syntax does not run it (arguments are still substituted). Included files and command output are inserted
as data and never scanned for placeholders. The expanded prompt is limited to 256 KiB, a command file to 64 KiB.

## 4. Role (agent) definitions

Definitions add roles to a swarm: `<agents>/<name>.md`, with frontmatter and a body that becomes the role's
instructions (its "role pin"). They are read **only in swarm sessions** (`sleipnir swarm N ...`, `run --swarm N`,
`chat --swarm N`). The manager is told which project roles exist and can `spawn` them like the built-in ones
(`backend`, `frontend`, `fullstack`, `tester`, `reviewer`, `scout`, `docs`).

```markdown
---
name: security-reviewer
description: Reviews diffs for injection, authz and secret-handling problems.
tools: Read, Grep, Glob, Bash(go vet:*)
disallowedTools: WebFetch
model: heimdall/deepseek/deepseek-v4.1-flash
permissionMode: plan
maxTurns: 40
priority: 1
short: sr
---
You are a security reviewer. Read the diff and the code it touches. Report injection, authz and
secret-handling problems with file:line and a concrete fix. Do not modify files.
```

| Key | Type | Effect |
|---|---|---|
| `name` | string | Role name; default the file name. Lower-case letters, digits, `-`, `_`; at most 40 characters |
| `description` | string | One line (200 characters) shown to the manager; default: the first line of the body |
| `tools` | list or comma string | Allowlist of tools; `*` means no restriction. Enforced at run time, see below |
| `disallowedTools` | list of rules | Tool rules the role may never use (`WebFetch`, `Bash(rm:*)`) |
| `model` | string | `provider/model` this role runs on (`inherit` or empty: the session's model). Applied; `--role-model role=model` overrides it |
| `permissionMode` | string | `default`, `plan`, `accept-edits`/`acceptEdits`. Values that would loosen the session (`bypassPermissions`, `dontAsk`, `auto`) are ignored with a warning |
| `maxTurns` | integer | Step budget of one assignment, 1 to 1,000 (default 150) |
| `priority` | integer | Order of this role's requests under contention: 0 interactive, 1 worker (default), 2 background |
| `short` | string | Prefix of the agent ids (`sr` gives `sr-1`, `sr-2`), 2 to 4 lower-case letters or digits starting with a letter; derived from the name when absent or already used |
| `readonly` | bool | Sleipnir addition: the role may not write files or run mutating commands |
| `skills` | list | Informational |

Accepted and ignored without a warning: `mcpServers`, `memory`, `background`, `omitClaudeMd`, `effort`, `isolation`,
`color`, `initialPrompt`, `experimental`, `kind`, `temperature`, `timeout_mins`, `version`, `license`. `hooks` warns.

**Tool restrictions are run-time permissions, never hidden tools.** Every agent of a swarm, whatever its role, sends
the same tool list, so the provider caches the tool schemas once for the whole team. A definition restricts its role
through a permission profile the engine applies on top of the session's: a role can only be tightened, never loosened.

* `readonly: true`, `permissionMode: plan` or an allowlist made only of read-only tools (`Read`, `Grep`, `Glob`, `LS`,
  `WebFetch`, `WebSearch`, `Skill`, ...) makes the role read-only: plan mode, and it does not count against the
  swarm's writer cap. Any unknown or shell/edit/MCP/delegation tool in the list makes it a writer.
* An allowlist denies every class it leaves out: `Bash`, `Edit`, `WebFetch`/`WebSearch`, MCP tools. A pattern inside
  an entry (`Bash(go vet:*)`) is not enforced: the entry counts as the whole tool.
* `disallowedTools` entries are denied as written.

Checked with a definition like the one above (on a stand-in model): the reviewer's `touch worker-file` was refused
(`role security-reviewer: denied by rule Edit`), a `write` was refused (`the security-reviewer role is read-only:
report findings instead of changing files`), `go vet ./...` ran, its requests went to the model named in `model:`, and
every agent of the swarm sent the identical tool list.

**Model per role.** Set `model:` in the definition, or pass `--role-model role=provider/model` (repeatable, works for
built-in roles too). `models.roles` in a config file is validated but not used yet.

**Limits and reserved names.** The instructions cost their size on every request of every agent of the role: over 800
tokens loads with a warning that says so, over 4,000 tokens is refused. At most 128 definitions, 64 KB each. A
definition may not take the name or short id of a built-in role (`manager`/`mgr`, `backend`/`be`, `frontend`/`fe`,
`fullstack`/`fs`, `tester`/`ts`, `reviewer`/`rv`, `scout`/`sc`, `docs`/`dc`), so a repository cannot replace the
reviewer.

## 5. Hooks

Hooks run your commands at points of an agent's life: veto a tool call, rewrite its input, add context, format files
after an edit, or refuse to let the agent finish. The format is Claude Code's, so existing hooks can move over; the
location is Sleipnir's own: the `hooks` key of `~/.sleipnir/config.json` (or a project file, which only runs with
`--trust-project`).

```jsonc
"hooks": {
  "PreToolUse": [
    { "matcher": "Bash",
      "hooks": [ { "type": "command", "command": "~/.sleipnir/hooks/block-dangerous.sh", "timeout": 5 } ] }
  ],
  "PostToolUse": [
    { "matcher": "Edit|Write",
      "hooks": [ { "type": "command", "command": "~/.sleipnir/hooks/format.sh" } ] }
  ],
  "Stop": [
    { "hooks": [ { "type": "command", "command": "~/.sleipnir/hooks/stop-checks.sh", "timeout": 120 } ] }
  ]
}
```

Each event holds matcher groups; a group has an optional `matcher` and a list of `hooks`. A hook has `type`
(`command`; `http` is parsed but never runs, see below), `command`, `timeout` in seconds, `if`, and `failClosed`.
Mistakes are errors that name the event and index (`hooks: PreToolUse[1].hooks[0]: command is required`) and disable
hooks for the session with a notice; the session still starts. Event names may be spelled in any case with or without
`_`/`-` (`pre_tool_use`, `pretool`); short aliases exist for some (`promptsubmit`, `posttool`, `toolfailure`,
`permission`, `notify`, `agentstart`, `agentstop`).

### Events

| Event | Fires | Matcher is tried against | Extra fields on stdin |
|---|---|---|---|
| `SessionStart` | before the first goal of a session, also when resuming (`source` is always `startup`) | `source` | `source` |
| `UserPromptSubmit` | for every goal: the `run` prompt, each `chat` line, an expanded slash command | (none: all hooks run) | `prompt` |
| `PreToolUse` | before each tool call, before the permission check | tool name | `tool_name`, `tool_input` |
| `PermissionRequest` | when the permission engine would ask a human | tool name | `summary`, `command`, `paths` |
| `PostToolUse` | after a tool call that succeeded | tool name | `tool_response` |
| `PostToolUseFailure` | after a tool call that returned an error | tool name | `tool_response` |
| `Stop` | when an agent is about to end its turn with a final answer | (none) | `stop_hook_active` |
| `PreCompact` | before a **manual** `/compact` | `trigger` (`manual`) | `trigger` |
| `PostCompact` | after a manual compaction | `trigger` | `trigger` |
| `SessionEnd` | when a session that ran at least one goal closes; `reason` is `other` | `reason` | `reason` |

`Notification`, `SubagentStart` and `SubagentStop` are accepted in a config file but **never fired** in this version.
Automatic (planner-triggered) compactions fire no hooks. Events that exist only in Claude Code (`Setup`,
`InstructionsLoaded`, `PermissionDenied`, `TaskCreated`, `FileChanged`, ...) are ignored with a warning, so one file can
serve both tools. Hooks also run for a swarm's workers; `agent_id`, `agent_type` and `role` say which one.

### Matchers

A matcher selects which tool (or `source`, `reason`, `trigger`) a group applies to:

* empty or `*`: everything;
* letters, digits, `_` and `|` only: an exact list of names, ignoring case and `_`/`-`: `Bash|Edit`, `WebFetch`
  (matches `web_fetch`);
* anything else: a regular expression (Go RE2, unanchored, at most 512 characters): `^Notebook`, `mcp__memory__.*`.

Hooks are written for Claude Code's tool names, so a tool answers to those too:

| Tool | Name a hook sees in `tool_name` | Also matched by |
|---|---|---|
| `bash` | `Bash` | |
| `bash_output`, `bash_kill` | `BashOutput`, `KillShell` | `KillBash` |
| `read`, `write`, `glob`, `grep`, `ls` | `Read`, `Write`, `Glob`, `Grep`, `LS` | |
| `edit` | `Edit` | `MultiEdit` |
| `apply_patch` | `apply_patch` | `Edit`, `Write`, `MultiEdit` |
| `web_fetch`, `web_search` | `WebFetch`, `WebSearch` | |
| `spawn` | `spawn` | `Task`, `Agent` |
| others (`skill`, `recall`, `mail`, `note`, `task`, `wait`) | their own name | |

A hook's `if` is a permission rule (`Bash(git push:*)`, `Edit(src/**)`) evaluated by the permission engine's own
matching; the hook runs only for tool calls the rule matches. It applies to tool events only (elsewhere it is ignored
with a warning), and a condition that cannot be evaluated does not hold.

### What a hook receives

A command hook runs under `/bin/sh -c`, in the session's working directory, with a JSON object on stdin (sorted keys):

```json
{
  "agent": "main", "agent_id": "main", "agent_type": "worker", "role": "worker",
  "cwd": "/work/project", "hook_event_name": "PreToolUse", "session_id": "20260930-032830-5669da",
  "sleipnir_tool_name": "bash", "tool_name": "Bash",
  "tool_input": { "command": "touch made-by-agent" }
}
```

`tool_name` is the Claude Code spelling; `sleipnir_tool_name` appears when the tool's own name differs.
`tool_input` is the tool's input; for `read`, `write` and `edit` it also carries `file_path` (a copy of `path`), because
hooks written for Claude Code read that field. `apply_patch` has `patch` (the whole patch text, with `*** Add File:` and
`*** Update File:` lines). `tool_response` is the output text. A payload over 4 MiB is shrunk (`payload_truncated:
true`). Stdin is written concurrently, so a hook that never reads it is harmless.

The environment is the harness's, **scrubbed**: variables whose names look like credentials (`*_API_KEY`, `*_TOKEN`,
`*_SECRET`, `*_PASSWORD`, key ids, PATs, DSNs, cookies, the SSH agent socket, database URLs) or whose values are URLs
with a password are removed, and the providers' key variables are removed by name. Sleipnir adds `SLEIPNIR_PROJECT_DIR`
and `CLAUDE_PROJECT_DIR` (the project root), `SLEIPNIR_HOOK_EVENT`, `SLEIPNIR_SESSION_ID`, `SLEIPNIR_AGENT`,
`SLEIPNIR_ROLE`, `NO_COLOR=1` and `GIT_TERMINAL_PROMPT=0`.

### What a hook may answer

**Exit status.**

* `0`: success. Stdout is read as JSON if it starts with `{` (below). On `SessionStart` and `UserPromptSubmit`, plain
  text on stdout is added as context for the model.
* `2`: a blocking error, with stderr as the reason, on `PreToolUse` (the call is refused; the model sees `A hook
  blocked this call: <reason>`), `PermissionRequest` (denied), `UserPromptSubmit` (the goal is not sent: `run` stops
  with `blocked by a hook: <reason>`), `Stop` (the agent is sent back to work; see below) and `PreCompact` (`compact:
  blocked by a hook: <reason>`). On `PostToolUse` and `PostToolUseFailure` the tool has already run, so `2` means "tell
  the model": the reason is appended to the tool result. On other events it is only an error notice.
* anything else, a crash, a timeout, invalid output: an error notice; nothing is blocked (Claude Code behaviour). Set
  `"failClosed": true` on a hook and such failures **block** on the events that can be blocked.

**JSON on stdout (exit 0).** Both shapes Claude Code documents are accepted:

```json
{ "hookSpecificOutput": {
    "hookEventName": "PreToolUse",
    "permissionDecision": "deny",
    "permissionDecisionReason": "network tools are off limits here" } }
```

* `permissionDecision` (or top-level `decision`) `deny`/`block`, with a reason: vetoes a tool call, or blocks
  `UserPromptSubmit`, `Stop`, `PreCompact`. `allow` and `ask` are accepted on `PreToolUse` but change nothing: hooks can
  refuse and rewrite, they cannot approve, and a hook never lifts a denial by the permission engine.
* `updatedInput`: an object that replaces the tool's input (`PreToolUse`). The first hook, in configuration order, that
  returns one wins; a call that anything denied is not rewritten.
* `additionalContext`: text appended to the tool result as `[hook] ...` (`PreToolUse`, `PostToolUse`), or to the goal
  (`SessionStart`, `UserPromptSubmit`, under `[context from your hooks]`). Capped at 10,000 characters.
* `PermissionRequest` answers with `hookSpecificOutput.decision.behavior` of `allow` or `deny`. `allow` answers a
  question a human would have been asked; it is not remembered as a rule.
* `continue: false` with `stopReason` stops the tool call or prompt like a block; `systemMessage` shows a notice to you.

All matching hooks of an event run in parallel (at most 8 hooks at once across the session); answers are combined in
**configuration order**, never completion order: deny beats ask beats allow, reasons and context are concatenated, the
first rewrite wins. The same command configured twice (two files, two matchers that both match) runs once per event.

**Timeouts and limits.** A hook without `timeout` gets 30 seconds; the largest accepted is 10 minutes. It runs in its
own process group, and the whole group is killed (SIGTERM, then SIGKILL) at the timeout or when the run is cancelled;
background children that keep its output open are ended when it finishes. Output is capped (256 KiB of stdout, 64 KiB
of stderr, and the hook is stopped if it prints 16 MiB), stripped of terminal escapes and hidden Unicode, and a reason
is capped at 2 KiB (4 KiB combined) because it ends up in front of a model.

**A `Stop` hook** runs when an agent is about to finish. Exit `2` (or `decision: "block"`) sends the reason back to the
agent as a new message (`[stop hook] <reason>`) and it keeps working, at most **three** times per run; `stop_hook_active`
is `true` while the agent is already continuing because an earlier hook objected, which is how a hook avoids blocking
forever.

### Trust: what a repository's hooks may do

Nothing. `hooks` in a project file (`.sleipnir/config.json`, `config.local.json`) is dropped when the config loads
unless you pass `--trust-project`, with the notice `ignored security-sensitive settings from the project's config
(hooks); pass --trust-project to apply them`. Your own hooks keep running. With `--trust-project` a project's hooks run
with your rights, exactly like yours, and a project's list for an event **replaces** yours for that event, so a
repository can switch off your guard hook by defining its own `PreToolUse`. Read `sleipnir config --trust-project` first.

`type: "http"` hooks (post the payload to a URL) are parsed but disabled in this build: they log `http hooks are
disabled (Runner.AllowHTTP is false); the hook was not run`. `prompt`, `agent` and `mcp_tool` hooks are skipped with a
warning; `async`, `once`, `shell`, `asyncRewake` and `allowedEnvVars` are ignored with a warning. Every hook run is
logged as a `hook.run` event (hook, milliseconds, exit status, whether it blocked).

### Example 1: block a dangerous command

`~/.sleipnir/hooks/block-dangerous.sh` (make it executable; needs `jq`):

```sh
#!/bin/sh
# PreToolUse hook for Bash: refuse destructive commands.
cmd=$(jq -r '.tool_input.command // empty')
case "$cmd" in
  *"rm -rf"*|*"git push --force"*|*"git push -f "*)
    echo "Blocked by policy: '$cmd' is destructive. Use a narrower command." >&2
    exit 2
    ;;
esac
exit 0
```

The model sees `A hook blocked this call: Blocked by policy: 'rm -rf build' is destructive. Use a narrower command.` and
can adapt. To restrict a hook to some commands without a script, use `if`:
`{ "type": "command", "if": "Bash(git push:*)", "command": "echo 'pushes are reviewed by a human' >&2; exit 2" }`.
The permission engine still runs after your hook, so this is a second layer, not a replacement.

### Example 2: run the formatter after edits

`~/.sleipnir/hooks/format.sh`, for `PostToolUse` with matcher `Edit|Write` (which also matches `apply_patch`):

```sh
#!/bin/sh
# gofmt the Go files the last edit touched.
files=$(jq -r '
  [ .tool_input.file_path // empty,
    ((.tool_input.patch // "") | scan("(?m)^\\*\\*\\* (?:Add|Update) File: (.+)$") | .[0]) ]
  | .[]')
for f in $files; do
  case "$f" in
    *.go) [ -f "$f" ] && gofmt -w "$f" ;;
  esac
done
exit 0
```

A file written unformatted is formatted by the time the agent's next step starts. The hook runs in the session's working
directory, so relative paths resolve. To tell the model what happened, print JSON instead:
`{"hookSpecificOutput":{"hookEventName":"PostToolUse","additionalContext":"formatted foo.go"}}`.

### Example 3: veto a premature stop

`~/.sleipnir/hooks/stop-checks.sh`, for `Stop`:

```sh
#!/bin/sh
# The agent may not finish while the project's checks fail.
# stop_hook_active is true when the agent is already continuing because an earlier Stop hook objected.
[ "$(jq -r '.stop_hook_active // false')" = "true" ] && exit 0
if out=$(make check 2>&1); then
  exit 0
fi
printf 'make check fails; fix it before you finish:\n%s\n' "$(printf '%s\n' "$out" | tail -n 15)" >&2
exit 2
```

When the agent says it is done and `make check` fails, it receives `[stop hook] make check fails; fix it before you
finish:` followed by the tail of the output, and keeps working. After three vetoes in one run, or when its next attempt
finds `stop_hook_active` true, it is allowed to stop.

## 6. Sessions: list, resume, continue

Every `chat` and `run` records a session in `<state>/sessions/<id>/` (`<state>` is `$SLEIPNIR_HOME`, else `~/.sleipnir`)
with the id `YYYYMMDD-HHMMSS-xxxxxx` (UTC time plus a random suffix):

```text
events.jsonl     append-only log of everything that happened (the source of truth)
blobs/           content-addressed bodies: prompts, completions, tool output, snapshots
checkpoints/     file snapshots taken before each turn, for /rewind and /diff
```

`sleipnir sessions [-n N]` lists them, newest first (by modification time), with model, cost and the first prompt
(this output is from a test run against a scripted model). A `↺` marks a session that can be continued:

```text
↺ 20260930-032935-3d11d2  fake-model                   $0.0044   goal one
↺ 20260930-032915-a3bb32  fake-model                   $0.0036   goal one
```

**Resume.** `chat --resume ID` (an id, a session directory, or `latest`) and `chat --continue` (the same as
`--resume latest`) continue an earlier single-agent session; `run` takes the same flags: `sleipnir run --continue "now
add tests"`. `latest` is the newest session **of this project** (matched by the project root recorded at its start) that
finished at least one turn. Giving both flags is an error.

What a resume restores: the agent's conversation as of its last snapshot (snapshots are written at the end of every
turn and after every compaction, so after a compaction it is the retained turns), its notes and spine (the folded
history), the counters (requests, compactions, token and cost totals continue from where they were), and the session
directory itself: same id, the log and checkpoints continue, and `/rewind` still sees earlier turns.

What it does not restore, or does differently:

* **The provider cache.** The first request writes the cached prefix again, once: a declared rebase, priced like any
  other. Chat says so: `resumed: 4 turns restored; the first request writes the cached prefix again, once`. Thinking
  blocks bound to the old prefix are dropped where the route binds them.
* **The shared layers** are rebuilt from the project as it is now: an edited `AGENTS.md`, new skills or changed config
  take effect.
* **The model and the permission mode** are whatever you pass now (`--model`, `--mode`, config), not what the session
  had. The `SessionStart` hook runs again with `source` `startup`.
* **File-read state.** After a resume an edit needs a fresh `read` of the file first, which is what the staleness check
  exists to guarantee.

**Swarm sessions cannot be resumed yet.** A session that ran a swarm, and `--resume ... --swarm`, are refused
(`... was a swarm session, and swarm sessions cannot be resumed yet (its log and checkpoints stay for inspection)`,
`resuming into a swarm is not supported yet: start a new swarm session`). A session that never finished a turn has no
snapshot (`has no snapshot: no turn finished, so there is nothing to resume from`).

`sleipnir inspect <session dir>` opens a read-only dashboard over any recorded session, resumable or not.

## 7. Manual compaction

`/compact [focus]` in `chat` folds the older part of the conversation now instead of waiting for the planner. In a swarm
chat it compacts the manager.

* It sends **one extra request** to the model, a fork of the agent's own request (so it reads the same cache), asking for
  a compaction patch; the two newest units stay verbatim (a unit is a tool-calling assistant turn together with its
  results, or a single turn). With a `focus` (up to 400 characters) the compactor is told what you care about:
  `/compact keep the API design decisions`.
* The result is validated and committed by the harness. If the model's answer is unusable, a deterministic "mechanical"
  patch is used instead; if only one unit is foldable, bulky tool results are masked. Nothing is lost: folded turns stay
  in the archive and the model can page them back with `recall`.
* It is a declared, priced rebase: the next request re-writes the cached prefix once. Chat prints the outcome, for
  example (a thread of nine 1,600-token replies, with a stand-in model that could not produce a patch):

```text
compacted (mechanical): 14 turns folded, 16398 → 3644 tokens; the next request writes the cached prefix again, once
```

  `model` instead of `mechanical` when the model's patch was used, `mask` when only bulky results were masked, and
  `nothing to compact yet` when the thread is small (the mechanical target is a quarter of the thread, at least 2,000
  tokens). Run it between turns; Ctrl-C cancels it.
* Hooks: `PreCompact` runs first (before anything else, even when there is nothing to fold) and can refuse with exit `2`
  or `decision: "block"`: `compact: blocked by a hook: <reason>`. `PostCompact` runs after a successful compaction. Both
  carry `trigger: "manual"`; automatic compactions, which the planner starts by itself when the thread grows past
  `cache.thread_soft_limit_tokens` and `cache.compact_threshold_tokens`, fire no hooks.

## 8. Config-directory protection

The directories that hold your extensions are also where a compromised agent could plant the next session's
instructions or a hook that runs a command with your rights. Sleipnir therefore puts these Ask rules in front of
yours, for every session:

```text
Edit(./.sleipnir/**)   Edit(./.claude/**)   Edit(./.git/hooks/**)   Edit(./.git/config)
Edit(~/.sleipnir/**)   Edit(~/.claude/**)
```

A write there **always asks, in every mode, `bypass` included**, and an unattended run refuses it
(`approval required: rule Edit(./.sleipnir/**) requires approval`). It covers the file tools and shell redirections
(`echo x > .sleipnir/notes.txt` asks). Answering `a` ("always this session") does not remember it: a question that comes
from an Ask rule is asked every time. Writes under `.git/` are refused outright by a separate built-in protection (use
`git` commands). To let an agent maintain these files, approve each write, or edit them yourself.
