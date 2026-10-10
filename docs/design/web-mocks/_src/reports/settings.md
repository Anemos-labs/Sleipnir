# Report: config, models, trust, MCP, schedule and the CLI tools as APIs (2026-10-09)

Read-only survey. Shorthand: `cmd/` = cmd/sleipnir/, `i/` = internal/. H = `$HOME/.sleipnir` (config, auth.json, chatgpt.json, update.json, user skills/commands/agents). S = `$SLEIPNIR_HOME` or H (sessions, trust.json, mcp-approvals.json, schedule.json, cache/). `SLEIPNIR_HOME` moves only S (docs/CONFIGURATION.md section 1). (Saved by the coordinator from the investigator's report.)

## Headlines

1. Nearly everything needed is already exported in `internal/*`: config, session, trust, perm, mcp, skills, commands, hooks, sched, chatgptauth, gateway, update, inspect, friction, probe. What is trapped in package main is the glue: the model catalogue and favourites, login and key storing, flag-to-Options mapping, the schedule daemon, doctor, `mcp test`, session listing, and redaction.
2. Running the same binary as a subprocess is a poor fit.
   - Output is human text. Only `config --json` (no provenance), `doctor --json`, `sim --json`, `friction --json`, `inspect --json` and `run --json` are machine-readable.
   - Most commands use `flag.ExitOnError`, so a bad argument calls `os.Exit(2)`. That includes doctor (main.go:426), models (cmd/models.go:149), init and config (cmd/setup.go:38,136), login, sim, recon, update and daemon.
   - Commands write straight to `os.Stdout` and `os.Stderr`.
   - `harden.MoveKeys` (main.go:45) unsets exported API keys from the environment, so a child sleipnir sees only keys stored in auth.json. Only `jobEnv` (cmd/schedule.go:198) puts the held keys back.
3. There is a precedent server: `i/inspect/server.go`. It has embedded assets, a Host allowlist (:255), token plus a SameSite=Strict cookie (:303), CSP (:33), and a loopback-only guard (:223-250). It is GET-only, so it has no CSRF or Origin handling. `sleipnir web` will have mutating endpoints.

## UI feature to Go entry point ("In-proc" = callable from a server as-is)

| Feature | Entry point (file:line) | In-proc? | Disk state | Notes and risks |
|---|---|---|---|---|
| Models catalogue (ctx, $/M, tools/reasoning, filters) | `gateway.Fetch` i/provider/gateway/catalog.go:222 (unauthenticated GET `<base>/models`; `Entry` :46). Main-only wrappers: `fetchModels` cmd/models.go:251, `modelFilter.keep` :49, `usableProviders` :87, `localSources` :109, `planModels` :294 | Fetch yes; the rest needs refactor | S/cache/catalog-<h>.json, 6h TTL, written only by session enrichment (session/providers.go:675) | `cost.Model` has no JSON tags, so a DTO is needed. Plan models have no price. Local servers give ids only. `sleipnir models` itself has no picker. |
| Favourites | `toggleFavorite` cmd/models.go:398, `saveFavorites` :417 -> `config.Save` | Save yes, wrappers main | H/config.json `models.favorites` | `Save` (i/config/save.go:30) drops comments, sorts keys, takes no lock. |
| Manager and per-role model, effort, budget | `config.Save` patch of `models.default` / `models.roles.<role>` (config.go:160) and `swarm.budget_usd` (:255). Live: `Session.SwitchModel` session/switch.go:22, `CheckModel` :60, `SetBudget` :79, `RoleModels` :121, `SetEffort` session/effort.go:13. `provider.ModelEfforts` provider/effort.go:73 | yes | config, plus the `session.effort` event in the log | Effort has no config key. `SwitchModel` refuses swarms; the chat restarts the team instead (cmd/chat_tty.go:536). Role names: `swarm.BuiltinRoles` swarm/roles.go:35, mailman, compactor, agent defs. |
| Permissions: mode, rules, origin | `config.Load` load.go:188 (`Report.Origins` per leaf), `perm.ParseRule` perm/rule.go:52, `Engine.Rules` engine.go:422 | yes | config layers | Origin per rule is NOT available. A merged list records only its last supplier (config/merge.go:129-137), and `layer` is unexported. A new config API is needed. A project can only add to deny/ask. Its allow and mode are dropped when untrusted (config/trust.go:14). |
| `tests` preset | `perm.TestsAllow` perm/presets.go:7 | yes | none | The summary text and `expandAllow` are in main (cmd/allow.go:41,48). |
| "Would it ask?" tester | `perm.NewEngine` engine.go:222 + `Check` :376, a recording Prompter, `Config.Audit` :65 | engine yes; faithful config no | none | The real engine config is only in unexported `Session.buildPerm` (session.go:506): `protectedConfigDirs` (ext.go:140), read-only role profiles, manager in plan mode, StateDir. The Request must be built by hand (bash: Tool "bash", Command, Cwd, Writes; tools/shell/bash.go:117). |
| Trust: files, hash, add/forget/list | `trust.Scan` trust/trust.go:85, `Ledger` ledger.go:40-126, `session.TrustLedgerPath` session/trust.go:47. Print-only wrappers cmd/trust.go:96-230 | yes | S/trust.json `{version, projects{cwd:{digest, files{path:sha16}, saved}}}`, atomic | Keyed by cwd, not root. A partial footprint cannot be remembered. The ledger doc says concurrent writers can lose an update (ledger.go:36). |
| MCP: list, approve, revoke | `session.MCPEntries` session/mcp.go:422, `OpenMCPApprovals` :393 (`Approve`/`Revoke`) | yes | S/mcp-approvals.json `{approved{root{fingerprint:name}}}` | Keyed by project root. `approvalStore` loads once and does not re-read on write (:313-377), so a running session overwrites CLI changes. |
| MCP: test, reconnect, tools | `mcpTest` cmd/mcp.go:159 (main); `Session.MCPReconnect` :266, `MCPStatus` :249, `MCPToolNames`; `mcp.NewManager` mcp/manager.go:218 | test: refactor. reconnect: live session only | `mcp test` leaves a session dir | `mcpTest` calls `session.New`, which creates a session directory. It prompts on the TTY for project entries. Reconnect has no CLI. The start/approve/env wiring lives in `Session.startMCP` (:55). |
| Skills, commands | `skills.Discover` skills/skills.go:172, `commands.Load` commands/commands.go:198 | yes | `<root>/.sleipnir\|.claude/{skills,commands,agents}`, H and `~/.claude` equivalents | No CLI. Project entries need `TrustProject`. Editing is plain file writes. |
| Hooks | `hooks.ParseAs` hooks/parse.go:49, `Set.Hooks` types.go:138, `hooks.Events` events.go:30 | yes | `hooks` key in config | No CLI. Hooks are opaque per layer and additive for project layers (merge.go:82). Once a project is trusted, its hooks run as user origin (session/hooks.go:30). `Hook.Headers` can hold credentials. |
| Providers and login | `session.ProviderNames` providers.go:139, `LookupProvider` :225, `ProviderInfo` :236, `ProviderReady` :336, `config.SaveStoredKey` config/auth.go:47. Main-only: `loginChoices` cmd/login.go:32, interactive `login` :100, `checkKey` cmd/pick.go:230 | partial | H/auth.json `{ENV_NAME: key}`, 0600, atomic | `checkKey` sends a real 8-token request. Providers: chatgpt, heimdall, openrouter, openai, anthropic, ~27 hosted, 6 local (providers.go:40-107). |
| ChatGPT plan | `chatgptauth.Login` chatgptauth/login.go:61 (`LoginIO{Out, Open, Paste}`); `Open`, `Connected`, `Who`, `Logout`, `Models` chatgptauth.go:158-491 | yes | H/chatgpt.json (access, refresh and ID tokens), 0600 | `Login` blocks up to 5 minutes, so run it async. A custom `Open` can hand the URL to the browser. The redirect is `127.0.0.1:<random>`, so the browser must be on the same machine, otherwise use `Paste`. |
| Config layers | `Report{Layers, Sources, Origins, Warnings, ProjectRisks}` config/load.go:45 (JSON-tagged) | yes (in-proc only) | H/config.json, `<root>/.sleipnir/config.json`, `config.local.json`, `SLEIPNIR_*` (`EnvVars` env.go:100) | `cmdConfig` prints to stderr, and `--json` has no provenance. The Cwd/Root must be passed per project. |
| Run settings | `session.Options` session.go:55 (Swarm, Workers, Isolation, Verify, Commit, `Mailman *bool`, BudgetUSD, RoleModels) | yes | config `swarm.*` | Flag mapping lives in main: `teamOf` cmd/chat.go:59, `chatOptions` :244, `finishRun` cmd/run.go:411. There is no config key for verify or commit. |
| Appearance | none | n/a | none | Only env vars are read (`NO_COLOR`, `REDUCE_MOTION`, `SLEIPNIR_ANIM`; tui/term/caps.go:215). Keep it browser-local. |
| Sessions: list, resume, prune | `inspect.Sessions` inspect/registry.go:397 (digest with model, cost, goal; Live is a mtime heuristic :362). Main versions: `printSessions` cmd/setup.go:263, `planPrune` cmd/sessions_prune.go:50. `session.Resumable` resume.go:91, `ResolveResume` :23, `RemoveStoredSession` session/prune.go:20 | mixed | S/sessions/<id>/{events.jsonl, blobs, checkpoints, tmp, .lock, .pruned} | `RemoveStoredSession` is flock-protected, salvages git worktrees, and reports `BranchesKept`. Resuming a session that another process holds fails on `.lock`. |
| Sessions: rename, close | do not exist | no | none | Rename needs a sidecar file. Do not append to `events.jsonl` from outside; it is single-writer. Close exists only for a session the server hosts (`Session.Close` session.go:1199). A hosted session keeps `.lock` until closed, so prune fails on it. |

## Tools catalogue (every command)

| Command | Entry | Subprocess-safe? | TTY | Network |
|---|---|---|---|---|
| config | cmd/setup.go:135 | read-only, but stderr text | no | no |
| init | :37 | writes `.sleipnir`, `AGENTS.md` and `.gitignore`; refuses to overwrite | `--user` with no flags runs the picker on a TTY | no |
| sessions | :249 | read-only | no | no |
| sessions prune | cmd/sessions_prune.go:161 | dry run by default; `--yes` is idempotent and lock-safe | no | no |
| models | cmd/models.go:145 | read-only | no | per-provider `/models` |
| models fav | :506 | idempotent config write | no | no |
| doctor | main.go:425 | read-only; `--json` | no | billable POSTs |
| recon | run.go:429 | read-only; `session.BuildRecon` is exported | no | no |
| login / logout | cmd/login.go:232,268 | key via stdin pipe, not argv; logout idempotent | secret prompt | check request; ChatGPT: auth.openai.com |
| trust | cmd/trust.go:49 | `add` needs `--yes`; others idempotent; text only | prompt | no |
| mcp | cmd/mcp.go:39 | `approve` needs `--yes`; `test` runs servers | prompt | servers |
| schedule | cmd/schedule.go:34 | `add` is not idempotent | no | no |
| daemon | :121 | NOT lock-guarded; two daemons can double-start a job | no | the runs |
| update | cmd/update.go:69 | `--check` is safe; install replaces the executable | no | api.github.com + release |
| sim | cmd/sim.go:20 | pure, offline, `--json` | no | no |
| friction | cmd/friction.go:27 | read-only, `--json` | no | no |
| inspect | cmd/inspect.go:28 | `--json` one-shot; otherwise a server on :8787 | no | no |
| replay | cmd/watch.go:166 | `--final` and `--record` need no TTY | default needs one | no |
| watch | :303 | TTY only | yes | no |
| demo | cmd/demo.go:26 | writes a temp dir | cockpit on a TTY | no (mock) |
| mock | main.go:516 | a server | no | no |
| rl | cmd/rl.go:61 | long, billable; `serve` is a server | no | endpoints |
| chat | cmd/chat.go:73 | TTY program | yes | yes |
| run / swarm | cmd/run.go:69 | headless; see `Options` above | no | yes |

TTY-only parts:
- Hidden key prompt (`readSecret`, login.go:51).
- Model picker `pickModel` (pick.go:30), reached only from chat start and `init --user`.
- y/N prompts of `trust add` (trust.go:160) and `mcp approve` (mcp.go:143).
- The in-session approval Prompter. Return `Decision{Allow, Remember: perm.ScopeProject}` to remember a trust or MCP answer (sink.go:358-385).
- `watch`, and `replay` without `--final`/`--record`.
- ChatGPT sign-in opens a browser via xdg-open; headless falls back to `Paste`.

## What must leave package main

1. Model catalogue and favourites (models.go:31-139,238-308,397-428) -> a new package returning tagged DTOs.
2. Key management: split `loginChoices`, `checkKey`, and the stored-key save/forget (with `harden.Provide`) from the interactive `login()`, so there is a non-interactive provider-status service.
3. Four copies of the state-directory logic: `stateDir` (cmd/rl_run.go:371), `sessionsRoot` (cmd/watch.go:37), cmd/setup.go:266, `session.stateRoot` (session.go:465). Also `userHome` and the update.json path. Export one `StateRoot`.
4. Session listing and prune planning (`summarize`, `planPrune`, `dirSize`, `parseAge`) into `session`, plus a real liveness probe (export the `lockDir` check) and rename metadata.
5. `projectFootprint`, `mcpTest` and `noModel`, `mcpApproval`: return structs; `mcpTest` should not leave a session dir.
6. Extract `NewPermEngine` from `Session.buildPerm`; move `expandAllow` and `testsPresetSummary` out; export the enum lists (config/validate.go:19-24); add a per-layer values API for rule origins.
7. Schedule store, `daemonTick`, `runJob` and `jobEnv` into `i/sched`, and add a lock.
8. `cmdDoctor` body and `providerFlags`/`headerRecorder` (cmd/provider.go) -> a function returning `probe.Report`; `runUpdate` with version/commit passed in.
9. Run glue: `teamOf`, `defaultTeam`, `chatOptions`, `finishRun`, `endReasonOf`, `restartArgs`.
10. Convert every reusable command from `ExitOnError` and bare stdout/stderr to `ContinueOnError` and `io.Writer`.
11. `redactedConfig` (cmd/setup.go:202) into `i/config`, covering more than MCP.

## Secrets-handling hazards

1. `config.StoredKeys` (auth.go:22) returns plaintext, and `harden.Secret` and `harden.Held` hold keys. Expose only `{provider, env_name, present, source}`. Take a new key only in a POST body, never in a URL or argv (`/proc/<pid>/cmdline` is world-readable), and don't log bodies. After save call `harden.Provide` (login.go:197). After removal call `harden.Provide(env,"")` (pick.go:248); `cmdLogout` (login.go:292) doesn't, which is fine for a short-lived process but not for a server.
2. The key source (env or stored) cannot currently be told apart, and there is probably a precedence bug. `MoveKeys` unsets env keys first (main.go:45). `LoadStoredKeys` then calls `Provide`, which checks only `os.LookupEnv` (harden.go:231-242). So a stored key appears to overwrite a moved env key. That contradicts "environment wins" (login.go:198, SECURITY.md:176). Please verify; no test covers this order. To show the source, capture `Status.Moved` from `harden.Process` before `LoadStoredKeys`; main discards it.
3. chatgpt.json: expose only `Connected` and `Who()` (email). Never return the `connection` struct. The `Login` Out text contains the auth URL, and `Paste` carries the callback code.
4. Config dumps leak. Providers' `Headers` (config.go:114), hooks (command lines, `Hook.Headers`), and MCP (env, headers, URL path tokens) are all serialisable. `ServerConfig.MarshalJSON` (mcp/config.go:998) is unredacted. `Redacted()` (:901) keeps args as written, and `Describe` prints args. `config --json` redacts only MCP. `scanSecrets` merely warns. Use allow-list DTOs.
5. Session logs and blobs are verbatim prompts and tool output (SECURITY.md:75). Treat session endpoints like key endpoints. Untrusted text (MCP `Instructions`, skill descriptions, hook output) must be escaped in the DOM.
6. The web API is itself a privilege escalation path for a prompt-injected agent. `Engine.SetMode` (engine.go:287) is unguarded. The API could also add trust, approve MCP servers, schedule yolo runs (schedule.go:85), and run `update`. The agent shares the user's uid, and `curl` is limited only by permission rules (SECURITY.md:143). Needed: a per-launch random token; no token in env or argv (inspect accepts `--token` and prints it in the URL); an HttpOnly SameSite=Strict cookie; a Host allowlist; an Origin check and JSON content-type on every POST; loopback only. New code must read credential-like variables through `harden.Secret` (`TestReadersOfCredentialsUseSecret`, harden/readers_test.go:40).
7. A subprocess needs held keys re-injected into its environment (the `jobEnv` pattern). In-process avoids that.
8. `${NAME}` references in MCP entries resolve held keys (session/mcp.go:84-89). The approval UI must show `EnvRefs()`, as `Describe` does.
9. Concurrent writes: config, auth, chatgpt, trust, approvals and schedule are all read-modify-write with an atomic rename and no cross-process lock. Serialise per file in the server and re-read before write.
10. `config.Save` strips JSONC comments, so warn before the first UI write. `Config.Persist` (the "remember for project" hook) is unused in production, so project-scope remembers never write rules.
