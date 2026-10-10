# Sleipnir Cockpit core: architecture

This is the shared engine and the baseline app of the five Cockpit variants. It is a **single-file web mock**: modular source under `src/`, concatenated
by `build.mjs` into one self-contained HTML file (`docs/design/web-mocks/v2/00-core.html`), no network except the Google Fonts link, no dependencies.

Everything is sample data. The numbers come from one table, the clock from one governor, the screens from one event log per session.

* [Start a variant](#1-start-a-variant-in-three-commands)
* [Module map and load order](#2-module-map-and-load-order)
* [Build system](#3-build-system)
* [Data: fixtures, the adapter and the data pack](#4-data-fixtures-the-adapter-and-the-data-pack)
* [State: sessions, meta, the model](#5-state)
* [Events](#6-events)
* [The two clocks and the time governor (`SL.time`)](#7-the-two-clocks-and-the-governor)
* [Sessions API (`SL.sessions`)](#8-sessions-api)
* [Actions (`SL.act`)](#9-actions)
* [Views and scopes: the one rule (`SL.views`, `SL.makeScope`)](#10-views-and-scopes-the-one-rule)
* [Hover linking, the frame loop, the bus](#11-hover-linking-the-frame-loop-the-bus)
* [UI modules](#12-ui-modules)
* [Approvals and the security rules](#13-approvals-and-the-security-rules)
* [The runner (`SL.runner`)](#14-the-runner)
* [Palette and keyboard](#15-palette-and-keyboard)
* [Tokens, type scale, layout, motion](#16-tokens-type-scale-layout-motion)
* [Extension points: how a variant adds things](#17-extension-points)
* [Test hooks and the verification scripts](#18-test-hooks-and-the-verification-scripts)
* [Limits and known gaps](#19-limits-and-known-gaps)

The component catalogue (class names with a tiny HTML sample each) is in `COMPONENTS.md`, generated from the live `?kit` view.

---

## 1. Start a variant in three commands

```bash
cp -r $S/work/core $S/work/vN && cd $S/work/vN          # the source, the build, the tests: a full copy you own
$EDITOR manifest.json src/css/*.css src/js/*.js         # swap, add or remove files in the lists of the manifest
. ~/.local/sleipnir-toolchains.sh && node build.mjs --manifest manifest.pack.json --out OUT.html   # with the data pack; use manifest.json for none
```

* `node build.mjs --test --out dist/test.html` adds the test hooks (`window.__SL`, `SL.test`) for the verification scripts; the shipped file never has them.
* `test/` holds the scripts. They take the file to test as argument: `node test/t-lifecycle.mjs dist/test.html`.
* Paths in `manifest*.json` (`data`, `json`) are relative to the manifest. `../data/data.js` assumes the variant directory is a sibling of the data pack.
* Do not touch `src/js/00..70` unless the variant needs a different engine: they are the part five builders must agree on. A variant changes
  `src/css/*` (look), `src/html/shell.html` (layout), `80-ui-*.js` and `9x-*.js` (screens), and adds files after `95-kit.js`.

## 2. Module map and load order

`build.mjs` joins the files of `manifest.json` in list order inside ONE IIFE that owns `const SL = {}`. A module is `(function (SL) { ... })(SL)`; it reads what
earlier modules published on `SL` and publishes its own. Nothing is global except `window.SLDATA`/`window.SLCLISPEC` (the pack, read by one file) and, in a test build, `window.__SL`.

| # | File | Publishes | Role |
| --- | --- | --- | --- |
| 1 | `00-util.js` | `SL.u`, `SL.bus` | pure helpers: `$ $$ esc mk sv frag fmtK fmtN fmtUsd fmtMs clock mmss tod dur clamp hitCls rng hash uid deepCopy upperBound agCol copy`, the bus (`on`, `emit`, `count`) |
| 2 | `10-fixtures.js` | `SL.FX` | the built-in sample data: roles, prices, the roster table (the one table), tasks, plan, code samples, models, rules, 26 recorded sessions, spec of the fallback runner |
| 3 | `11-data-adapter.js` | `SL.D` | the one object the engine reads data from: `SL.FX`, overridden piece by piece by `window.SLDATA` when its shape matches |
| 4 | `20-clock.js` | `SL.time`, `SL.TIME_CONST` | the time governor (hold / slow / catch-up) |
| 5 | `30-model.js` | `SL.model`, `SL.calc` | the pure reducer from events to a model; every derived number (`SL.calc.agent/totals/...`) |
| 6 | `40-scripts.js` | `SL.scripts` | deterministic timelines (shop, orders-api, docs-sweep, new run, resumed), the roster builder, the director (verify queue, endgame), canned replies |
| 7 | `50-sessions.js` | `SL.sessions` | the multi-session registry: `Session` (event log, world and view models, clocks), create/close/rename, the cross-session inbox, prune arithmetic |
| 8 | `60-actions.js` | `SL.act`, `SL.settings`, `SL.G`, `SL.actions` | the store: every thing a person can do is one action; per-viewer settings; state shared by all sessions |
| 9 | `70-view.js` | `SL.makeScope`, `SL.views`, `SL.link`, `SL.loop` | the view lifecycle (the one rule), hover cross-highlighting, the single frame loop |
| 10 | `80-ui-shell.js` | `SL.ui.shell`, `ui.TABS` | HUD, session strip, view tabs, replay banner, footer, phone nav |
| 11 | `81-ui-hero.js` | `SL.ui.hero` | the drawn Sleipnir: a horse with eight legs (the eight workers), the prefix bar |
| 12 | `82-ui-board.js` | `SL.ui.board` | the task board (cockpit band and Board view) |
| 13 | `83-ui-cockpit.js` | view `cockpit`, `ui.SG`, `ui.typed`, `ui.sparkBars` | the manager's card, worker stalls, gantt, merge queue, mail list, governor gauge; registers the Cockpit view |
| 14 | `84-ui-chat.js` | `SL.chat`, `ui.togglePin`, `ui.setDraft` | the Radio rail: channels, transcript (row cap, digest rows), plan box, hold chip, composer, slash menu, queue, steer |
| 15 | `85-ui-approvals.js` | `SL.ui.approvals`, `ui.inbox` | the approval question with its quiet period; the cross-session inbox |
| 16 | `86-ui-overlays.js` | `ui.modal`, `ui.confirm`, `ui.toast`, `ui.sessionMenu`, `ui.IC`, `ui.still`, `ui.applyMotion` | dialogs, sheets, toasts, in-page confirm, reduced motion |
| 17 | `87-ui-sheets.js` | `ui.sheets.*`, `ui.dialogs.*`, `ui.fmt`, `ui.diffHtml` | goal, model, mode, effort, budget, permissions, trust, context, help, history, rewind, diff, login, mcp, skills, team; New / Resume / Rename |
| 18 | `88-ui-drawer.js` | `ui.openDrawer`, `ui.closeDrawer`, `ui.layerStack`, `ui.miniStack` | the per-agent drawer (Log, Cache, Mail, Steer, Interrupt) in the current view's layer |
| 19 | `90-views-a.js` | views `cache`, `mail`, `board`, `replay`; `ui.compactDialog` | Cache (one table: rows, `all` row, layer stack, per-request sparkline), Mail (arcs), Board (kanban), Replay (scrub) |
| 20 | `91-views-b.js` | views `sessions`, `settings` | live and recorded sessions, prune; settings with the store round trip |
| 21 | `92-runner.js` | `SL.runner`, view `runner`, `ui.runCli` | the generic command runner (form from `cli-spec.json`, command line, streaming output, result card) |
| 22 | `93-palette.js` | `SL.palette` | the command registry: slash commands (all dispatching), views, sessions, `sleipnir ...` commands; the palette dialog |
| 23 | `94-keys.js` | `SL.keys` | global keys and the Esc precedence |
| 24 | `95-kit.js` | view `kit`, `ui.popover`, `ui.ringSvg` | the live component catalogue (`?kit`) |
| 25 | `99-app.js` | `SL.boot` | boot: settings, sessions, shell scope, first view, loop |

CSS files, in order (`src/css/`): `10-tokens` (all colours, fonts), `20-base` (reset, type, focus, `.layer`), `29-layout` (the app grid), `30-hud`, `40-panels` (`.panel .ph .pb`),
`50-stalls`, `51-panels` (board, gantt), `52-cockpit`, `60-rail` (question, plan, composer), `61-cmds` (palette list), `62-rail-extra` (channels, transcript rows, vertical fit),
`70-overlays` (sheets, toasts, key-value), `72-components` (the kit), `80-views`, `82-views-extra`, `85-link` (hot / dimmed), `90-responsive` (breakpoints, phone, reduced motion).

`src/html/shell.html` is the markup of the app (ids below), `src/html/favicon.svg` the favicon (inlined as a data URI).

## 3. Build system

`manifest.json`:

```json
{ "title": "...", "lang": "en", "fonts": "<google fonts css url>", "favicon": "src/html/favicon.svg",
  "css": ["10-tokens.css", "..."], "js": ["00-util.js", "..."], "shell": "src/html/shell.html",
  "data": [], "json": {}, "test": ["zz-test-hooks.js"] }
```

* `css` / `js` are file names under `src/css` / `src/js`, concatenated in list order. `shell` and `favicon` are paths relative to the manifest.
* `data`: JS files inlined at the top of the IIFE, before the modules (the data pack: `../data/data.js`, `../data/outputs.js`). `json`: `{ GLOBAL: "file.json" }` becomes `window.GLOBAL = <json>`.
  Inlined data is escaped for an inline script (`</script` becomes `<\/script`, `<!--` becomes `<\!--`, equal inside a string); the build refuses to continue if the final script text still contains either.
* `test`: files under `test/`, appended **only** with `--test`.
* The page head holds exactly: charset, viewport, title, the fonts link (the only network request), the favicon, ONE `<style>`. The body holds the shell and ONE `<script>`.

`node build.mjs [--manifest M] [--out FILE] [--test]`. `./go.sh` builds the shipped core (`docs/design/web-mocks/v2/00-core.html`) and the scratch builds `dist/test.html` (hooks),
`dist/pack.html` and `dist/pack-test.html` (data pack, without and with hooks). `manifest.pack.json` is `manifest.json` plus `data` and `json`.

## 4. Data: fixtures, the adapter and the data pack

`SL.D` is the only object the engine reads sample data from. `SL.FX` (built in, always complete) fills it; `adopt()` in `11-data-adapter.js` replaces a piece only when `window.SLDATA`
(or `window.SLCLISPEC`) carries it **in the shape the engine needs**; otherwise the built-in piece stays. A variant needing a new pack field adds one line to `adopt()`, never a read elsewhere.

| `SL.D.*` | Built-in | Adopted from the pack (`SLDATA.`) |
| --- | --- | --- |
| `roster` | the one table (prompt, read, out per agent) | `team[]` (prompt, read, out, state, task, doing, scope when it has globs); the built-in order (legs) is kept |
| `g5` | G5 tokens per agent | `team[].layerTokens.G5` |
| `prices` | manager $3 / $0.30 / $15, workers $1 / $0.10 / $4 per M | `prices.manager`, `prices.worker` (`inPerM`, `readPerM`, `outPerM`) |
| `layers`, `hitSeries` | G0..G5, per-request ratios | `layers[]` (tokens, holds), `hitSeries` |
| `models`, `providers`, `shortcuts`, `mcp`, `skills`, `config`, `trustFiles`, `testsPreset` | small samples | `models[]`, `providers[]`, `shortcuts[]`, `mcp.servers[]`, `skills[]`, `config.effective[]`, `trust.files[]`, `permissions.testsPreset.rules` |
| `recorded` | 26 recorded sessions | `sessions.recorded[]` + `sessions.archive[]` (sorted newest first) |
| `spec` (runner), `slash` | a small spec, the 35 slash commands of `chatHelp` | `SLCLISPEC` (61 commands, 6 argument types) and its `chatSlash` |
| `outputs` | none (the runner has its own for 5 commands) | `outputs[path](flags, ctx)` |
| `extra.*` | | `recon friction sim inspect replay rl doctor update files config trust permissions goal question conversation channels script mail mailFuture orders docsSweep projects real fmt costOf totals` as given, for variants that want them |

`D.ctx()` is the context the runner hands to a pack output (`{ state }`, the pack's own mutable state); `D.packSync(ids)` removes pruned sessions from it so `sleipnir sessions` agrees with the registry.
Helpers: `D.model(ref)`, `D.layerToks(agentId)`, `D.output(path)`.

Rules: every number is **sample** (the footer chip says `MOCK · sample data`); prices are labelled sample; the four Anthropic ids are the only real model names. The pack and the built-in agree on the roster
(same tokens), so a build with and without the pack shows the same percentages (verified by `t-numbers.mjs` on both).

## 5. State

### Session (`SL.sessions.get(id)`)

```
S.id S.name S.kind('shop'|'orders'|'docs'|'new'|'resumed') S.sid (the on-disk id)
S.meta   { cwd model mode effort budget swarm isolation verify commit mailman trustProject noMcp roleModels{} rules[] goalText launch t0 ... }  // chat flags + live settings
S.roster [{ id role code nth k leg scope ro model spawn }]   // manager first; leg -1 for the manager, 0..7 for workers (a 9th worker shares leg 0)
S.log    [events sorted by t]            // the ONLY source of truth
S.wt     world clock (s)                 // advances 1 s per wall second for EVERY session
S.vt     view clock (s)                  // advances by dtWall x governor rate, never ahead of wt (active session only)
S.wm     world model (no chat rows)      // drives the director, needs-you, the cross-session inbox, the badges
S.m      view model (chat rows)          // only for the active session; rebuilt from the log on activation
S.ui     { chan view drawer drawerTab planOpen selAg draft queued[] scroll{} }   // per-session UI state (which channel, which view ...)
S.hold   { pinned vtSaved }              // the hold is per session
S.replay null | { playing speed }        // a replay is a view of the same log at an earlier vt
S.interrupted { agentId: { task planDone[] } }   // for resume after Interrupt
```

### Model (`S.m`, `S.wm`; built by `SL.model.newModel`)

```
m.ag[id]  { id role code nth k leg scope ro model state doing task  rd un out  calls ratios[] nreq lastReq  segs[] spawned steered }
          rd = prompt tokens READ from the cache, un = UNCACHED (paid), out = output tokens          <- the one table
m.order   agent ids (manager first)     m.tasks{id: {id title owner deps scope st t ms}}   m.torder[]   m.merged[]
m.qs[]    approval questions (answered: null | choice)     m.q  the open one       m.qHead  the merge-queue head
m.plan[6] 'pending'|'act'|'done'          m.goal {state}   m.verdict   m.final     m.mail[]   m.anomalies[]   m.compactions[]   m.ckpts[]
m.chan    { mgr: [...], mail: [...], 'be-2': [...], ... }  transcript entries per channel (capped at 4500 in memory, ROW_CAP in the DOM)
m.marks   gantt markers      m.reqLog   m.rpm/rpmHist   m.lastReq (for the warm clock)   m.steps   m.refused   m.ver (bumps on every event)
```

### `SL.calc` (the only place tokens become percentages and dollars)

`calc.agent(a)` -> `{prompt, read, un, out, hit, pct, cost, saved}`; `calc.totals(m)` -> same for the team plus `hit1` (one decimal) and `calls`;
`calc.warmLeft(m, vt)`, `calc.active(m)`, `calc.workers(m)`, `calc.turnRunning(m)`, `calc.openQuestion(m)`, `calc.waiting(m)`, `calc.allMerged(m)`.
`hit = read / (read + un)`; cost = `un x in + rd x cached + out x out` at the agent's price; saved = `rd x (in - cached)`. HUD ring, stalls, drawer, Cache tab (including its `all` row), Sessions card and Replay frame
all call these, so they agree to the percent at every moment (tokens are integers, so the `all` row is exactly the sum of the rows).

### Settings and shared state

* `SL.settings` `{ hover: 'both'|'chat'|'off', motion: 'auto'|'reduce'|'full', density: 'comfortable'|'compact', ver }`, persisted in `localStorage` under try/catch (the page works without it); per viewer.
* `SL.G` state shared by every session: `mcp[]`, `favs` (Set), `trust[]`, `trustDirs[]`, `providers[]`, `schedule[]`, `roleModels`, `history[]`, `runs[]` (runner history), `ver` (bump to re-render).

## 6. Events

An event is `{ t, k, ...fields }`, `t` in session seconds. `reduce(m, ev, ctx)` is the **only** code that changes a model; it runs on the world model (no chat rows) and on the view model (rows).
`S.add(events)` inserts at `max(t, wt)`; scripts and the director produce events ahead of time; a person's action produces events at `wt`.

| `k` | Fields | Effect on the model |
| --- | --- | --- |
| `say` | `who` (`you`/`sys`/`mgr`/`scouts`/`local`), `text`, `glyph`, `plan`, `task`, `stream`, `rate` | a transcript row in the manager channel |
| `sys` | `ch`, `glyph`, `text`, `ag`, `task` | a system line in a channel |
| `local` | `title`, `html` | a local card (the output of `/cost`, `/status` ...) |
| `tool` | `id`, `name`, `arg`, `out`, `ok`, `refused`, `reason`, `file`, `add`, `del`, `task` | agent channel row + manager feed row; `calls++`, `steps++`; `refused` counts |
| `note` | `id`, `g` (glyph kind), `text`, `task` | a one-line note in the agent's channel and the feed |
| `state` | `id`, `s` (`idle think tool edit wait ask done stuck`), `doing`, `task` | agent state, a gantt segment |
| `task` | `id`, `title`, `owner`, `deps`, `scope`, `s` (`todo running verify merged`) | creates/moves a task |
| `plan` | `n`, `s` | plan item `n` -> `pending`/`act`/`done` |
| `verdict` | `text` | the judge's verdict |
| `req` | `id`, `ratio`, `p` (prompt tokens), `o` (output tokens), `hist` | one request: `rd += round(p x ratio)`, `un += round(p) - rd`; history rows (`hist`) only add to the per-request ratios |
| `use` | `id`, `rd`, `un`, `out` | set an agent's token table directly (snapshots) |
| `warm` / `gov` | `rpm` | the prefix warm clock / the governor gauge |
| `mail` | `from`, `to`, `text` | mail list, mail channel, both agents' channels |
| `ckpt` | `cid`, `ts`, `files`, `note`, `skipped` | a checkpoint (rewind targets) |
| `ask` | `q` `{id agent cmd why scope task cont ...}` | opens an approval question |
| `answer` | `qid`, `choice` (1 yes, 2 yes + rule, 3 no), `note` | closes it |
| `queue` | `head`, `cmd`, `step`, `ms` | merge-queue head (rebase, verify command) |
| `merge` | `id`, `cmd`, `ms` | task merged |
| `break` | `id`, `kind`, `read`, `expected`, `why` | a cache anomaly (flash, row, Cache tab) |
| `compact` | `id`, `from`, `to`, `pct` | context compaction |
| `stream` | `id`, `text`, `rate`, `code` | streamed text (typed by the view clock) |
| `diff` | `file`, ... | the diff of a file (drawer, `/diff`) |
| `goal` | `s` (`active paused met cleared`) | goal state |
| `final` | | the end of the turn (summary row) |
| `steer` | `to`, `text`, `quiet` | a steer message to an agent |
| `reply` | `id`, `text` | an agent's reply to a steer |
| `interrupt` | `id` (agent or `turn`) | an interrupt row |
| `refuse` | `id`, `name`, `arg`, `reason` | a refused action (headless, nobody to ask; permission) |
| `digest` | | placeholder: the digest row is made by `collapse()`, not by an event |

Adding an event type: handle it in `reduce` (30-model.js), add its kind to `VISIBLE` if it should count as "new" for the hold chip, produce it from a script or an action. Nothing else.

## 7. The two clocks and the governor

* **World clock** `S.wt`: 1 s per wall second, for every session, always. Events happen on it. A background session keeps running and can raise a question.
* **View clock** `S.vt`: `dtView = dtWall x rate`. The **rate** eases exponentially toward a target; nothing sets it directly.

| Situation | Target | Easing tau |
| --- | --- | --- |
| pointer over the chat transcript, keyboard focus inside it, or the hold pinned (**hold**) | 0 | 0.15 s down |
| pointer over a linking source (stall, leg, gantt row, mail row, task card ...) (**slow**) | 0.3 | 0.15 s down, 0.25 s up |
| otherwise | 1 | 0.25 s up |
| view behind the world (gap > 0.12 s) after a hold (**catch-up**) | up to `MAX_RATE` 6 | 0.1 s |

Everything that moves is driven by `dtView` (events, leg cycles, rings, gantt, arcs, token tickers, streaming text, chat scroll); wall time is used only for things about the person (the approval quiet period,
toasts, the hold chip's own animation). **While held nothing is appended, scrolled or typed**; events queue in the log and are counted (`S.unseen()`, shown on the chip).

**Catch-up on release is bounded** (`S.collapse()`): events older than `WINDOW` = 18 s are applied instantly (collapsed) and summarised as ONE digest row in the manager channel (`◆ while held · 45 min: ... · N events`, expandable, lazy);
the last 18 s replay at up to 6x; `T.snap` closes the last 0.12 s. So a 45-minute hold costs about 3.7 s to catch up and adds one row, not thousands.

API (`SL.time`): `T` (state), `C`/`SL.TIME_CONST`, `tick(dtWall, gap, replay)` (called by the loop), `setHover('chat'|'linked'|null)`, `setFocus(bool)`, `pin(bool)`, `release()` (Esc), `setMode('both'|'chat'|'off')`,
`noteKey()`, `quiet()` (wall seconds since the last key), `holdWanted()`, `slowWanted()`, `reset()`; getters `rate held pinned wall catching gap`. Bus topic `hold` `{held, pinned, wanted}`.
`Esc` releases a hover/focus hold until the pointer or focus leaves and returns (`T.suppress`). Setting `Hover behaviour` = `chat` removes the slow, `off` removes both (the pin still works).

## 8. Sessions API

`SL.sessions`: `list`, `active`, `get(id)`, `init()`, `activate(id)`, `create(spec, activateIt)`, `close(id)` (refuses the last session), `rename(id, name)`, `needs()` (the open questions of all sessions, oldest first),
`recorded` (the recorded list), `prune(olderStr, keep, apply)`, `pruneCandidates`, `parseAge('30d')`, `recordedMb()`, `PLANS`, `make(spec)`, `nowTod()`.

`Session`: `initRun()` (also `/swarm`, `/restart`, `/new`), `add(events)`, `stepWorld`, `advanceWorld(dt)`, `stepView`, `advanceView(dt)`, `rebuild(t)` (silent, from the log), `collapse()`, `goLive()`, `seek(t, playing)`,
`unseen()`, `gap()`, `state()` (`run ask paused idle done`), `touch()`, `setMeta(patch)`.

* **Switching** never keeps DOM: the old active session drops its view model; the new one rebuilds `S.m` from its log at `wt` (or at its saved `vt` when its hold is pinned).
* **Fixtures** (deterministic scripts, `40-scripts.js`): `shop` (manager + 8 workers, snapshot at 38 s, asks `npm install --save-dev vitest` at T6, then verification of T4..T8 and the endgame), `orders-api` (single agent, the off-by-one fix from `docs/media/chat.png`),
  `docs-sweep` (headless, 14 tasks, ends in a **refused** approval: nobody to ask). `create()` makes a `new` session (a short script: recon survey, plan, workers by `--swarm`), `resumeSession(recId)` a `resumed` one.
* **Events on the bus**: `activated`, `sessions-changed`, `needs-changed`, `ask-arrived`, `session-done`, `roster-changed`, `meta`, `rebuilt`, `collapsed`, `recorded-changed`, `replay-ended`.
* **Prune arithmetic** (matches the CLI and the data pack): a recorded session is deleted when it is older than `--older-than` AND not among the newest `--keep` sessions on disk (live sessions are the newest and use slots) AND not written in the last ten minutes.

## 9. Actions

`SL.act.<name>(...)`. Rule: **panels render from state and never change it.** An action (a) changes meta/state, (b) inserts events at `wt` so the change shows in the chat, the log and the replay, (c) emits `action` on the bus.
Most take a trailing `sid` (default: the active session). Results are `{ok, why}` where an action can refuse.

| Group | Action | Notes |
| --- | --- | --- |
| settings | `setHover(v)` `setMotion(v)` `setDensity(v)` | per viewer, saved in localStorage; `setHover` also sets the governor mode |
| mode | `setMode(mode, {confirm}, sid)` `cycleMode(sid)` | `default accept-edits plan` freely; `bypass`/`yolo` only with `{confirm: mode}` (typed name); **`cycleMode` (shift+tab) never reaches them** and leaves them for `default` |
| model | `setModel(ref)` `setRoleModel(role, ref)` `setEffort(lv)` | models with unknown price are allowed and said so |
| budget | `setBudget(usd \| 'off')` | pauses the goal when already over |
| team | `setIsolation('none'\|'worktree')` `setVerify(cmd)` `setFlag('commit'\|'mailman'\|'noMcp'\|'trustProject', on)` `restartTeam({swarm, ...}, sid)` `newChat(sid)` | flags apply when the team starts again |
| permissions | `allowRule(rule, origin)` `denyRule` `askRule` `removeRule(rule)` | each rule carries its origin (this session, "don't ask again", project config ...) |
| goal | `setGoal(text)` `pauseGoal()` `resumeGoal()` `clearGoal()` | the judge's verdict comes from the endgame script |
| approvals | `answerQuestion(qid, choice, note, sid)` | choice 2 adds an allow rule; the keyboard path additionally needs the quiet period (see 13) |
| control | `interrupt('turn' \| agentId)` `steer(agent, text)` `compact(focus)` `rewind(checkpointId)` `send(text)` | interrupt cancels that agent's future events and records `interrupted[id]`; a steer to an interrupted agent resumes it |
| sessions | `switchSession(id)` `newSession(spec)` `closeSession(id)` `renameSession(id, name)` `resumeSession(recId)` `pruneSessions(older, keep, apply)` | |
| shared | `favModel(ref)` `setMcp(name, patch)` | change `SL.G` and emit `models-changed` / `mcp-changed` |

Adding an action: `act('name', fn)` in `60-actions.js` (or from a variant file loaded later: `SL.act` is a plain object but wrap with the same pattern so `action` is emitted); do the three things above; never touch the DOM.

## 10. Views and scopes: the one rule

> **A view owns everything it starts.** Every timer, rAF, listener, observer, Web Animation, bus subscription and every transient element (mail arc, flash, tooltip, popover, drawer) is created through the view's `scope`,
> and `unmount()` cancels and removes all of it. Transient layers live **inside the view's own root** (`scope.layer()`), never on `<body>` or a shared overlay. A view is remounted from the session's state and log, never from retained DOM.

`SL.views.register({ name, title, nav?: false, mount(scope, root, params, S) })` registers a view (it joins `V.order`, and the palette's "go to" list, unless `nav: false`).
`SL.views.show(name, params)` unmounts the current view first (everything it started dies) and **then** mounts the new one into a fresh `<section class="view v-NAME">` in `#views`. `S.ui.view` remembers the view per session.

`scope` (also `SL.makeScope(root, label)` for shell pieces and modals):

| Call | Does | Dies with the scope |
| --- | --- | --- |
| `scope.timeout(fn, ms)` `scope.interval(fn, ms)` `scope.clear(id)` | timers | yes |
| `scope.raf(fn)` | `requestAnimationFrame` (display-bound work only) | yes |
| `scope.frame((dtView, vt, S, dtWall) => ...)` | per-frame callback on the **sim clock**; returns a remover | yes |
| `scope.update((S, m) => ...)` | re-render hook: runs when the model, meta, settings or shared state changed | yes |
| `scope.listen(target, type, fn, opts)` | `addEventListener` | yes |
| `scope.observe(el, cb)` | `ResizeObserver` | yes |
| `scope.animate(el, keyframes, opts)` | Web Animation | yes (cancelled) |
| `scope.layer(cls)` | a `div.layer` appended to the view root, for arcs, flashes, tooltips, popovers | yes (removed) |
| `scope.on(topic, fn)` | bus subscription | yes |
| `scope.onUnmount(fn)` | a last hook | runs on dispose |

Checklist for a new view: (1) build `root.innerHTML` from state in `mount`; (2) render in `scope.update`, animate in `scope.frame`; (3) every listener through `scope.listen`; (4) every popover/tooltip/arc/flash through `scope.layer()` or `ui.popover(scope, ...)`;
(5) never keep a reference to DOM or a timer outside the scope; (6) text from tools, files, mail and models goes through `esc()` or `textContent`. `t-lifecycle.mjs` fails if any of these leak.

```js
SL.views.register({ name: 'files', title: 'Files', mount(sc, root, params, S) {
  root.innerHTML = '<section class="panel"><div class="ph"><h2>Files</h2></div><div class="pb list"></div></section>';
  const list = root.querySelector('.list');
  sc.update((S, m) => { list.innerHTML = Object.keys(m.diff).map(f => '<div class="mrow" data-file="' + SL.u.esc(f) + '">' + SL.u.esc(f) + '</div>').join(''); });   // render from state
  sc.listen(list, 'click', e => { const r = e.target.closest('[data-file]'); if (r) SL.ui.toast(r.dataset.file); });                                              // listeners via the scope
}});
```

To put it in the tab bar, add `['files', 'Files', 'f']` to `ui.TABS` (80-ui-shell.js) and `f: 'files'` to `VIEW_KEYS` (94-keys.js). A view with `nav: false` (runner, kit) is reachable from the palette only.

## 11. Hover linking, the frame loop, the bus

* **Linking** (`SL.link`): any element with `data-ag`, `data-task` or `data-file` (space-separated lists) is a source and a target. On pointer-over or focus, every linked element gets `.hot` and everything else linkable `.dimmed`
  (an agent also lights its tasks and vice versa; a file lights its owner). `SL.link.select(id)` pins a selection (used by the drawer and arrow keys). Linking also tells the governor: over the transcript = hold, over any other source = slow.
  The class `talk` (with `data-hold="chat"`) marks a transcript: **every channel transcript is a hold source**, and so is any element a variant gives that class.
* **Frame loop** (`SL.loop`): one `requestAnimationFrame` loop. `step(dtWall)`: advance every session's world, run `SL.actions.pump()`, tick the governor, advance the active view, scale CSS animation playback to the rate (capped at 1x),
  run the `update` hooks when the render key changed (session, model version, meta, settings, replay, shared state) and the `frame` hooks. It pauses when the tab is hidden. `loop.dirty = true` forces an update.
* **Bus** (`SL.bus.on/emit/count`): in-engine pub/sub (topics listed in 8 and in `bus.emit` calls); also `ev` `{S, ev}` for every event applied to the view model with animation (mail arcs, flashes), `hold`, `action`, `view-mounted`, `view-unmounted`, `modal`, `models-changed`, `mcp-changed`, `motion`.

## 12. UI modules

Persistent shell (mounted once under the shell scope by `99-app.js`): `ui.shell.mount` (HUD: goal, budget gauge, cache ring, prefix warm clock, team; session strip with badges; view tabs; replay banner; footer status line; phone bottom nav),
`SL.chat.mount` (Radio rail), `ui.approvals.mount`, `SL.link.bind`, `SL.keys.mount`. Each re-renders from the active session through `scope.update`.

Shell ids (`src/html/shell.html`): `#app` (`data-pv` = phone view), `#hud`, `#goalBtn`, `#ringHit #hitTxt`, `#ringWarm`, `#teamBox`, `#sstrip`, `#main`, `#tabs`, `#banner`, `#qBanner`, `#views`, `#rail`, `#chans`,
`#qSlot`, `#planBox`, `#talk`, `#newPill`, `#holdChip`, `#composer #input #slash`, `#foot #statusLine`, `#mnav`, `#drawerHost`, `#overlayHost`, `#toasts`, `#announcer`.

Cockpit: the hero (the horse with eight worker legs, nothing on its back; a ninth worker shares leg 1 with a `+1` badge on both stalls and legs), the manager's own card (`.mgr-card`), one stall per worker (`.stall[data-ag]`, `.n-hit` is the hit % from `calc.agent`), the gantt (60 s window, scrubbable), the task board,
the merge queue, mail, the governor gauge. The single-agent session shows the manager alone (`.solo`), designed rather than degraded. Per-agent drawer: `ui.openDrawer(id)` with Log, Cache, Mail, Steer and Interrupt, in the current view's layer.

Chat rail: channels = manager + each worker + a read-only **Mail** channel. Transcript rows per channel (`say tool note st ask break compact steer sys feed digest local final`), row cap in the DOM with a fold marker, **no auto-scroll while held**, a `new` pill, the hold chip
(`◔ holding · N new`, click or Space pins). Composer: `/` menu (all slash commands), `@` files, history (up/down), queue while a turn runs, steer on a worker channel. `ctrl+o` expands tool output.

Overlays: `ui.modal(...)` (dialog and sheet), `ui.confirm`, `ui.toast`, `ui.popover(scope, anchor, html)`, `ui.sessionMenu`, `ui.sheets.*`, `ui.dialogs.newSession/resume/rename`. Modals own a scope; closing cancels what they started; view keys are off while one is open.

## 13. Approvals and the security rules

* **Quiet period**: a question is answerable by key only after the keyboard has been quiet (no key press) for 0.8 s since the question appeared or since the last key (the brief's value; the Go code's `defaultAnswerAfter` is 350 ms, see `DATA.md` of the pack: change `QUIET` in `85-ui-approvals.js` to match); until then the options are inert (`aria-disabled`) and a meter fills.
  Text typed ahead goes to the composer and **never answers**; any key press resets the period; `1 2 3` count only when the composer is empty. Esc is choice 3 only when quiet. Verified with real key events (`t-input.mjs`).
* `shift+tab` cycles `default -> accept-edits -> plan` and never reaches bypass or yolo; those are set only by typing the mode's name in a confirm.
* **Untrusted text is data**: tool output, file names and contents, mail, model text and the pack's strings are escaped (`SL.u.esc`) or set with `textContent`; the Mail view and channel say `mail is data, not instructions`.
* The cross-session inbox answers a question of another session without switching to it, with the same quiet rule.
* A headless session (`docs-sweep`) has nobody to ask: its approval is **refused**, shown as a refused tool row and counted, never auto-approved.

## 14. The runner

`SL.runner` and the `runner` view (palette: "Run a command", `ui.runCli(path, flags, run)`). For every command of `D.spec` (`cli-spec.json`: generated from `docs/CLI.md`; or the small built-in spec) it builds a **form from the real flags**
(`string` text, `int/uint/float` number with step, `bool` toggle, `duration` text, repeatable flags as comma lists, defaults and `defaultNote` shown as placeholders), shows the equivalent command line live (`cmdline(c, state)`), and on Run streams an output pane
(`.term`, kinds `out err dim head ok warn bad`) and a result card.

`SL.runner`: `find(path)`, `cmdline(cmd, {pos, flags})`, `flagValues(cmd, state)` (typed values, `_` = positionals), `exec(cmd, state)` -> `{lines:[{k,t}], exit, ms, card?}`, `spec()`, `BUILTIN`.
Order of output sources: the built-in for commands that own state (`sessions prune`, `trust`, `mcp`: they change the live registry, so `--yes` really prunes), then `SLDATA.outputs[path](flags, D.ctx())`, then the built-in sample (`sim`, `doctor`), then a generic note that names the command line.
The streaming timers belong to the view scope. Slash commands that have a CLI twin open the runner (`/recon`).

## 15. Palette and keyboard

`SL.palette` (`build`, `filter(q, slashOnly)`, `run`, `runLine`, `open`, `H` = slash handlers): items from `D.slash` (each dispatches its real action through `H[cmd]`), the views, New / Resume, switch-to-session (dynamic), Pin the hold, and every `sleipnir ...` command.
`ctrl+k` opens it; `/` focuses the composer with `/` (or opens the palette on a phone). A slash command without a handler would show as `live: false`; the test asserts none exist.

Keys (`SL.keys`): `o c m b r s ,` views; `ctrl+k` palette; `/` composer; `?` help; `alt+t` / `alt+g` (and `ctrl+`) Cache / Cockpit; `shift+tab` mode cycle; `space` pin the hold (over the chat) or toggle replay play; arrows and `shift` scrub the replay;
`Home End + -` replay; `up/down` select an agent, `enter` opens its drawer; `1 2 3` answer after the quiet period. **Esc precedence**: release a hold -> close an overlay (modal, inbox, session menu, drawer, slash menu) -> answer 3 when quiet -> leave a field -> leave replay -> interrupt the turn (pauses the goal).

## 16. Tokens, type scale, layout, motion

* **Colour**: only tokens on `:root` (`10-tokens.css`); components never contain a colour literal (the drawn horse and the favicon aside). Roles `--c-mgr --c-be --c-fe --c-sc --c-ts --c-dc --c-rv --c-fs`, shades per agent `--a-sc-1 ... --a-rv-2`
  (`SL.u.agCol(id)` picks them), state `--warm --err --ok --info`, surfaces `--bg --panel --panel2 --raise --line --line2 --well`, text `--fg --fg2 --dim --faint --bright`, alpha variants `--warm-a --err-a --mgr-a ...`, `--focus`. Dark only.
* **Type**: `--f-lab` Barlow Condensed (labels, uppercase, wide tracking), `--f-ui` Barlow Semi Condensed (text), `--f-num` JetBrains Mono (numbers, commands, code; tabular). Scale in px: 10 micro label, 10.5 label, 11 meta, 12 dense, 12.5 body-dense, 13 body, 14 lead, 17 sheet title, 19 brand.
* **Layout**: `#app` is a grid `hud / strip / main + rail / foot`; `--rail: 344px`, `--gap: 8px`, `--hud-h: 64px`. The page never scrolls on desktop; each region scrolls inside itself. In the rail the transcript gives way first (its flex-shrink is 20x), then the question box scrolls inside its slot, and below 800 px high the plan box hides while a question is open, the channels get smaller chips and the question box gets tighter (`t-fit.mjs`).
  Breakpoints: 1360 (notes hidden), 1300, 1180, `max-height: 800` (compact HUD, rail), 1100 (narrow rail), 900 (phone: bottom nav, one pane at a time, the rail is a view), 600-900 (large phone).
* **Motion**: everything that moves follows the governor. `ui.still()` (setting `Motion` or `prefers-reduced-motion`) puts `body.still` on the page: the horse stands, arcs and flashes are skipped, no Web Animation or looping CSS animation runs; the simulation still advances (`t-motion.mjs`).
* **No emoji.** State is a text glyph plus a word (`✓ done`, `? ask`, `✎ edit`, `◇ think`, `◌ idle`, `⏹`), never colour alone. Focus ring `2px solid var(--focus)`.

## 17. Extension points

| I want to ... | Do this |
| --- | --- |
| add a panel to the Cockpit | a function `mountMyPanel(scope, host)` that renders with `scope.update` and animates with `scope.frame`; call it from the cockpit mount (83-ui-cockpit.js) with the cockpit's scope; add its CSS in a new `src/css/NN-*.css` listed in the manifest |
| add a view (screen) | `SL.views.register(...)` (section 10), a tab in `ui.TABS`, a key in `VIEW_KEYS`; nothing else |
| replace the layout of the shell | edit `src/html/shell.html` and `29-layout.css`; keep the ids in section 12 (the shell modules look them up); keep `class="talk"` + `data-hold="chat"` on every transcript |
| add an action | `act('name', fn)` (section 9) |
| add an event type | `reduce` + `VISIBLE` (section 6), then a script or action produces it |
| add a slash command | a handler in `H` (93-palette.js) and the entry in `SL.FX.SLASH` / the pack's `chatSlash`; the palette test fails when a listed command has no handler |
| add a pack field | one line in `adopt()` (section 4) |
| add a sheet or dialog | `ui.sheets.name = () => ui.modal({...})` and a palette entry; it reads the session and changes it only through `SL.act` |
| add a runner output | `BUILTIN['cmd path'] = flags => ({ lines, exit, ms, card })` in 92-runner.js, or `outputs[...]` in the pack |
| add a persistent setting | `SL.settings` key (60-actions.js `load/save` list), an action `setX`, a control in the Settings view |
| make a new simulated session kind | a script function in 40-scripts.js returning `{events, wt0}` and a branch in `Session.initRun` |

## 18. Test hooks and the verification scripts

The shipped file contains **no** hook. `build.mjs --test` appends `test/zz-test-hooks.js`, which exposes `window.__SL` (the namespace) and `SL.test`:

* `manual()` / `?manual` in the URL: the page never steps by itself; `step(dt)`, `run(seconds, dt)`, `until(pred, maxSec)` drive the loop deterministically from t = 0 (a time warp).
* `flood(S, t0, t1, opt)`: deterministic synthetic events through the normal `S.add` path (a long-running job).
* `counts()`: live timers, intervals, rAFs, listeners (attached and on detached nodes), observers, running animations, DOM nodes, bus subscribers, frame and update hooks, scopes: instrumentation wraps `setTimeout`, `setInterval`, `requestAnimationFrame`,
  `addEventListener` (weakly held, so a collected node drops out), `ResizeObserver`, `Element.animate`. `gc()` forces a collection (the browser is started with `--expose-gc`), `idle()` / `settle()` wait for real frames and timers, `listenerMap()` names listeners by target, `sig(m)` is a state signature.

`test/cdp.mjs` is a tiny headless-Chromium driver over the DevTools protocol (Node 22, no dependencies): `open(file, {w, h, scale, reduced, query})` returns `{eval, shot, key, move, errors, fontMisses, close}`.
Console errors, exceptions and failed loads are collected in `errors` (the offline Google Fonts miss is kept apart in `fontMisses`).

| Script | Guards |
| --- | --- |
| `t-governor.mjs` | 45-minute hold: catch-up seconds, DOM node delta, state equality with a never-held run; 20 rapid hold/release cycles without drift; a hold during a mail arc |
| `t-lifecycle.mjs` | 30 rapid view switches (timers/listeners/observers/animations/bus/scopes before = after; detached nodes collected); 50 session switches while questions arrive (badges, inbox); new/close; the mail-arc test; each view re-mounted 10 times at a frozen time has identical node and listener counts |
| `t-numbers.mjs` | the Cache numbers (rows, `all` row, header, session line), HUD ring, stalls, the manager card, drawer chips, Sessions card and Replay frame agree at 5 moments in every view |
| `t-input.mjs` | with real input events: hover hold and slow, pin, Esc precedence, the approval quiet period (typed-ahead text never answers) |
| `t-func.mjs` | the store round trip, channels, steer, interrupt and resume, goals, new/resume/restart, the 61 runner commands, the palette (every slash command has a handler) |
| `t-layout.mjs` | no horizontal overflow at 390, 768, 1024, 1280, 1440, 1920 in every view |
| `t-fit.mjs` | vertical fit at 1024x700 ... 2560x1300 (minimum 640 high): the page and the rail do not scroll, the composer and footer are visible, the transcript keeps at least 40 px, the question box is clipped by at most 12 px (with and without a question, in two channels) |
| `t-motion.mjs` | reduced motion: no animation, legs still, no arcs, simulation advances |
| `t-prune.mjs` | `sessions prune` arithmetic against an independent computation and, with the pack, against the pack's own output (ids and counts), then `--yes` shrinks the list by exactly that many |
| `t-shipped.mjs` | the shipped `00-core.html` itself (no hooks): `window.__SL`/`SL` undefined, real frame loop, every view opens from its tab, palette and help open from the keyboard, a real pointer move holds and leaving releases, no console error (phone and desktop) |
| `t-pack.mjs` | the data pack adapter: what `SL.D` adopted, the roster order, hit % per agent equal to the pack's, every spec command runs |

All take the file to test as `argv[2]` (default `../dist/test.html`; run them from `test/`). They print numbers and exit; read them, do not trust a green word.
`test/run-all.sh` runs the whole set, **one browser at a time**, against both `dist/test.html` and `dist/pack-test.html` and writes `dist/results/BUILD-NAME.txt`. A script that throws or is killed does not leave a browser behind (`cdp.mjs` kills it on exit); run browser tests serially.
Pictures: look at what you change. `test/snap.mjs NAME W H [JS]` writes a PNG of the scratch build (`SHOTS=dir`), `test/final-shots.mjs` renders the whole verified set (cockpit, break flash, answered question, hold and digest, every view, dialogs, single-agent and headless sessions, phone, 1024, 1920, reduced motion).

## 19. Limits and known gaps

* There is no Files / Changes / Checkpoints tracker view: file and diff information is in the drawer, `/diff` and `/rewind`.
* The hover-hold cannot detect a pointer that never moves (a held pointer with no events keeps the last classification): a hold ends on pointer leave, Esc, or unpin.
* `T6` takes the verification slot after the statically scheduled T4, T5, T7 (the director queues in arrival order).
* The cache-break extra cost on the anomaly row is derived from the prices (`$0.0040`), not the TUI's `$0.02`.
* The data pack's `outputs` keep their own state (sessions, trust, schedule); only pruning is synchronised with the registry.
* A resumed session replays a short generic script (survey, plan), not the original session.
* Everything is sample data; durations, costs and token counts of recorded sessions are invented.
