# Changelog

All notable changes to Sleipnir are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/).

## [0.1.0] - unreleased

The first release.

### Prompt engine

- Layered, generational prompt cache. Seven layers ordered by volatility: constitution and tools (G0), shared
  project pin (G1), role pin (G2), notes (G3), spine (G4), thread (G5) and a small always-fresh hot view (G6).
  Every change to a stable layer is a declared, priced event; a guard flags silent cache regressions.
- Compaction as a patch: a background compactor forks the agent's own request (same cache), returns a JSON patch,
  and the harness validates it and commits it at the cheapest moment (or at a cold moment for free). Masking before
  summarising; nothing is lost (`recall`).
- Cache models for OpenAI-style automatic prefix caching (routing keys, gateway-reported cost) and Anthropic explicit
  breakpoints (max four, 20-block lookback, 5m/1h TTL), including preserved thinking, turn-scoped system messages
  and declared rebases.
- `sleipnir sim`: a policy simulator with printed assumptions, and the rule it found: layering wins when the shared
  pin is dense.

### Swarm

- One manager and up to dozens of workers over one repository, dispatched from a task card without a briefing.
- A typed board (immutable snapshots), rate-limited typed mail through a router, write leases and task scopes,
  content-hash staleness on every edit, checkpoints, a writer cap, and harness-owned "done" (a task cannot leave
  `doing` without the evidence gate and the verifier).
- Warm gate (one primer per cold prefix), governor (requests per minute, priorities, `Retry-After`, AIMD), watchdog
  and failure recovery.
- Worktree isolation (`swarm.isolation: worktree`, `--isolation`): every writer edits a git worktree of its own, and its
  work reaches your checkout through a serial merge queue that verifies every integration and undoes a merge whose check
  fails; the result is applied at the end of the run (as edits, or as commits with `--commit`) and is never lost when it
  cannot be.
- Optional mailman (`swarm.mailman`, `--mailman`): worker mail is digested in bursts by a small read-only agent that has
  a model of its own if you give it one; the router's checks are unchanged, a mailman that is absent or slow costs delay,
  never mail, and every digest names its senders.
- The manager is supervised: in a batch run its final answer is held while its workers or reviews are unfinished (a
  worker that is only writing its closing message is waited for, not counted), in a chat it is woken, with a short
  harness-written note, when workers finish or fail.
- Bounded by default: a swarm has a built-in budget (US$50, `swarm.budget_usd`, only you can raise or remove it), a
  ceiling on its size (`swarm.max_agents`), and peer mail can wake one worker only 40 times per task; budget checks fail
  closed. Read-only roles are denied the background-job tools by name.

### Providers

- OpenAI-style chat completions (OpenAI, marketplaces such as Heimdall and OpenRouter, vLLM, SGLang), with reasoning
  replay, optional token-id capture, and exact gateway costs.
- Native Anthropic Messages adapter with explicit cache breakpoints and per-gateway limits declared as options.
- `sleipnir doctor` measures a real endpoint: streaming, tools, cache reporting, block granularity, warm-up needs. Its
  cache verdict counts nine repeat requests (`yes`, `partly`, `NO`), because a marketplace cache can hit on some and miss on
  others.
- Every model of a session, roles' models included, is described from its endpoint's catalogue when the built-in table
  does not know it, and `session.start` records the prices each was described with, so `inspect` shows the gateway's
  figures instead of a generic estimate.

### Tools, permissions and extensions

- File, shell, web and recall tools; every agent sends the same tool list and roles are restricted at run time by the
  permission engine (modes, rules, shell-syntax analysis, role profiles, hard denies for credentials).
- Skills (listing in the shared layer, loaded on demand), custom slash commands, markdown role definitions, hooks
  (SessionStart/End with the reason a session ended, UserPromptSubmit, PreToolUse, PostToolUse, PermissionRequest,
  Notification, Stop for the agent you talk to and SubagentStart/Stop for a swarm's workers, PreCompact/PostCompact around
  every compaction, automatic ones included), MCP servers (tools frozen per
  session, per-entry approval for project servers, prompts as slash commands, `sleipnir mcp`, `/mcp`), checkpoints
  and rewind. Repository-supplied skills, commands, roles, hooks and instruction files are read only when the project
  is trusted, and writes to the directories that hold them always ask.

### RL environment

- The harness is an RL environment: `sleipnir rl taskgen|tasks|rollout|eval|serve|reward|export|expand|verify|show`.
- Exact-prompt capture (content-hash manifests and wire hashes), token ids and logprobs from self-hosted policies,
  verifier rewards in a clean checkout, hack detectors, cost repriced under a target provider, per-role rewards,
  GRPO/RLOO/anchor advantages, and exporters for steps, tokens, groups, SFT, DPO, KTO, ATIF and a lossless canonical
  archive. Redaction happens at export; raw logs stay private.
- Sampling seeds are 31-bit: a real marketplace refused 63-bit and 53-bit seeds ("outside the 0 to 2147483647 this
  endpoint accepts"), which failed every rollout of a first run against it.
- Task generators: mined from git history, mutation, composite swarm tasks and recall (memory) tasks.

### Sessions

- Every finished turn saves a snapshot of the agent (thread, notes, spine) in the event log. `sleipnir chat|run --resume
  <id|latest>` (or `--continue`) picks the conversation up where it stopped, in the same session directory and log; the
  first request writes the cached prefix again, once, and says so. Swarm sessions cannot be resumed yet.
- `/compact [focus]` folds the older thread on request, with an optional hint about what to keep in view; it is a declared
  and priced rebase like any other, and fires the PreCompact and PostCompact hooks.
- `sleipnir sessions` marks the sessions that can be continued.

### Security

`docs/SECURITY.md` is the threat model: what is protected, what is not, what is hardened on each OS, how to confine harder.

- Hostile repositories: instruction files, skills, commands, roles and hooks from a repository are read only when the
  project is trusted; symlinks and imports cannot leave the project; invisible Unicode is stripped; repository text is
  rendered "(scope, unverified)". A repository's configuration can add to your `permissions.deny`/`ask` rules and your
  hooks, never remove them, and cannot define providers unless trusted. A project's MCP server starts only after you
  approve that exact entry.
- Keys: the provider key goes only where you allowed it (the provider's own host, loopback, or a host in your user
  configuration), never over plain `http` to another machine, and no redirect carries it off the original origin. It is held
  out of the harness's environment, so nothing the harness starts inherits it; on Linux the process is non-dumpable and its
  initial environment is erased, on macOS debugger attach is refused. The RL rollout server sends a policy key only where its
  operator listed.
- Endpoints are not believed: responses are bounded as they are read, silent servers are timed out, hostile usage counters,
  costs and catalogue prices are clamped or dropped, error text is sanitised.
- The permission engine hard-denies credential paths; Ask rules apply even in bypass mode; unattended sessions refuse to ask;
  nothing defaults to allow-all (an agent, swarm member or tool environment built without a permission policy is denied).
- The prompt engine treats what it did not write as data: a compactor may write only its own notes sections, every text that
  enters a layer is escaped and bounded, what a person typed is pinned in full (beginning and end above the bound), and the
  harness's own task text is never pinned as the user's word. Tool results per turn are budgeted (the excess is saved behind a
  recall handle) and a tool call has a deadline.
- Session state is private (0700/0600), blobs are verified by hash, a damaged log line is recorded rather than truncating the
  rest, a session directory has one writer at a time, and checkpoint restore refuses anything that would write outside the
  project. Terminal output drops escape sequences and control characters that a model, tool, file or page wrote.
- Swarm: mail, notes, status lines and alerts are framed and bounded as untrusted peer data; scopes are enforced at write
  time; "done" belongs to the harness; Retry-After is capped everywhere.
- Independent adversarial reviews of the prompt engine, the swarm and the trust boundaries are in `docs/reviews/`, with what
  was fixed (all of it) and what remains.

### Configuration

- Every key in the file format is read by something: the keys of earlier drafts that nothing read are gone (an old file
  that still has them gets an "unknown key" warning with a suggestion). `cache.shared_ttl` reaches the breakpoints of
  single agents and swarms, and provider options are validated per dialect (unknown names warn, wrong kinds and values
  the adapter does not take are errors; a test keeps the list and the code that reads it together).
- `sleipnir config` lists each warning once, with file, line and column; `init --user` invents no provider (`--local-url`
  adds a self-hosted one); `swarm -h` and `-h` everywhere exit 0 and print usage; a bad worker count, a role model for a role
  that does not exist and a budget that is not a positive number are errors that say what was expected.

### Interfaces

- `sleipnir chat` (slash commands, Ctrl-C per turn), `run`, `swarm`, `recon`, `init`, `config`, `sessions`, `models`,
  `demo` (a scripted 14-agent team on a mock endpoint, no key needed) and `inspect` (a live or after-the-fact web
  dashboard: layers, hit ratio, compactions, swarm, cost; for a swarm also its worktrees and merge queue, the mailman and
  the manager's supervision).
