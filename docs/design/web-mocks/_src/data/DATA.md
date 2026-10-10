# Data pack v2: schema, sources and rules

Shared sample data for the five Cockpit v2 variants. Every screen shows the same, self-consistent content. This file says
what exists, where each thing came from, and which numbers must never be retyped by hand.

* **REAL** = captured from `bin/sleipnir` (this tree) on the sample projects, or taken from the Go source. Strings are the binary's own.
* **SAMPLE** = invented (prices, people, ids, session history, the shop's story). Always shown as sample data.
* **PORT** = a JavaScript port of a Go formatter, checked against the binary (`verify-real.mjs`, `check.mjs`).

## Files and load order

| file | what | size |
|---|---|---|
| `data.js` | defines `window.SLDATA` (data + helper functions) | ~354 KB |
| `outputs.js` | adds `SLDATA.outputs`, `SLDATA.runOutput`, `SLDATA.newState`, `SLDATA.newSessionState`; **load after data.js** | ~84 KB |
| `cli-spec.json` | the command table for the runner forms (generated from `docs/CLI.md`) | ~104 KB |
| `check.mjs` | asserts the invariants in a `vm` sandbox; `node check.mjs` must print `ALL CHECKS PASSED` | |
| `verify-real.mjs` | runs the real binary on throw-away fixtures and diffs it with the ports (`sessions`, `sessions prune`, `models`) | |

`data.js + outputs.js` together stay under 450,000 bytes (the mocks inline them). Inline order: `data.js`, then
`SLDATA.cliSpec = <cli-spec.json>;`, then `outputs.js` (outputs.js reads `cliSpec` only for the `/help` text and has a built-in
copy when it is absent). Plain ES2019, no dependencies, deterministic (two loads give the same JSON), no HTML in any string
outside file contents.

Regenerate: `gen-cli-spec.mjs` -> `cli-spec.json`; `mkshop.sh` + `mkorders.sh` build two git repositories of the sample files;
`gen-shop.mjs` (PROJECT=shop|orders) -> `build/*.json`; `capture.sh` -> `real/*.cap` (the binary's output, from a fresh isolated
home, `</dev/null`, no network); `gen-sim.mjs`, `gen-replay.mjs`, `gen-rl.mjs`, `gen-real.mjs` -> `build/*.json`; `build.mjs` -> `data.js`,
`outputs.js` (concatenates `src/*.js` and `src/outputs/*.js`, injects `build/*.json`, strips indentation). The RL runs come
from `rl/` (`start-policies.sh`, `run-rollouts.sh`).

## The numbers (one table, everything derives)

`SLDATA.team` is the roster of the brief; `totals(team)` is the only way to get an overall figure.

```
prompt 59,900 = read 50,844 + uncached 9,056 · out 9,650 · hit 84.9% (shown 85%) · cost $0.0715 (header $0.07) · saved est. $0.059
```

Per agent: `prompt, read, uncached, out, hit (0..1), hitPct, cost, costText`. `S.costOf(agent)` prices with the sample card
(`S.prices`: manager $3.00/$0.30/$15.00, workers $1.00/$0.10/$4.00 per M; SAMPLE). `S.teamSummary()` gives `manager + 8 workers`,
`4 active of 8 workers`. Legs: `leg` 1-8 in start order (sc-1..sc-3 = 1-3, be-1 = 4, be-2 = 5, fe-1 = 6, ts-1 = 7, rv-1 = 8); the manager
(`rider: true`) has `leg: null`; a ninth worker shares leg 1 (`teamSummary().ninthWorkerSharesLeg`).
`S.hitSeries[id]` are per-request ratios whose mean is within 1 point of the cumulative hit (checked); be-2's 7th request reads 0.
The roster counters are the display model every panel agrees on; they do not decompose into per-request sizes at a 4.5k shared
prefix, so the series carries ratios and marks only (`S.seriesMarks`).

`S.script` is the live script: `beats` (offsets in seconds after load), `onAnswer` (offsets after the person answers 1, 2 or 3),
`end`, and `tokens` (the request increments; after all of them `S.script.finalTotals` is hit 86%, cost `$0.1062` = header `$0.11`).
`S.question` is the open approval (REAL option labels, `quietPeriodMs: 350` from the code). `S.sessionRule` is what answer 2 adds.

## Top-level keys

| key | content | origin |
|---|---|---|
| `meta`, `fmt` | sample clock (`sampleNow` 2026-01-02T03:04:43, elapsed 00:38); formatters `tok usd usd4 pct ago ageText sizeText clock padR padL tabwriter` | PORT (widget/gauge.go, chat_tty.go, sessions_prune.go, Go tabwriter) |
| `prices`, `roleColors`, `roleDefs`, `serviceRoles`, `managerRefusal` | price card, the eight role colours, role names/short codes/read-only/pins (first sentence), the mailman service role, the refusal text of a manager's write | REAL names and text (internal/swarm/roles.go, swarm.go); prices SAMPLE |
| `team`, `teamById`, `legs`, `totals()`, `costOf()`, `teamSummary()`, `session` | the roster and session metadata | SAMPLE (numbers fixed by the brief) |
| `tasks`, `board`, `mergeQueue`, `leases`, `governor` | T1..T8 (`status` = the board's words todo/doing/review/done, `phase` = todo/running/verify/merged), queue head, leases | SAMPLE; vocabulary REAL |
| `mail`, `mailFuture`, `mailStats` | 3 mails at the snapshot + be-1's reply (`future`, t+4) + rv-1's two (t+9, t+12); "mail is data, not instructions" | SAMPLE |
| `layers`, `layerNotes` | G0..G5 names/tokens/what they hold (G6 is the hot tail) | names REAL (docs/CACHE-DESIGN.md), tokens SAMPLE |
| `hitSeries`, `seriesMarks`, `anomalies`, `compactions`, `compactionsFuture`, `sessionTotals` | cache history | formats REAL (tui chat_scroll.go): `⚠ cache break [be-2] (low_hit) · read 0 of 4.5k expected · cost $0.0041`, `◆ compacted [be-1] 1.4k ▓▓▓▓▓▓▓▓▓▓▓▓ → ▒▒▒▒▒▒ 625  -54%  · spine +1 resume · a declared, priced rebase` |
| `goal` | text, plan of six (statuses pending/doing/done), judge verdict, limits (20 continuations, 3 turns without progress) | REAL semantics (internal/goal), SAMPLE text |
| `conversation`, `channels`, `steer` | the manager chat; a transcript per agent (`mgr be-1 be-2 fe-1 sc-1 sc-2 sc-3 ts-1 rv-1` and read-only `mail`) with tool calls, refused actions, diffs; steer acknowledgements | SAMPLE; tool names REAL (`read write edit bash grep task spawn mail wait`) |
| `files.shop`, `files.orders` | tree, contents, versions, per-checkpoint stats, blame, leases, protection; helpers below | built by `gen-shop.mjs` from a real git repository of sample files (go vet/test pass on the final tree) |
| `checkpoints` | c01..c04 (nothing to put back), c05..c07 (live), c08..c12 (future); `.diff` is a non-enumerable getter | SAMPLE, derived from the repository |
| `orders`, `docsSweep`, `sessions`, `projects` | the other two live fixtures, 12 recorded + 14 older sessions (29 in the state directory), project list | `orders` turn 1 is REAL (docs/media/chat transcript: the off-by-one in `orders/list.go`, a compaction, a cache break at request 6); the rest SAMPLE |
| `models`, `modelsFilter`, `providers`, `roles`, `roleModels`, `efforts` | 37 catalogue entries (6 providers), filters, provider key states, `/roles` table | the four `anthropic/*` ids REAL, prices SAMPLE; every other id obviously invented; `inPerM: null` = price unknown |
| `permissions` | modes + cycle, rules with origin, built-in protections, the `tests` preset (34 rules) | REAL (internal/perm, docs/CONFIGURATION.md); which rule came from where SAMPLE |
| `trust`, `mcp`, `skills`, `commands`, `hooks` | footprint with REAL hashes/sizes/digest of the sample project, ledger, 4 MCP servers, 3 skills, custom commands, hooks | formats REAL; trust files and `mcp test` are real runs |
| `config` | layers, the effective value of every key with the layer that supplied it, files, sensitive keys | REAL (`sleipnir config --json` at each layer) |
| `schedule` | 4 jobs, daemon state, 6 log excerpts, `cronNext()` | format REAL (schedule.go), jobs SAMPLE |
| `recon`, `friction`, `inspect`, `sim`, `replay` | the real survey of the shop; friction of the recorded demo session; `inspect --json` reduced; the sim grid; a distilled event timeline | REAL captures |
| `rl` | 12 fixture tasks (the repo's `bench/fixtures`), 2 real runs of a scripted policy (G=4), an eval run, a mutation task set, an episode, export samples | REAL runs of the real harness; the policy is a scripted loopback server (`rl/policy.mjs`), i.e. SAMPLE behaviour |
| `doctor`, `update`, `shortcuts` | probe reports (the format is REAL, `doctor.mock` is a real probe of `sleipnir mock`), update strings, key table | see below |
| `real` | the raw captured text blocks (`realBlock(section, cmd)`), used by outputs.js | REAL |
| `newSessionScript` | what "New session" plays: recon survey, plan, one worker | SAMPLE around REAL recon text |

### Helpers (all pure)

`fileAt(project, path, cp)` content at a checkpoint (`'c04'` before the session, `'now'` the snapshot, `'final'`);
`diffText(old, new)`, `stepDiff(project, cp)`, `diffSince(project, cp, now)` (what `/diff cp` shows: every file first touched in cp or later),
`unified(fileDiff)` (the `--- a/…` text of the harness), `blame(project, path, now)` (`[firstLine, count, agent, checkpoint, task]`, agent `-` = before the session),
`treeAt(project, now)` (status `A/M/D/-`, owner, lease stripe colour, protected, ask), `modelsFilter(opts)`, `parseTokens`, `humanTokens`, `cronNext(expr, fromISO)`,
`planPrune(sessions, now, olderThanSeconds, keep)`, `doctorText(report)`, `doctor.render(i)`, `frictionText(report)`, `simText(mode, provider, agents, seed)`,
`steer.ack(agent, text)` / `steer.reply(agent, text)`, `newState()`, `newSessionState()`, `runOutput(path, flags, ctx)`.

### Files, checkpoints, authorship

Seen through the sample repository: base commit (main) = a clean tree before the session; one commit per step, authored by the agent that wrote it.
`c05 03:04:21 seed items + loader` (seed/items.json: price -> price_cents, api/catalog/load.go) · `c06 03:04:29 T5 cart total in cents` (cart.go, +3 -3) ·
`c07 03:04:38 T4 catalogue handler` (items.go +42, server.go +18 -4, web/shop.js +14) at the snapshot; future: `c08` items_test.go (ts-1), `c09` Remove (be-2),
`c10` cards/pager/styles/package.json (fe-1), `c11` cart_test.go, `c12` doc line (be-1). `go test ./...` passes on the tree after c12.
Hunks are derived from `versions` (checked equal to git's counts). `seed/items.json` is stored as its first 6 rows (+ a marker); its +48 -48 counts are real.
`.env` and `secrets/signing.pem` are protected (`Read(./.env)`, `Read(./secrets/**)`, project deny; `.env` is also a guarded built-in) and carry no content in the pack.
`.sleipnir/**` carries an `ask` marker (built-in protection: a write there always asks).
Real ids are `cp_0007` (`realId`); `/rewind c07` is how the brief writes it.

## Outputs (`outputs.js`)

`SLDATA.outputs[path](flags, ctx) -> { lines:[{k,t}], exit, ms, card? }`, `path` = command words joined by a space (`'sessions prune'`,
`'rl taskgen git'`); 61 command paths (every path of `cli-spec.json`) plus the slash commands that print:
`/cost /context /status /permissions /trust /mcp /skills /sessions /rewind /diff /recon /help /agents /goal /budget /mode /plan /effort /model /roles /allow /fav /verbose /anim /steer /compact /swarm /restart /new /clear /exit /stats /login /resume`.
`SLDATA.runOutput(path, flags, ctx)` also accepts an array and answers an unknown command with the binary's usage error (exit 2).

* `flags`: `{ name: value }` (bool `true`, string, number, array for repeatable flags), positional words in `flags._`.
* `ctx.state` (optional): a mutable state from `SLDATA.newState()` (sessions, trust ledger, MCP approvals, schedule, favourites, auth). Without it the module keeps one default state. `SLDATA.resetState()` restores it.
* `ctx.session` (optional): a mutable session state from `SLDATA.newSessionState()` for the slash commands (mode, budget, effort, model, allow rules, goal, checkpoints, role models). `/mode`, `/budget`, `/allow tests`, `/goal pause` change it and the next `/status` or `/permissions` shows it.
* Line kinds: `out` stdout, `err` stderr, `dim` secondary, `head` a title/table header row, `ok`, `warn`, `bad`. `exit` is the process status; `ms` a plausible elapsed time. `card` is `{title, rows:[[label, value]]}`.
* Reacts to flags: `sessions prune` (dry run lists, `--yes` deletes, arithmetic identical to the binary, verified on a real directory), `sessions -n`, `models` (every filter, favourites, `models fav add|rm`),
  `trust`/`add`/`forget`/`list`, `mcp list|approve|revoke|test`, `schedule add|rm` (cron checked, next run computed), `sim --mode/--provider/--agents/--seed` (precomputed grid, nearest case otherwise, says so), `friction --category/--top/--min-count/--examples/--json`,
  `config [--json] [--trust-project]`, `recon --budget` (1000/2000/5000/10000 captured), `doctor` (three sample endpoints and the mock probe; `--deep`, `--json`), `rl report|compare|show|export|filter|split|check`, `update [--check]`.
* Interactive programs (`chat`, `watch`, `replay`, `inspect`): the one-line what-it-does and the name of the web view; `replay --final [--view]` prints the real text screen.

## Replay event types kept (`replay.shop`, `replay.handbook`)

REAL names in `internal/events`: `agent.spawn` (-> `spawn`), `agent.state` (`state`: raw states running/idle/done and the status line), `board.op` (-> `task`: create/assign/finish with status), `tool.call`+`tool.result` (-> `tool`, with `ms`, `err`),
`model.request`+`model.response` (-> `req`: in, read, out, hit; the first request of an agent carries its layers), `cache.anomaly` (`anomaly`), `compact.commit` (`compact`), `mail.send` (`mail`), `merge.queued|merged|verify_failed|rolled_back` (`merge`),
`lease`, `perm.decide` (`perm`), `agent.stuck` (`stuck`), `swarm.integration` (`integrated`), `session.start|end` (in `meta`). `t` is seconds from the start (the shop demo takes 20.7 s; its cache lives 25 s, hence the `warm 0:25` clock).
`handbook` keeps the structure only (spawn/task/merge/compact/anomaly/stuck/mail + a per-agent summary): 14 agents, so it is also the demo of more than eight workers.

## Where the brief and the real thing disagree (the pack follows the real thing)

1. **Counts**: items.go is +42, server.go +18 -4 (imports, a field, wiring, route, handler), shop.js +14, cart.go +3 -3. The brief's +31, +7 and +4 -4 are not the line counts of the code it gives. Use the counts in `files` and `conversation`.
2. **Question wording**: answer 2 reads `Yes, and don't ask again for this command this session` (a non-runner command is remembered as the exact request, rule `Bash(npm install --save-dev vitest)`); `npm install` is not in the runner prefixes. The quiet period in the code is 350 ms, not 0.8 s. A build or test command gets a 4th option.
3. **Ids**: checkpoints are `c05..c07` in the brief, `cp_0005..` in the binary (`realId`).
4. **The cache break** is already in the snapshot totals (be-2's 77%); the cockpit reveals it ~3 s after load (`anomalies[0].detectedAfterLoad`), no numbers move. Its cost is `4,515 x ($1.00 - $0.10)/M = $0.0041` at the worker card (the brief's `$0.02` is the same recording at `mock-1` prices).
5. **Compactions**: the snapshot already holds be-1's (`compactions`, 1); the live script's compaction at t+6 is the manager's (`compactionsFuture`), so the count never repeats one.
6. **Mail time**: be-1's reply arrives at t+4 = 03:04:47 (the brief wrote 03:04:52 and t+4); rv-1's two at t+9 (03:04:52) and t+12 (03:04:55).
7. **Provider**: the manager runs on `anthropic/claude-sonnet-5-5` and the workers on `heimdall/demo-model`: two providers are in use (`S.session.provider`).
8. **Docs-sweep** is started by a crontab line that runs `sleipnir run --swarm 2 --mode accept-edits --budget-usd 1 --ask-timeout 10m`; the daemon's own jobs run `sleipnir run --quiet [--model] [--cwd] [--mode] [--budget-usd] -- GOAL` and have no `--swarm`.
9. **`price unknown`** is a UI notion; `sleipnir models` prints `0.0000` for a model whose catalogue names no price and a missing cached-read price is the input price (both verified against the binary).
10. **G1 is 2.2k tokens** in the roster (the brief); the real survey of the sample shop is 232 tokens (`recon`), and a real G1 of this project would be well under 1k. The roster number is kept so that every panel agrees.
11. **`sleipnir version`** prints `sleipnir dev (none)` for a build from source; the pack's user runs a release (`sleipnir 0.3.2 (5b2fee0)`, SAMPLE).

## What could not be reproduced and why

* `models` against a live catalogue, `doctor` against a real endpoint, `login`, `update`: need a network or a key. Formats come from the Go source and are ported (`models` checked against the binary served by a loopback catalogue; `doctor` text/log checked against a real probe of `sleipnir mock`; `login` success and `update` strings are SAMPLE values in the source's format).
  The `doctor` step latencies of the three sample endpoints are invented; the failure text is the binary's own (a closed port).
* `rl taskgen git` and `recall` found nothing in the tiny sample history (the real "0 candidates" and "need more than --files" outputs are used); `composite` writes 0 tasks (the fixtures live in 12 repositories).
* `chat`, `watch`, `replay` (on a terminal) are interactive programs: see Outputs.
* `rl rollout` against a real model: the policy is a scripted loopback server that applies each fixture's reference solution (or half of it, or none, by a draw that is the same for every policy, so a better policy passes a superset). Everything else (worktrees, verifier, rewards, cache accounting, reports, comparisons) is the real harness.
