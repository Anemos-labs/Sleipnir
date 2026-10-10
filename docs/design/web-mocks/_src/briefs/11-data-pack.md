# Data pack v2: shared sample data and real CLI output for the five Cockpit variants

You build the **data** the five round-2 mocks share, so that every screen shows the same, truthful, self-consistent
content. You do not build UI. Read `00-shared.md` and `10-v2-shared.md` first (roster, numbers, scenario, rules).

Output directory: `$S/work/data/` (create it). Deliverables:

1. `gen-cli-spec.mjs` + `cli-spec.json`
2. `data.js` (defines `window.SLDATA = {...}`)
3. `outputs.js` (defines `SLDATA.outputs`, loaded after data.js)
4. `DATA.md` (schema, where each thing came from: REAL = captured from the binary or taken from code, SAMPLE = invented)
5. `check.mjs` (node script: loads data.js + outputs.js in a `vm` sandbox and asserts the invariants below; it must pass)

Total size of data.js + outputs.js: <= 450 KB (the mocks inline them), plain ES2019 JS, no dependencies, deterministic.

## Ground rules
* **Run real commands whenever the binary can answer offline.** `bin/sleipnir` in the repo root (cwd) is a build of this
  tree; if it is missing or stale rebuild it: `. ~/.local/sleipnir-toolchains.sh; go build -o bin/sleipnir ./cmd/sleipnir`.
  ALWAYS run it with an isolated state dir so the owner's real `~/.sleipnir` is never touched:
  `export SLEIPNIR_HOME=$S/work/data/home HOME=$S/work/data/home`. Never run anything that needs a network or a key
  (`models`, `doctor`, `login`, `update`, `run` with a real model): for those, read the Go source (`cmd/sleipnir/*.go`,
  `internal/...`) and reproduce the **exact output format** with sample values, marked SAMPLE.
* Do not modify the repo (no git, no edits under the repo); write only under `$S/work/data/` and temp dirs under `$S`.
* Do not paste long real files into the data; extract the useful part. Keep the real strings real.
* Sample means sample: model prices, provider catalogue entries and the shop project are invented. Use the four real
  Anthropic ids only as ids (`anthropic/claude-fable-5-1`, `-opus-5-5`, `-sonnet-5-5`, `-haiku-5-5`); give them prices
  marked `sample:true`, and make several models `price unknown` (`in:null`). Invent other ids with obviously sample names.

## 1. cli-spec.json (generated, not hand-written)
`gen-cli-spec.mjs` parses `docs/CLI.md`: every `<!-- flags: NAME -->` ... `<!-- /flags -->` block, plus the
`sleipnir rl ...` sections, into
```json
{ "generatedFrom": "docs/CLI.md", "commands": [
  { "path": ["rl","taskgen","git"], "usage": "sleipnir rl taskgen git --repo PATH [flags]", "summary": "first sentence of its prose",
    "positional": [{"name":"PATH","required":true}],
    "flags": [ {"name":"repo","arg":"string","default":null,"repeatable":false,"desc":"..."}, {"name":"yes","arg":"bool",...} ] } ] }
```
Types come from Go's flag listing (`-name string`, `-n int`, `-x duration`, `-v value` = repeatable, no type = bool).
Include every command in `docs/CLI.md` (init, config, sessions, sessions prune, mcp, trust, chat, run, swarm, recon,
inspect, watch, replay, doctor, models, update, login, schedule, daemon, demo, mock, sim, friction, rl *). Verify the count
against the headings and print a table of what was found. Also add `chatSlash`: the slash-command table from
`chatHelp` in `cmd/sleipnir/chat.go` as `[{cmd:"/goal",args:"TEXT",desc:"...",group:"conversation"}]`.

## 2. data.js: what must exist (shape is yours, document it in DATA.md; keep names stable and obvious)
* `team`: the roster of `10-v2-shared.md` exactly (ids, roles, colours, state, doing, task, scope, tokens read/uncached/
  out, cost), computed by a function from the token table (so totals derive; export `totals(team)` that returns prompt, read,
  uncached, out, hit, cost, savedEst). Prices block labelled sample.
* `tasks` T1..T8 (title, owner, status, deps, scope), `mail` (the four messages + rv-1's two future ones), `layers`
  (G0..G5 names, tokens, what each holds), `hitSeries` per agent (arrays of per-request hit ratios consistent with the
  cumulative hit in the table: be-2's last request is 0 and mean 77%), `anomalies`, `checkpoints` (c07..c04 + older ones
  with files and diffs), `goal` (text, plan of six, judge verdict).
* `files` for the shop project: a full tree (api/, web/, seed/, .sleipnir/config.json, AGENTS.md, Makefile, go.mod, `.env` and
  `secrets/` as protected-by-deny, .gitignore), each with size, status (`A/M/D/-`), owner agent and task, lease, and **content**
  for every changed or relevant source file (items.go, server.go, cart.go, shop.js, items_test.go (full, ~40 lines),
  index.html, shop.css, seed/items.json first 6 rows), plus the **unified diffs per checkpoint** c04..c07 and per-line
  authorship (`blame`: line -> agent/task/checkpoint). Use the code from `00-shared.md` verbatim where given; the rest is yours and
  must compile in spirit (valid Go/JS). The orders-api project (single agent): tree, `orders/list.go` with the real off-by-one fix
  from `docs/media/chat.png`, test file, 2 checkpoints.
* `sessions`: the three live fixtures' metadata (id, name, cwd, model, mode, swarm N, isolation, verify, budget, cost, state)
  and **12 recorded sessions** for `/sessions`, `/resume`, prune (ids like `20260102-030405-5eed01`, age, model, cost,
  first prompt, resumable flag, size on disk in MB, last written), including some >30 days old so `sessions prune
  --older-than 30d --keep 20` has something to list. Make the prune arithmetic correct.
* `models`: >= 36 catalogue entries over heimdall, openrouter, openai, anthropic, chatgpt (plan), local (self-hosted vLLM,
  price unknown), each with ref, context, in/out/cached $/M or null, tools, reasoning, favourite, `sample`. `providers`:
  six entries with key state (stored in auth.json / env var / none / signed in), base URL, notes. `roles`: role -> model
  defaults (manager `anthropic/claude-sonnet-5-5`, workers `heimdall/demo-model`, mailman model unset).
* `permissions`: modes with descriptions; rules per list (allow/deny/ask) each with its **origin** (built-in protection,
  user config, project config, this session, "don't ask again"), including the `tests` preset expansion from the code
  (`cmd/sleipnir/allow.go`), `.env` and `secrets/**` denies, `Bash(git push:*)` ask.
* `trust`: the project's files with hashes and the ledger (`trust.json` format from code), two other directories.
* `mcp`: 4 servers (user-config running with 14 tools; project `.mcp.json` needing approval; one failed reconnect; one off) with
  tools lists; `skills` (3, one `(you only)`), `commands` (custom slash commands), `hooks`.
* `config`: the layers (defaults -> user `~/.sleipnir/config.json` -> project `.sleipnir/config.json` (untrusted keys ignored
  until trusted) -> `config.local.json` -> env -> flags) and, per key, the value and which layer supplied it, as
  `sleipnir config` prints; include `swarm.max_workers`, `swarm.isolation`, `models.default`, `permissions.mode`, `cache.*`.
  Capture the real output of `bin/sleipnir config` and `config --json` on a scratch project and adapt.
* `schedule`: 4 jobs (cron, goal, cwd, model, mode, budget, next/last run, status) + daemon state + 6 log excerpts in the
  real `schedule-logs` style.
* `recon`: the **real** `bin/sleipnir recon` output on a scratch copy of the shop project (create a small Go + web project
  from the sample code under `$S/work/data/shop/`, `git init`, commit, run `recon`; keep the real output and its footer).
* `friction`: real `bin/sleipnir friction` output (text and `--json`) on a recorded demo session.
* `sim`: real `bin/sleipnir sim --json` for each mode (compare, scenarios, pins, agents), trimmed but real, plus the printed text.
* `inspect`: real `bin/sleipnir inspect --json` summary of a demo session (cache layers per request, hit ratios, compactions,
  anomalies, swarm coordination, cost vs baselines), reduced to what a UI needs.
* `replay`: a **real event timeline**: run `bin/sleipnir demo --plain --scenario shop --dir <scratch>` (needs git and sh; ~20 s)
  and distil its `events.jsonl` into `replay.shop` = a compact list of `{t, type, agent, ...}` (agent states, tool calls,
  mail, task transitions, merges, cache reads/writes, compactions, anomalies, governor) plus `replay.handbook`.
  Document the event types you kept and their real names from `internal/events`.
* `rl`: a fixture set small enough to read: 12 tasks (id, kind, language, difficulty, prompt, verify, tags), a rollout run
  summary (G samples per task, pass/fail, cost, cache), a report (pass rate with interval, cost, friction), a compare of
  two runs with paired intervals and gates, reward breakdown. Shape them like the real outputs: read `internal/rl` and
  `docs/TRAINING-DATA.md`; run real `rl` subcommands that work offline on a tiny fixture (`rl tasks`, `rl taskgen fixture`
  with a hand-made fixture dir, `rl report/compare/show/verify` on a run you can produce with the mock endpoint if feasible).
  If a real run is infeasible, build the output from the code's printing functions and mark SAMPLE.
* `doctor`: the report format from the code with a realistic probe (streaming ok, tools ok, prefix-cache behaviour with a
  granularity and minimum prefix, warm-up needs), for 3 endpoints incl. one that fails.
* `update`: the real strings of `sleipnir update` / `--check` (from `cmd/sleipnir/update.go`).
* `shortcuts`: the full key table (terminal keys + web aliases).

## 3. outputs.js
`SLDATA.outputs[cmdPath]` = `function(flags, ctx) -> { lines:[{k,t}], exit, ms, card? }` where `k` is one of
`out|err|dim|ok|warn|bad|head` (a UI colours them) and `t` the text; `card` is optional structured data for a result
card (`{title, rows:[[label,value]]}`). One entry for every command path in `cli-spec.json` and for the slash commands
that print (`/cost /context /status /permissions /trust /mcp /skills /sessions /rewind /diff /recon /help /agents`).
Outputs must **react to flags** where it matters (`sessions prune` without `--yes` lists what would go, with `--yes`
says what went and is arithmetically right; `models` honours filters; `trust list|add|forget`; `schedule add` appends a job;
`mcp approve`; `sim --mode`; `friction --category/--top`; `config --json`). Use real captured text for the base case and
derive variants by code. Where a command is a long-running interactive TUI (`chat`, `watch`, `replay` on a terminal) the
output is the one-line what-it-does and the equivalent web view's name.

## 4. check.mjs invariants (must pass)
Totals derive from the team table (59,900 / 50,844 / 9,056 / 9,650 / 84.9% / $0.0715 ± rounding); every agent's `hitSeries`
mean equals its cumulative hit within 1 point; every file referenced by a diff/checkpoint exists; every flag in
`cli-spec.json` has a type and description; every command path has an `outputs` entry; prune arithmetic is right;
all JSON is serialisable; sizes within budget; no string contains an HTML tag you did not mean (the UI renders as text).

Final message: a short report: what is REAL vs SAMPLE, the size of each file, any command you could not reproduce and why,
and the exact load order for inlining (`data.js`, `outputs.js`), plus anything a UI builder should know.
