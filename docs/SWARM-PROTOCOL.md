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
6. **Text one agent can influence is data to every other agent.** Task titles, notes, mail, status lines, alerts,
   file names and command lines are made single-line, bounded and inert before another agent sees them; nothing in
   them can forge a harness header, close a tag, grant permission or change a task's status.

## 2. Roles and tools

Roles are pins (G2) plus runtime restrictions. Built-ins: `manager` (coordinates, does not implement), `backend`,
`frontend`, `fullstack`, `tester`, `docs` (writers), `reviewer`, `scout` (read-only). Users add roles with markdown
files (`.sleipnir/agents/*.md`).

**Every agent sends the same tool list**, byte for byte (fs, bash, web, recall, and the five swarm tools), so the
provider caches the schemas once for the whole swarm. Roles are restricted at run time: the permission engine judges
each call under the session posture *and* the role's profile (read-only roles run under the plan profile: writes and
mutating commands are denied by the engine that understands shell syntax, not by a command allowlist), and swarm
tools check the caller's role, which the harness puts in the call (`spawn`, `create`, `accept`, `reject`, `reopen`,
`fail` and scope changes are manager-only). Without a permission engine (tests, embedding) read-only roles fall back
to a strict allowlist of one plain inspection command (no operators, redirections, substitutions, exec flags, or
paths outside the project).

| Tool | Actions | Notes |
|---|---|---|
| `task` | create\*, list, claim, update, block, resume, done, accept\*, reject\*, reopen\*, fail\* | \* manager only; see section 4 |
| `mail` | send | one recipient; typed; section 5 |
| `note` | propose | a durable fact for the shared layer; staged, folded at an epoch |
| `spawn` | role, task, optional `agent=` to reuse an idle worker | manager only; section 6 |
| `wait` | until tasks change, mail arrives, or a timeout | sleeps at no request cost; reports what changed since the caller last looked |

## 3. The board

Tasks: `id`, `title`, `desc`, `status`, `owner`, `role`, `deps`, `files` (scope), `line` (latest one-line progress),
`result` (what the worker said), `evidence` (what the harness observed), `attempts`, `rev`. Statuses: `todo -> doing ->
review -> done`, with `blocked` and `failed` as side states.

* The board is an **immutable snapshot with a version**; every operation produces a new snapshot under a single
  writer, and an operation that changes nothing produces nothing (no version, no wake-up). Readers (the hot-view
  renderer, the inspector, `wait`) never block and never see torn state.
* **Transitions are a table, and `done` is terminal.**

  | Verb | From | To |
  |---|---|---|
  | claim / spawn (assign) | todo, unowned, dependencies done, scope free | doing |
  | block / resume | doing / blocked (owner; the manager may resume) | blocked / doing |
  | submit (the gate, section 4) | doing (owner) | review |
  | accept | review | done |
  | reject (send back) | review | doing (same owner) |
  | fail | todo, doing, blocked, review | failed |
  | reopen | failed | todo |
  | requeue (the harness) | doing, blocked | todo, or failed after `MaxAttempts` |

  Every assignment gets a new `rev`; the harness settles a finished run only against the `rev` it started with, so a run
  that ends late can never undo a newer assignment (a reject, a reuse).
* Ownership is one compare-and-set on the board: a worker's own `claim` and the manager's `spawn` for the same task
  cannot both succeed, and scope overlap is checked in the same critical section.
* Agent status is **derived by the harness** from observed events (running, waiting, idle, done, failed; current task;
  context size; cost). A model cannot claim to be "done" or "idle". The line shows the *kind* of tool in use ("running a
  command"), never its arguments.
* Operations are events (`board.op`, section 11), so state can be rebuilt from the log.
* Notes are *proposals*: visible immediately in the hot view of every agent, folded into the shared layer only at an
  **epoch** (section 9).
* **The board is bounded**, because every agent's prompt carries a rendering of it: 1000 tasks; titles 160 characters,
  descriptions 2000, at most 16 dependencies and 16 scope entries per task; 48 pending notes (8 per author, the oldest
  evicted, 300 characters each); 8 alerts, which expire after 2 minutes and are cleared when the conflict ends.

## 4. Task lifecycle and harness-owned "done"

```
create (manager)            todo
claim / spawn               doing   (owner set, dependencies and scope checked, leases acquired lazily on write)
worker: done(text)          --> the HARNESS runs the gate:
                                 1. evidence: files the harness saw the agent edit, commands it ran and their exit codes
                                 2. verifier: the configured command (--verify "go test ./...") in the agent's workdir
                              pass  -> review (result = the model's text; evidence = what the harness observed)
                              fail  -> doing (the verifier output is returned to the worker, not to the manager)
worker stops without done   the same gate runs on its behalf; on failure the worker is sent back to work with the
                            output (twice at most), then the task returns to todo and the manager is told once
manager: accept             done    (re-runs the verifier when configured; a task cannot become done without a pass)
manager: reject / reopen    review -> doing with feedback (or todo when the worker is gone); reopen also failed -> todo
manager: fail               failed  (its worker, if running, is stopped)
```

Rules: a task never leaves `doing` for review without the gate; `accept` cannot bypass the verifier, and a verifier
that could not run (error, timeout) is reported as such and is never a pass (nor a failed test); verification is
bounded (a deadline, and at most two runs at once) and a verifier that ignores its context cannot hold the caller past
it; the evidence attached to a result is what the harness observed (edited files, the last test command and its exit
status), never what the worker wrote about itself. A test counts only if the program run is a test runner (`echo "go
test ok"` is not one), its status is the command's own (a test piped into `tail` or followed by another command is
reported as masked), and the status comes from the tool's structured result (`Meta.exit_code`), so truncated output
can never turn a failing test into a passing one. Dependencies are enforced on claim **and** on spawn.

## 5. Mail

Mail exists for what cannot simply be in the cached prefix: an interface changed, a blocker, a question. It is
ephemeral: delivered into the recipient's next turn, cached for a few turns, compacted like any turn, then gone.

* **Typed**: `info` (no reply expected), `request`, `blocker`, `answer`, `contract` (an interface changed).
* **No broadcast, no self-mail, known recipients only** (`to: manager` resolves to the manager).
* **Bounded**: 600 characters per message, 8 per minute per sender, 3 per minute per sender->recipient pair, identical
  text to one recipient deduplicated for 5 minutes; over-limit errors are written for the sending model and say what to
  do instead (use `note`, wait, or fold messages). The router's bookkeeping is swept, so it does not grow with the
  number of agents or messages that ever existed.
* **Framed by the harness.** A delivered message is one line: a harness-written header (`[mail <id> <kind> from
  <agent>]`), the text reduced to one printable line in which harness-looking markers (`[mail`, `[end`, `[system`, ...),
  angle brackets and control or direction-changing characters are defused, and the statement that the text is untrusted
  peer data. The sender field is the caller's own id; nothing in the text can change it. Mail cannot carry approvals:
  nothing an agent receives can grant permission, change a task's status, or authorise an action, and a message is never
  delivered as human steering. Mail the harness writes itself (a worker stopped, a verification failed, a rejection) has
  the sender `harness` and is not marked.
* **Delivery is a turn-boundary event**, never an interruption. Mail that arrives while a worker is finishing starts
  another run when it goes idle (a bounded number of times), so it is never stranded. A delivery to a retired agent or a
  swarm that has shut down is refused to the sender (`mail.drop` is logged; the sender's rate budget is returned). An
  idle worker is woken by mail; the manager queues it for its next turn.
* **Bounded inbox.** An agent's inbox holds at most 12 waiting messages; further mail is coalesced per sender and kind
  into one digest that goes in when the inbox has drained, so a manager between turns cannot be buried.
* **Optional mailman mode** (not implemented): instead of direct typed delivery, mail can be routed through a `mailman`
  agent (a read-only role with one action: deliver) that deduplicates, digests bursts and picks recipients. The harness
  would still validate and rate-limit every delivery; the mailman would only decide.

## 6. Spawning and reuse

`spawn(role, task)` creates a worker on a task card (title, acceptance, scope). The worker needs no briefing: G0-G2
come from the provider's cache. Reusing an idle worker (`spawn ... agent=be-1`) is preferred for follow-up work in the
same area: its notes, spine and thread are warm. **A reused worker always receives the new task card as a user turn** (and
its scope), never silently continues an old one.

Spawn is one decision under one lock: the agent and writer limits, who owns the task, its dependencies and scope
overlap are checked and applied together, so concurrent spawns, reuses and self-claims cannot exceed a limit or give two
agents one task. A refused spawn leaves nothing behind (no task, no agent, no consumed id). A new worker is registered
(visible to the roster and to mail) only when it is built and assigned.

Limits, all enforced in code:

| Limit | Default | Why |
|---|---|---|
| agents (running or idle) | 24 (config `swarm.max_agents`) | registration budget |
| concurrent **writers** in the shared tree | 4 | overlapping agent changes conflict 20-42% of the time; readers (reviewers, scouts, test runners) are unlimited. Counts *running* writers, and applies to reuse via `agent=` too. A worker woken by mail is answering, not starting work, and is not counted. Isolated worktrees (section 7) lift it. |
| per-agent and swarm budget (USD) | unlimited | a stop is an outcome, not a crash. The swarm budget is a ledger over running *and* retired agents; once spent, no request is admitted and running workers are stopped (their tasks return to todo, without counting as an attempt) |
| steps per assignment | 60-150 by role | runaway guard |
| attempts per task | 3 | a task whose workers keep stopping is failed, not requeued forever |
| idle retire | 15 min | frees registration; a retired worker's notes are archived, its spend stays in the ledger, its unfinished tasks return to todo |

## 7. Workspace safety

Layered, cheapest first:

1. **Scopes.** Each task declares the paths it touches (`files`): directories (`src/api`: the directory and everything
   under it), files, and globs (`api/**`, `web/*.tsx`, `src/{a,b}/**`; `**` spans directories, `*` does not), relative to
   the repository root. Overlapping scopes between active *writer* tasks (doing or blocked) are refused at spawn and
   claim, comparing normalised patterns (`src//a`, `./src/a`, `src/../src/a`, absolute paths, case and `\` variants are
   the same directory; `src/a` and `src/ab` are not); read-only roles neither take part nor are blocked. Scopes are
   **enforced at write time** by the lease guard: an edit outside the scope of the writer's task is rejected with a
   message naming the scope, and raises a fixed-text alert. A worker cannot widen its own scope (only the manager can,
   through `task update files`). An agent with no task, or whose task declares no scope, is not restricted.
2. **Write leases** with a TTL and conflict alerts (visible in the hot view of the agents involved; the alert names the
   agents, never a file name, and goes away when the lease is released or expires).
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
scope violations, stuck workers), related tasks and teammates, pending shared notes. It is personalised, deterministic
and token-capped (900 tokens for workers, 2200 for the manager), it is the only per-request uncached text in the
system, and it costs the same whatever the age of the session: it lists at most 14 tasks (40 for the manager, most
relevant first, at most 3 failed), 30 teammates, the 8 newest notes and 6 alerts, and selects what fits the budget in
one pass. How it reaches the model depends on the provider (`docs/CACHE-DESIGN.md`, HotMode): inline at the tail
(default), or, where the provider binds thinking blocks to the exact prefix, as a persisted frozen message
(turn-scoped system messages where the provider has them, otherwise appended only when it changes) so history stays
append-only.

The **warm gate** stops a fan-out from paying for one prefix N times: the first request over a cold prefix (keyed by the
shared layers, the routing shard and the role layer) is the primer; followers wait for its first response byte and then
read the cache. The **governor** admits requests by priority (manager > workers > background compaction, first come
first served within a priority, with aging so background work is never starved) within the requests-per-minute budget,
honours `Retry-After` up to a ceiling (60 s: an endpoint cannot freeze the swarm), and adapts multiplicatively on 429s
and additively on success; one burst of simultaneous 429s counts as one rate-limit episode.

## 9. Compaction and epochs across agents

Each agent compacts privately (generational: thread -> spine, notes, masking). Facts an agent thinks the *team* should know
become promotion proposals; they are visible at once through the hot view and folded into the shared (G1) or role (G2)
layer only at an **epoch**: session start, a phase boundary, or when the swarm is idle. An epoch changes the shared
prefix for everyone, so it is rare, batched, and self-warming (the first agent after it is the primer for the rest).
Overlapping epochs are serialised, so every agent ends on the newest one, and a worker built during an epoch is brought
up to it before it runs. Compactor patches can never rewrite the `instructions` notes, and text found in tool output is
never promoted as an instruction.

## 10. Failure handling

| Failure | Behaviour |
|---|---|
| worker stops (crash, panic, provider error, step or budget limit) | its task returns to `todo` (or `failed` after 3 attempts), leases are released, the manager is told in one line; a panic anywhere on the worker's goroutines is contained (the process, the manager and the other workers carry on) |
| cancelled from outside (Ctrl-C, shutdown, the manager cancelled) | workers are stopped with it; their tasks return to `todo`, no attempt is counted, no notice is sent |
| stuck worker | watchdog: no model or tool event for 10 minutes -> alert; at 20 minutes the run is cancelled and its task requeued (attempt counted); a run that ignores the cancel for 30 more seconds is abandoned: the worker is retired and its goroutine left to finish alone |
| provider outage / 429 / 5xx | retry with backoff inside the request; the governor slows the whole swarm |
| swarm budget spent | no request is admitted, running workers are stopped, the manager is told once |
| manager finishes | workers finish their current task and idle; the run ends with the board's state and a summary |
| shutdown | agents are cancelled and awaited for at most 10 s; nothing new starts afterwards |
| verifier flaky or broken | infra errors (could not run, timed out) are reported as such and never fail a task nor count as a pass; `--verify-repeat N` requires unanimity |
| forged or hostile mail | defused and framed as data; never grants anything |

## 11. Observability

Every operation is an event: `agent.spawn` and `agent.assign` (a reused worker), `agent.state` (status changes),
`agent.end`, `agent.panic`, `board.op`, `mail.send`/`mail.deliver`/`mail.drop`, `lease` (acquire, conflict, scope,
release), `governor` (rate-limit episodes), `swarm.budget`, compaction and cache events. A `board.op` names its operation
(`create`, `claim`, `assign`, `update`, `scope`, `finish`, `block`, `resume`, `requeue`, `agent`, `agent-remove`,
`note`, `notes-take`, `alert`, `alert-clear`, `alert-expire`), the new version, and its operands (task, status, owner,
title, line, result, files, ...), and events keep the order of the versions. `sleipnir inspect` shows the live board,
per-agent context and hit ratio, mail and lease activity, and compaction timelines; the same log is the RL training
corpus (`docs/TRAINING-DATA.md`), where the swarm's DAG (spawn, mail, compaction edges) is preserved.

## 12. Not implemented, and known gaps

* The `mailman` mode (section 5).
* Restoring the board from the event log after a crash: the log carries what is needed (operands on every `board.op`),
  nothing replays it yet.
* On a cold prefix a higher-priority follower (the manager) can wait behind a worker-priority primer, because the warm
  gate is entered before the governor; the fix belongs in the agent's request path or the gate.
* The manager is not woken by a worker's completion once its own run has ended; it sees the board at its next turn.
