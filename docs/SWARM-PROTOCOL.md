# Swarm protocol

How one manager and up to dozens of workers coordinate over one repository. This page is normative: it states what
the harness guarantees, and the tests in `internal/swarm` and `internal/session` hold the implementation to it. The
cache side of the design (why a worker costs a cache read, not a briefing) is in `docs/CACHE-DESIGN.md`.

## 1. Principles

1. **Agents are not peers; the harness is the authority.** Models propose (create a task, finish it, mail a teammate);
   deterministic code validates, applies and records. Task status, "done", leases, scopes, mail delivery and
   promotions never depend on a model's say-so.
2. **A shared board, not a chat.** Agents coordinate through typed operations on an immutable, versioned snapshot.
   There is no free-form channel between agents; mail is typed, capped and routed.
3. **Read-mostly and cheap.** Almost everything an agent needs is already in its cached prefix (project map, role
   conventions, its own notes). Coordination adds a few hundred tokens of *hot* view and the occasional mail.
4. **Requests, not tokens, are scarce** on marketplaces (for example 600 requests/minute per key): a manager that
   sleeps (`wait`) instead of polling costs nothing; parallel read-only tool calls and batched edits save requests.
5. **Failure is normal.** Workers time out, crash, get stuck, or lie. The protocol assumes it and recovers without
   asking the model.

## 2. Roles and tools

Roles are pins (G2) plus runtime restrictions. Built-ins: `manager` (coordinates, does not implement), `backend`,
`frontend`, `fullstack`, `tester`, `docs` (writers), `reviewer`, `scout` (read-only). Users add roles with markdown
files (`.sleipnir/agents/*.md`).

**Every agent sends the same tool list**, byte for byte (fs, bash, web, recall, and the five swarm tools), so the
provider caches the schemas once for the whole swarm. Roles are restricted at run time: the permission engine judges
each call under the session posture *and* the role's profile (read-only roles run under the plan profile: writes and
mutating commands are denied by the engine that understands shell syntax, not by a command allowlist), and swarm
tools check the caller's role (`spawn` is manager-only).

| Tool | Actions | Notes |
|---|---|---|
| `task` | create, list, claim, update, block, done, accept, fail, reopen | see section 4 |
| `mail` | send | one recipient; typed; section 5 |
| `note` | propose | a durable fact for the shared layer; staged, folded at an epoch |
| `spawn` | role, task, optional `agent=` to reuse an idle worker | manager only; section 6 |
| `wait` | until tasks change, mail arrives, or a timeout | sleeps at no request cost |

## 3. The board

Tasks: `id`, `title`, `desc`, `status`, `owner`, `role`, `deps`, `files` (scope), `line` (latest one-line progress),
`result`. Statuses: `todo -> doing -> review -> done`, with `blocked` and `failed` as side states.

* The board is an **immutable snapshot with a version**; every operation produces a new snapshot under a single
  writer. Readers (the hot-view renderer, the inspector) never block and never see torn state.
* Agent status is **derived by the harness** from observed events (running, waiting, idle, done, failed; current task;
  context size; cost). A model cannot claim to be "done" or "idle".
* Operations are events (`board.op` with the actor and the new version), so any state can be rebuilt from the log.
* Notes are *proposals*: visible immediately in the hot view of every agent, folded into the shared layer only at an
  **epoch** (section 9).

## 4. Task lifecycle and harness-owned "done"

```
create (manager)            todo
claim / spawn               doing   (owner set, scope checked, leases acquired lazily on write)
worker: done(text)          --> the HARNESS runs the gate:
                                 1. evidence: files the harness saw the agent edit, commands it ran and their exit codes
                                 2. verifier: the configured command (--verify "go test ./...") in the agent's workdir
                              pass  -> review (result = the model's text + harness-observed evidence)
                              fail  -> doing (the verifier output is returned to the worker, not to the manager)
manager: accept             done    (re-runs the verifier when configured; a task cannot become done without a pass)
manager: reopen / fail      doing / failed
```

Rules: a task never leaves `doing` without the gate; `accept` cannot bypass the verifier; the evidence attached to a
result is what the harness observed (edited files, last test command and exit code), never what the worker wrote
about itself; dependencies are enforced on claim **and** on spawn.

## 5. Mail

Mail exists for what cannot simply be in the cached prefix: an interface changed, a blocker, a question. It is
ephemeral: delivered into the recipient's next turn, cached for a few turns, compacted like any turn, then gone.

* **Typed**: `info` (no reply expected), `request`, `blocker`, `answer`, `contract` (an interface changed).
* **No broadcast, no self-mail, known recipients only** (`to: manager` resolves to the manager).
* **Bounded**: 600 characters per message, 8 per minute per sender, 3 per minute per sender->recipient pair, identical
  text to one recipient deduplicated for 5 minutes; over-limit errors are written for the sending model and say what to
  do instead (use `note`, wait, or fold messages).
* **Framed by the harness.** A delivered message is one block with a harness-written header (`[mail <id> <kind> from
  <agent>]`); message text is untrusted data, sanitised so it cannot forge headers, close tags or impersonate the
  system, and the constitution tells recipients so. Mail cannot carry approvals: nothing an agent receives can grant
  permission, change a task's status, or authorise an action.
* **Delivery is a turn-boundary event**, never an interruption; a delivery to a stopped agent is recorded and either
  wakes it (workers) or queues (bounded and coalesced: a manager inbox cannot grow without limit).
* **Optional mailman mode**: instead of direct typed delivery, mail can be routed through a `mailman` agent (a
  read-only role with one action: deliver) that deduplicates, digests bursts and picks recipients. The harness still
  validates and rate-limits every delivery; the mailman only decides.

## 6. Spawning and reuse

`spawn(role, task)` creates a worker on a task card (title, acceptance, scope). The worker needs no briefing: G0-G2
come from the provider's cache. Reusing an idle worker (`spawn ... agent=be-1`) is preferred for follow-up work in the
same area: its notes, spine and thread are warm. **A reused worker always receives the new task card as a user turn** (and
its scope), never silently continues an old one.

Limits, all enforced in code:

| Limit | Default | Why |
|---|---|---|
| agents (running or idle) | 24 (config `swarm.max_agents`) | registration budget |
| concurrent **writers** in the shared tree | 4 | overlapping agent changes conflict 20-42% of the time; readers (reviewers, scouts, test runners) are unlimited. Applies to reuse via `agent=` too. Isolated worktrees (section 7) lift it. |
| per-agent and swarm budget (USD) | unlimited | a stop is an outcome, not a crash |
| steps per assignment | 60-150 by role | runaway guard |
| idle retire | 15 min | frees registration; a retired worker's notes are archived |

## 7. Workspace safety

Layered, cheapest first:

1. **Scopes.** Each task declares the paths it touches (`files`, globs). Overlapping scopes between active tasks are
   refused at spawn, and scopes are **enforced at write time** by the lease guard: an edit outside the task's scope is
   rejected with a message naming the scope.
2. **Write leases** with a TTL and conflict alerts (visible in the hot view of the agents involved).
3. **Content-hash staleness** on every edit: an edit is accepted only if the file is what the agent last read, whichever
   agent changed it since. Correct even if a lease were stolen.
4. **Checkpoints** before every write; `/rewind` restores per file, per agent, or everything.
5. **Isolation (optional)**: `swarm.isolation: "worktree"` gives each writer its own git worktree and integrates finished
   trees through a serial, verifying **merge queue**: three-way merge onto the integration tip, structured conflict
   report (files, hunks) returned to the manager without leaving the tree dirty, verify command after each merge,
   rollback on failure.
6. **Role gates**: reviewers and scouts cannot write; the engine denies it.

## 8. The hot view and the governor

The **hot view** is the always-fresh part of each agent's prompt: its own status and task, alerts (lease conflicts,
scope violations), related tasks and teammates, pending shared notes. It is personalised, deterministic and
token-capped (900 tokens for workers, 2200 for the manager), and it is the only per-request uncached text in the
system. How it reaches the model depends on the provider (`docs/CACHE-DESIGN.md`, HotMode): inline at the tail
(default), or, where the provider binds thinking blocks to the exact prefix, as a persisted frozen message
(turn-scoped system messages where the provider has them, otherwise appended only when it changes) so history stays
append-only.

The **warm gate** stops a fan-out from paying for one prefix N times: the first request over a cold prefix (keyed by the
shared layers, the routing shard and the role layer) is the primer; followers wait for its first response byte and then
read the cache. The **governor** admits requests by priority (manager > workers > background compaction) within the
requests-per-minute budget, honours `Retry-After`, and adapts multiplicatively on 429s and additively on success; one
burst of simultaneous 429s counts as one rate-limit episode.

## 9. Compaction and epochs across agents

Each agent compacts privately (generational: thread -> spine, notes, masking). Facts an agent thinks the *team* should know
become promotion proposals; they are visible at once through the hot view and folded into the shared (G1) or role (G2)
layer only at an **epoch**: session start, a phase boundary, or when the swarm is idle. An epoch changes the shared
prefix for everyone, so it is rare, batched, and self-warming (the first agent after it is the primer for the rest).
Compactor patches can never rewrite the `instructions` notes, and text found in tool output is never promoted as an
instruction.

## 10. Failure handling

| Failure | Behaviour |
|---|---|
| worker stops (crash, cancel, step or budget limit) | its task returns to `todo` (or `failed` after N attempts), leases are released, the manager is told in one line |
| stuck worker | watchdog: no progress event for N minutes -> alert, then retire and requeue |
| provider outage / 429 / 5xx | retry with backoff inside the request; the governor slows the whole swarm |
| manager stops | workers finish their current task and idle; the run ends with the board's state and a summary |
| verifier flaky | `--verify-repeat N` requires unanimity; infra errors are reported as such and never fail a task |
| forged or hostile mail | dropped or framed as data; never grants anything |

## 11. Observability

Every operation is an event: `agent.spawn`, `board.op`, `mail.send`/`mail.deliver`, `lease`, `governor`, compaction and
cache events. `sleipnir inspect` shows the live board, per-agent context and hit ratio, mail and lease activity, and
compaction timelines; the same log is the RL training corpus (`docs/TRAINING-DATA.md`), where the swarm's DAG
(spawn, mail, compaction edges) is preserved.
