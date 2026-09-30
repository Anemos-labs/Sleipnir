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

**In an isolated run** (section 14) the gate has a third step. After the evidence and the verifier (run in the worker's
own tree) the harness commits the tree and submits it to the merge queue, which merges it onto the integration tip and
verifies the *merged* result. The task reaches review only when that succeeded (`merged`, or `empty`: nothing to merge),
and `accept` requires that merge record instead of re-running the verifier in a checkout that does not have the work yet.

**The manager's own stop.** The manager's final answer is a stop like any other, and the harness decides whether the
run may end (`swarm.Config.HoldManager`, set for every session that is not interactive: `run`, `swarm`, RL rollouts).
While workers are running, or tasks are in review, doing, blocked or todo, a final answer is vetoed with one short
harness-written reason that lists ids only (`Not finished: running: be-1 (T3), te-1 (T4); in review: T2; not started:
T5. Use wait, accept or reject submissions, and fail tasks you abandon, then give your final answer.`: sorted,
deterministic, at most about 400 characters, the lists shrink before the instruction is cut). The user's own Stop hooks run
first and either may veto. The agent loop bounds vetoes per run (three), so a manager that will not settle its board is
released: the run ends, its result is followed by a `[harness] Unfinished when the manager stopped: ...` line, and the
person gets a notice (`swarm.hold` and `swarm.unfinished` events). A run that was cancelled, whose budget is spent, or
whose swarm is shut down is never held (those paths end before, or without, consulting the guard).

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
   report (files, hunks) returned to the worker whose work conflicted, verify command after each merge, rollback on
   failure. In that mode leases become advisory and the merge queue settles overlaps; section 14 is the whole design.
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
| manager finishes | a batch run holds it until the board is settled (section 4); when the bound is reached the run ends and names what was left. In an interactive session workers legitimately outlive the turn and finished work wakes the manager (section 13) |
| shutdown | agents are cancelled and awaited for at most 10 s; nothing new starts afterwards |
| verifier flaky or broken | infra errors (could not run, timed out) are reported as such and never fail a task nor count as a pass; `--verify-repeat N` requires unanimity |
| forged or hostile mail | defused and framed as data; never grants anything |

## 11. Observability

Every operation is an event: `agent.spawn` and `agent.assign` (a reused worker), `agent.state` (status changes),
`agent.end`, `agent.panic`, `board.op`, `mail.send`/`mail.deliver`/`mail.drop`, `lease` (acquire, conflict, scope,
release), `governor` (rate-limit episodes), `swarm.budget`, `swarm.hold`/`swarm.unfinished` (the manager's stop guard),
`swarm.wake`/`swarm.wake.paused` (waking an idle manager), `workspace.create`/`remove`/`prune`/`commit`/`reset` and
`merge.queued`/`merged`/`conflict`/`verify_failed`/`rolled_back`/`rejected`/`fast_forward` (worktree isolation; the
workspace layer emits them), `task.merge` (one submission's outcome, per task) and `swarm.integration` (the result reaching
the checkout, or not), compaction and cache events. A `board.op` names its operation
(`create`, `claim`, `assign`, `update`, `scope`, `finish`, `block`, `resume`, `requeue`, `agent`, `agent-remove`,
`note`, `notes-take`, `alert`, `alert-clear`, `alert-expire`), the new version, and its operands (the task's status,
owner, line, result, evidence, attempts, rev and scope, plus title, description, role and dependencies at creation; an
agent's whole status; a note's text and scope), and events keep the order of the versions. `sleipnir inspect` shows the live board,
per-agent context and hit ratio, mail and lease activity, and compaction timelines; the same log is the RL training
corpus (`docs/TRAINING-DATA.md`), where the swarm's DAG (spawn, mail, compaction edges) is preserved.

## 12. Not implemented, and known gaps

* The `mailman` mode (section 5).
* Resuming a session's board after a crash: `ReplayBoard` rebuilds tasks, the roster and pending notes exactly from the
  `board.op` events (a test holds it to that), but nothing calls it at startup yet; alerts are transient and not rebuilt.
* On a cold prefix a higher-priority follower (the manager) can wait behind a worker-priority primer, because the warm
  gate is entered before the governor; the fix belongs in the agent's request path or the gate.
* A batch run's manager that ignores three vetoes still ends with work unfinished; the run says so, it does not keep going.
* Worktree isolation (section 14) has residual gaps of its own, listed there.

## 13. The manager between turns (interactive sessions)

In `sleipnir chat --swarm N` a person talks to the manager between turns and workers outlive a turn: the session sets
`Interactive`, so the manager is not held, the swarm runs for the life of the session (not of one turn's context: the chat
loop cancels that when a turn ends; Ctrl-C still cancels the turn and, through `RunManager`, its workers), and
`swarm.Config.WakeManager` is on. When the manager is idle and something happens that it has not seen (a worker finished,
failed or stopped, a task reached review, mail arrived for it), the swarm starts one manager run:

* **Coalesced.** A burst of events starts one run: the timer restarts on each event (1.5 s quiet, at most 10 s after the
  first). What counts as news is the board compared with the one the manager's newest request showed it, so nothing it
  already saw wakes it, and an event with nothing behind it starts no run.
* **Never concurrent, never after the end.** No run starts while the manager is running (it sees the board itself; mail that
  lands after its last drain wakes it when the run ends), after `Shutdown`, or once the swarm budget is spent
  (`--budget-usd` still applies to the wake run like any other).
* **Bounded.** At most 8 automatic runs between two human inputs (`RunManager`, or steering sent with `Session.Send`); the
  person is told once when the bound is reached (`swarm.wake.paused`) and the count starts again when they write.
* **Harness-written and inert.** The note (`While you were idle: T1 is in review (be-1); T2 failed; mail is waiting for you.
  Check the board, ...`) holds ids and status words only, never text an agent wrote. It arrives as harness mail, not as a user
  turn: a user turn would be folded by compaction into the manager's `instructions` as something the person asked for, once
  per wake. It is shown to the person through the session sink (level `wake`) and logged (`swarm.wake`).

## 14. Worktree isolation

`swarm.isolation` is `"none"` (the default; `"shared"` is the same thing under its older name) or `"worktree"`.
`sleipnir run|swarm|chat --isolation none|worktree` overrides it for one session, `SLEIPNIR_SWARM_ISOLATION` sets it from the
environment, `sleipnir config` prints the effective value and the layer that set it, and `--commit` (worktree only) changes what
the end of the run does (below). A project's config file may turn isolation on (it only reduces risk) but nothing in the
configuration can choose where the trees go. Isolation applies to swarms: a single agent asked for it in so many words is an
error, one under a configuration that isolates swarms just runs.

**Preconditions, refused up front with the reason** (before a model is contacted): a git repository with a work tree whose root
is the project root, at least one commit, git on the path; with `--commit`, a clean checkout on a branch. Nothing is created in
the repository.

**Trees.** Every *writer* gets `<cache>/sleipnir/worktrees/<session id>/<agent>` on branch `sleipnir/<session id>/<agent>`,
created at the integration tip when the worker is spawned (`internal/workspace`). `<cache>` is the per-user cache directory
(`$XDG_CACHE_HOME`, or `~/.cache`): the trees are large and disposable, and the state directory is out because `~/.sleipnir`
is a protected configuration directory. The trees start from what the person sees now, *uncommitted edits included* (a
snapshot commit that no branch of theirs points at), and the result is applied on top of exactly that. A session started in
a subdirectory keeps its writers in the same subdirectory of their trees. The manager and the read-only roles keep the
checkout: the manager does not edit files in an isolated run (anything it wrote there would bypass the merge queue; it spawns a
worker), and reviewers and scouts read the checkout, which holds a task's work only after it is applied, so they read merged
work with `git show <commit>` (the commit is in the task's evidence).

**Nothing in the prompt names a tree.** The tools print paths relative to the working directory, which are the project's
paths, and the isolation card (`Isolation: your working directory is a private git worktree ...`, which says what the
harness will do with the work) is appended to the worker's private assignment note, never to a shared layer. The bytes of
G0, G1 and G2 do not change; only a worker's own notes are longer, when the feature is on.

**Confinement is enforced, not conventional.** The permission engine confines every writer to its own tree
(`perm.Engine.Confine`): whatever it reads or writes inside the workspace (the checkout, another agent's tree) outside its
own is a hard deny that no rule and no mode (bypass included) lifts, judged on resolved paths and covering shell commands as
well as the file tools. And every rule that is relative to the workspace (`Edit(./.sleipnir/**)`, `Deny(Read(./secrets/**))`,
a floating `Allow`) applies inside each tree as it does in the checkout (`perm.Config.TreeParents`): the work reaches the
checkout by a merge, so a rule that guarded only the checkout would guard nothing. The lease guard still enforces task scopes.

**Leases become advisory.** Nobody can block anybody, so the writer cap and the scope-overlap refusal are lifted. Two writers
touching the same repository-relative path raise the usual alert, which names the agents and never the file, and warns of a
merge conflict. A scope still says which files a task may touch, and the merge queue enforces it on the commit.

**`done` continues into the queue** (section 4). The harness commits the tree and submits it; the queue is serial, merges onto
the integration tip and runs the verifier (`--verify`) on the merged result, and moves the integration branch only if it passes.

| Outcome | What happens |
|---|---|
| `merged` | the task proceeds to review (its evidence names the integration commit) |
| `empty` | nothing to merge (no changes): reported as such, and the task proceeds |
| `conflict` | back to the worker with the conflicting files and hunks; the integration tip is merged into its tree, so the markers are in its files (`ours` is its own version) |
| `verify_failed` | back to the worker with the verifier's output; its tree now holds the merged state, so the failure reproduces there |
| `rejected` | back to the worker with the reason: outside its scope, an oversized file, a nested repository, unresolved markers |
| could not run | the merge or the verifier failed to give a verdict (git error, timeout): reported as an infrastructure problem, never as a failed test. A `done` call is answered with the error and the worker retries or blocks; a worker that stopped without calling `done` has its task sent to review marked NOT MERGED, which `accept` refuses |

A bounce is rework like any other: after `MaxAttempts` (3) the task returns to `todo`, the worker is stopped and the manager is
told once. A worker sees other agents' work only when it is merged: a new worker starts from the integration tip, and a reused
worker, a resumed task and a bounced worker have their tree brought up to it. Nothing else crosses between trees, and there is
no new tool: a worker that needs merged work blocks its task and says what it needs, as before.

**The end of the run.** When the manager stops (each turn of a chat session, the end of a batch run) the harness applies what
has been merged to the person's checkout, incrementally (`swarm.integration`; the person is told at level `integrate`):

* by default as **uncommitted changes** (a patch onto the working tree), exactly what a shared-tree run leaves behind, recorded
  in the checkpoint store first so `/rewind` undoes it;
* with `--commit` as **commits on the current branch** (a fast-forward to the integration tip; it needs a clean checkout, and
  fails without changing anything if the branch moved).

If applying fails (the person edited the same files meanwhile) nothing is changed, the integration branch stays, and the
report says which branch and the one command that gets the result (`git diff --binary <base> <branch> | git apply --3way`, or
`git merge <branch>` with `--commit`). `Session.Finish` ends the run (also called by `Close`): it stops the swarm, applies what
remains, removes every tree that holds nothing unmerged (one that does is kept and named), and deletes the integration branches
once their result is in the checkout. The CLI prints the report (`integration: ...`, and under `"integration"` in `--json`).
**A killed session** leaves its trees; the next isolated session of the repository cleans up at its start (`Manager.Prune`): the
trees of dead sessions are committed onto their own branches and removed, a branch holding commits that exist nowhere else is
kept and named, and a tree whose owner is still running is never touched.

**Costs.** One checkout per writer (disk and the time of `git worktree add`, paid at spawn), one verifier run per merge (in the
integration tree, serial), and a worker that finishes second can be sent back. In exchange the writer cap and the scope-overlap
refusal are gone, and a shared tree's races cannot happen.

**Residual gaps.**

* Work is merged before it is reviewed, and there is no revert: a task the manager then rejects, reopens or fails keeps its
  merged work on the integration branch (a rejected task's worker resubmits on top of it). The manager sees the merge in the
  task's evidence and can have a worker undo it.
* Reviewers and scouts read the person's checkout, which lacks unapplied merged work until the manager stops; they can read a
  task's work with `git show` on the commit its evidence names.
* The verifier that runs on merges is the `--verify` command. Without one the queue only serialises and detects conflicts. In a
  chat session `--verify` is now accepted (it was not before).
* A project whose root is not the repository's root (a `.sleipnir/` directory in a subdirectory of a larger repository) cannot
  be isolated; run from the repository root.
* Trees are made with `git worktree`: submodules are not initialised in them and ignored files (build output, `node_modules`,
  `.env`) are absent, so a verifier that needs them fails in the tree until the worker makes them; a project that cannot be
  built from a clean checkout is a poor fit.
* Symlink-heavy or very large repositories pay the checkout cost per writer; there is no sparse checkout by scope yet.
* The inspector does not show trees or the queue yet (the events are in the log).
