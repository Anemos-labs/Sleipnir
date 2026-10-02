# Configuring Sleipnir

This page is for people who installed the binary and want to point it at a model, tune permissions and limits, or
share settings through a repository. Extension files (instruction files, skills, slash commands, agent definitions,
hooks) are in `docs/EXTENDING.md`; every command and flag is in `docs/CLI.md`.

`sleipnir config` shows the configuration a session would use and which file each top-level key came from.
`sleipnir config --json` prints the merged result. Run it after every edit: it reports syntax errors with file, line
and column, and validates values.

Statements here were checked against the code and against the binary. Every key below is read by something. A key this
version does not know (a typo, or a setting of a newer version) is reported with a "did you mean" hint and has no
effect; provider options are checked the same way (section 7).

## 1. Where configuration comes from

Lowest precedence first:

| # | Source | Where | Notes |
|---|---|---|---|
| 1 | built-in defaults | the binary | listed per key in section 6 |
| 2 | user file | `~/.sleipnir/config.json` | trusted. `sleipnir init --user` writes a starter (mode 0600) |
| 3 | project file | `<project>/.sleipnir/config.json` | meant to be committed. `sleipnir init` writes a starter. Untrusted input (section 4) |
| 4 | local file | `<project>/.sleipnir/config.local.json` | same schema, for your own overrides of a shared project file. Keep it out of git (`sleipnir init` adds it to `.gitignore`). Untrusted like the project file |
| 5 | environment | `SLEIPNIR_*` variables | section 5 |
| 6 | flags | `--model`, `--mode`, `--budget-usd`, ... | not part of the file schema: each flag overrides one setting (section 2) |

The **project root** is the nearest directory, from the working directory upwards, that contains `.git` (a directory
or a file, as in worktrees) or a `.sleipnir/` directory. The `.sleipnir/` in your home directory does not make your
home a project. With no marker, the working directory is the root. `chat` and `run` take `--cwd DIR` to start the
search elsewhere; `config` and `doctor` always start from the directory you run them in.

`$HOME` decides where the user file lives. `SLEIPNIR_HOME` is a different thing: it moves the *state* directory
(recorded sessions, the model-catalogue cache, `rl-work`) and nothing else. The user's config, instruction file,
skills, commands and agent definitions are always under `$HOME/.sleipnir/`.

A file that does not exist is skipped. A file that cannot be read or parsed stops the run with an error; Sleipnir
never continues with part of a configuration, because silently dropping a layer could drop a `deny` rule.

Example output (example (a) in section 9; temp directory shortened to `~`):

```text
layers (lowest precedence first):
  defaults  defaults (active)
  user      ~/.sleipnir/config.json (found)
  project   ./.sleipnir/config.json (not found)
  local     ./.sleipnir/config.local.json (not found)
keys:
  models       ~/.sleipnir/config.json
  permissions  ~/.sleipnir/config.json
  providers    ~/.sleipnir/config.json
  swarm        ~/.sleipnir/config.json
configuration is valid
```

## 2. Precedence and merging

Layers are merged by presence, not by value:

* objects merge key by key, recursively (`providers`, `providers.<name>`, `permissions.roles.<role>`, every section);
* lists replace the list below them: a higher layer's `permissions.allow` does not add to a lower one, it replaces it.
  The exceptions are the guardrails, which a repository can only add to: `permissions.deny`, `permissions.ask`, the
  same per role (`permissions.roles.<role>.deny`, `.ask`) and the events under `hooks`. In a project file
  (`.sleipnir/config.json` or the local override) a longer list appends to yours, and a shorter one, an empty one or
  `null` changes nothing;
* a scalar is overridden only if the higher layer contains the key, so an explicit `false` or `0` beats a `true` or
  `5` below it;
* `null` deletes whatever the lower layers set for that key (except for the guardrails above, when it comes from a
  project file);
* each event under `hooks` (`hooks.PreToolUse`, ...) adds the project's hook groups after yours when the project is
  trusted (yours run first, so a guard hook of yours cannot be switched off by a repository); between two of your own
  layers a later one replaces the event as a whole;
* unknown top-level keys are kept but do nothing, and produce a warning with a "did you mean" hint. Unknown keys
  inside a section are ignored with a warning. `$schema` and other `$`-prefixed top-level keys are editor metadata and
  never warn.

Worked example. The user file sets nothing about the swarm; the project file has `"swarm": {"max_agents": 12}`; the
local file has `"swarm": {"max_agents": 6}` and `"models": {"default": null}`; the shell exports
`SLEIPNIR_SWARM_MAX_AGENTS=30`:

| Source | `swarm.max_agents` |
|---|---|
| project file | 12 |
| + local file | 6 (and `models.default` from lower layers is removed) |
| + `SLEIPNIR_SWARM_MAX_AGENTS=30` | 30 |

Flags are applied by the commands themselves, after the merge, and always win over every layer:

| Flag | Overrides | Commands |
|---|---|---|
| `--model M` | `models.default` (and `SLEIPNIR_MODEL`) | `chat`, `run`, `swarm`, `doctor`, `rl rollout`, `rl eval` |
| `--mode M` | `permissions.mode` (role profiles under `permissions.roles` still apply) | `chat`, `run`, `swarm` |
| `--budget-usd N` | `swarm.budget_usd` (and, with `--swarm`, the built-in cap), and it also caps a single agent. Zero, negative and NaN are not budgets: `0` is the same as leaving the flag out, and the others are refused. To run a swarm without a cap set `swarm.budget_usd` to 0 in your own file or `SLEIPNIR_SWARM_BUDGET_USD=0` | `chat`, `run`, `swarm` |
| `--swarm N` | the team size: N agents in all, the manager included. `swarm.max_agents` is the ceiling: a request for more agents than it allows is refused before anything starts | `chat`, `run` |
| `--role-model role=M` | the model of one role (repeatable). A role the session does not have is an error (`no role named "backnd"`, with the roles it has); without `--swarm` there is only one agent and the flag draws a warning | `run`, `swarm`, `rl rollout`, `rl eval` |
| `--no-web` | removes `web_fetch` and `web_search` | `run`, `swarm` |
| `--trust-project` | the trust gate of section 4 | `chat`, `run`, `swarm`, `config`, `doctor` |

## 3. File format

Files are JSONC: JSON with `//` and `/* */` comments and trailing commas. A UTF-8 byte-order mark is skipped, an empty
file (or one with only comments) is an empty object, and a file is limited to 1 MiB and 64 levels of nesting. Strings
use double quotes. A number where an integer is expected must be an integer.

Every problem in every layer is reported in one run, with file, line, column and field path, for example:

```text
sleipnir: ~/.sleipnir/config.json:7:28: swarm.max_agents: expected an integer, got a string
~/.sleipnir/config.json:3:26: providers.acme.dialect: unknown dialect "openai" (valid: anthropic, openai-chat, openai-responses)
~/.sleipnir/config.json:3:48: providers.acme.base_url: must be an absolute http(s) URL such as https://api.example.com/v1
~/.sleipnir/config.json:3:86: providers.acme.api_key_env: must be the NAME of an environment variable (letters, digits and underscores), not the key itself
~/.sleipnir/config.json:5:26: models.default: must look like "provider/model", got "gpt-5"
~/.sleipnir/config.json:6:28: permissions.mode: unknown permission mode "paranoid" (valid: default, accept-edits, plan, bypass, yolo)
~/.sleipnir/config.json:6:46: permissions.allow[0]: rule "Bash(git status": missing closing parenthesis
~/.sleipnir/config.json:7:49: swarm.budget_usd: must be zero (no limit) or a positive amount
```

**Never put a key in a file.** A provider names the environment variable that holds its key (`api_key_env`); the
variable is read when the session starts. A value in a file that looks like a credential (a well-known key prefix, or a
long token-like string under a key named `key`, `token`, `secret` or `password`) produces a warning, and a
`api_key_env` that is not a valid variable name is an error (the message does not echo the value).

## 4. Trust: project files are untrusted input

`<project>/.sleipnir/config.json` and `config.local.json` arrive with a repository, which may be someone else's.
Settings that could send your API key to another host, run commands or widen what the agent may do without asking are
**ignored unless you pass `--trust-project`**:

| Ignored key | Why |
|---|---|
| `providers.<name>.base_url` | redirects requests, and the key that signs them |
| `providers.<name>.api_key_env` | chooses which environment variable is sent |
| `providers.<name>.headers` | can add credentials or route requests |
| `providers.<name>.options` | provider-specific behaviour, may hold URLs |
| `providers.<name>.allow_hosts`, `.allow_insecure_http` | say where your key may go: dropped from **every** project layer, trusted or not (section 6) |
| `permissions.mode` | `bypass` and `yolo` turn off the prompts |
| `permissions.allow` | pre-approves actions |
| `permissions.roles.<role>.mode`, `.allow` | the same, per role |
| `hooks` | runs commands |
| `mcp` | starts servers |
| `tools.web_allow_private`, `tools.web_allow_hosts` | reaches internal networks |
| `swarm.budget_usd` | the built-in cap on what a swarm may spend (section 6): a repository must not be able to lift it |

Everything else in a project file applies without trust: `models`, `cache`, `swarm` (`isolation` and `mailman` reduce
what an agent can reach or change nothing about it, and a project cannot choose where anything is written), the tool
limits, and `permissions.ask` and `permissions.deny` (a project can add to your lists, never remove from them). Providers are gated as a whole: an untrusted project contributes no provider entry at all, not even one without
a URL.

In a session (`chat`, `run`, `swarm`) an untrusted project's sensitive settings are dropped with a notice such as
`ignored security-sensitive settings from the project's config (hooks); pass --trust-project to apply them for this run, or run `sleipnir trust add` to remember them until they change`.
`sleipnir config` lists them under "security-sensitive settings from project files" (with `--trust-project` it lists
the same settings as applied).

`--trust-project` is all or nothing and lasts for one command; nothing is remembered. It gates more than config: the
project's instruction files (`AGENTS.md`, ...), skills, slash commands and agent definitions are also read only with it
(`docs/EXTENDING.md`). `models` and every `rl` command never trust a project file: put the provider they need in your
user file, or pass `--base-url`/`--api-key-env` to `rl` and `doctor`.

What a repository cannot do, trusted or not: remove or shorten your `permissions.deny` and `permissions.ask` rules,
or switch off your hooks. Run `sleipnir config` in a repository you do not know: the layers and the origin of every
value are listed, and the security-sensitive settings the project made are named.

## 5. Environment variables

### Settings

Each `SLEIPNIR_*` variable that names a setting is one entry above the files and below the flags. An empty value counts
as unset, booleans accept `1/true/yes/on` and `0/false/no/off`, lists are comma-separated, and a value of the wrong
type is an error that names the variable (`env:SLEIPNIR_SWARM_MAX_AGENTS: swarm.max_agents: expected an integer, got
"lots"`). Other `SLEIPNIR_*` variables are ignored. Providers, hooks and MCP servers can only be set in files.

| Variable | Setting |
|---|---|
| `SLEIPNIR_MODEL` | `models.default` |
| `SLEIPNIR_MODEL_<ROLE>` | `models.roles.<role>` (the role name is lower-cased: `SLEIPNIR_MODEL_MANAGER`) |
| `SLEIPNIR_PERMISSION_MODE` | `permissions.mode` |
| `SLEIPNIR_<SECTION>_<FIELD>` | any scalar or list field of a section, named by its JSON path in upper case |

The last form covers: `SLEIPNIR_MODELS_DEFAULT`, `SLEIPNIR_PERMISSIONS_MODE`, `SLEIPNIR_PERMISSIONS_ALLOW`,
`SLEIPNIR_PERMISSIONS_ASK`, `SLEIPNIR_PERMISSIONS_DENY`, `SLEIPNIR_CACHE_SHARED_TTL`,
`SLEIPNIR_CACHE_MIN_LAYER_FOR_BREAKPOINT`, `SLEIPNIR_CACHE_COMPACT_THRESHOLD_TOKENS`,
`SLEIPNIR_CACHE_THREAD_SOFT_LIMIT_TOKENS`, `SLEIPNIR_CACHE_HOT_MAX_TOKENS`, `SLEIPNIR_CACHE_AFFINITY_SHARDS`,
`SLEIPNIR_SWARM_MAX_AGENTS`, `SLEIPNIR_SWARM_REQUESTS_PER_MINUTE`, `SLEIPNIR_SWARM_MAX_CONCURRENT_REQUESTS`,
`SLEIPNIR_SWARM_ISOLATION`, `SLEIPNIR_SWARM_MAILMAN`, `SLEIPNIR_SWARM_BUDGET_USD`, `SLEIPNIR_TOOLS_MAX_OUTPUT_CHARS`,
`SLEIPNIR_TOOLS_DEFAULT_TIMEOUT_SEC`, `SLEIPNIR_TOOLS_MAX_TIMEOUT_SEC`, `SLEIPNIR_TOOLS_WEB_ALLOW_PRIVATE` and
`SLEIPNIR_TOOLS_WEB_ALLOW_HOSTS`. (A variable that sets a gated key is yours, not the repository's, so the trust gate
does not apply to it.)

### Other variables the binary reads

| Variable | Used for |
|---|---|
| the variable named by a provider's `api_key_env` | the API key. Built-in providers: `HEIMDALL_API_KEY`, `OPENROUTER_API_KEY`, `OPENAI_API_KEY`. At start-up the harness moves every `*_API_KEY` variable (and any other variable a provider names) out of its own environment into memory, so nothing it starts (git, hooks, verifiers, MCP servers, the agent's shell) inherits it; name yours `..._API_KEY` |
| `<NAME>_BASE_URL` | overrides the `base_url` of provider `<NAME>` (upper-cased, `-` becomes `_`): `HEIMDALL_BASE_URL`, `OPENROUTER_BASE_URL`, `OPENAI_BASE_URL`, `LOCAL_BASE_URL`. Applies to configured providers too. The environment is not your word (a `.envrc` or a CI job can set it), so the key follows an override only to the provider's own host, to loopback, or to a host in `allow_hosts`; anything else is refused with the line of configuration that would allow it |
| `SLEIPNIR_HOME` | the state directory (default `~/.sleipnir`): `sessions/`, `cache/`, `rl-work/` |
| `SLEIPNIR_INSPECT_TOKEN` | access token for `sleipnir inspect` on a non-loopback address |
| `BRAVE_API_KEY`, `TAVILY_API_KEY`, `SEARXNG_URL` | enable the `web_search` tool (the first one set wins, in this order); without one only `web_fetch` exists |
| `HTTPS_PROXY`, `HTTP_PROXY`, `NO_PROXY` | honoured by the provider clients and the web tools |

The agent's own shell commands do not inherit variables whose names or values look like credentials (`*_API_KEY`,
`*_TOKEN`, `*_SECRET`, `GITHUB_TOKEN`, URLs with a password, well-known key formats), and there is no setting to pass
them through in `chat`/`run`. Hooks are scrubbed the same way (`docs/EXTENDING.md`).

## 6. Key reference

Types are JSON types. "Applied" says where the key has an effect: `yes` everywhere, `swarm` only in swarm sessions.

### `providers`

An object of named endpoints. A name may not be empty, contain whitespace or contain `/` (model references are
`provider/model`). Three providers exist without being configured, each usable as soon as its key variable is set:

| Name | `base_url` | `api_key_env` | Options and headers |
|---|---|---|---|
| `heimdall` | `https://api-staging.impossiblecarrot.cc/api/v1` | `HEIMDALL_API_KEY` | `session_header`, `cache_key_body`; header `X-Title: Sleipnir` |
| `openrouter` | `https://openrouter.ai/api/v1` | `OPENROUTER_API_KEY` | `session_header`; header `X-Title: Sleipnir` |
| `openai` | `https://api.openai.com/v1` | `OPENAI_API_KEY` | `cache_key_body` |

A configured entry with the same name and **its own `base_url`** replaces the built-in one entirely: it defines the provider outright and
inherits nothing, in particular not the built-in key variable. An entry with **no `base_url`** (typically one that only sets `allow_hosts`) extends
the built-in provider: its URL, dialect, key variable, options and headers stay.

| Key | Type | Default | Applied | Meaning |
|---|---|---|---|---|
| `dialect` | string | `openai-chat` | yes | The wire protocol: `openai-chat` (OpenAI-style chat completions), `anthropic` (Messages API) or `openai-responses` (OpenAI's Responses API: `{"dialect": "openai-responses"}` on an `openai`-style provider with a key; options `reasoning_summary` (`auto`, `concise`, `detailed`: ask for the reasoning's summary), `extra_body`, and the timeouts of the others) |
| `auth` | string | none (an API key) | yes | `chatgpt-plan`: the provider is a ChatGPT plan, signed in with `sleipnir login chatgpt` (the built-in provider `chatgpt` is one). There is no key; the dialect is `openai-responses`; where the token goes is the provider's own `base_url`, never an environment variable's or a project file's |
| `base_url` | string | none | yes | Absolute http(s) URL, required for every provider that is not built in. `openai-chat` posts to `<base_url>/chat/completions`. `anthropic` posts to `<base_url>/v1/messages`, or to `<base_url>/messages` when the URL already ends in `/v1`. A trailing `/` is ignored. Credentials inside the URL produce a warning. A key is sent over `https`, or over plain `http` to loopback only, unless `allow_insecure_http` says otherwise; a redirect is followed only within the original scheme, host and port |
| `allow_hosts` | list of strings | none | yes | **User file only.** Hosts, besides the provider's own, that may receive its key when the URL arrives through the environment (`<NAME>_BASE_URL`) or a project file: `"gateway.example.com"` or `"gateway.example.com:8443"`, case-insensitive. A refusal prints the exact snippet to add |
| `allow_insecure_http` | bool | `false` | yes | **User file only.** Lets the key travel over plain `http` to a host that is not this machine (a trusted LAN proxy). Warned about by `sleipnir config` |
| `api_key_env` | string | none | yes | The **name** of the environment variable holding the key. If set and the variable is empty when a session starts: `provider "x" needs NAME to be set`. If omitted, no credential header is sent (local servers). `openai-chat` sends `Authorization: Bearer <key>`; `anthropic` sends `x-api-key: <key>` unless `options.auth_style` is `bearer` |
| `headers` | object of strings | none | yes | Extra headers on every request. Names must be valid HTTP tokens, values may not contain CR, LF or NUL. On `anthropic`, an `anthropic-beta` header is merged with the betas the adapter needs instead of replacing them |
| `options` | object | none | yes | Dialect-specific request options, section 7. An option the dialect does not read is a warning (with a "did you mean" hint, or the dialect it belongs to), a value of the wrong kind or outside what the adapter takes is an error |

**Model references.** A model is written `provider/model`, and the model part may contain slashes
(`heimdall/deepseek/deepseek-v4.1-flash` is provider `heimdall`, model `deepseek/deepseek-v4.1-flash`). A **bare id**
(no known provider before the first slash) goes to the default provider, which is: the only provider under
`providers`, otherwise the first of `heimdall`, `openrouter`, `openai` whose key variable is set. With two or more
configured providers and none of the built-in keys set there is no default and a bare id fails with `no provider
configured`. A provider of your own (`sleipnir init --user --local-url URL` writes one named `local`) makes it the only
configured provider: from then on a bare marketplace id such as `deepseek/deepseek-v4.1-flash` is sent to it. Write
`heimdall/deepseek/...` or add the provider you use next to it.

### `models`

| Key | Type | Default | Applied | Meaning |
|---|---|---|---|---|
| `default` | string | none | yes | `provider/model` used when neither `--model` nor `SLEIPNIR_MODEL` is given. It must contain a `/`; a bare id such as `gpt-5` is a validation error here (it is fine after `--model`) |
| `roles` | object | none | yes | Role name to `provider/model` for swarm roles (`manager`, `worker`-style roles, your own definitions, `mailman`). Lowest priority: `--role-model role=provider/model` and `model:` in an agent definition (`docs/EXTENDING.md`) come first. An entry for a role a session does not have is not an error: one file may serve projects with different roles. The role `compactor` is not an agent: it names the model that writes the compaction summaries for every agent (`qwen/qwen3.8-flash-next` is a cheap choice). It shares no cache with the agent, so it reads the thread at full input price, and it is used only while the thread fits three quarters of its context window; past that, and when no `compactor` is set, each agent compacts on its own model and its own cache. In the chat, `/roles` shows what each role runs on, and `/roles ` opens a menu of the roles and then, after the `=` of one, of the models (the chat restarts with the choice) |
| `favorites` | list | none | yes | `provider/model` references that `sleipnir models` lists first, marked `*` (`sleipnir models fav add REF`, `rm`, `list` edit it in your user config) |

**Catalogue and price overrides: there are none.** No key sets a price or a context window. Costs and windows come from,
in order: a built-in table of well-known models; the endpoint's own catalogue, fetched from `<base_url>/models` when the
model is not in the table and cached for six hours under `<state>/cache/`; a conservative fallback (200k context, $3 per
million input tokens, $15 output). When an endpoint reports the cost of a call (Heimdall does, as `usage.cost`) the bill
uses that figure and the table is only an estimate. This holds for every model of a session: the main one and the
models of roles (`--role-model`, `models.roles`, an agent definition's `model:`, the mailman's). `session.start` in the
event log records what each was described with (the numbers and where they came from: `catalogue`, `table`, `given`,
`fallback`), and `sleipnir inspect` prices the run with those numbers, so its dollar figures for a marketplace model are the
gateway's, not the fallback's. `sleipnir models` prints a marketplace catalogue with prices.
`--context-window N` on `run` overrides the window for one run.

### `permissions`

| Key | Type | Default | Applied | Meaning |
|---|---|---|---|---|
| `mode` | string | `default` | yes | `default`, `accept-edits`, `plan`, `bypass` or `yolo`. `--mode` and `SLEIPNIR_PERMISSION_MODE` override it. `bypass` and `yolo` warn: `bypass` still asks about the very dangerous, `yolo` never asks and belongs in a sandbox |
| `allow` | list of rules | `[]` | yes | Actions that need no question |
| `ask` | list of rules | `[]` | yes | Actions that always ask, whatever the mode or the allow list says |
| `deny` | list of rules | `[]` | yes | Actions that are refused, in every mode |
| `roles` | object | none | yes | Per-role overlay: `roles.<role>` with `mode`, `allow`, `ask`, `deny`. Section 8 |

Rule syntax, precedence, modes and examples are in section 8. Every rule is parsed when the file loads, so a typo is
an error with a line number, not a rule that silently never matches. A rule that is in both `allow` and `deny` is a
warning.

### `cache`

Tuning of the prompt-cache engine. A `0` means "use the engine's default".

| Key | Type | Default | Applied | Meaning |
|---|---|---|---|---|
| `shared_ttl` | string | `5m` | yes | `5m` or `1h`: the lifetime requested for the shared and role layers on a provider with explicit breakpoints (the Messages dialect). `1h` costs more per cache write and pays for a swarm or a session that is used over more than five minutes. Chat-completions providers cache by prefix and have no such setting |
| `min_layer_for_breakpoint` | integer | `1500` | yes | Do not place a cache breakpoint after a layer smaller than this many tokens. A `0` behaves like the default: a positive value is needed to change it. Single agents and every agent of a swarm |
| `compact_threshold_tokens` | integer | `0` (60,000) | yes | Thread size at which compaction is forced whatever it costs |
| `thread_soft_limit_tokens` | integer | `0` (20,000) | yes | Thread size at which the planner starts considering a compaction |
| `hot_max_tokens` | integer | `0` (900) | swarm | Cap on the always-fresh "hot" view a worker sees (the manager's is 2,200 and is not configurable) |
| `affinity_shards` | integer | `0` (one key) | swarm | Spread a large swarm over this many provider routing keys |

The swarm's warm gate (the first request of a swarm writes the shared prefix before the others are released) is always on.

### `swarm`

| Key | Type | Default | Applied | Meaning |
|---|---|---|---|---|
| `max_agents` | integer | `0` (no ceiling) | yes | The ceiling on the size of a session's swarm, the manager included. `--swarm N` asks for N agents and is refused when N is more than this (`swarm: 9 agents requested (the manager included) but swarm.max_agents caps a session at 4`); a smaller request is honoured. It is how a user file keeps a stray `--swarm 50` from spending a budget. `0` sets no ceiling |
| `requests_per_minute` | integer | `0` (500) | yes | Request budget of the whole swarm. Lower it if the endpoint answers 429 |
| `max_concurrent_requests` | integer | `0` (24) | yes | Requests in flight across the swarm |
| `isolation` | string | `none` | yes | `none` (all agents edit the one working tree, guarded by write leases; `shared` is the older spelling of the same thing) or `worktree`: each writer works in a git worktree of its own and finished work is integrated through a verifying merge queue (`docs/SWARM-PROTOCOL.md`). `--isolation` overrides it. The trees live in your cache directory, whatever the configuration says |
| `mailman` | boolean | `false` | yes | Route worker mail through a mailman agent that digests bursts (`docs/SWARM-PROTOCOL.md`); `--mailman` overrides it. It runs on the session's model unless `--role-model mailman=<model>` names one |
| `budget_usd` | number | `50` | swarm | Total spend cap for a swarm run, retired agents included; when spent, workers stop and no request is admitted. On by default, because a swarm can spend many times what one agent does; `0` (in your own file or `SLEIPNIR_SWARM_BUDGET_USD`) removes it. A project file cannot set it unless trusted (section 4). `--budget-usd` overrides it for one run and is the way to cap a single agent. The run's header line shows the cap, and the message when it is reached says how to raise it |

At most four agents that may write files run at once (fixed; not configurable).

### `tools`

| Key | Type | Default | Applied | Meaning |
|---|---|---|---|---|
| `max_output_chars` | integer | `24000` | yes | Model-visible tool output is cut to this many characters (head and tail kept); the full text stays behind a `recall` handle |
| `default_timeout_sec` | integer | `120` | yes | Timeout of a shell command that names none. May not exceed `max_timeout_sec` |
| `max_timeout_sec` | integer | `600` | yes | The longest timeout a shell command may ask for |
| `web_allow_private` | boolean | `false` | yes | Turns the private-address guard of `web_fetch` off: loopback, private, link-local and cloud-metadata addresses become reachable |
| `web_allow_hosts` | list of strings | `[]` | yes | Hosts `web_fetch` may reach even though the guard would block them: a name (`docs.internal`), an IP, `host:port`, `[::1]:8080`, or `*.corp.example` (subdomains only, not the apex) |

`web_allow_hosts` is an exemption from the private-network guard, not a list of the only hosts the agent may fetch:
public hosts are reachable, subject to permissions. Whether a fetch is allowed at all is decided by `permissions`:
in `default` mode `web_fetch` asks; `WebFetch(domain:example.com)` in `allow` lets it through (host and subdomains), and
an allow rule also lifts the ban that `plan` mode puts on network access.

### Recording and training data

There is no section for them. Every session writes its event log and blobs to `<state>/sessions/<id>/` (`events.jsonl`,
`blobs/`, `checkpoints/`; `<state>` is `$SLEIPNIR_HOME`, else `~/.sleipnir`, and `run --session-dir DIR` puts one run
elsewhere), and the RL pipeline (`sleipnir rl ...`, `docs/TRAINING-DATA.md`) reads those. Redaction is a flag of
`sleipnir rl export` (`--no-redact`, `--redact-salt`), and token capture is the `--capture` flag plus the provider
option `capture_tokens` (section 7). Keys that earlier drafts listed here (`ui`, `training`, `models.compactor`,
`cache.prewarm`, `cache.keepalive`) never did anything and are gone: a file that still has them gets an "unknown key"
warning.

### `hooks` and `mcp`

`hooks` is an object of events, each a list of matcher groups; the format, events and examples are in
`docs/EXTENDING.md`. Hooks from a project file run only with `--trust-project`. `mcp` is an object of named tool
servers (`docs/MCP.md`): entries in your own file are trusted, a project's arrive only with `--trust-project` and each
one still needs your approval (`sleipnir mcp approve`); the key is on the gated list of section 4.

## 7. Provider options

`providers.<name>.options` passes dialect-specific switches to the adapter. Booleans must be JSON `true`/`false`,
strings must be strings, and numbers must be numbers; a value of the wrong kind, a negative number of seconds, or a
string the adapter does not take (`system_role`, `max_tokens_field`, `auth_style` and `thinking_display` have a fixed
set) is an error with file, line and column. A name the provider's dialect does not read is a warning that says what it
looks like (`unknown option "session_headr" for the openai-chat dialect; it is ignored (did you mean
"session_header"?)`) or which dialect it belongs to. A test keeps this list and the code that reads the options
together. If a setting seems to have no effect, look at the request itself (`sleipnir inspect <session dir>`) before
suspecting the endpoint.

### Options of `openai-chat`

| Option | Type | Default | Effect |
|---|---|---|---|
| `session_header` | bool | `false` | Send the session's routing key as an `X-Session-Id` header, so a gateway keeps a conversation on the engine that holds its cache |
| `cache_key_body` | bool | `false` | Send the same key as `prompt_cache_key` in the body (OpenAI, Heimdall) |
| `system_role` | string | `system` | Role name of the system message; `developer` for OpenAI reasoning models |
| `max_tokens_field` | string | `max_tokens` | `max_tokens` or `max_completion_tokens`, the request member that carries the output limit (16000, or the model's maximum if smaller) |
| `reasoning_effort_field` | string | none | Any non-empty value lets a request's reasoning effort through as `reasoning_effort`. The harness does not set an effort in this version, so this has no effect in normal runs |
| `cache_control_parts` | bool | `false` | Add `cache_control` markers to content parts at breakpoints, for gateways that front Anthropic models |
| `extra_body` | object | none | Members merged into the top level of every request body. They replace members of the same name, so do not use it to change `model` or `messages` |
| `capture_tokens` | bool | `false` | Declares that the server can return prompt and completion token ids and logprobs (a self-hosted vLLM or SGLang policy server). It does not switch capture on: the request members `logprobs: true` and `return_token_ids: true` are sent only when `--capture` is given (`run`, `doctor`, `rl rollout`, `rl eval`) |
| `context_window` | int | model-dependent | The context window, in tokens, that the server really gives the model. A catalogue that lists ids only (Ollama, LM Studio, llama.cpp, vLLM) does not say, so a model of a keyless server on this machine is assumed to have **8192** tokens and to cost nothing; say the real window here (`"providers": {"ollama": {"options": {"context_window": 32768}}}`, after raising `num_ctx` in Ollama itself, which cuts a longer prompt off without a word). Compaction keeps the prompt inside it |

Both dialects also take the timeouts below. A silent server is cut off, so no request holds an agent for ever.

| Option | Type | Default | Effect |
|---|---|---|---|
| `first_byte_timeout_sec` | number | `120` (the idle value when only that is set) | How long a request may wait for the first byte of the response body. Headers alone do not count. A request that gets no response twice is not retried again |
| `stream_idle_timeout_sec` | number | `60` | The longest silence inside a stream. Keep-alive comments and pings count as activity |
| `stream_timeout_sec` | number | `1800` | The whole of one streamed response. Raise it for a slow self-hosted model that writes very long answers |
| `request_timeout_sec` | number | `600` | A call that is not streamed (at most a day) |

### Options of `anthropic`

| Option | Type | Default | Effect |
|---|---|---|---|
| `auth_style` | string | `x-api-key` | `x-api-key` or `bearer` (`Authorization: Bearer`) |
| `version` | string | `2023-06-01` | The `anthropic-version` header |
| `betas` | list of strings | none | `anthropic-beta` values sent on every request, in addition to the ones the adapter adds itself |
| `cache_control` | bool | `true` | `false` when the gateway drops cache markers: no breakpoints are planned or sent |
| `max_breakpoints` | integer | `4` | Cap on `cache_control` markers per request |
| `no_turn_scoped_system` | bool | `false` | The gateway has no system messages in the middle of a conversation; such text is folded into a user message instead |
| `no_thinking_replay` | bool | `false` | The gateway cannot take thinking blocks back; they are dropped from history instead of replayed |
| `no_zero_max_tokens` | bool | `false` | The gateway rejects `max_tokens: 0`; cache warm-ups use `1` |
| `session_header_name` | string | none | Header that carries the routing key (for example `X-Session-Id`); Anthropic itself ignores it |
| `default_max_tokens` | integer | `8192` | Used only when a request carries no `max_tokens`. The harness always sets one (16000, or the model's maximum if smaller), so this rarely matters |
| `thinking_display` | string | none | Value of `thinking.display`, sent only when a request carries a `thinking` object. The harness does not turn thinking on itself; it adds such an object only for models that always think, to carry the binding control after a compaction |
| `thinking_budget` | integer | `8192` | `budget_tokens` for models that do not have adaptive thinking. Only used when thinking is requested, which the harness does not do in this version |
| `extra_body` | object | none | Members merged into the request body. Naming a member the adapter owns (`model`, `max_tokens`, `stream`, `thinking`, `output_config`, `temperature`, `stop_sequences`, `metadata`, `tool_choice`, `tools`, `system`, `messages`) is an error |

`capture_tokens` on an `anthropic` provider is refused at startup: the Messages API returns no token ids or logprobs,
and RL data capture needs a chat-completions server.

## 8. Permissions in detail

The engine decides every tool call before it runs. A request is settled by the first of these that applies:

1. **Hard denies**: built-in protections, and your `deny` rules. They beat everything, `bypass` included.
2. Your `ask` rules: the answer is a question, whatever `allow` or the mode says.
3. High-risk shell commands (`sudo`, `rm -rf` of the workspace or home, `dd` to devices, force-pushes to shared
   branches, `git reset --hard` on a shared branch, `shutdown`, ...): ask, and plan mode refuses. A blanket `Bash` or
   `Bash(*)` allow does not lift this; an exact rule, or one that names the risky flag, does.
4. Your `allow` rules.
5. The mode's defaults.

### Modes

| Mode | Behaviour |
|---|---|
| `default` | Reads inside the workspace and read-only shell commands are allowed; everything else asks |
| `accept-edits` | Also writes inside the workspace (file tools, redirections, `mkdir`, `touch`, `cp`, `mv`, `rm`, `rmdir`, `tee` on workspace paths) and the commands that build and test a project (`go test`, `npm test`, `cargo build`, `pytest`, `make test`… the `--allow tests` list); `ask` and `deny` rules still win |
| `plan` | Read-only. Writes, network access and commands that are not provably read-only are refused with a message that says to present a plan. An `allow` rule still carves an exception (say `Edit(docs/plan.md)`) |
| `bypass` | Full control without asking, except about the very dangerous: the high-risk class (`sudo`, a recursive delete of the workspace, home or `/`, or of a path only known when the command runs such as `rm -rf "$DIR"`, a forced push to a shared branch, disk tools, shutdown) still asks. Hard denies, `deny` rules, guarded paths without an `allow` rule and `ask` rules still apply |
| `yolo` | Never asks anything, for runs with nobody there: what `bypass` allows, and the high-risk class too. Hard denies, `deny` rules and guarded paths still refuse, and so does a rule that would have asked (an `ask` rule): refusing is not a question. For sandboxes. `--continue` never brings `bypass` or `yolo` back |

Switch a running chat with `/mode <m>` or `/plan`.

**When no human is available** (`run`/`swarm` with stdin not a terminal, or any unattended run) a question cannot be
asked, so the action is refused with `approval required: <why>` and a fixed sentence saying that this run has no one to ask
(a model that is not told keeps looking for another way to the same action: a real one spent twenty-four tool calls on
that). A run that was refused something says so at its end, with the commands and the edits and what would let them through (`--mode accept-edits` for edits, a rule for a command). Give the
run what it needs with `--allow` (repeatable, for this run only: `--allow 'Bash(go test:*)'`, or `--allow tests` for the build and
test commands of most projects), `permissions.allow` in the configuration, or `--mode`. `tests` stands for `go test|build|vet`,
`gofmt`, `cargo test|build|check|clippy|fmt`, `npm test` and `npm run test|build|lint`, `pnpm test`, `yarn test`, `node --test`,
`pytest`, `python -m pytest|unittest`, `mvn test`, `gradle test`, `dotnet test|build`, `make test|check|build|lint` and `ctest`: they
run the project's own code, as running its tests is meant to, and never install a package, download, or hand an interpreter a
program of its own. In a terminal the question is
`allow? [y]es once / [a]lways this session / [n]o` (`run`, and the line chat); `a` adds a rule for the rest of the session, the exact command or, for the runner commands of `docs/SECURITY.md` (`go test`, `npm run`, `make`...), its prefix (it is not
written to any file), and anything but `y`/`a` refuses. The chat program asks with a box instead, answered by `1` (yes),
`2` (yes, and do not ask again for this exact request: the same rule as `a`) or `3` (no; `esc` and Ctrl-D refuse too).

### Rules

A rule is `Tool` or `Tool(pattern)` in `allow`, `ask` or `deny`.

| Rule | Matches |
|---|---|
| `Bash(go test:*)` | a shell command whose words start with `go test` (`:*` or a trailing ` *` allows any further arguments) |
| `Bash(make lint)` | exactly `make lint` (`make lint extra` is not covered) |
| `Bash` or `Bash(*)` | every shell command |
| `Read(./.env)`, `Read(~/.ssh/**)` | reads of those paths **by any tool**, the shell included (`cat .env` counts) |
| `Edit(docs/**)` | writes to matching paths, by the edit tools or by shell redirections |
| `Read`, `Edit` | every read, every write |
| `WebFetch(domain:example.com)` | requests to that host and its subdomains (`domain:*.example.com`: subdomains only) |
| `WebFetch(https://api.example.com/*)` | requests whose URL matches the wildcard |

Tool names are case-insensitive and ignore `_` and `-` (`WebFetch` is `web_fetch`). `Read(...)` and `Edit(...)` are
about access, not about which tool did it: they apply to the file tools and to the paths a shell command reads or
writes. `WebFetch` matches `web_fetch` and `WebSearch` matches `web_search`. Any other name (`skill`, `recall`, ...)
matches a tool by name.

Path patterns are gitignore-like: `**` spans directories, `~` is your home, `./x` and `x/y` are relative to the
workspace, an absolute path is itself, and a pattern without a slash matches at any depth (for `deny` and `ask`
anywhere on disk, for `allow` inside the workspace). A pattern covers the directory it names and everything below it.
Paths are compared after symlinks are followed, and an `allow` rule only ever matches the resolved path.

`Bash` rules match the parsed command. Every simple command in a line is judged, including those inside `$(...)`,
backticks, `eval` and `sh -c` strings, and a line is allowed only if every part is. An `allow` rule sees the command as
written (`./git` is not `git`); `deny` and `ask` rules also see through `sudo`, `env`, `nohup`, absolute paths and
quoting. Commands the engine cannot analyse (variable-built names, unterminated quotes, `curl | sh`, ...) are never
auto-allowed; only `bypass` lets them pass.

### Built-in protections

Refused whatever the mode or your rules: any access to `~/.ssh` private keys (and writes to anything in `~/.ssh`),
`~/.aws`, `~/.gnupg`, `~/.config/gcloud`, `/proc/*/environ`, `/etc/shadow`; writes to `.git/**` (change repositories
with `git` commands, which are judged as commands); writes to `/etc`, `/usr`, `/bin` and other system directories;
recursive delete, chmod or archive of a directory that contains any of those (`rm -rf ~`, `rm -rf /`).

Denied unless an `allow` rule names the path (a blanket `Read` does not count): `.env` and `.env.*` files (except the
conventional templates `.env.example`, `.env.sample`, `.env.template`, `.env.dist`, `.env.tpl` and `.env.defaults`, which
hold placeholders; `.env.example.local` is not a template), `~/.ssh` public keys, `known_hosts` and `config` for reads, and token files such as `~/.npmrc` and
`~/.git-credentials`.

Writes into the directories that hold hooks, skills, commands, agent definitions and project configuration always
ask, in every mode: `docs/EXTENDING.md`.

### What a decision looks like

A refusal reaches the model as a tool error naming the cause. These are real messages from a run with `allow:
["Bash(go test:*)", "Edit(docs/**)"]`, `ask: ["Bash(git push:*)"]` and `deny: ["Bash(curl:*)", "Edit(./secrets/**)"]`,
unattended, in `default` mode:

```text
bash  make lint extra   -> permission denied: approval required: default mode: make lint extra: not on the read-only allowlist
bash  curl https://...  -> permission denied: denied by rule Bash(curl:*)
bash  git push origin   -> permission denied: approval required: rule Bash(git push:*) requires approval
write secrets/x.txt     -> permission denied (write secrets/x.txt): denied by rule Edit(./secrets/**)
read  .env              -> permission denied (read .env): built-in protection: ... is an .env file and may hold secrets
bash  cat ~/.ssh/id_rsa -> permission denied: built-in protection: ~/.ssh holds credentials: ~/.ssh/id_rsa is protected
bash  sudo ls           -> permission denied: approval required: high risk: it runs with elevated privileges (sudo/doas/su)
write docs/a.md         -> Created docs/a.md (1 line, 6 bytes)
```

### Role profiles

`permissions.roles.<role>` overlays a stricter posture on one role. The role's requests are judged under the session
and under its profile and the **more severe** answer stands: a profile can add denials and questions or pick a
stricter mode, never grant what the session would not. A role that takes a stricter mode than the session is judged
from scratch under it: the session's `allow` rules do not carve exceptions out of the role's mode, only the role's own
`allow` rules do.

Role names: `worker` is the role of a single agent (`chat`/`run` without `--swarm`); a swarm adds `manager`, `backend`,
`frontend`, `fullstack`, `tester`, `reviewer`, `scout`, `docs`, plus roles defined in `.sleipnir/agents/`.
`reviewer` and `scout` are read-only in the engine (plan mode, with `go test`, `go vet`, `pytest`, `npm test`,
`npm run test:*`, `cargo test`, `cargo check` and `make test` allowed so they can verify what they review). Their
profile also denies the job tools `bash_output` and `bash_kill` by name: background jobs are session-wide, and a
reviewer could otherwise read another agent's job output. (In an isolated run the manager has the same profile.)

```jsonc
"permissions": {
  "mode": "accept-edits",
  "roles": {
    "worker":   { "deny": ["Bash(go build:*)"], "ask": ["Edit(docs/**)"] },
    "reviewer": { "deny": ["WebFetch"] }
  }
}
```

A refusal by a role says so: `permission denied: role worker: denied by rule Bash(go build:*)`. Every agent still
sends the same tool list; a role is restricted at run time, never by hiding tools.

## 9. Examples

All four validate with `sleipnir config`. None contains a key: the key stays in the environment.

### (a) An OpenAI-style marketplace (Heimdall)

Heimdall's web site and documentation are at `https://staging.impossiblecarrot.cc`; its API is served from
`https://api-staging.impossiblecarrot.cc/api/v1` (the site host answers 404 for `/api/v1`, so use the `api-` host as the
base URL). Chat completions are at `<base>/chat/completions`, and the catalogue at `<base>/models` is public
(`sleipnir models` works without a key). This block reproduces the built-in `heimdall` provider: with
`HEIMDALL_API_KEY` exported you can leave `providers` out entirely and keep only what you want to change.

```jsonc
// ~/.sleipnir/config.json
{
  "providers": {
    "heimdall": {
      "dialect": "openai-chat",
      "base_url": "https://api-staging.impossiblecarrot.cc/api/v1",
      "api_key_env": "HEIMDALL_API_KEY",
      "headers": { "X-Title": "Sleipnir" },
      "options": {
        "session_header": true,  // send the routing key as X-Session-Id
        "cache_key_body": true   // and as prompt_cache_key in the request body
      }
    }
  },
  "models": { "default": "heimdall/deepseek/deepseek-v4.1-flash" },
  "permissions": {
    "mode": "default",
    "allow": ["Bash(go test:*)", "Bash(make lint)"],
    "deny": ["Read(./.env)", "Read(./secrets/**)"]
  },
  "swarm": { "requests_per_minute": 400, "budget_usd": 5 }
}
```

```sh
export HEIMDALL_API_KEY=...           # create a key in the web UI; never paste it into a file
sleipnir models | head                # catalogue and prices, no key needed
sleipnir doctor --model heimdall/deepseek/deepseek-v4.1-flash --deep
sleipnir chat
```

### (b) Native Anthropic-style routes

Two cases use `"dialect": "anthropic"`: the Anthropic API itself, and a marketplace's Anthropic-style route.

The Anthropic API needs no options; the defaults (explicit cache breakpoints, `x-api-key`, version `2023-06-01`) are
its own. `base_url` is required.

```jsonc
// ~/.sleipnir/config.json: the Anthropic API itself
{
  "providers": {
    "anthropic": {
      "dialect": "anthropic",
      "base_url": "https://api.anthropic.com",
      "api_key_env": "ANTHROPIC_API_KEY"
    }
  },
  "models": { "default": "anthropic/claude-sonnet-5-5" }
}
```

A marketplace route is a different animal: it translates the Messages wire format to whatever engine serves the model,
and what it forwards decides which options you need. Heimdall's route is `POST <base>/messages`, so the same `.../api/v1`
base URL works (the adapter appends `/messages` because the URL ends in `/v1`). Its published Messages documentation
(`https://staging.impossiblecarrot.cc/docs/api/messages`) says that `cache_control` and other Anthropic-specific block
hints are accepted but not forwarded, that no cache usage is reported, that `thinking` is accepted without effect, that
a system turn in the middle of a conversation is folded into the leading system text, that `max_tokens` must be a
positive integer, and that `X-Session-Id` is the way to keep a conversation on one engine. The options below follow
from that:

```jsonc
// ~/.sleipnir/config.json: a marketplace's Anthropic-style /messages route
{
  "providers": {
    "heimdall-messages": {
      "dialect": "anthropic",
      "base_url": "https://api-staging.impossiblecarrot.cc/api/v1",  // requests go to <base_url>/messages
      "api_key_env": "HEIMDALL_API_KEY",
      "options": {
        "cache_control": false,           // the route does not forward cache markers: plan none
        "no_turn_scoped_system": true,    // fold mid-conversation system text into user text
        "no_zero_max_tokens": true,       // warm-ups send max_tokens 1, not 0
        "session_header_name": "X-Session-Id"  // routing key for engine affinity
      }
    }
  },
  "models": { "default": "heimdall-messages/deepseek/deepseek-v4.1-flash" }
}
```

**What is and is not verified.** The URL, headers (`auth_style`, `betas`, `session_header_name`, custom headers),
`cache_control` markers and `max_tokens` that the adapter sends for these options were checked against a local
stand-in server; `no_turn_scoped_system` and `no_zero_max_tokens`, which only matter for a swarm's hot view and cache
warm-ups, were read from the code, not exercised. **One live endpoint has been measured, once**: Heimdall's `/messages`
route with exactly the options above (with `deepseek/deepseek-v4.1-flash`, September 2026). `doctor` found streaming,
usage, and a tool-calling round trip working, no exact cost and no cache reads reported (as the route's documentation says:
the bill is then priced from the endpoint's catalogue), and no reasoning exposed; a six-step agent task (list, write, read,
run a command) completed and was priced from the catalogue. That says nothing about `betas`, `max_breakpoints`,
`thinking_*` or `extra_body`, nor about any other gateway: how a real one reacts to `cache_control` is unmeasured, and
the values above come from the marketplace's documentation plus that one measurement. Measure your own endpoint:

```sh
sleipnir doctor --model heimdall-messages/deepseek/deepseek-v4.1-flash --deep
```

`doctor` works with any configured provider, either dialect. It runs a series of real requests and reports whether
streaming works, whether usage and exact cost are reported, whether tool calling round-trips, whether cached tokens are
shown and how many of nine repeated prefixes hit the cache (`yes`; `partly` when some did and some did not, which is
what an endpoint that serves one conversation from several engines looks like; or `NO`), the cache granularity and the
smallest cached size (`--deep`), whether a parallel burst needs a warm-up (`--deep`), whether reasoning is exposed, and (`--capture`) whether
token ids come back. It prints the cost of the probe. Add `--json` for a machine-readable report, and `--no-affinity`
to send no routing key. Acting on it: if `prefix cache works` is `NO` on the Messages route but `yes` on the chat route
of the same marketplace, use the chat route (example (a)); the docs of the harness recommend that route for
cache-aware runs on Heimdall (`docs/ARCHITECTURE.md`, `docs/VALIDATION.md`).

### (c) A local vLLM or SGLang policy server for RL data capture

Rollouts need token ids and logprobs from the policy. A self-hosted OpenAI-compatible server (vLLM, SGLang) can return
them; hosted gateways cannot.

```jsonc
// ~/.sleipnir/config.json: a self-hosted vLLM or SGLang server as the policy to train
{
  "providers": {
    "local": {
      "dialect": "openai-chat",
      "base_url": "http://127.0.0.1:8000/v1",
      // "api_key_env": "LOCAL_LLM_KEY",   // only if the server checks a key
      "options": {
        "capture_tokens": true              // this server returns token ids and logprobs
        // "extra_body": { "top_k": 20 }    // any extra request member the server accepts
      }
    }
  },
  "models": { "default": "local/my-policy" }
}
```

Put this in the **user** file: `rl` commands never read provider settings from a project file. Alternatively skip the
provider entry and name the server on the command line: `--model my-policy --base-url http://127.0.0.1:8000/v1`.

```sh
sleipnir doctor --model local/my-policy --capture     # do token ids come back? logprobs? are they prefix-stable?
sleipnir rl rollout --tasks tasks/all.train.jsonl --model local/my-policy --group 8 --capture --out runs/r1
```

With `--capture` the request carries `logprobs: true` and `return_token_ids: true`. Without it, nothing extra is sent,
even though `capture_tokens` is set: the option only declares what the server can do. RL policies must speak
`openai-chat`; a provider with another dialect is refused (`docs/TRAINING-DATA.md`).

For illustration, this is the capture line of `doctor --capture` against the harness's own mock server (`sleipnir
mock`, which imitates vLLM's token ids); your server's answer is the one that counts:

```text
  token ids            yes (logprobs yes, prefix-stable for packing yes)
```

## 10. Troubleshooting

| Message | Cause |
|---|---|
| `no model configured: pass --model provider/model, set SLEIPNIR_MODEL, or write one into your config with ...` | no model given and `models.default` unset |
| `no provider configured: set HEIMDALL_API_KEY (or OPENROUTER_API_KEY / OPENAI_API_KEY) or define one under "providers" ...` | a bare model id and no default provider (section 6), or no built-in key set |
| `provider "x" needs NAME to be set` | the provider's `api_key_env` variable is empty in this shell |
| `provider "x" has no base_url` | a configured provider without `base_url` |
| `not signed in with ChatGPT: run sleipnir login chatgpt` | a model of the `chatgpt` provider was asked for with no sign-in (or after `sleipnir logout chatgpt`) |
| `the ChatGPT sign-in has ended (...): run sleipnir login chatgpt` | the issuer refused to renew the sign-in (revoked, or unused for long): sign in again |
| `ignored security-sensitive settings from the project's config (...)` | the project file set gated keys; `--trust-project` applies them (section 4) |
| `unknown key "modles" (did you mean "models"?); it is kept but has no effect` | a misspelled key; nothing reads it |
| `bypass mode turns off permission prompts except for very dangerous commands; use it only where a mistake is cheap` | `permissions.mode` is `bypass` |
| `yolo mode never asks anything, dangerous commands included; use it only inside a sandbox` | `permissions.mode` is `yolo` |
| `provider: auth (http 401): Missing or invalid API key` | the key variable is empty, wrong or expired. `sleipnir doctor --model provider/model` shows the answer without starting a session |
| `429` from the endpoint | lower `swarm.requests_per_minute` |
| `server (http 503): ... retrying in 28s (attempt 9, waited 4m0s of 5m0s for the endpoint)` | the endpoint answered that it is down or overloaded (a status of 500 or more, or 429). Every failure gets six attempts; for this kind the agent goes on, a wait of at most half a minute between attempts, until the waits add up to five minutes (`session.DefaultOutagePatience`), and then the run ends with the error. Ctrl-C stops the wait. A refusal of the request (400, 401, 403, 404) and a failure with no status (a misspelt URL, a refused connection) are not waited for |

`sleipnir config` lists every warning once, with file, line and column, under "warnings:", and ends with
`configuration is valid, with N warning(s) listed above` (or plain `configuration is valid`). An error stops it and is
printed instead.
