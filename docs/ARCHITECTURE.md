# Sleipnir architecture

Sleipnir is a coding-agent harness for existing OpenAI-style and Anthropic-style model endpoints. One manager
brain, many legs: it is built to run **10–50 agents over one repository** whose prompts share a provider-cached
prefix, so dispatching an agent costs almost nothing and needs no briefing. It reaches feature parity with the
major harnesses (tools, permissions, sessions, MCP, skills, hooks) and adds three things they lack:

1. a **layered, generational prompt-cache engine** (`docs/CACHE-DESIGN.md`),
2. a **swarm runtime** with a shared board, typed mail, leases and harness-owned "done" (`docs/SWARM-PROTOCOL.md`),
3. an **RL environment**: every run is recorded so that its exact prompts, completions and token ids can be
   rebuilt and scored by verifiable rewards, and exported as trainer-ready data (`docs/TRAINING-DATA.md`).

## Principles

* **Layout follows volatility.** Stable bytes first, volatile bytes last; every change to a stable layer is a
  priced, declared, batched event. Bytes are a pure function of content.
* **The log is the truth.** Everything (transcripts, layer versions, board, mail, costs) is written through an
  append-only event log or derivable from it. Crash recovery, resume, replay, simulation and training data fall out.
* **The model proposes; the harness decides.** Compaction patches, task status, "done", leases, promotions and
  permissions are validated and applied by deterministic code. Models never edit shared state directly.
* **Reversible by construction.** Nothing folded out of a prompt is lost: archive + `recall`.
* **Requests, not tokens, are the scarce resource** on marketplaces (600 req/min/key): parallel read-only tool
  calls, batched edits, a governor with priorities, and a manager that sleeps (`wait`) instead of polling.
* **Provider-neutral core, provider-specific adapters.** No provider name appears outside `internal/provider`,
  `internal/cost` and configuration.

## System map

```
 CLI (chat · run · swarm · rl · inspect · …)  /  web inspector  /  headless JSON events
                │
            session ── config (trust-gated layers), memory files, skills, hooks, MCP servers,
                │      permission engine, checkpoints, one-writer session directory, resume
                │
     ┌──────────┴───────────── swarm ─────────────────────────────┐
     │ manager ─ spawn/wait/task ─► workers (roles)                │
     │ board (immutable snapshots)  mail router (+ optional mailman)│
     │ leases + scopes   governor   warm gate   hot view            │
     │ manager stop guard + wake                                    │
     │ optional worktree isolation ─► verifying merge queue         │
     └──────────┬──────────────────────────────────────────────────┘
                │ each agent
             agent loop ── tools (fs, shell, web, recall, skill, swarm, MCP) ─► workspace (shared | worktree)
                │   render          ▲ compaction patches (fork of the agent's own request)
                ▼                   │
               kv  ─ layers · stack · renderer · breakpoints · guard · patch/apply · planner · archive
                │
            provider adapters (openaichat · openairesp · anthropic) ─► endpoints
                │
        events (append-only JSONL) + blobs (content-addressed)   ◄── every layer above writes here
```

## Package map

`docs/BUILDING.md` lists every package with its one-line job; this is how they fit together.

| Package | Job |
|---|---|
| `internal/core` | provider-neutral vocabulary: `Turn` (with an `Origin`: user, model, tool, mail, system, digest, task), `Block` (with verbatim `Wire`), `Prompt`, `Usage`, canonical JSON, token estimator |
| `internal/events` | append-only event log (torn-tail recovery, group commit with a flush timer, lossy-but-never-blocking subscribers), blob store (hash-verified, private) |
| `internal/cost` | provider cache models, model prices (validated at the boundary), input-token-equivalent weights |
| `internal/kv` | **the cache engine**: layers, stack, renderer, breakpoint planner, drift guard, compaction patch/apply/planner/fork, archive, and the escaping that keeps text it did not write inert |
| `internal/provider` | `Provider` interface, errors, SSE and stream limits, watchdog, endpoint rules (where a key may go); `openaichat` and `anthropic` adapters; `gateway` (marketplace catalogue); `probe` (endpoint doctor); `mock` (deterministic test servers; the chat-completions one models an *automatic* prefix cache only, and explicit-breakpoint caching lives in a separate engine, so a test passing against one says nothing about the other's rules) |
| `internal/agent` | the loop: render → call (retry, governor, gate) → tools (parallel read-only, ordered writes, per-turn and per-call budgets) → boundary (compaction, epochs); snapshot and restore |
| `internal/swarm` | board, mail router (and the optional mailman), leases, governor, warm gate, hot view, roles, spawn/dispatch, coordination tools, evidence, the manager's stop guard and wake, worktree isolation |
| `internal/tools` | tool contract, file-state staleness tracker, truncation with recall handles; `fs`, `shell`, `web`, `recall`, `skilltool`; MCP tools join the same registry |
| `internal/perm`, `internal/shellparse` | permission modes, rules, role profiles and confinement; bash analysis. Nothing defaults to allow-all |
| `internal/harden` | the harness process made opaque to the commands it runs: non-dumpable, environment erasure, provider keys held in memory (`harden.Secret`) |
| `internal/checkpoint` | pre-modification snapshots and rewind |
| `internal/config`, `internal/memory` | layered JSONC config with trust gating of project layers; AGENTS.md/CLAUDE.md-style instruction files → shared-layer text |
| `internal/skills`, `internal/commands`, `internal/agentdefs`, `internal/hooks`, `internal/mcp` | the ecosystem: skills (listing in the shared layer, bodies on demand), slash commands, markdown role definitions, hooks, the MCP client |
| `internal/workspace`, `internal/gitx` | git worktrees per writer and the verifying merge queue; the only gateway to the git binary |
| `internal/session` | assembles provider, tools, permissions, checkpoints, layers (constitution, shared pin from `recon` + instruction files, role pins), event log and one agent or a swarm; the CLI, the RL harness and tests all build sessions the same way |
| `internal/tui` | the terminal interface (`docs/UX.md`): `term` (raw mode, capabilities), `cell` and `render` (what a screen is made of, drawn inline or full-screen), `input` (the editor), `widget`s, `state` (every figure on screen is a reduction of the session log), `vt` (the emulator the tests read the screen through), `svg` (recordings) and `app`, the programs: the inline `chat`, `watch`, `replay`. The programs read the log and receive a sink and a prompter that only forward; nothing in them changes what the model sees |
| `internal/friction`, `internal/stats`, `bench/` | the measuring side: the friction miner over event logs (`sleipnir friction`), the statistics of reports (Wilson intervals, paired bootstrap), and the benchmark suite (fixtures, lock file, `scripts/bench.sh`; `docs/BENCHMARKS.md`) |
| `internal/ptytest` | a pseudo-terminal harness for running the real binary in tests |
| `internal/inspect`, `internal/demo` | the cache inspector (a read-only model of a session log and a web dashboard); the scripted team behind `sleipnir demo` |
| `internal/rl` | RL vocabulary (`Episode`, `Step`, `Task`, rewards, flags) and its subpackages: `env` (tasks, isolated rollouts, clean-checkout verifier, task generators, eval, rollout server), `traj` (event log -> episode, exact prompt replay), `reward` (components, hack detectors, repricing, probes), `adv` (group advantages), `export` (steps, tokens, groups, sft, dpo, kto, atif), `redact`, `recall` (memory tasks), `harness` (implements `env.Harness` on `session`: the rollout runs the real assembly under a fixed config, a scrubbed shell environment, refused prompts and hard budgets) |
| `cmd/sleipnir` | the binary: every command, the chat (the inline program on a terminal, the line REPL on a pipe or with `--plain`), the RL subcommands |

## One request, end to end

1. **Boundary.** The agent commits a ready compaction patch if the planner says the moment is right, or starts a
   background compactor, or installs a new shared epoch. Both commits are *declared rebases*: they rewrite the
   thread (and drop its thinking blocks, which no longer match the bytes before them) in one step under one lock.
   Thinking is never stripped on its own, because a strip is itself a prefix change and is priced as one.
2. **Render.** `kv.Render` builds the prompt from the stack snapshot: tools, constitution, then message 0 =
   `<shared-context>`, `<role-context>`, `<my-notes>`, `<history>`, then the thread, then the hot view. The hot view
   is delivered in one of three modes (`kv.HotMode`, `docs/CACHE-DESIGN.md` §3): inline after the last marker
   (default, ephemeral, rebuilt every request); persisted on change as a frozen block in the next user turn (forced
   where preserved thinking is replayed, since an ephemeral tail would change bytes the signatures bind to); or as a
   turn-scoped system message on a provider that supports it. `kv.PlanMarks` then places breakpoints for the
   provider profile.
3. **Guard.** `kv.Guard` compares the prompt (request parameters, tools, system, every block) with the previous
   request; an unexplained change in the common prefix is a `cache.anomaly`, a declared rebase is not. After the
   response, cache reads are compared with what the guard expected, but only on a provider that reports cache usage
   and only inside the TTL.
4. **Gate + governor.** The warm gate elects a primer for each cold prefix level (shared, then shard and role) and
   releases the followers when its first byte arrives, inside a window derived from the TTL; the governor admits the
   request by priority within the RPM budget.
5. **Call.** The adapter renders wire JSON without reordering or re-serialising history, streams the reply,
   preserves provider-native blocks verbatim, normalises usage and cost.
6. **Log.** `model.request` (recipe) and `model.response` (usage, hit ratio, gateway cost, expectation) are written;
   the assistant turn is appended to thread, archive and log.
7. **Tools.** Calls run concurrently when read-only, in order otherwise; each write passes permission →
   lease guard → checkpoint → file-state staleness check. Results are truncated with a recall handle.
8. Loop, or return the final answer. A call that fails the same way (same tool, same arguments, same result) eight
   times among the last twenty calls ends the run with `agent stuck`; the model is told once, in its results turn, at the
   fourth (`agent.stuck` events). Refusals of a run with nobody to ask (`run`, `swarm`, a rollout) count together, whatever was
   asked, among the last twenty calls: a model that is refused tries another command each time, so no call repeats; it is told
   at five of the twenty and the run ends at ten. A step limit and a budget are the backstops; this notices that nothing changes.
   An answer that holds the markup of a chat format where a call should be (`<|call|>`, `to=functions.read`, `<tool_call>`: a
   gateway that does not parse the model's format) is not an answer: it is sent back twice at most (`internal/agent/leak.go`,
   `agent.stuck` with `phase: "leak"`), then taken as it was.

**A standing goal** (`/goal TEXT` in the chat; `internal/goal`, `session.GoalTurn`) is kept by the harness across turns and is not a repeat of
the text. The first turn is told to write what the goal requires as steps with the `plan` tool, which the harness already shows back every
request. After each turn a judge (a separate request with no tools, `goal:judge`, on the session's model) is shown the goal, those steps, the
last answer and the results of the turn's last tool calls, and answers `done`, `continue` or `blocked` with a reason and what is missing; it is
told that a claim is not evidence. `continue` sends the agent on with a message at the tail (the cached prefix is untouched) that names what is
missing and the open steps. The loop ends by itself when the judge finds the goal met, when it says only a person can go on, after three turns
without progress (no tool called, or the same reason again), after twenty continuations, when the turn is interrupted, or when the judge cannot be
asked. `/goal` alone says where it stands; `pause`, `resume` and `clear` do what they say. A goal is not kept across a restart of the chat. The judge's requests
are in the session's cost (`/cost`, the budget line), not in any agent's.

## Providers

Two dialects, one `core.Prompt`. `openai-chat` speaks chat completions (OpenAI, Heimdall, OpenRouter, vLLM, SGLang, ...):
automatic prefix caching, a routing key so a gateway keeps a conversation on the engine that holds its cache, exact
gateway costs, reasoning replay, optional token capture. `anthropic` speaks the Messages API (Anthropic, and gateways that
forward its wire format): explicit breakpoints (at most four, a 20-block lookback, 5 minute or 1 hour lifetimes), preserved
thinking, turn-scoped system messages; what a gateway drops is declared per provider in its options. A gateway's
Anthropic-style route may not forward `cache_control` or report cache usage (`docs/research/02-provider-caching.md`), so
`sleipnir doctor --deep` measures a real endpoint (streaming, tools, whether cached tokens are reported, block granularity by
gcd of read-count differences, minimum cached prefix, whether a parallel burst over a cold prefix shares anything,
reasoning round-trips, rate-limit headers) and refines the profile. The `openai-responses` adapter (`internal/provider/openairesp`) serves OpenAI's Responses
API with an API key, and a ChatGPT plan signed in with `internal/chatgptauth` (`docs/PROVIDERS.md`).

The endpoint is not trusted (`docs/SECURITY.md`): a key goes only where the user allowed (`provider.CheckEndpoint`),
redirects stay on the origin, a response is bounded as it is read, a silent server is timed out, and usage, costs and
catalogue prices are validated.

## Workspace safety

Many agents editing one tree is the main hazard (public agent PRs conflict textually 20–42% of the time when they
overlap). Layered defences: **scopes** declared per task and checked at spawn (overlap refused); **write leases**
with TTL and conflict alerts; **content-hash staleness** on every edit (correct even if a lease is stolen);
**checkpoints** before every write; a **writer cap** with unlimited read-only roles; **role gates** (reviewers
cannot write); and, optionally, **worktree isolation** (`swarm.isolation: "worktree"`, `--isolation worktree`): every
writer works in a git worktree of its own, confined to it by the permission engine, and its finished work goes through
a verifying merge queue (`internal/workspace`) before it reaches the person's checkout. `internal/session` builds it
(`isolate.go`), `internal/swarm` runs it (`isolate.go`); see `docs/SWARM-PROTOCOL.md` section 14.

## Security posture

Tool output, web pages, file contents and mail are data. Pins and notes are user-role context, never system. Mail is typed,
rate-limited, deduped, capped, never broadcast, and cannot carry approvals. `docs/SECURITY.md` is the threat model; the
rules the code follows are these:

* **Permissions are code, and they fail closed.** Modes, rules, shell-syntax analysis and role profiles decide; an agent or a
  tool environment built without a permission policy is denied everything (`perm.DenyAll`).
* **A repository is untrusted until you say otherwise.** Its config cannot define providers, permission modes, hooks or MCP
  servers unless trusted, and can only *add* to your deny/ask rules and hooks; a project's MCP server starts only after you
  approve that exact entry.
* **What enters a prompt layer is escaped and bounded** (`kv.EscapeUntrusted`, `GuardFrame`), and only the harness writes the
  `instructions` and `assignment` notes: a person's words are pinned, a task the harness hands out (`OriginTask`) is not one
  of them, and a compactor can write only its own sections.
* **Keys go only where the user allowed and are held out of the environment** (`harden.MoveKeys`, `harden.Secret`).
* **Endpoints are not believed**: bounded reads, timeouts, clamped counters, sanitised errors.
* **State is private** (0700/0600, hash-verified blobs, one writer per session directory), and redaction happens where data
  leaves the log (`internal/rl/redact`), not where it is written.

## Interfaces

* CLI: `chat` (interactive; slash commands, `--resume`/`--continue`), `run`, `swarm`, `recon`, `init`, `config`, `sessions`,
  `models`, `doctor`, `sim`, `demo`, `inspect`, `mcp`, and the `rl` family. `docs/CLI.md` is generated from `--help`.
* Headless JSON: `run --json` streams events as JSON lines.
* The web **cache inspector** (`sleipnir inspect`): layer stack, per-layer tokens, hit ratio, compactions, the live board,
  mail and leases, cost against a baseline; it is read-only and works on any session log, including every RL rollout.

## Decisions and alternatives

* **Go**: one static binary for every OS, goroutine-per-agent maps directly onto the swarm, the race detector
  guards the shared-state code, and startup is instant. TypeScript/Bun and Rust were considered; both cost either
  distribution weight or iteration speed.
* **Event sourcing** over a database: append-only JSONL is greppable, crash-safe, trivially exportable and is the
  training data. An index can be added without changing the model.
* **Client-side compaction** over API-native compaction: portable across providers, and required for the swarm
  structure (shared/role/private layers). API-native background compaction is an optional accelerator.
* **Immutable snapshots + single-writer commits** over locks in the swarm: readers (hot view rendering) never block
  and never see torn state.
