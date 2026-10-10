# VOCAB: the UI event vocabulary of `sleipnir web`

Binding for B2 (translator), B1 (host-originated events), A2 (transport), C1 (reducer patches). The mock's reducer
(`_src/v3/src/js/30-model.js`, function `reduce`) is the consumer; the server produces exactly these objects. Field names, types
and units below are the contract. JSON examples are normative for spelling; values are illustrative.

Contents: 1 Principles · 2 Envelope, ids, ordering · 3 Time · 4 Sources · 5 The thirty mock kinds · 6 The five additive kinds ·
7 Additive fields on existing kinds and the reducer patches · 8 Mapping tables (agents, tools, states, tasks, plan) · 9 Questions ·
10 Stream frames (control vocabulary) · 11 Late joiners, reconnects, keyframes · 12 Resumed sessions · 13 Hold and catch-up ·
14 Bounds and coalescing · 15 New harness events · 16 Log events this program adds

---

## 1. Principles

1. The server emits the mock's vocabulary. The page keeps its reducer, its governor (hold, slow, catch-up), its digest and its views;
   the server only supplies events. No server code renders HTML; no event carries HTML.
2. Every event belongs to one **tab** (a live session hosted by the server; see CONTRACT.md "Tabs"). Events never cross tabs.
3. Additive only: new kinds and new optional fields. An event the mock already handles keeps its meaning. A field the reducer reads
   keeps its type. Where the mock's simulation produced something the real harness cannot, this file says "not produced" and
   FEATURES.md has the row.
4. Untrusted text (model text, tool output, file names, mail, MCP text, notices that quote any of these) is sent as plain strings.
   The server sanitizes terminal controls (`tools.SanitizeForTerminal`), caps lengths, and masks secret-shaped substrings
   (`internal/rl/redact`); the page escapes every string it renders (`SL.u.esc` or `textContent`). Both, always.
5. No timestamps, ids or map-ordered data ever flow from this vocabulary back into a prompt (AGENTS.md). Nothing here is sent to a model.

## 2. Envelope, ids, ordering

A UI event is a flat JSON object:

```json
{ "t": 41.237, "k": "tool", "seq": 1042, "id": "be-2", "name": "Edit", "arg": "api/cart/cart.go", "out": "1 hunk", "ok": true,
  "file": "api/cart/cart.go", "add": 4, "del": 4, "task": "T5", "tid": "call_7f2" }
```

| Field | Type | Rule |
|---|---|---|
| `t` | number, seconds, 3 decimals | session-relative time (section 3). Non-decreasing in `seq` order within a tab, except keyframe events (section 11) which carry the keyframe's `t`. |
| `k` | string | the kind (sections 5 and 6). |
| `seq` | integer | the tab journal's sequence number: starts at 1 per tab generation, +1 per event, never reused within a generation. The page stores it on the event (`S.add` already writes `e.seq`; the page must keep the server's value, see UI-WIRING.md "live.js"). |
| other | per kind | below. Absent optional fields are omitted, never `null`, unless a row says `null` is meaningful. |

Ids that appear in events:

| Id | Form | Source |
|---|---|---|
| agent id | `mgr`, `be-1`, `sc-2`, ... (role short code + `-` + n) | swarm ids (`internal/swarm/spawn.go:189`); the single agent `main` (`internal/session/resume.go` const `soloAgent`) is translated to `mgr`; the harness's own sender (mail from the harness) is never an agent id in this vocabulary |
| task id | `T1` ... | the board |
| question id | `q_` + 26 lowercase base32 chars (130 random bits) | the approvals bridge (section 9) |
| checkpoint id | `c` + the store number, at least two digits: `cp_0007` is `c07`, `cp_0123` is `c123` | checkpoint store ids (`internal/checkpoint/store.go:297`); the server accepts `c07`, `cp_0007`, `7` |
| message id `mid` | `m` + the `seq` of the event that opened the message | the translator |
| tool call id `tid` | the harness tool call id (opaque) | `core.Block.ToolID` |

Ordering: events of a tab are delivered in `seq` order. The page inserts by `t` (`Session.add`, `50-sessions.js`) and breaks ties by
arrival; because `t` is non-decreasing in `seq` order the two orders agree.

## 3. Time

* **Run start.** `startedAt` (epoch milliseconds, in the tab's meta) is the moment the tab's current harness session was opened
  or resumed by this server (`session.New` returning, measured on the server's wall clock). A fresh restart (section 10 `reset`)
  opens a new run with a new `startedAt`; a resume, a restart that carries the conversation, and a model change on a team are also
  new runs.
* **`t`** = (event wall time − `startedAt`) / 1000, rounded to milliseconds, never negative (a harness event stamped before
  `startedAt`, such as the resume's own `session.start`, gets `t: 0`). Event wall time is `events.Event.TS` for log-derived events and
  the server's clock at the moment of the sink or bridge call for live ones.
* **`at`** (optional, epoch milliseconds) is set on history events (section 12) whose `t` was clamped to 0, so that the page can show
  their real time of day. The page's time-of-day helper prefers `at` when present (UI-WIRING.md, "tod with at").
* **Clock on the page.** `meta.startedAt` gives the page `S.meta.t0` (seconds since local midnight of `startedAt`, used by `tod`) and
  `S.meta.started` (`hh:mm:ss`). The world clock `S.wt` is set from the snapshot's `now` and advances with wall time as in the mock;
  `ping` frames (every 15 s) carry each tab's `now` and the page sets `S.wt = max(S.wt, now)`; an arriving event with `t > S.wt` first
  sets `S.wt = t` (so a live event is never held back by a slow page clock); an event with `t < S.wt` is inserted at `S.wt` by the
  existing `Session.add` clamp. Units everywhere are seconds unless a field name ends in `ms` or `At`.

## 4. Sources (abbreviations used below)

| Tag | Source | Where |
|---|---|---|
| L | the session log, `events.Log.Subscribe` (`internal/events/log.go:475`), folded by a per-tab `state.State` (`internal/tui/state/state.go:109`) | B2 translator |
| St | a diff of `state.State.SnapshotAt(now)` (`internal/tui/state/snapshot.go:50`) taken on a 250 ms tick and after each applied log event: per-agent status, tokens, TTL, governor | B2 |
| S | `agent.Sink` callbacks `Text`, `Thinking`, `ToolStart`, `ToolEnd`, `Response`, `Notice` and `Resetter.Reset` (`internal/agent/agent.go:80`) | B2 sink, installed by B1 as `Options.Sink`/`NewSink` |
| P | the approvals bridge (the `perm.Prompter` of the tab, `internal/perm/perm.go:266`) | B1 bridge, reported to B2 |
| H | the host: user actions, the goal loop, restarts | B1, through `Translator.Emit` |
| C | checkpoint changes (section 16) | B3 hook, B2 translation |

## 5. The thirty mock kinds

The reducer's `switch` (30-model.js) handles exactly: `say sys local tool note state task plan verdict req use warm gov mail ckpt ask
answer queue merge break compact stream diff goal final steer reply interrupt refuse digest` (30). Each subsection: shape, fields the
reducer or a view reads, when the server emits it, how it is derived, coalescing class (section 14).

### 5.1 `say`

```json
{ "t": 4.0, "k": "say", "seq": 12, "who": "mgr", "text": "I'll have three scouts map the API", "stream": true, "rate": 240, "mid": "m12" }
{ "t": 0.4, "k": "say", "seq": 3, "who": "you", "text": "/goal Build the shop" }
{ "t": 1.0, "k": "say", "seq": 4, "who": "sys", "glyph": "◇", "text": "goal set; plan:", "plan": true }
```

| Field | Type | Read by |
|---|---|---|
| `who` | `you` \| `mgr` \| `sys` \| `scouts` \| `local` | reducer: picks the row kind |
| `text` | string (cap 16 KiB per event; more arrives as `more`) | rows `you`, `say`, `sys` |
| `glyph` | one character (`◇ ⚙ ⚠ ✓ ↺ ⏸ ▶ ❯ ◆`) | `sys` row |
| `plan` | bool | `sys` row: appends "N steps, see Goal plan" |
| `task` | task id | `sys` row `data-task` |
| `stream` | bool (default true for `mgr`) | `say` row typed at `rate` |
| `rate` | characters per second | typing speed |
| `lines` | `[[agentId, text], ...]` | `scouts` row |
| `title`, `html` | strings | `local` row |
| `mid` | message id (additive, 7.1) | `more` target |
| `at` | epoch ms (additive) | history time display |

Emitted: `who:"you"` (H) when the host delivers a person's message to the agent (on send, or when a queued line is drained; text is
the line as shown in the composer, section 9 of CONTRACT.md "display text") and when a slash command that the mock echoes is typed
(`/goal TEXT`, `/plan PROMPT`); history: `user.input` with no `origin` (L). `who:"mgr"` (S): the first non-empty `Text` delta of a
response of agent `mgr` (or `main`) opens the message with `stream: true`, `rate: 240`, `mid`; later deltas are `more` (6.1).
History: `turn.append` assistant text blocks (L), `stream: false`. `who:"sys"` (H): host status lines (goal set, resumed, restarted,
budget reached). `who:"scouts"` and `who:"local"`: **not produced** by the server (`scouts` was a scripted summary; `local` cards are
made by the page itself, `93-palette.js` `local()`). Class: never coalesced (the `more` continuation is).

### 5.2 `sys`

```json
{ "t": 17.1, "k": "sys", "seq": 80, "ch": "mgr", "glyph": "⚙", "text": "lease api/catalog/** → be-1", "ag": "be-1", "task": "T4" }
```

Fields read: `ch` (channel: `mgr` or an agent id), `glyph`, `text`, `ag`, `task`. Emitted (L): `notice` events (level `warn`/`error` →
`⚠`, else `◇`; text capped 300), `lease` events of action `conflict` and `scope` (`⚠ lease conflict: <path> held by <holder>`),
`swarm.stall` raise and clear (section 15), `swarm.handover` begin/done/abort, `merge.conflict` (`⚠ T3: merge conflict in <files>`),
`merge.verify_failed` (`⚠ T3: <cmd> failed (exit 1)`), `merge.rejected`, `mail.drop`, `swarm.budget` (`⚠ budget reached: ...`),
`agent.panic`/`tool.panic`/`supervisor.panic` (`⚠ <agent> crashed: ...`, text without stack). Emitted (H): acknowledgments of
person actions, exact texts in CONTRACT.md per endpoint ("mode: plan (read-only)", "budget: $5.00 for the turns from now on",
"--isolation worktree (applies when the team starts again)", "reverted a hunk of P", ...). Emitted (S): `Notice(agent, level, msg)`
for agents (ch = the agent when not `mgr`). Class: never coalesced; rate-limited per source (section 14).

### 5.3 `local`

Page-only: `{ "k": "local", "title": "/cost", "html": "<table>…</table>" }`. **Never sent by the server.** The page builds it from
its own model with escaped values (`93-palette.js`). It is not in the journal: a reload loses local cards (UI-LOCAL).

### 5.4 `tool`

```json
{ "t": 27.21, "k": "tool", "seq": 140, "id": "be-1", "name": "Write", "arg": "api/catalog/items.go", "out": "wrote 31 lines",
  "ok": true, "file": "api/catalog/items.go", "add": 31, "del": 0, "task": "T4", "tid": "toolu_01" }
{ "t": 17.4, "k": "tool", "seq": 85, "id": "mgr", "name": "Edit", "arg": "api/server.go", "out": "", "ok": false, "refused": true,
  "reason": "the manager edits no file: spawn a worker" }
```

Fields read: `id` (agent), `name` (display name, section 8.2; the views test `/^(Write|Edit)$/` and `Bash`), `arg` (one line, cap
160), `out` (one line, cap 200), `ok`, `refused`, `reason` (cap 300), `file` (project-relative path, `/` separators), `add`, `del`,
`task`. Additive: `tid`. Emitted (S): `ToolEnd(agent, call, res, took)`: `ok = !res.IsError`; `out` = the first non-empty line of the
result text, or for multi-line results `"<n> lines"`; `refused`/`reason` when the result is a permission refusal (the tool result's
`refused` meta, `internal/agent/exec.go`); `file/add/del` from the result meta of `edit` (path, added, removed), `write` (path,
created/bytes: `add` = line count of the content, `del` = 0 when created, else the previous line count when known), `apply_patch`
(one `tool` event per touched file is **not** emitted: one event, `file` = the first file, `arg` = the files joined by `, `, `add`/`del`
= totals). `task` = the agent's current task in the per-tab `state.State`. A refusal for want of anyone to ask is sent as `refuse`
(5.29), not as `tool`. History (L): `tool.call` + `tool.result` pairs (`meta` carries the same numbers). Class: never coalesced.

### 5.5 `note`

```json
{ "t": 33.4, "k": "note", "seq": 170, "id": "be-1", "g": "done", "text": "submitted T4", "task": "T4" }
```

Fields read: `id`, `g` (`done` `tool` `edit` `mail` `ask` `steer` `x`; feed glyph), `text`, `task`. The digest counts notes whose
text starts with `submitted`. Emitted (L): a board transition of a task to `review` by its owner → `g:"done"`, `text:"submitted <T>"`;
`agent.end` with non-empty `evidence` → `g:"done"`, `text` = evidence (cap 160). Class: never coalesced.

### 5.6 `state`

```json
{ "t": 30.5, "k": "state", "seq": 158, "id": "be-2", "s": "edit", "doing": "Edit api/cart/cart.go", "task": "T5" }
```

Fields read: `id`, `s` (`idle think tool edit wait ask done stuck`), `doing` (one line, cap 120), `task` (string or `null`; absent
= unchanged). Every `state` closes the agent's open gantt segment and opens a new one (`A.segs`); a change into `ask done stuck idle
wait` adds a status row to the agent's channel. Emitted (St + S): whenever the mapped status, `doing` or task of an agent changes;
`ToolStart` produces it immediately (no wait for the tick). Mapping in section 8.3. Class: **never coalesced, never dropped** (the
gantt is built from them); repeats with identical `s`/`doing`/`task` are suppressed at the source.

### 5.7 `task`

```json
{ "t": 16.7, "k": "task", "seq": 60, "id": "T4", "title": "catalogue: GET /items?page&size", "owner": "be-1", "deps": ["T3"],
  "scope": "api/catalog/**, api/server.go", "s": "running" }
```

Fields read: `id`, `title`, `owner`, `deps` (array), `scope` (string: the task's file globs joined by `, `; `-` when none), `s`
(`todo running verify merged`). The first event of a task creates it; later events move it (`s`) and the reducer keeps the creation
fields. Emitted (L): every `board.op` that changes a task's derived column, title, owner or deps (`state.Task`, `internal/tui/state/
types_swarm.go:26`). Mapping in 8.4. Additive: `closure` (7.6). Class: never coalesced.

### 5.8 `plan`

```json
{ "t": 16.8, "k": "plan", "seq": 64, "n": 2, "s": "act" }
{ "t": 16.8, "k": "plan", "seq": 64, "steps": ["Survey the API", "Catalogue endpoint"], "st": ["done", "act"] }
```

Fields read: `n` (index), `s` (`pending act done verify edit ask queued`). Additive full-replacement form `steps` + `st` (7.4).
Emitted (L): a `tool.call` of the tool `plan` by the agent the person talks to (`mgr`/`main`) whose input `items` normalizes
(`internal/plan/plan.go` `Normalize`) → the full form. The single-index form is not produced by the server. Class: latest wins per tab.

### 5.9 `verdict`

```json
{ "t": 19.2, "k": "verdict", "seq": 90, "text": "not yet: T4-T8 unfinished, and no passing go test in the evidence",
  "kind": "continue", "left": ["T4-T8 merged", "go test ./... passes"] }
```

Fields read: `text` (backticks are stripped by the views). Additive: `kind` (`checking` `continue` `done` `blocked`), `left`
(array of strings, at most 8, each cap 160). Emitted (H): when the host's goal loop starts judging (`kind:"checking"`, `text:"checking:
the judge reads the evidence of this turn"`) and when the verdict arrives: `continue` → `"not yet: " + reason`, `done` → `"met: " +
reason`, `blocked` → `"blocked: " + reason`; a judge that failed → `"not yet: the judge could not be asked (...)"`. History: the
`goal.judge` log event (section 16). Class: latest wins.

### 5.10 `req`

```json
{ "t": 37.6, "k": "req", "seq": 190, "id": "be-2", "ratio": 0.7700348, "p": 8612, "o": 300 }
{ "t": 0, "k": "req", "seq": 3, "id": "be-2", "ratio": 0.95, "hist": true }
```

Fields read: `id`, `ratio` (0..1, exact, not rounded: the reducer computes `rd += round(p × ratio)`), `p` (prompt tokens: input + cache
read + cache write), `o` (output tokens), `hist` (history only: adds to the ratio series and request count, not to tokens, the warm
clock or the gantt marks). Emitted (L): each `model.response` of a **main** request (`model.request` kind `main`, not the compactor)
with usage: `ratio = cache_read / (input + cache_read + cache_write)` (0 when the prompt is 0), `p`, `o = output_tokens`. Keyframes
send the agent's retained `Hits.Ratios` as `hist:true` events (section 11). Class: never coalesced (counters are cumulative).

### 5.11 `use`

```json
{ "t": 37.9, "k": "use", "seq": 191, "id": "be-2", "rd": 6622, "un": 1978, "out": 2100, "wr": 1200, "cost": 0.0123, "saved": 0.0060 }
```

Fields read: `id`, `rd`, `un`, `out` (absolute token counters; replace the agent's table). Additive: `wr`, `cost`, `saved` (7.3).
Mapping from `state.Agent.Tokens` (`internal/tui/state/types.go:76`): `rd = CacheRead`, `un = Input + CacheWrite`, `out = Output`,
`wr = CacheWrite`, `cost = CostUSD` (reported cost, compactor calls included), `saved = SavedUSD`. Emitted (St): after every
`model.response` of the agent (main or side) and in keyframes. Class: latest wins per agent.

### 5.12 `warm`

```json
{ "t": 37.6, "k": "warm", "seq": 192, "ttl": 300 }
```

Fields read: none but `t` (`m.lastReq = t`, the warm clock's refill moment). Additive: `ttl` (seconds, 7.3). Emitted (St): when the
newest `TTLEntry` of kind `prefix` (`internal/tui/state/types_swarm.go:397`) changes its `Last`; `t` is that `Last` (the start of the
request that refreshed the prefix), `ttl = TTLSeconds`. For a single agent (no prefix entry) the agent entry of `mgr`/`main` is used.
Class: latest wins.

### 5.13 `gov`

```json
{ "t": 40.0, "k": "gov", "seq": 200, "rpm": 44, "r429": 0, "retries": 0, "inflight": 3, "queued": 0 }
```

Fields read: `rpm`. Additive: `r429`, `retries`, `inflight`, `queued` (7.3). Mapping from `state.Governor` (`types_swarm.go:235`):
`rpm = RPM`, `r429 = RateLimited`, `retries = Retries`, `inflight`, `queued`. Emitted (St): at most every 5 s while any value changed,
and at once when `r429` or `retries` change. Class: latest wins.

### 5.14 `mail`

```json
{ "t": 31.0, "k": "mail", "seq": 150, "from": "be-1", "to": "fe-1", "text": "catalogue: GET /items?page=1&size=12 -> {items[], page}" }
```

Fields read: `from`, `to`, `text` (cap 400). Emitted (L): `mail.send` between agents (`from`/`to` mapped per 8.1). Mail whose sender
is the harness itself (wakes, nudges) is not a `mail` event (it is not agent mail; it may appear as `sys` when its notice is
user-relevant). Digested mail (`mail.digest`) is sent as one `mail` from the mailman's recipients' view: `from` = the first sender,
`text` = the digest text, prefixed `digest of N: ` (mailman on only). Class: never coalesced.

### 5.15 `ckpt`

```json
{ "t": 33.0, "k": "ckpt", "seq": 160, "cid": "c07", "ts": "03:04:38", "files": 3, "note": "turn 3: build the shop", "skipped": false,
  "at": 1760050278000, "agents": ["be-1"], "add": 61, "del": 4 }
```

Fields read: `cid` (the id shown), `ts` (time of day, `hh:mm:ss`, server local time), `files` (count), `note` (label), `skipped`
(true when no file was touched), `safety` (a checkpoint taken by the web before a restore). The mock's `step` field (pack change-set
name) is **not produced**. Additive: `at`, `agents`, `add`, `del`; and the update rule (7.5). Emitted (C): when a checkpoint begins
(files 0, skipped true) and each time its file set changes, debounced to 1 s; a restore's safety checkpoint with `safety: true`.
Class: latest wins per `cid`.

### 5.16 `ask`

```json
{ "t": 37.0, "k": "ask", "seq": 180, "q": {
  "id": "q_k3m9w2c8x4b7d1f5h6j0n2p4r8", "agent": "fe-1", "task": "T6", "cmd": "npm install --save-dev vitest", "cwd": "web/",
  "why": "installs a package from the network and edits web/package.json; it is not a build or test command",
  "scope": "cwd web/ (inside fe-1's lease web/**)", "what": "this command", "rule": "Bash(npm install --save-dev vitest)",
  "kind": "command", "tool": "bash", "offersTests": false } }
```

Fields read (85-ui-approvals.js, 30-model.js, 50-sessions.js, 91-views-b.js, 90-views-a.js): `q.id`, `q.agent`, `q.task`, `q.cmd`,
`q.cwd`, `q.why`, `q.scope`, `q.what`, `q.rule`; the reducer adds `t0`, `answered`, `choice`. `q.cont` is mock-only (scripted
continuation) and is not produced. Additive: `kind`, `tool`, `offersTests` (section 9). Emitted (P): when the bridge accepts a
prompter call. Class: **never coalesced, never dropped**.

### 5.17 `answer`

```json
{ "t": 41.9, "k": "answer", "seq": 205, "qid": "q_k3m9w2c8x4b7d1f5h6j0n2p4r8", "choice": 2, "note": "", "by": "you",
  "rule": "Bash(npm install --save-dev vitest)" }
```

Fields read: `qid`, `choice` (1 yes, 2 yes and remember, 3 no), `note`. The reducer closes the question and writes a `sys` row
"you answered N to agent: cmd". Additive: `by` (`you` `timeout` `canceled` `closed` `nobody`), `rule` (the rule added by choice 2).
Emitted (P): when the bridge resolves the question, whatever resolved it (an answer from any page, a timeout, an interrupt, the tab
closing). For `by` other than `you` the choice is 3. Class: never coalesced, never dropped.

### 5.18 `queue`

```json
{ "t": 37.4, "k": "queue", "seq": 185, "head": "T4", "cmd": "go test ./api/catalog/...", "step": "verifying" }
{ "t": 43.7, "k": "queue", "seq": 230, "head": "T4", "cmd": "go test ./api/catalog/...", "step": "verified", "ms": 6300 }
{ "t": 44.6, "k": "queue", "seq": 236, "head": null, "conflicts": 0, "bounced": 1 }
```

Fields read: `head` (task id, `"goal"`, or `null` = empty), `cmd`, `step` (`verifying` \| `verified`), `ms`. Additive: `conflicts`,
`bounced` (absolute counters, 7.3). Emitted (L), isolated teams only: `merge.queued` → `verifying` (head = the task id parsed from the
subject `T3: title`, cmd = the session's `--verify` with `{dirs}` expanded to the task's directories, or `merge (no verify)`);
`merge.merged` → `verified` with `ms` = merged ts − queued ts, then `head: null` after the `merge` event; `merge.conflict` and
`merge.rejected` → `head: null`, `conflicts` +1; `merge.verify_failed` → `head: null`, `bounced` +1. The goal-level head (`"goal"`)
is **not produced** (the mock's endgame script). Shared-tree teams have no merge queue: no `queue` events. Class: latest wins.

### 5.19 `merge`

```json
{ "t": 44.0, "k": "merge", "seq": 233, "id": "T4", "cmd": "go test ./api/catalog/...", "ms": 6300 }
```

Fields read: `id` (task), `cmd`, `ms`. Effect: task merged, a `sys` row "T4 merged: rebase ✓ · cmd ✓ 6.3s", a note in the owner's
channel, a gantt mark. Emitted (L): `task.merge` with outcome `merged` or `empty` (isolated teams). A shared-tree task the manager
accepts moves with `task {s:"merged"}` and no `merge` event. Class: never coalesced.

### 5.20 `break`

```json
{ "t": 41.0, "k": "break", "seq": 210, "id": "be-2", "kind": "low_hit", "read": 0, "expected": 4500,
  "why": "the prompt prefix did not change: the endpoint did not serve it" }
```

Fields read: `id`, `kind`, `read`, `expected`, `why` (cap 200). Emitted (L): `cache.anomaly` (`state.Anomaly`, types.go:342):
`read = Actual`, `expected = Expected`, `why` = the explanation title of the kind (`internal/inspect/explain.go`, exported for B2 as a
pure function, OWNERSHIP.md). Class: never coalesced.

### 5.21 `compact`

```json
{ "t": 43.0, "k": "compact", "seq": 220, "id": "be-1", "from": 1400, "to": 625, "pct": -55 }
```

Fields read: `id`, `from`, `to` (tokens), `pct` (signed integer percent: `round((to − from) / from × 100)`). Emitted (L):
`compact.commit` (`state.Compaction` Before/After). Class: never coalesced.

### 5.22 `stream`

```json
{ "t": 40.0, "k": "stream", "seq": 200, "id": "be-2", "text": "Total() keeps cents as int64", "rate": 240, "mid": "m200" }
{ "t": 42.3, "k": "stream", "seq": 215, "id": "ts-1", "text": "package catalog\n\nimport \"testing\"\n", "rate": 400, "code": true,
  "file": "api/catalog/items_test.go", "mid": "m215" }
```

Fields read: `id`, `text`, `rate`, `code`. The reducer stores `m.streams[id]` (stall typing, Workspace "being written") and pushes a
row into the agent's channel (and, unless `code`, into the manager's feed). Additive: `mid`, `file` (7.2). Emitted (S): the first
non-empty `Text` delta of a response of a worker opens a prose stream (`rate: 240`), later deltas are `more`; a `ToolStart` of the
`write` tool whose `content` is at most 64 KiB of text opens a code stream with the whole content at once (`code: true`, `rate:
400`, `file` = the project-relative path). Class: the opening event is never coalesced; continuations are `more`.

### 5.23 `diff`

```json
{ "t": 27.3, "k": "diff", "seq": 141, "file": "api/catalog/items.go", "done": true }
```

Fields read: `file`, `done` (the Workspace and the diff sheet stop the "live" caret when true). Emitted (S): `ToolEnd` of `write`,
`edit`, `apply_patch` (one per file). Class: latest wins per file.

### 5.24 `goal`

```json
{ "t": 1.0, "k": "goal", "seq": 5, "s": "active", "objective": "Build the shop ...", "turns": 0, "max": 20 }
{ "t": 90.0, "k": "goal", "seq": 400, "s": "paused", "objective": "Build the shop ...", "turns": 3, "max": 20, "paused": "you interrupted it" }
```

Fields read: `s` (`active paused met cleared`). Additive: `objective`, `turns`, `max`, `paused` (why), `reason` (the judge's last
reason) (7.7). Emitted (H): every change of the host's `goal.State` (set, continue, pause, resume, clear, met). History (L):
`goal.state` events (`internal/session/goal.go:162`; the struct has no JSON tags: keys `Objective Turns Max Paused Reason Repeats
Done`): `Done` → `met`, `Paused != ""` → `paused`, a null goal after one → `cleared`, else `active`. Class: latest wins.

### 5.25 `final`

```json
{ "t": 731.0, "k": "final", "seq": 300 }
```

Fields read: none (`m.final = {t, steps}`; a summary row "-- 731s · 12 steps · $0.04"). Emitted (H): when a turn of the agent the
person talks to ends (`Session.Run` returned, goal loop included: after the last continuation), together with `turn {s:"end"}`.
History: the end of each `user.input`'s turn in the log. Class: never coalesced.

### 5.26 `steer`

```json
{ "t": 50.0, "k": "steer", "seq": 260, "to": "mgr", "text": "use the existing pager component" }
{ "t": 42.0, "k": "steer", "seq": 212, "to": "fe-1", "text": "don't install anything", "quiet": true }
```

Fields read: `to`, `text`, `quiet` (no feed row). Emitted (H): `POST .../steer` accepted (`Agent.Steer` on the manager,
`internal/agent/agent.go:497`) → `to: "mgr"`; an answer 3 with a note → `to` = the asking agent, `quiet: true` (the note reaches the
agent as the refusal's reason, section 9). History: `user.steer` is never written by the harness today; steers are not in history.
Class: never coalesced.

### 5.27 `reply`

Shape `{ "k": "reply", "id": "mgr", "text": "..." }`. **Not produced**: the manager's answer to a steer is its next ordinary `say`.

### 5.28 `interrupt`

```json
{ "t": 60.2, "k": "interrupt", "seq": 300, "id": "turn" }
```

Fields read: `id` (`turn` or an agent id). Emitted (H): `POST .../interrupt` and `POST .../stop` accepted, once per call, `id:"turn"`.
Agent-level interrupts are not offered (the person talks to the manager only). Class: never coalesced.

### 5.29 `refuse`

```json
{ "t": 284.0, "k": "refuse", "seq": 900, "id": "dc-2", "name": "Bash", "arg": "npm run lint:md docs/",
  "reason": "needs approval and nobody can answer: headless run, --ask-timeout 10m" }
```

Fields read: `id`, `name`, `arg`, `reason`. Emitted (S): a `ToolEnd` whose refusal reason is the engine's "no one to ask" or "no
answer in time" refusal (`internal/perm` constants `NoOneToAsk`, `askTimedOut`), i.e. a question that could not be asked or was not
answered. Class: never coalesced.

### 5.30 `digest`

Page-only: the catch-up digest row is made by `Session.collapse()` (50-sessions.js), not by an event. **Never sent.**

## 6. The five additive kinds

### 6.1 `more`: continue a streaming message

```json
{ "t": 4.35, "k": "more", "seq": 13, "mid": "m12", "text": " then split the build by directory" }
{ "t": 6.10, "k": "more", "seq": 40, "mid": "m12", "text": "", "end": true }
```

Appends `text` to every transcript entry and stream record opened by the event whose `mid` it names; `end: true` closes the message
(no more text; the typing caret ends when the typed position reaches the end). Emitted (S): text deltas of an open message,
coalesced on the server into one `more` per message per 100 ms; `end` on `Response`, on `ToolStart` of the same agent, on `Reset`
(a retried request: `end` is sent with `"reset": true`, and the page leaves the shown text as it is, as the TUI does), and on turn end.
A message is capped at 64 KiB; beyond it the server sends `end` and drops the rest. Class: ordinary (the translator merges a
message's deltas per 100 ms before publishing; a page that misses one refetches the snapshot, which holds the whole text).

### 6.2 `turn`

```json
{ "t": 700.0, "k": "turn", "seq": 3, "s": "start" }
{ "t": 731.0, "k": "turn", "seq": 300, "s": "end" }
```

`start` clears `m.final` so that `calc.turnRunning` and `Session.state()` show a new turn running after a turn that ended (the mock
never ran a second turn after `final`). `end` is sent with `final`. Emitted (H): the host starts and ends a turn (message delivered,
goal continuation, queued line drained). Class: never coalesced.

### 6.3 `stall`

```json
{ "t": 300.0, "k": "stall", "seq": 990, "id": "be-1", "task": "T4", "kind": "claimed_no_progress", "s": "raise",
  "text": "be-1 claimed T4 9 minutes ago and has not reported progress" }
```

Emitted (L): `swarm.stall` (`internal/swarm/stall.go:424`): `s` = action (`raise`/`clear`), `kind` (`claimed_no_progress`
`manager_waiting_on_idle` `orphaned_task` `blocked_cycle` `review_starved`), `id` = agent (may be absent), `task`, `text` = detail
(cap 200). The reducer stores it (`m.stalls`); **not shown** as such: the server also emits a `sys` row for `raise` (`⚠` + text) and
for `clear` (`◇ <kind> cleared`), which is how the mock's existing rows show it. Class: never coalesced.

### 6.4 `handover`

```json
{ "t": 410.0, "k": "handover", "seq": 1200, "task": "T5", "from": "be-2", "to": "be-3", "s": "done", "closure": "handed_off(be-3)" }
```

Emitted (L): `swarm.handover` (`internal/swarm/handover.go:147,223,283`): `s` = phase (`begin` `done` `abort`), `from`, `to`, `task`,
`closure`, `error` (cap 200). The reducer stores it; **not shown** as such: the server also emits a `sys` row ("↺ T5 handed from be-2
to be-3") and, on `done`, a `task` event with the new owner, which moves the card's owner colour as the board already does.
Class: never coalesced.

### 6.5 `layers`

```json
{ "t": 37.6, "k": "layers", "seq": 193, "id": "be-2", "toks": [1900, 2200, 400, 300, 350, 1000] }
```

The agent's latest prompt by layer G0..G5 (six integers, tokens). G6 (the hot tail) is folded into G5 so that the mock's six-layer
bar is unchanged. Derivation (St): `state.Agent.Stack` (`types.go`): G1..G4 from `Sections` (`shared role notes spine`), G0 =
`min(constitution estimate, Unsectioned)` and G5 = `Unsectioned − G0`, exactly as the TUI's stack bar splits it
(`internal/tui/app/cacheview.go:187` `promptLayers`, to be exported as a pure helper by B2). Emitted after each main response of the
agent. Class: latest wins per agent.

## 7. Additive fields on existing kinds, and the reducer patches (C1 implements in 30-model.js)

The patches change no rendering path; they only keep the model faithful to real data. Each is a few lines.

| # | Kind | Field(s) | Reducer change |
|---|---|---|---|
| 7.1 | `say`, `stream`, `reply` | `mid` | `push` remembers `m.mids[mid] = [entries]` (all copies: channel and feed); `stream` also stores `m.streams[id].mid` |
| 7.1 | `more` (new) | `mid text end reset` | for each entry in `m.mids[mid]`: `entry.text += text`; if the agent's `m.streams[id].mid === mid` also extend `m.streams[id].text`; `end` sets `entry.done = true`; unknown `mid`: ignore |
| 7.2 | `stream` | `file` | stored on `m.streams[id].file` (Workspace "being written", UI-WIRING C2) |
| 7.3 | `use` | `wr cost saved` | `A.wr = ev.wr; A.cost = ev.cost; A.saved = ev.saved` when present |
| 7.3 | `warm` | `ttl` | `m.ttl = ev.ttl` when present |
| 7.3 | `gov` | `r429 retries inflight queued` | `m.r429 = ...; m.retries = ...` when present |
| 7.3 | `queue` | `conflicts bounced` | `m.conflicts = ev.conflicts; m.bounced = ev.bounced` when present (the mock never incremented them) |
| 7.4 | `plan` | `steps st` | when `steps` is an array: `m.planText = steps.slice(); m.plan = st.map(map)` (`pending`→`pending`, `doing`/`act`→`act`, `done`→`done`) |
| 7.5 | `ckpt` | update rule, `at agents add del` | if an entry with the same id exists in `m.ckpts`, `Object.assign` it instead of `unshift`; store the additive fields |
| 7.6 | `task` | `closure` | `T.closure = ev.closure` when present |
| 7.7 | `goal` | `objective turns max paused reason` | merged into `m.goal` (already `Object.assign`; the extra keys simply ride along) |
| 7.8 | `verdict` | `kind left` | `m.verdictKind = ev.kind; m.left = ev.left || []` |
| 7.9 | `ask.q` | `kind tool offersTests` | none (copied with `q`) |
| 7.10 | `answer` | `by rule` | `q.by = ev.by` |
| 7.11 | `turn` (new) | `s` | `start`: `m.final = null; m.turn = true`; `end`: `m.turn = false` |
| 7.12 | `stall`, `handover`, `layers` (new) | | `stall`: `m.stalls[(ev.id||'')+'|'+ev.kind+'|'+(ev.task||'')] = ev` on raise, delete on clear; `handover`: `m.handovers.push(ev)` (cap 100); `layers`: `A.layers = ev.toks` |
| 7.13 | all | `at` | none (the view's `tod` prefers `e.at`, UI-WIRING) |

`calc.agent(a)` (the only place tokens become dollars) uses `a.cost` and `a.saved` when they are numbers, else the mock's price
formula. `calc.warmLeft(m, vt)` uses `(m.ttl || 25)` in place of the constant 25. `newModel` initialises `mids: {}`, `stalls: {}`,
`handovers: []`, `planText: null`, `ttl: 0`, `r429: 0`, `retries: 0`. `VISIBLE` (the hold chip's "new" count) adds nothing: `more`,
`turn`, `stall`, `handover`, `layers` are not visible kinds.

## 8. Mapping tables

### 8.1 Agent ids

| Harness | UI |
|---|---|
| `mgr` (swarm manager) | `mgr` |
| `main` (single agent) | `mgr` |
| workers `be-1` ... | unchanged |
| the mailman (`mm-1`, service role) | not in the roster; its mail shows as digests (5.14) |
| the harness sender of mail | not an agent: no `mail` event |

Roster entries (CONTRACT.md `Roster`): `{id, role, code, nth, k, leg, scope, ro, model, spawn}`; `k` = 1-based start order of workers
(by `agent.spawn` seq, service agents excluded); `leg = (k − 1) mod 8`; manager `leg: -1`, `k: 0`; `spawn` = `t` of its
`agent.spawn`; `scope` = the joined globs of its current task, `- (read-only)` for read-only roles, `- (edits no file)` for the
manager of a team, `**` for a single agent; `ro` from the role (`internal/swarm/roles.go:35` ReadOnly).

### 8.2 Tool display names (`tool.name`, `refuse.name`, `doing`)

| Harness tool | `name` | `arg` |
|---|---|---|
| `bash` | `Bash` | the command line (one line) |
| `bash_output`, `bash_kill` | `Bash` | `output <job>`, `kill <job>` |
| `read` | `Read` | path |
| `write` | `Write` | path |
| `edit` | `Edit` | path |
| `apply_patch` | `Edit` | the files, joined by `, ` |
| `glob` | `Glob` | pattern |
| `grep` | `Grep` | pattern and path |
| `ls` | `Ls` | path |
| `web_fetch` | `WebFetch` | URL host and path |
| `web_search` | `WebSearch` | query |
| `plan` | `Plan` | `N steps` |
| `task` (board) | `TaskBoard` | `create T4 T5`, `claim T4`, ... |
| `spawn` | `Spawn` | the agent ids |
| `mail` | `Mail` | `→ <to>` |
| `note` | `Notes` | `append` |
| `wait` | `Wait` | what it waits for |
| `recall` | `Recall` | the handle |
| `skill` | `Skill` | the skill name |
| MCP tools `mcp__srv__tool` | `srv.tool` | the first argument |
| anything else | the harness name with its first letter upper-cased | `Request.Summary` |

### 8.3 Agent status → `state.s`

| `state.Status` (types.go:19) | `s` | `doing` |
|---|---|---|
| `starting`, `thinking` | `think` | `Line` when set, else `thinking` |
| `tool` | `tool` | `<Tool> <ToolSummary>` (8.2 names) |
| `editing` | `edit` | `<Tool> <ToolSummary>` |
| `waiting` | `wait` | `Line` when set (e.g. "waits for T4 to merge"), else `waits` |
| `asking` | `ask` | `wants to run ` + the open question's `cmd` in backticks |
| `idle` | `idle` | `waits for work`, or the manager's `waits at the prompt` |
| `stuck` | `stuck` | `Stuck` note (cap 120) |
| `done` | `done` | `Evidence` (cap 120) or `done` |
| `error` | `stuck` | `failed: <error>` (cap 120) |

The manager of a team whose turn ended but whose workers run shows `wait` with `waits for the team (T4 T5 ...)` (the open tasks).

### 8.4 Task column → `task.s`

| `state.TaskState` | `s` |
|---|---|
| `todo` | `todo` |
| `running` | `running` |
| `verifying` | `verify` |
| `merged` | `merged` |
| `failed` | `todo` with `closure` set (DECISION NEEDED D-04 in FEATURES.md: the mock has no failed column) |

`closure` (string) is the task's typed closure (`internal/swarm/closure.go:14-36`) as written: `verified`, `agreed`, `blocked_on(T2)`,
`superseded(T7)`, `canceled`, `denied`, `verifier`, `exhausted`, `handed_off(be-3)`. A task closed `superseded` or `canceled` keeps its
last column.

### 8.5 Plan status

`pending` → `pending`, `doing` → `act`, `done` → `done` (`internal/plan/plan.go:18-20`). The mock's `verify edit ask queued` plan
states are not produced.

## 9. Questions (approvals)

* **Ids.** The bridge assigns `q_` + 26 base32 characters from `crypto/rand` (130 bits) per prompter call. Ids are single-use: a
  second answer to the same id is `409 answered`. They are not secrets (they appear in the answer route's path); they are
  unguessable so that nothing can answer a question it was not shown.
* **What is asked.** One `ask` per prompter call. Identical concurrent requests are already coalesced by the engine
  (`internal/perm/ask.go:45` `promptKey`), so one question can answer several waiting tool calls. Trust and MCP questions are not
  sent as questions by the web host (CONTRACT.md "Trust and MCP at session start"); a later `ToolProjectTrust`/`ToolMCPServer`
  request that does arrive is shown with `kind` `trust`/`mcp` (FEATURES.md D-09).
* **Fields.** `agent` (8.1), `task` (the agent's task), `cmd` = `Request.Command` for shell requests, else `Request.Summary` without its
  bracketed reason; `cwd` = `Request.Cwd` relative to the project root with a trailing `/`, or `.`; `why` = `Request.Why` (a field B3
  adds, set by `lead()` from the verdict reason); `scope` = `cwd <cwd>` plus ` (inside <agent>'s lease <globs>)` when the agent has a
  scope; `what` = `Request.Remembers` (e.g. `"go test" commands`), or `this command` when empty; `rule` = the first of
  `Request.RememberRules` (a field B3 adds: the rules a "don't ask again" would add); `kind` = `command` (bash), `edit` (write, edit,
  apply_patch), `read`, `web`, `trust`, `mcp`, `other`; `tool` = the harness tool name; `offersTests` = `Request.OffersTests`.
* **Choices → `perm.Decision`** (the TUI's table, `internal/tui/app/chat_dialog.go:68,123`):

  | `choice` | Decision |
  |---|---|
  | 1 | `{Allow: true, Reason: "allowed by user"}` |
  | 2 | `{Allow: true, Reason: "allowed by user", Remember: ScopeSession}`; for `kind` `trust`/`mcp`: `Remember: ScopeProject` |
  | 3 | `{Allow: false, Reason: perm.DeclinedWith(note)}` (B3 exports it: the engine's refusal advice plus `The person says: <note>`; with no note it is `"denied by user"`, to which the engine appends its own advice) |

  The TUI's fourth answer (`Preset: "tests"`) is not offered: the mock has three buttons.
* **Quiet period.** The page keeps its own rule (85-ui-approvals.js: `QUIET = 0.8` s of a quiet keyboard since the question appeared or
  since the last key; typed-ahead text never answers). The server enforces a floor independently: an answer is accepted only when
  `now ≥ armAt`, `armAt = max(question created, the tab's last answer) + 350 ms` (`defaultAnswerAfter`, `internal/tui/app/
  chat_dialog.go:49`). Earlier answers get `409 {"error":"too_soon","retryAfterMs":n}`. The page never hits the floor in normal
  use (0.8 s > 0.35 s); the floor stops a script that answers in the same instant the question is created.
* **Lifetime.** A question waits until answered, except: the agent's context ends (interrupt, stop, close: `by:"canceled"` or
  `"closed"`), the engine's `AskTimeout` passes (`sleipnir web --ask-timeout`, default 0 = no limit: `by:"timeout"`), or no page has had
  a stream connected for `--ask-grace` (default 10 minutes: `by:"nobody"`). An interrupt of the turn refuses the open questions of the
  agent the person talks to; workers' questions survive (as in the TUI, `internal/tui/app/chat.go:610-645`).
* **Order.** The engine asks one question at a time per engine (`ask.go` `lead`, semaphore); questions of different tabs are
  independent. The page shows the oldest open question of the tab first (`calc.openQuestion`); "N waiting" counts the tab's open ones.

## 10. Stream frames (control vocabulary)

One SSE connection per page (`GET /api/stream`, the hub topic `"ui"`, CONTRACT.md section 4) carries every tab. Each SSE message
has `id: <n>` (the hub's per-topic id, from 1 per server run), `event: <frame>` and `data: <JSON>`. The page learns the starting id
from `GET /api/hello` (`streamAfter`) and opens `EventSource('/api/stream?after=' + streamAfter)`.

| Frame | Data (Go type) | Page action |
|---|---|---|
| `ev` | `{tab, ev: <UI event>}` (`wire.EvFrame`) | `S.add([ev])` after the clock rule of section 3; drop if `ev.seq ≤ S.lastSeq` |
| `meta` | `{tab, patch: {...}}` (`wire.MetaFrame`) | `Object.assign(S.meta, patch)`; `S.touch()` |
| `roster` | `{tab, roster: [Roster]}` (`wire.RosterFrame`) | replace `S.roster`; add missing agents to `S.wm` and `S.m` (`SL.model.addAgent`); emit `roster-changed` |
| `tab` | `{op: "add"\|"update"\|"remove", tab: TabSummary}` (`wire.TabFrame`) | create, rename, or drop the page Session; `sessions-changed` |
| `reset` | `{tab, gen}` (`wire.ResetFrame`) | the tab started a new generation: discard `S.log`, fetch the snapshot |
| `recorded` | `{}` | refetch `GET /api/recorded` (list changed: prune, delete, a tab closed) |
| `run` | `{id, lines?: [{k, t}], step?, verdict?, result?}` (`wire.RunFrame`) | runner, doctor and run-now output |
| `toast` | `{tab?, text, kind}` (`wire.Toast`) | `ui.toast(text, kind)` (server notices that are not rows: a background job ended) |
| `ping` | `{now: {tabId: seconds}}` (`wire.Ping`) | every 15 s from the host; clock rule of section 3 |
| `bye` | `{reason}` (`wire.ReasonFrame`) | the server is shutting down; the page shows the disconnected state (FEATURES.md D-13) |
| `gap` (hub) | `{reason, dropped, last}` | the page may have missed frames (a replay id aged out, or ordinary frames dropped for a slow page): refetch every tab's snapshot |
| `lagged` (hub) | `{}` | the hub cut the page off for not keeping up with critical frames; `EventSource` reconnects with `Last-Event-ID` |
| `closed` (hub) | `{}` | the topic closed (shutdown): the disconnected state |

`hello` is not a frame: it is `GET /api/hello` (CONTRACT.md 5). The hub's heartbeats are SSE comment lines.

## 11. Late joiners, reconnects and keyframes

* **Journal (B2, per tab).** Every UI event of a tab is appended to the tab's journal with its `seq`. The journal keeps the most recent
  events up to 50,000 events or 32 MiB of JSON, whichever comes first. Events evicted from the front are folded into a **mirror**
  (a Go reduction of what the page's world model holds, minus transcript rows: per agent `state/doing/task`, token table, ratio
  history (last 256), `layers`, `lastReq`; tasks; plan; goal; verdict; checkpoints; open questions; merged list; counters; last 400
  mails; last 100 anomalies and compactions).
* **Snapshot (`GET /api/sessions/{id}/snapshot`).** `{tab, gen, seq, now, meta, roster, keyframe: [events], events: [events], hist,
  questions}`. `keyframe` is the mirror as synthetic events stamped with the `t` of the first retained event: one `use`, `layers`,
  `state` per agent; `req {hist: true}` per retained ratio; one `task` per task; `plan` (full form); `goal`; `verdict`; `ckpt` per
  checkpoint; `mail` for the retained mails; `break`/`compact` for the retained anomalies/compactions; `ask` for each open question
  that was evicted (never, in practice: open questions are pinned and never evicted); `warm`; `gov`; `queue`. `events` are the
  retained journal events in `seq` order. `seq` is the last included seq. The page builds `S.log = keyframe ⧺ events` (keyframe
  events get the `seq` 0), sets `S.wt = now`, rebuilds `S.wm` and, for the active tab, `S.m` (the mock's `rebuild`).
* **Order of operations on (re)connect.** The page calls `GET /api/hello`, opens the stream with `?after=streamAfter` and buffers `ev`
  frames per tab; then fetches the snapshots; then applies buffered frames with `seq > snapshot.seq` and discards the rest. Frames
  for a tab whose snapshot is in flight are buffered (bounded: 10,000 per tab; overflow → refetch that snapshot).
* **Reconnect.** `EventSource` reconnects with `Last-Event-ID`. If the hub's ring of the topic still holds the frames after that id
  (Δ3: 20,000 frames or 32 MiB) it replays them; otherwise it sends `gap` and the page refetches every snapshot. A server restart
  invalidates the cookie, so a page never resumes against another run; `hello.boot` is checked anyway.
* **Generations.** A fresh restart (`/new`, `/clear`, `/swarm`, Run settings Apply, a team role change that the person chose to start
  empty) starts a new generation: new journal, `seq` from 1, new `startedAt`; the hub sends `reset`. A restart that carries the
  conversation is also a new generation (new `startedAt`) whose snapshot begins with the history of section 12.

## 12. Resumed sessions and carried conversations: how history is produced

When a tab opens a harness session that already has a log (`--resume`, `--continue`, a restart that carries the conversation), B2
translates the existing `events.jsonl` (`events.Scan`, `internal/events/log.go:567`) before the new run's live events:

* Conversation rows: `user.input` (no origin) → `say you`; `turn.append` assistant text → `say mgr` (`stream: false`); tool call and
  result pairs → `tool`; `goal.state` → `goal`; `goal.judge` → `verdict`; checkpoints → `ckpt`; `board.op` → `task`; mail → `mail`.
  Every history event gets `t: 0` and `at` = its log timestamp. Workers' streamed prose is not in the log and is not shown.
* Counters and states: from a `state.State` folded over the whole old log (`state.FoldUntil`, `internal/tui/state/load.go:21`),
  emitted as keyframe events at `t: 0` (agents' token tables, ratio series as `req {hist: true}`, tasks, plan, layers).
* A `say sys` row `↺ resumed <sid> · <date of the last run>` (glyph `↺`) at `t: 0` follows the history and precedes the live run.
* Bounds: at most the last 4,000 history events (the oldest are dropped; the chat's fold marker counts what the page holds); history
  older than that is visible through Replay of the recorded session (FEATURES.md D-10).
* The Replay tape covers the current run (history sits at 0), as the mock's resumed session does.

## 13. Hold and catch-up with a real stream

The page keeps its governor unchanged (20-clock.js): the server never knows whether the page holds. Events arrive and go into `S.log`
at the world clock; while the view is held they pile up between `S.vt` and `S.wt` and are counted on the chip (`S.unseen()`, visible
kinds only); on release the view collapses everything older than 18 s into one digest row and replays the rest at up to 6x
(`Session.collapse`, `TIME_CONST.WINDOW`). Consequences the server relies on:

* The server must not delay, batch or reorder events for the page's sake; continuations (`more`) and `use` coalescing are bounded to
  100 ms and never change `t`.
* Questions arrive while the view is held: the world model sees them at once (`stepWorld` runs on `S.wm`), so the question box, the
  "Needs you" inbox and the tab badge appear immediately, as in the mock ("a question is waiting behind the hold" toast).
* A typed message is never blocked by a hold: `submit()` releases the hold (84-ui-chat.js) and the server echoes `say you`.

## 14. Bounds and coalescing

Three buffers exist; each has a rule per kind.

| Buffer | Owner | Bound | Overflow |
|---|---|---|---|
| sink queue (agent → translator) | B2 | 8,192 items per tab | merge text deltas of the same message first; if still full, drop the oldest coalescible items, count the loss, and emit one `sys` row `⚠ the page fell behind: some activity rows were skipped` plus a fresh `use`/`state`/`layers` set for every agent. `Sink` methods never block (agent.go:78 contract). |
| log subscription | B2 | `Log.Subscribe(4096)` | gaps are detected by `seq`; the translator calls `Log.Flush()` and re-scans `events.jsonl` from the last seen seq (as `pastState`, `cmd/sleipnir/chat_tty.go:151`) |
| per-page SSE queue | A2 (committed hub, `internal/web/stream.go`) | 2,048 frames or 8 MiB (Δ3) | the hub's loss policy: coalescable frames are replaced by a newer one with the same key or dropped first, ordinary frames next (the page gets `gap`), critical frames never (a page that cannot keep up with them gets `lagged` and reconnects with `Last-Event-ID`). **No event is lost to the page: anything dropped here is in the tab journal and comes back through a snapshot.** |

| Class (hub flag) | Kinds | Rule |
|---|---|---|
| `Critical` | `ask answer state task goal final turn merge mail tool say stream sys note refuse interrupt steer break compact req stall handover`, the first `ckpt` of an id; frames `tab reset roster meta bye` | delivered in order; never dropped |
| `Coalescable` with a key | `use` (`use/<tab>/<agent>`), `layers` (`layers/<tab>/<agent>`), `gov` `warm` `plan` `verdict` `queue` (`<kind>/<tab>`), `diff` (`diff/<tab>/<file>`), later `ckpt` of an id (`ckpt/<tab>/<cid>`), `ping` (`ping`) | the newer replaces the older in a slow page's queue; the journal keeps every one (the mirror folds them) |
| ordinary | `more`; frames `recorded`, `run`, `toast` | dropped after the coalescable ones for a slow page, which is told with `gap` and refetches; the translator already merges `more` per message per 100 ms before publishing |

Rate limits at the source (B2): `sys` rows from `notice` and `lease` at most 20 per tab per 10 s (further ones are counted into one
`sys` "N more notices"); `state` events per agent at most 20 per second (later ones replace earlier ones of the same tick).

## 15. New harness events and fields: mapping

| Harness | UI | Shown |
|---|---|---|
| `swarm.stall` raise/clear | `stall` + a `sys` row | the `sys` row only (6.3) |
| `swarm.handover` begin/done/abort | `handover` + a `sys` row + on `done` a `task` with the new owner | the `sys` row and the owner change (6.4) |
| `goal.state` | `goal` (with the additive fields) | the HUD goal state, the plan box, the Goal sheet |
| board.op `closure` | `task.closure` | not shown (8.4; D-04) |
| board.op `blocked_on` | `task.closure` = `blocked_on(T)`; the task stays `running`; the owner's `state` is `wait` with `doing` `blocked on T2` | via `doing` |
| board.op `kind` (plan task) / `agreement(s)` | not translated | not shown |
| board.op `verification_failures` | counts into `queue.bounced` for shared-tree teams | the merge queue's "bounced" |
| `agent.spawn` `handover_from` | roster entry (new agent) | as any spawn |

## 16. Log events this program adds (so that history can be rebuilt)

| Type | Data | Writer | Why |
|---|---|---|---|
| `checkpoint` | `{id, label, files: [paths], agents: [ids], time, safety?}` | the session, from a `checkpoint.Store` change hook (B3 adds the hook, B1 wires it to `Log.Emit`) | checkpoints are not logged today (`internal/checkpoint/store.go:510` Begin) |
| `checkpoint.restore` | `{id, files, safety, undo_of?}` | the web workspace service (B3) through the session log | Replay and history show restores |
| `goal.judge` | `{verdict, reason, left: []}` | `Session.JudgeGoal` (B1, `internal/session/goal.go:40`) | the verdict is not logged today |
| `user.steer` | `{text}` | the web host when a steer is accepted (B1) | the type exists and is never emitted |
| `web.action` | `{action, detail}` (no secrets, no file contents) | the web host for person actions that change state (mode, rules, budget, restart, revert, restore, rename) | audit trail; history `sys` rows |

These are additive log types; `state.State` counts them as unknown until B2 adds handlers; `inspect` ignores unknown types.
