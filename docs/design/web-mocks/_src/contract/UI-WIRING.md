# UI-WIRING: turning the mock's page into the real page

Binding for A3, C1, C2, C3. The page is `internal/web/ui` (plain files, no build step, classic scripts sharing `window.SL`, as A3
packaged it). The rule: **the look and the behaviour stay the mock's; only where data comes from changes.** Every view keeps its
markup, classes and texts; a data source that was a fixture becomes a live cache with the same shape; an action that changed the
simulation becomes an API call whose result arrives as events.

Contents: 1 Module plan · 2 Load order · 3 Boot · 4 api.js · 5 live.js · 6 SL.D and SL.G as live caches (11-data-live.js) ·
7 SL.act, action by action · 8 Every view's data, loading, empty and error states · 9 The data pack, fixtures and the dev fixture mode ·
10 UI-local state · 11 Test hooks · 12 Security and accessibility in page code · 13 Patches to kept modules, file by file

---

## 1. Module plan

| Mock module | Fate | Owner | What changes |
|---|---|---|---|
| `00-namespace.js` (A3) | kept | A3 | `window.SL = {}` |
| `00-util.js` | **unchanged** | | |
| `10-fixtures.js` | removed from the shipped page | A3 moves it to `uidev/` | the sample data |
| `11-data-adapter.js` | replaced by `11-data-live.js` | C1 | `SL.D` built from the server, same field names (section 6) |
| `data.js`, `outputs.js`, `cli-spec.js` | removed from the shipped page | A3 moves them to `uidev/` | the data pack |
| `20-clock.js` | **unchanged** | | the governor |
| `30-model.js` | patched | C1 | the additive vocabulary (VOCAB.md 7), `calc.agent` cost, `calc.warmLeft` TTL, `addAgent` helper |
| `40-scripts.js` | removed from the shipped page | A3 moves it to `uidev/` | the simulation |
| `50-sessions.js` | rewritten in place (same API) | C1 | `Session` keeps its clocks, log, models, `collapse`, `seek`; loses `initRun` scripts; registry becomes server-backed |
| `60-actions.js` | rewritten in place (same API) | C1 | every `SL.act.*` keeps its name, arguments and synchronous `{ok, why}` result; bodies call `api.js` |
| `api.js` (new) | added | C1 | fetch wrapper (section 4) |
| `live.js` (new) | added | C1 | the stream, snapshots, frames → sessions (section 5) |
| `70-view.js` | **unchanged** | | |
| `80-ui-shell.js` | patched | C1 | warm ring life from TTL, govtxt 429s/retries from the model, conn chip from `hello` and stream state, "(sample prices)" titles (D-07) |
| `81-ui-hero.js` | patched | C1 | `D.layers` tokens from the model (section 6) |
| `82-ui-board.js` | **unchanged** | | |
| `83-ui-cockpit.js` | patched | C1 | Governor gauges' 429s/retries, mail note `dup`, note text (D-07) |
| `84-ui-chat.js` | patched | C1 | streaming rows that grow (`more`), `tod` with `at`, ctrl+c twice (D-14) |
| `85-ui-approvals.js` | patched | C1 | `answer` goes through `SL.act.answerQuestion` unchanged; the server's `409 too_soon` re-arms; kinds `trust`/`mcp` (D-09) |
| `86-ui-overlays.js` | **unchanged** | | |
| `87-ui-sheets.js` | patched | C1 | New session (projects list, trust step), Resume (recorded list), Rename; unreachable sheets deleted (section 13) |
| `88-ui-drawer.js` | patched | C1 | "files owned" from the Workspace index; layer stack from the model |
| `90-views-a.js` | patched | C1 | Cache view: write column from `A.wr`, expected-prefix texts from data, compact estimate; Replay: unchanged |
| `91-views-b.js` | patched | C3 | Sessions view: recorded list, prune, Replay of a recorded session (D-10) |
| `92-runner.js` | patched | C3 | `exec` → `POST /api/runs`; output from `run` frames; `BUILTIN` removed |
| `93-palette.js` | patched | C1 | slash list per tab; `/exit`; unknown slash lines → `POST .../command`; `files()` from the Workspace cache |
| `94-keys.js` | **unchanged** | | |
| `95-kit.js` | **unchanged**, or dropped from the shipped page (D-20, recommended: dropped and kept in `uidev/`) | C1 | it renders sample markup only; `ui.popover`/`ui.ringSvg` have no other user |
| `96-ui-nav.js` | **unchanged** | C3 | owned by C3 in case a badge source needs a change; none is expected (badges read the model and `ui.ws.counts`) |
| `96b-ws-data.js` | rewritten in place (same API) | C2 | async cache over the Workspace endpoints (section 8.11) |
| `97-ui-workspace.js` | patched | C2 | hard-coded sample paths removed; loading state; actions through the API |
| `98-ui-settings.js` | patched | C3 | `X.*` reads from live caches; actions through the API; texts of D-01/D-07/D-08 |
| `98b-ui-tools.js` | patched | C3 | Doctor from `POST /api/doctor` frames; Schedule from `/api/schedule` |
| `99-app.js` | patched | C1 | boot waits for `live.start()` (section 3) |
| `zz-test-hooks.js` | dev only | A3 (`uidev/`) | section 11 |

## 2. Load order (`index.html`, A3 writes it)

```
00-namespace.js  00-util.js  api.js  11-data-live.js  20-clock.js  30-model.js  50-sessions.js  60-actions.js  live.js
70-view.js  80-ui-shell.js  81-ui-hero.js  82-ui-board.js  83-ui-cockpit.js  84-ui-chat.js  85-ui-approvals.js  86-ui-overlays.js
87-ui-sheets.js  88-ui-drawer.js  90-views-a.js  91-views-b.js  92-runner.js  93-palette.js  94-keys.js  95-kit.js  96-ui-nav.js
96b-ws-data.js  97-ui-workspace.js  98-ui-settings.js  98b-ui-tools.js  99-app.js
```

All are `<script src>` at the end of `body` (no `defer`/`async`, no `type="module"`), in this order. The footer's `MOCK · sample
data` chip: D-07.

## 3. Boot

`99-app.js` `boot()` becomes:

1. `SL.settings.load()`, governor mode, motion, density, cache detail classes (unchanged).
2. Mount nothing yet; `SL.live.start()` (live.js): `GET /api/hello`. On `401` → `location.reload()` (the server answers a navigation
   without a session with its sign-in page, `internal/web/auth.go` `signInPage`; D-13). On network error → the disconnected state (D-13)
   and retry every 2 s.
3. Open the stream (`EventSource('/api/stream?after=' + hello.streamAfter)`), buffer frames.
4. Fetch, in parallel: the snapshot of every tab in `hello.tabs` (`GET /api/sessions/{id}/snapshot`), `GET /api/cli`, the Settings
   caches needed by the shell (`/api/models`, `/api/providers`, `/api/projects`, `/api/recorded`); the rest lazily when a page opens.
5. Build `SL.sessions` from the snapshots (section 5), apply buffered frames.
6. Then the mock's boot from `SL.sessions.init()` on: shell scope, `ui.shell.mount`, `ui.navMount`, `SL.chat.mount`, approvals, link,
   keys; the first view from `?view=`, the active tab from `?session=` else `hello.active` else the first tab; `SL.loop.start()`.
7. With zero tabs (D-12): the shell mounts with the placeholder session and opens `ui.dialogs.newSession()`.

The page never renders sample data while loading: until step 6, `#app` keeps the shell markup of `index.html` with empty values
(the HUD shows its static defaults for under a second on loopback; D-13 covers a slow server).

## 4. `api.js`: `SL.api`

```js
SL.api = { get(path), post(path, body), put(path, body), patch(path, body), del(path, body), confirm(action, args, tab) }
```

* `fetch(path, { method, credentials: 'same-origin', headers: { 'Content-Type': 'application/json', 'X-Sleipnir-Web': '1' },
  body: JSON.stringify(body) })` for routes with a body; routes declared without a body get no body and no content type, but the
  `X-Sleipnir-Web: 1` header; GETs send neither. Only paths under `/api/` (A3's asset test enforces it).
* Resolves `{ ok: true, data }` or `{ ok: false, status, code, message, detail }` (from the error body `{error, code, detail?}`:
  `message` = `error`); never throws on HTTP errors; a network failure resolves `{ ok: false, status: 0, code: 'network', message:
  'the server is not reachable' }` and tells live.js (disconnected state).
* `401` anywhere → `location.reload()` (sign-in page). `428 confirm_required` → a programming error (the page asks for every
  confirmation it needs before the request): toast the message. `429` → one retry after `Retry-After` for GETs; mutations surface
  the message.
* `confirm(scope)` → `POST /api/confirm {scope}` → the id, or null; the caller sends it as the `X-Confirm` header
  (`api.post(path, body, { confirm: id })`). Scopes per CONTRACT.md 20; `d16(values)` (the first 16 hex of SHA-256 over the
  canonical JSON) is computed with `crypto.subtle.digest`.
* Every mutation that creates something carries a `clientId` (`'c' + counter`).

## 5. `live.js`: `SL.live`

* **Sessions from snapshots.** For each `TabSnapshot`: `S = SL.sessions.make({ id: tab.id, name: tab.name, sid: tab.sid, meta })`
  (no script), `S.roster = snapshot.roster`, `S.log = keyframe.concat(events)` (keyframe events get `seq: 0`), `S.seq = snapshot.seq`
  (`lastSeq`), `S.wt = snapshot.now`, `S.vt = S.wt`, `S.widx = 0`, `S.wm = SL.model.newModel(S, { noChat: true })`, `S.stepWorld(S.wt,
  true)`, `S.hist = snapshot.hist`; meta derived fields: `t0` = seconds since local midnight of `meta.startedAt`, `started` =
  `hh:mm:ss` of it; the tab's open questions are already in its events.
* **Frames.** `ev` → if `ev.seq <= S.lastSeq` drop; else clock rule (VOCAB.md 3: `if (ev.t > S.wt) { S.wt = ev.t; }`), `S.add([ev])`
  (the event keeps the server's `seq`: C1 changes `Session.add` to keep an existing `e.seq`), `S.lastSeq = ev.seq`, `S.stepWorld(S.wt)`
  (fires `ask-arrived`, `needs-changed`, `session-done` exactly as the mock), `SL.loop.dirty = true`. `meta` → `Object.assign(S.meta,
  patch)` (+ derived `t0`/`started`, `S.ui.queued = patch.queued` when present), `S.touch()`. `roster` → section 5.1. `tab` → add
  (make + fetch snapshot), update (name, sid), remove (`SL.sessions.close` locally without calling the server). `reset` → refetch the
  snapshot, rebuild the session in place (keep `S.ui`), emit `roster-changed`. `recorded` → refetch `/api/recorded`, emit
  `recorded-changed`. `run` → `SL.bus.emit('run', data)` (runner, doctor, schedule). `toast` → `ui.toast`. `ping` → clock rule per
  tab. `gap` (hub) → refetch every snapshot. `lagged` (hub) → nothing (EventSource reconnects with `Last-Event-ID`). `bye`,
  `closed` (hub) → disconnected state.
* **5.1 Roster changes.** Replace `S.roster`; for every new id call `SL.model.addAgent(S.wm, r)` and, when `S.m`, `addAgent(S.m, r)`
  (the per-agent init of `newModel`, exported); then `SL.bus.emit('roster-changed', S)` (99-app.js remounts the current view, as the
  mock does after `/swarm`).
* **Reconnect.** `EventSource` reconnects by itself (`retry: 2000`); live.js tracks `open`/`error` for the conn chip (D-13).
* **Clock.** `SL.loop.step` keeps advancing `S.wt` by wall time for every session (unchanged); events and pings correct it upward.
  `SL.actions.pump` becomes a no-op (the queue is the server's).

## 6. `SL.D` and `SL.G` as live caches (`11-data-live.js`)

`SL.D` keeps every field name the views read; values come from the server. Fields read synchronously from caches; when a cache
fills, `SL.G.ver++` and `SL.loop.dirty = true` re-render through the mock's own `update` hooks (the render key includes `SL.G.ver`).

| `SL.D.*` | Source | Shape kept |
|---|---|---|
| `roles`, `roleOrder` | `ModelsView.roles` / `roleOrder` (static copy of `BuiltinRoles` as fallback) | `{manager: {code, ro, desc}, ...}`, `['scout', ...]` |
| `models`, `model(ref)` | `ModelsView.models` | `{ref, ctx, in, out, cached, tools, reasoning, fav, sample: false}` |
| `roleModels` | `ModelsView.roleModels` | `{role: ref}` |
| `modes` | static UI copy (the mock's five modes and texts) | unchanged |
| `rules`, `testsPreset` | `PermissionsView` (`rules` fallback empty, `testsPreset = .testsPreset.rules`) | unchanged |
| `shortcuts` | static UI copy (the mock's list) | unchanged |
| `skills` | `SkillsView.skills` → `{name, desc: summary, from: source}` | unchanged |
| `layers` | getter: names, notes and colours are the mock's static G0..G5 table; `tok` = the active tab's manager `A.layers[i]` (G0..G2 are shared) | `[{id, name, tok, note, col}]` |
| `layerToks(id)` | the active tab's `m.ag[id].layers`, else six zeros | `[6 ints]` |
| `g5` | getter `{id: A.layers[5]}` | unchanged |
| `prices` | `{ mgr: {in:0, cached:0, out:0}, worker: {...}, sample: false }` (only the fallback of `calc.agent`) | unchanged |
| `spec` | `GET /api/cli` | `wire.CLISpec` = the mock's cli-spec.json shape |
| `slash` | `GET /api/sessions/{id}/slash` of the active tab | `[{cmd, args, desc, group}]` |
| `recorded`, `live`, `plan`, `goal`, `tasks`, `code`, `orderCode`, `treeShop`, `treeOrders`, `ckpts`, `streamTest`, `hitSeries`, `roster`, `nreq`, `deps`, `outputs`, `ctx`, `packSync`, `output` | removed (no reader remains after sections 8 and 13) | |
| `extra` (= `X`) | per-page caches below | |

| `X.*` | Source |
|---|---|
| `permissions` | `GET /api/sessions/{id}/permissions` (`PermissionsView`: `modes order managerWrites testsPreset rules{allow,deny,ask}`) |
| `trust` | `GET /api/sessions/{id}/trust` (`TrustView`: `project files covers ledger question`) |
| `mcp` | `GET /api/sessions/{id}/mcp` (`MCPView`: `servers sessionNote`) |
| `skills`, `skillsBudget`, `commands`, `hooks` | `GET /api/sessions/{id}/skills` |
| `providers`, `providerNote` | `GET /api/providers` |
| `config` | `GET /api/sessions/{id}/config` |
| `schedule` | `GET /api/schedule` (re-fetched on the `run` result of a job) |
| `projects` | `GET /api/projects` mapped to `[{root: dir}]` |
| `doctor` | `{endpoints: GET /api/doctor/endpoints}` |
| `cronNext(expr, from)` | a memo of `GET /api/schedule/next?cron=`: returns the cached next time or `undefined` while unknown (C3 keeps the previous hint while `undefined`) |
| `files`, `diffText`, `blame`, `fileAt`, `treeAt`, `stepDiff`, `diffSince`, `unified`, `checkpoints` | removed (the Workspace uses `SL.ws`, section 8.11) |

`SL.G` (state shared by every session): `mcp` (from `X.mcp.servers` → `{name, origin, transport, state, tools}`), `favs`
(`new Set(ModelsView.favs)`), `trust` (`TrustView.files` → `{file: path, hash, state: 'trusted'}`), `trustDirs` (`TrustView.ledger`
mapped exactly as the mock's `ledger()` maps the pack's ledger), `providers` (`ProvidersView.providers`), `schedule` (unused by
views; kept), `roleModels`, `history` (unused), `runs` (`GET /api/runs` → `{cmd, exit, ms, path, flags}`), `sched` (`ScheduleView` as
`{jobs, daemon, logs, n}`), `ver`.

Caches are refreshed: on boot (models, providers, projects, recorded), on opening a Settings page or the Tools views (its endpoints,
then every 30 s while open), on tab switch (the per-tab ones), and on the frames that change them (`recorded`; a `meta` patch with
`rules` refreshes `X.permissions.session`).

## 7. `SL.act`, action by action

Pattern: each action keeps its signature and synchronous result. It runs the mock's own pre-checks (same `why` texts) and returns
`{ok:false, why}` when they fail; otherwise it starts the request and returns `{ok:true}` at once. The effect on the screen comes
from the events and meta the server sends (never from a local insertion). When the request fails, the action shows
`ui.toast(message, 'err')` (`'warm'` for 409s), where `message` is the server's sentence. An action never inserts UI events, except
`local` cards (93-palette.js) which are page-only.

| Action | Pre-check (sync result) | Request | On success | Notes |
|---|---|---|---|---|
| `setHover`, `setMotion`, `setCache`, `setDensity` | value in list | none (localStorage) | as mock | UI-LOCAL |
| `setMode(mode, opt)` | unknown mode; danger without `opt.confirm === mode` → mock's why | danger: `api.confirm('mode:' + mode + ':' + tab)` then `POST .../mode {mode}` with `X-Confirm`; else `POST .../mode {mode}` | `sys` row + `meta.mode` arrive | the mode menu's toast `mode: x` stays |
| `cycleMode` | | as `setMode` with the next of `default accept-edits plan` | | never reaches bypass/yolo (mock rule kept) |
| `setModel(ref)` | | `POST .../model {ref}` | `sys` + meta; a team restarts (`reset`) | toast texts: D-07 |
| `setRoleModel(role, ref)` | | `POST .../model {ref, role}` | as above | manager → `setModel` |
| `setEffort(lv)` | | `POST .../effort {level}` | `sys` + meta | |
| `setBudget(usd)` | mock's parse and why | `POST .../budget {usd}` or `{off: true}` | `sys` + meta (+ goal pause) | |
| `setIsolation`, `setVerify`, `setFlag` | mock's lists | `PATCH .../launch {...}` | `sys` + meta | |
| `allowRule`, `denyRule`, `askRule(rule, origin)` | empty rule → mock's why | `POST .../rules {effect, rule, origin}` | `{ok, added}` returns the client's estimate (`tests` → `D.testsPreset.length`, else 1); meta `rules` arrives | |
| `removeRule(rule)` | | `POST .../rules/remove {rule}` | meta | |
| `setGoal(text)` | empty → `/goal needs text` | `POST .../goal {action:'set', text}` | `say you`, `goal`, `say sys` arrive | the mock's fixture refusal is gone |
| `pauseGoal`, `resumeGoal`, `clearGoal` | mock's state checks on `S.wm.goal.state` | `POST .../goal {action}` | `goal` + rows | |
| `answerQuestion(qid, choice, note, sid)` | question open in `S.wm.qs` | `POST /api/questions/{qid}/answer {choice, note}` | `answer` arrives (closes it everywhere) | `409 too_soon`: re-arm the meter (`shown[qid] = now`) and toast the mock's "the buttons wake up..." text; `409 answered`: silent |
| `markReviewed(path, cp, on)` | | `PUT .../ws/reviewed` | the Workspace cache updates the mark at once (optimistic), then the index | |
| `revertHunk(path, key)` | | `api.confirm('revert:' + tab + ':' + d16({path, key, from, to}))` then `POST .../ws/revert` with `X-Confirm` | `sys` row; Workspace cache refreshes the index and the file | `409 changed`: toast + refresh |
| `unrevertHunk(path, key)` | | `POST .../ws/revert/{rid}/undo` | as above | the `rid` comes from the cache's reverted list |
| `rewind(id)` | mock's "nothing to put back" check on the index | `api.confirm('restore:' + tab + ':' + id)` then `POST .../ws/restore {id, dryRun:false}` with `X-Confirm` | `ckpt {safety}` + `sys` arrive; `S.ws.restore` set from the response | |
| `undoRewind` | `S.ws.restore` set | `api.confirm('restore.undo:' + tab)` then `POST .../ws/restore/undo` with `X-Confirm` | `sys` | |
| `interrupt(target)` | mock's checks (`target` turn/mgr; `turnRunning`) | `POST .../interrupt {target:'turn'}` | `interrupt`, `goal`, `state` arrive | |
| `steer(agent, text)` | mock's checks | `POST .../steer {text}` | `steer` arrives; the manager's answer is its next `say` | |
| `compact(focus)` | | `POST .../compact {focus}` | `compact` (from the log) + `sys` | returns `{ok:true}` |
| `send(text)` | empty | `POST .../messages {text, display, clientId}` | `say you` arrives when delivered; `meta.queued` while queued | the composer pushes to `S.ui.queued` optimistically when `turnRunning` (as the mock), replaced by `meta.queued` |
| `switchSession(id)` | | none | as mock (`activate`) | then fetch the tab's slash list and per-tab caches |
| `newSession(spec)` | | `POST /api/sessions` (+ the trust step, section 8.13) | `tab add` frame → activate the tab whose `clientId` matches | returns `{name: spec.name || <slug of cwd>}` synchronously for the caller's toast |
| `closeSession(id)` | last tab → `{ok:false, why:'the last session cannot be closed: start another first'}` | `DELETE /api/sessions/{id}` | `tab remove` | |
| `renameSession(id, name)` | empty → keep | `PATCH /api/sessions/{id} {name}` | `tab update` | |
| `resumeSession(recId)` | not found / not resumable → mock's whys (from the cached list) | `POST /api/sessions/resume {from: recId}` | `tab add` → activate | `409 hosted`: switch to that tab |
| `restartTeam(patch)` | mock's "already manager + n workers" check | `POST .../restart {kind:'swarm', swarm, fresh:true}` (+ staged flags already on the server) | `reset` | |
| `newChat()` | | `POST .../restart {kind:'new', fresh:true}` | `reset` | |
| `pruneSessions(older, keep, apply)` | `SL.sessions.prune` arithmetic (client preview) | apply: `api.confirm('prune:' + d16(sortedIds))` then `POST /api/recorded/prune {olderThan, keep, apply:true}` with `X-Confirm` | `recorded` frame | the preview stays client-side over `/api/recorded` (the same rule); the server refuses `409 changed` if its plan differs |
| `favModel(ref)` | | `POST /api/models/fav {ref, on}` | update `G.favs` optimistically, `models-changed` | |
| `trustDir(dir, on)` | | on: `GET /api/trust/challenge?dir=` then `POST /api/trust {dir, on:true}` with `X-Confirm: <challenge.confirm>`; off: `POST /api/trust {dir, on:false}` | refresh `X.trust`, `G.trustDirs`, `trust-changed` | |
| `setProvider(id, patch)` | | sign out → `POST /api/providers/{id}/signout`; "I ran it" → `POST /api/providers/recheck` | refresh `G.providers` | D-01 |
| `setMcp(name, patch)` | | approve → `api.confirm('mcp.approve:' + d16({root, name, fingerprint}))` then `POST .../mcp/{name}/approve` with `X-Confirm`; revoke/test/reconnect → `POST .../mcp/{name}/{action}` | `MCPResult` → `ST.mcp.out[name]`; refresh `G.mcp`, `mcp-changed` | the Settings page calls these through its `mcpDo` (C3 maps it) |

`SL.actions`: `cancelFuture`, `ownerOf`, `deliver`, `resumeAgent` are removed (simulation); `pump` stays as a no-op (the loop calls it).

## 8. Every view's data, and its loading, empty and error states

Rule for states the mock does not have: reuse an existing empty-state element and text pattern of that view (`.c-note`,
`.stubnote`, `.ws-none`, `.bempty`, "no ... yet"); errors go to `ui.toast(message, 'err')`; nothing new is drawn except where D-13
says so.

1. **HUD** (80-ui-shell.js): goal, budget gauge, cost, hit ring, warm ring, team, govtxt — from `S.meta` and `S.m` (unchanged code).
   Warm ring: `WARM_LIFE` becomes `m.ttl || 25` for the fraction and the countdown (the 25 ticks stay 25 ticks); `govtxt` `429s` and
   `retries` read `m.r429`/`m.retries`. Loading: values at zero.
2. **Session strip**: `SL.sessions.list` (tabs), `S.state()`, costs (`calc.totals(S.wm)` with real `cost`), needs (`S.wm.qs`). The conn
   chip: `hello.server.addr` instead of the constant; `· loopback · token ✓` while the stream is open (D-13 for the other state).
3. **Left rail** (96-ui-nav.js): badges from the model and the Workspace cache (`ui.ws.counts`). Unchanged.
4. **Cockpit** (83): roster, model, hero legs. Governor gauges: `m.r429`, `m.retries`; note `gnote` text: D-07 (static in the mock).
   Mail note `dup 0`: D-07. Merge queue counters `m.conflicts`/`m.bounced` (now real). Single agent: unchanged.
5. **Radio rail** (84): transcript from `m.chan`, plan box from `m.planText`/`m.plan` (C1: `S.plan` getter returns `m.planText ||
   []`; an empty plan hides the box as the mock's `!goalText && !torder.length` rule does when there is no goal), hold chip unchanged,
   composer unchanged except `send`.
6. **Approvals and inbox** (85): questions from the models of every tab (unchanged).
7. **Drawer** (88): Details, Tool log, Mail, Cache from the model; "files owned": the Workspace index rows whose `owner` is the agent
   (C2 exposes `ui.ws.filesOf(S, id)` → `[{path, st, add, del}]`); while the index loads: the existing `no file yet` note.
8. **Cache view** (90): from the model; the `write` column reads `x.wr`; the "expected the 4.5k shared prefix" texts read the anomaly's
   `expected` (`fmtK`) instead of the literal; the price note and the compact estimate: D-07.
9. **Mail view** (90): from `m.mail`; `dup 0`: D-07.
10. **Board, Replay views** (90): from the model and `S.log` (unchanged). Replay of a recorded session: D-10.
11. **Workspace** (96b, 97; C2). `SL.ws` keeps its function names and arguments; implementation:
    * `info(S)` returns a cached `I` built from `GET .../ws/index` in the mock's shape: `{key, cps, pos, base, steps, stepOfId, raw:
      {tree}, cache}` where each `cp` is `{id, step: id (or null when skipped), time, label, skipped, safety, files, agents, tasks,
      added, removed, nfiles}`; `key` includes the index `version`. While loading: `null` → the view's `paintDerived` path, whose
      texts become the loading text (D-13).
    * A **point** `k` (0..n) maps to `at`: `k < n` → the id of `pos[k]` (content when it began, which is after the change sets
      `pos[0..k-1]`), `k = n` → `live`. `setAt(I, k)` keeps returning the step list; a helper `pointOf(I, steps)` maps it back.
    * `textAt`, `blame` → `GET .../ws/file?path=&at=` (cached per path and point; `blame` expands runs into the per-line array
      `[{ag, id, task}]`); `hunks`, `counts` → `GET .../ws/diff?path=&from=&to=`; `touches`, `statusOf`, `rows`, `filesFrom` → from
      the index (no request). A value not yet fetched returns the empty shape (`null` text, `{added:0, removed:0, hunks:[]}`) and
      schedules the fetch; arrival bumps `SL.G.ver`.
    * `liveFile(S, I)` unchanged (an agent in `edit` whose `doing` names a tree path); `pending(S, I, path, vt)` generalised: any
      `m.streams[id]` with `code` and `file === path`, typed from the stream (`ui.typed`), replacing the hard-coded test file;
      `isLive` drops the pack's `versions` test and uses `m.diff[path].done`.
    * Removed sample specifics in 97: the `'api/catalog/items_test.go'` and `'seed/items.json'` literals, the `'c04'` base checks, the
      "sample data" texts (D-07). Revert, restore and reviewed actions go through `SL.act` (section 7).
    * `SL.fileOwner(f)` from the index; `ui.ws.counts` unchanged on top of `rows`.
12. **Sessions view** (91; C3): live cards from tabs (unchanged), recorded list from `SL.sessions.recorded` (the `/api/recorded` cache),
    prune preview client-side, apply through `SL.act.pruneSessions`; Replay button: D-10. Loading: the table renders empty.
13. **New session dialog** (87; C1): `cwd` options from `/api/projects` (default selected: `default: true`); the mock's "Trust this
    project?" confirm shows when `trustProject` is on and the project's `trust` is not `trusted` (replacing the `/untrusted/` test).
    On its "Yes, trust these files" the page posts `POST /api/sessions`; the `409 trust_required` answer carries the token and the page
    repeats the request with `trustConfirm` at once (the person's yes was the dialog). When the page believed the project trusted and
    the server answers `409 trust_required` (a file changed since), the page opens the same confirm and repeats on yes. Models from
    `D.models`; role list from `D.roleOrder`.
14. **Resume dialog** (87): `SL.sessions.recorded`; `--continue` → `resumeSession('latest')`.
15. **Settings** (98; C3): each page renders from `X.*`/`G.*`/meta as in the mock. While a page's cache loads, it renders with empty
    lists (the mock's own empty texts: "no model matches", "No hooks.", "No custom command: ...", "No layer data."). Actions per
    section 7 and CONTRACT.md 13. The "Would it ask?" result comes from `POST .../permissions/check` (async: the result line keeps its
    previous text until the answer arrives); `judge()` (the mock's JS classifier) is removed.
16. **Tools** (98b): catalogue from `D.spec` (unchanged code). **Doctor**: endpoints from `X.doctor.endpoints`; Run → `POST
    /api/doctor`; `run` frames with `step` append steps exactly as the mock's timers did (`dstep` rows), the `verdict` frame fills
    "What this endpoint does"; `parse()` and the pack call are removed. **Schedule**: from `G.sched` (`/api/schedule`); Add, Remove,
    Run now (output from `run` frames into the log pane), Log, daemon buttons through the endpoints of CONTRACT.md 17;
    `nextHint` from the async `X.cronNext` memo.
17. **Runner** (92; C3): spec from `D.spec`; Run → `POST /api/runs` (`tab` = the active tab; privileged commands ask for a token
    through `api.confirm('run:' + d16(argv))` after the mock-style confirm C3 adds only for `mode: priv` commands, D-05); output lines from
    `run` frames (`k`, `t` as the mock's `L(k, t)`), the result card from `result`; Reset, another command, or leaving the view cancels
    (`DELETE /api/runs/{id}`, D-05); `tty_only` commands show the server's one-line message as a `bad` line with exit 2. Recent runs
    from `G.runs`.
18. **Palette** (93): items as the mock builds them; `D.slash` per tab (custom commands appear in the `/` menu with `live: true`);
    `run()` for a slash item without a page handler → `POST .../command`, its `output` shown with `local(title, '<pre>' +
    esc(output) + '</pre>')`; `/exit` → `ui.closeSessionAsk(S)` (unchanged).

## 9. The data pack, fixtures and the dev fixture mode

* The shipped page (embedded in the binary) contains no sample data: `10-fixtures.js`, `11-data-adapter.js`, `40-scripts.js`,
  `data.js`, `outputs.js`, `cli-spec.js` and `zz-test-hooks.js` are not under `internal/web/ui`. A3 keeps byte-identical copies in
  `internal/web/uidev/` (outside the embed) together with `index-mock.html`, which loads the original mock module list (the v3
  manifest's order) from there: this **is** the mock, served by `scripts/web-ui-dev.mjs --mock` for side-by-side parity.
* `scripts/web-ui-dev.mjs` serves `internal/web/ui` (real page shell, `/api/*` 404) by default, and with `--mock` serves the mock
  page from `uidev/`. It never serves `uidev/` without `--mock`.
* The "real" side of parity runs the Go server with the deterministic backend `sleipnir web --fixture shop` (B1, TEST-PLAN.md 3),
  not with sample data in the page.

## 10. UI-local state

| State | Where | Notes |
|---|---|---|
| Hover, motion, density, cache detail | `localStorage['sleipnir.web.settings']` | unchanged (60-actions.js `settings.load/save`, try/catch) |
| Left rail wide/narrow, Radio rail open/collapsed and width | `localStorage['sleipnir.web.dock']` | unchanged (96-ui-nav.js) |
| Per tab: channel, view, drawer and tab, plan box open, selected agent, draft, scroll, Workspace tab/file/point/mode/group/filters, verbose, feed open | `S.ui` in memory | kept across tab switches; lost on reload, as in the mock |
| The hold pin and its saved view time | `S.hold` in memory | unchanged |
| History of sent lines (ctrl+r, ↑) | `S.hist`, seeded from the snapshot's `hist` (the tab's `user.input` lines, newest 200) | lines sent in this page are appended locally |
| Local cards (`/cost`, `/status`, `/cwd`, command output) | `S.log` only | not in the server journal: a reload or a snapshot refetch drops them |
| Reviewed marks | **server** (session sidecar `web.json`, CONTRACT.md 12) | shared by every page and kept across reloads |
| Reverted hunks, latest restore | **server** (`WsIndex.reverted`, `.restore`) | |
| Runner recent runs | **server** (`GET /api/runs`) | |
| Toasts, open popovers, modals | DOM | unchanged |

## 11. Test hooks

* `zz-test-hooks.js` is never shipped. In the dev mock mode it is loaded by `index-mock.html` (as `build.mjs --test` did). For the real
  page, A3 provides `internal/web/uidev/hooks-real.js`, which `scripts/web-parity.mjs` injects through the DevTools protocol
  (`Page.addScriptToEvaluateOnNewDocument` before load; DevTools evaluation is not subject to the page's CSP, so the shipped CSP stays
  strict), exposing `window.__SL = SL` and `SL.test.counts()` / `idle()` / `settle()` (the instrumentation part
  of zz-test-hooks.js). The time-warp hooks (`manual`, `step`, `run`, `flood`) are not used against the real page: the real page runs on
  real time, and determinism comes from the fixture backend.
* `window.SL` exists in the shipped page (classic scripts share it, A3's `00-namespace.js`); the mock's `t-shipped.mjs` assertion that
  `SL` is undefined is replaced by: no `window.__SL`, no `SL.test`.

## 12. Security and accessibility in page code

* Every dynamic string goes through `esc()` or `textContent`; new code never assigns server strings to `innerHTML` unescaped. The only
  HTML that is not escaped is markup the page builds itself (`local` cards from its own tables; the server never sends HTML).
* No `eval`, `new Function`, inline handlers, `javascript:` URLs, `window.open`, `postMessage`, WebSocket, XHR, `import()`; `fetch`
  only to `/api/`; `EventSource` only to `/api/stream` (A3's asset test).
* Paths from the server (file names, cwd) are text; URLs are never built from server strings except `/api/...?path=` with
  `encodeURIComponent`.
* Accessibility is the mock's (roles, `aria-*`, focus order, `#announcer`); additions: the conn chip's state change is announced
  (`aria-live` on `#announcer`), the locked and disconnected states (D-13) are reachable by keyboard.

## 13. Patches to kept modules, file by file

**30-model.js (C1)**: VOCAB.md section 7 in full; export `addAgent(m, r)` (the per-agent part of `newModel`); `calc.agent` uses
`a.cost`/`a.saved` when numbers; `calc.warmLeft` uses `m.ttl || 25`; `newModel` initialises the new fields; `S.plan` is read by views
as before (C1 makes `Session.plan` a getter: `this.wm && this.wm.planText || []`, and `newModel` sizes `m.plan` lazily from
`planText`).

**50-sessions.js (C1)**: keep `Session` methods `add` (keep an existing `seq`), `stepWorld`, `afterWorld` (without the endgame
director), `advanceWorld`, `unseen`, `gap`, `stepView`, `advanceView`, `rebuild`, `collapse`, `goLive`, `seek`, `isLive`,
`openQuestion`, `state` (with `m.turn` and the server's `meta.running` for `run`), `touch`, `setMeta`; drop `initRun`'s scripts (a
session is built by live.js); registry: `init` (from `hello`), `activate` (unchanged), `create`/`close`/`rename` (API), `needs`
(unchanged), `recorded` (cache), `prune`/`pruneCandidates`/`parseAge`/`recordedMb` (client arithmetic over the cache, unchanged code),
`nowTod` (real clock), `PLANS` removed.

**80-ui-shell.js (C1)**: warm ring and govtxt (section 8.1); conn chip (8.2); footer `footModel` unchanged; `(sample prices)` titles:
D-07.

**81-ui-hero.js (C1)**: none beyond `D.layers` being a getter (section 6).

**83-ui-cockpit.js (C1)**: governor gauges (8.4).

**84-ui-chat.js (C1)**: a row whose entry has a `mid` and is not `done` stays in `L.stream` after its typed text catches up, and the
frame loop reads the entry's current `text` (`r.e.text`) instead of the `data-full` attribute; `tod(S.meta.t0, e.t)` becomes
`SL.u.todAt(S, e)` (C1 adds the helper to 84 itself, not to 00-util.js: `e.at ? hh:mm:ss of e.at : tod(S.meta.t0, e.t)`); ctrl+c
twice: D-14.

**85-ui-approvals.js (C1)**: `answer()` keeps its rule and calls `SL.act.answerQuestion`; on a `409 too_soon` from the server it resets
`shown[qid]`; the header word for `kind` `trust`/`mcp`: D-09.

**87-ui-sheets.js (C1)**: keep `sheets.goal` (evidence rows: tasks merged + the judge's `left` items (unchecked) or its reason when met,
replacing the literal "go test ./... passes on the merged result" line, D-07), `sheets.mode`, `sheets.context` (layers from section 6),
`sheets.help`, `sheets.history`, `dialogs.*`, `ui.diffHtml` (unused by v3 views; deleted with the sheets below); delete the
unreachable `sheets.model`, `effort`, `budget`, `permissions`, `trust`, `rewind`, `diff`, `login`, `mcp`, `skills`, `team` (no v3 path
opens them: 80-ui-shell, 93-palette and 98-ui-settings route to Settings pages and the Workspace instead; deleting them changes nothing
visible).

**88-ui-drawer.js (C1)**: `FILES_OF` → `ui.ws.filesOf` (8.7).

**90-views-a.js (C1)**: Cache view (8.8); compact dialog estimate: `D.g5.mgr` × the manager's input price from `D.model(S.meta.model)`
when known, else the sentence without a number (D-07).

**93-palette.js (C1)**: section 8.18.

**91, 92, 98, 98b (C3)**: sections 8.12 to 8.17.

**96b, 97 (C2)**: section 8.11.

**99-app.js (C1)**: section 3.
