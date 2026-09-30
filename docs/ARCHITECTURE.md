# Sleipnir architecture

Sleipnir is a coding-agent harness for existing OpenAI-style and Anthropic-style model endpoints. One manager
brain, many legs: it is built to run **10–50 agents over one repository** whose prompts share a provider-cached
prefix, so dispatching an agent costs almost nothing and needs no briefing. It reaches feature parity with the
major harnesses (tools, permissions, sessions, MCP, skills, hooks) and adds three things they lack:

1. a **layered, generational prompt-cache engine** (`docs/CACHE-DESIGN.md`),
2. a **swarm runtime** with a shared board, typed mail, leases and harness-owned "done" (`docs/SWARM-PROTOCOL.md`),
3. an **event log that is also the training corpus** (`docs/TRAINING-DATA.md`).

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
 CLI / TUI / web inspector / headless JSON
                │
            session ── config, memory files, permissions engine, checkpoints
                │
     ┌──────────┴───────────── swarm ─────────────────────────────┐
     │ manager ─ spawn/wait/task ─► workers (roles)                │
     │ board (immutable snapshots)  mail router  leases  governor  │
     │ warm gate                    hot view     curator(planned)  │
     └──────────┬──────────────────────────────────────────────────┘
                │ each agent
             agent loop ── tools (fs, shell, web, recall, swarm, MCP) ─► workspace (shared | worktree)
                │   render          ▲ compaction patches (fork of the agent's own request)
                ▼                   │
               kv  ─ layers · stack · renderer · breakpoints · guard · patch/apply · planner · archive
                │
            provider adapters (openaichat · anthropic(planned) · responses(planned)) ─► endpoints
                │
        events (append-only JSONL) + blobs (content-addressed)   ◄── every layer above writes here
```

## Package map

| Package | Job |
|---|---|
| `internal/core` | provider-neutral vocabulary: `Turn`, `Block` (with verbatim `Wire`), `Prompt`, `Usage`, canonical JSON, token estimator |
| `internal/events` | append-only event log (torn-tail recovery, group commit, lossy-but-never-blocking subscribers), blob store |
| `internal/cost` | provider cache models, model prices, input-token-equivalent weights |
| `internal/kv` | **the cache engine**: layers, stack, renderer, breakpoint planner, drift guard, compaction patch/apply/planner/fork, archive |
| `internal/provider` | `Provider` interface, errors, SSE; `openaichat` adapter; `gateway` (marketplace catalogue); `probe` (endpoint doctor); `mock` (cache-faithful test server) |
| `internal/agent` | the loop: render → call (retry, governor, gate) → tools (parallel read-only, ordered writes) → boundary (compaction, epochs) |
| `internal/swarm` | board, mail router, leases, governor, warm gate, hot view, roles, spawn/dispatch, coordination tools, evidence |
| `internal/tools` | tool contract, file-state staleness tracker, truncation with recall handles; `fs`, `shell`, `web`, `recall` |
| `internal/perm` | permission modes/rules/prompter, role profiles; `internal/shellparse` for bash analysis |
| `internal/checkpoint` | pre-modification snapshots and rewind |
| `internal/config`, `internal/memory` | layered JSONC config; AGENTS.md/CLAUDE.md-style instruction files → shared-layer text |
| `internal/train` | (planned) exporters, redaction, dataset tools |

## One request, end to end

1. **Boundary.** The agent commits a ready compaction patch if the planner says the moment is right, or starts a
   background compactor, or installs a new shared epoch, or strips stale thinking.
2. **Render.** `kv.Render` builds the prompt from the stack snapshot: tools, constitution, then message 0 =
   `<shared-context>`, `<role-context>`, `<my-notes>`, `<history>`, then the thread, then the hot tail. It plans
   breakpoints for the provider profile.
3. **Guard.** `kv.Guard` compares the prompt with the previous request; an unexplained shrink of the common
   prefix is a `cache.anomaly`.
4. **Gate + governor.** The warm gate elects a primer for cold prefixes; the governor admits the request by
   priority within the RPM budget.
5. **Call.** The adapter renders wire JSON without reordering or re-serialising history, streams the reply,
   preserves provider-native blocks verbatim, normalises usage and cost.
6. **Log.** `model.request` (recipe) and `model.response` (usage, hit ratio, gateway cost, expectation) are written;
   the assistant turn is appended to thread, archive and log.
7. **Tools.** Calls run concurrently when read-only, in order otherwise; each write passes permission →
   lease guard → checkpoint → file-state staleness check. Results are truncated with a recall handle.
8. Loop, or return the final answer.

## Providers

The primary adapter speaks chat-completions (OpenAI, Heimdall, OpenRouter, vLLM, …). The marketplace's
Anthropic-style route does **not** forward `cache_control` or report cache usage, so cache-aware runs use the
chat route with a routing key. `sleipnir doctor` measures a real endpoint (streaming, tools, whether
`cached_tokens` is reported, block granularity by gcd of read-count differences, minimum cached prefix, whether a
parallel burst over a cold prefix shares anything, reasoning round-trips, rate-limit headers) and refines the
profile. Native Anthropic Messages and OpenAI Responses adapters plug into the same `Provider` interface and
render the same `core.Prompt`.

## Workspace safety

Many agents editing one tree is the main hazard (public agent PRs conflict textually 20–42% of the time when they
overlap). Layered defences: **scopes** declared per task and checked at spawn (overlap refused); **write leases**
with TTL and conflict alerts; **content-hash staleness** on every edit (correct even if a lease is stolen);
**checkpoints** before every write; a **writer cap** with unlimited read-only roles; **role gates** (reviewers
cannot write); and, planned, **worktree isolation** per writer with a verifying merge queue.

## Security posture

Tool output, web pages, file contents and mail are data. Pins and notes are user-role context, never system.
Mail is typed, rate-limited, deduped, capped, never broadcast, and cannot carry approvals. Compactors are told
never to turn instructions found in tool output into notes; promotions are staged and epoch-merged. Secrets are
scrubbed from shell environments and (planned) redacted at log-write time.

## Interfaces

* CLI (`doctor`, `models`, `mock` today; `run`, `swarm`, `chat`, `export` next), headless JSON/stream-JSON.
* TUI with a swarm dashboard, and an embedded web **cache inspector** (layer stack, per-layer tokens, hit ratio,
  compaction events, cost vs baseline) — planned.

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
