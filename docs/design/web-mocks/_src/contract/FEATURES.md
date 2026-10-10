# FEATURES: the master inventory of the v3 UI

The acceptance checklist for "the same thing, but real". Every view, control, key, palette entry, Settings page and control, Tools
card, dialog, sheet, popover, drawer tab and toast-producing action of the mock (`_src/v3/src/js`) has a row. A row is done when the
real page does what the "Mock behaviour" column says, from the backend source named, and the parity screenshot of its screen matches
(TEST-PLAN.md). Nothing may be missing; where the real harness cannot do what the mock shows, the row says **DECISION NEEDED** and
section 12 lists the options.

**Status legend.** `REAL-NOW`: exists in the harness and is callable as is. `EXTRACT`: exists but is trapped in package main or
unexported; the owner moves it (OWNERSHIP.md 6). `DERIVE`: computable from existing data (events, state, store). `NEW-HARNESS`: needs
new Go code in a core package (what, in the row). `UI-LOCAL`: browser-only state; where it persists is said. `PAGE`: pure page logic
over data the page already has (no backend). `NOT-PRODUCED`: the mock's simulation showed it; the real system has no source; the
row says what the page shows instead.

**Owner phases.** A2 server, A3 packaging, B1 host/approvals, B2 translator, B3 workspace, B4 settings/tools, C1 core/chat/cockpit,
C2 workspace, C3 settings/tools/runner/sessions pages. "B1+C1" means both: the backend part and the page part.

File:line references are to this tree at 15b4c43. Reports cited: runtime.md, events.md, workspace.md, settings.md, infra.md
(`_src/reports`). Report corrections found while checking: runtime.md cites `chat_link.go`, `chat_dialog.go`, `app/chat.go` without
their directory (they are under `internal/tui/app/`); settings.md says `harden.Held` "holds keys" (it returns the held variable
**names**, `internal/harden/harden.go:222`; values come from `harden.Secret`); settings.md places `TestsAllow` at `presets.go:7`
(it is `presets.go:6`); events.md and runtime.md are otherwise accurate where this contract relies on them.

Contents: 1 Shell (HUD, strip, rails, footer, banners) · 2 Keyboard · 3 Cockpit · 4 Radio rail and approvals · 5 Drawer · 6 Cache,
Mail, Board, Replay · 7 Sessions · 8 Workspace · 9 Settings (11 pages) · 10 Tools, Doctor, Schedule, Runner, Kit · 11 Overlays,
palette and toasts · 12 DECISION NEEDED

---

## 1. Shell

### 1.1 HUD (`80-ui-shell.js` `mount`, `index.html` `#hud`)

| # | Element | Mock behaviour | Backend source | Status | Owner |
|---|---|---|---|---|---|
| 1.1.1 | Brand (`#brand`) | click: Cockpit view | none | PAGE | C1 |
| 1.1.2 | Goal button label `Goal`/`Session`, state `active/paused/met ✓/cleared` | from `meta.goalText` and `m.goal.state` | `goal` events (VOCAB 5.24): host goal loop over `goal.State` (`internal/goal/goal.go:30`), history `goal.state` (`internal/session/goal.go:162`) | DERIVE | B1+B2 |
| 1.1.3 | Goal text (`#goalT`) | objective or "no goal: a chat with the manager (type /goal TEXT to set one)" | `meta.goalText` (B1 patches on set/clear) | DERIVE | B1 |
| 1.1.4 | Elapsed `mm:ss` + `· live +Ns` | view clock; gap between world and view | session time (VOCAB 3) | PAGE | C1 |
| 1.1.5 | Goal button click | opens Goal sheet (11.2.1) | | PAGE | C1 |
| 1.1.6 | Budget gauge, `$cost of $budget` | `calc.totals(m).cost` / `meta.budget`; title "(sample prices)" | `use.cost` per agent = `state.Agent.CostUSD` (`internal/tui/state/types.go:127`); budget from `Session.Budget()` (`internal/session/session.go:1178`) | DERIVE | B2+C1 (title: D-07) |
| 1.1.7 | Cache-hit ring, `%`, title read/prompt | `calc.totals` hit = read/(read+uncached) | `use.rd/un` from `state.Agent.Tokens` (types.go:76) | DERIVE | B2 |
| 1.1.8 | Hit chip (Quiet mode) | same number, compact | as 1.1.7 | DERIVE | B2 |
| 1.1.9 | Prefix warm ring: 25 ticks, countdown `m:ss`, `low` under 8 s, `cold`, note text, pulse on a request (red on a 0% request) | `calc.warmLeft` with a 25 s life | `warm {t: TTLEntry.Last, ttl}` (types_swarm.go:397); `req` for the pulse | DERIVE | B2+C1 (ring life = `ttl`, VOCAB 7.3) |
| 1.1.10 | Team box: `manager + N workers` / `single agent`, `N active of M workers`, title capacity/started/active | roster length; `calc.active/started` | roster (VOCAB 8.1), `state` events | DERIVE | B1+B2 |
| 1.1.11 | Governor text: mode chip (`⚠` for bypass/yolo), `rpm N · 429s 0 · retries 0` | rpm from `gov`; 429s and retries constant 0 | `gov {rpm, r429, retries}` from `state.Governor` (types_swarm.go:235) | DERIVE | B2+C1 (constants become real) |

### 1.2 Session strip (`80-ui-shell.js` `stripHtml`)

| # | Element | Mock behaviour | Backend source | Status | Owner |
|---|---|---|---|---|---|
| 1.2.1 | Tab per session: glyph `● ? ◌ ✓ ⏸` (`⟳` headless running), name, cost, `⚠` for bypass/yolo, `? N` badge, title | `S.state()`, `calc.totals(S.wm).cost`, needs | tabs (`GET /api/sessions`, `tab` frames), every tab's events in the one stream | DERIVE | B1+C1 |
| 1.2.2 | Click a tab | `switchSession` (rebuild the view model from the log) | none (all tabs' logs are in the page) | PAGE | C1 |
| 1.2.3 | Double-click a tab, F2 | Rename dialog (11.1.3) | `PATCH /api/sessions/{id}` | NEW-HARNESS (sidecar name: `session.UpdateMeta`, B4) | B1+B4+C1 |
| 1.2.4 | Middle-click, `×`, Delete key | Close confirm (11.1.6); refuses the last session; warns when a question is open | `DELETE /api/sessions/{id}`: `Session.Close` (`session.go:1199`) + bridge refusal | NEW-HARNESS (in-process close of one of several hosted sessions; B1) | B1+C1 |
| 1.2.5 | Arrow keys, Home, End, Enter/Space on tabs | move focus, activate | none | PAGE | C1 |
| 1.2.6 | `+ New` | New session dialog (11.1.1) | | PAGE | C1 |
| 1.2.7 | `↺ Resume` | Resume dialog (11.1.2) | | PAGE | C1 |
| 1.2.8 | `⋯` session menu | popover: Rename…, Start the team again…, Stop the run…, Close this session… (11.3.2) | | PAGE | C1 |
| 1.2.9 | `Needs you N` | inbox popover (4.3) | open questions of every tab | DERIVE | B1+C1 |
| 1.2.10 | `all N $x of $y` budget button | popover: spend of every session with gauges; "Sample prices; the sum is an estimate." | per-tab `use.cost`, `meta.budget` | DERIVE | C1 (text: D-07) |
| 1.2.11 | Connection chip `● 127.0.0.1:6969 · loopback · token ✓` | static | `hello.server.addr`; stream state | DERIVE | A2+C1 (other states: D-13) |
| 1.2.12 | Strip cost refresh each second | `stripCosts` interval | model | PAGE | C1 |

### 1.3 Left rail and phone bar (`96-ui-nav.js`)

| # | Element | Mock behaviour | Backend source | Status | Owner |
|---|---|---|---|---|---|
| 1.3.1 | Items Cockpit, Radio, Files, Changes, Checkpoints, Cache, Mail, Board, Replay, Sessions, Tools, Settings; group heads; `aria-current` | go to the view / Workspace tab; Radio opens the rail and focuses the composer | none | PAGE | C3 (unchanged file) |
| 1.3.2 | Badges: Radio `? N`, Changes count, Checkpoints count, Cache `⚠ N`/`⚠`, Mail count, Sessions count | from the model and `ui.ws.counts` | model; Workspace index (8) | DERIVE | C2 (counts) |
| 1.3.3 | Widen/Narrow toggle | wide labels and chord hints; saved | `localStorage['sleipnir.web.dock']` | UI-LOCAL | C3 |
| 1.3.4 | Arrow/Home/End in the rail | focus moves | none | PAGE | C3 |
| 1.3.5 | Chord hint `g then …` | shown 1.5 s after `g` | none | PAGE | C3 |
| 1.3.6 | Phone bar Cockpit, Radio (ping when a question waits), Changes, Sessions, More | switch pane; More opens the Views sheet (11.2.7) | none | PAGE | C3 |
| 1.3.7 | Radio rail collapse `›`, `RADIO` strip with `? N` badge, resize handle (drag, ←/→, Home/End, Enter collapses, double-click resets 344 px) | saved | `localStorage['sleipnir.web.dock']` | UI-LOCAL | C3 |
| 1.3.8 | Question banner above views when the rail is collapsed (`#qBanner` "agent asks: cmd · answer ▸") | opens the rail | open question | DERIVE | C1 |

### 1.4 Footer and banners

| # | Element | Mock behaviour | Backend source | Status | Owner |
|---|---|---|---|---|---|
| 1.4.1 | Status line: dot colour, word (goal met / agent waits for your answer / manager waiting for the team / replay / manager doing), `mm:ss`, `↑prompt ↓out` of the manager, cost, `esc to interrupt` | from the model | `state`, `use`, `goal`, `ask` | DERIVE | B2 |
| 1.4.2 | `↺ run it again` after goal met | `restartTeam({force})` | `POST /api/sessions/{id}/restart {kind:"swarm", fresh:true}` | EXTRACT (`restartArgs`, `cmd/sleipnir/restart.go:52`, in-process) | B1+C1 |
| 1.4.3 | `MOCK · sample data` chip | static | none | **DECISION NEEDED D-07** | C1 |
| 1.4.4 | Footer model `model · sid`; rail head model | meta | `meta.model`, `tab.sid` | DERIVE | B1 |
| 1.4.5 | Replay banner: `Replay mm:ss of mm:ss · playing Nx/paused · go live` | `S.replay` | page clocks | PAGE | C1 |

## 2. Keyboard (`94-keys.js`, `84-ui-chat.js`, `85-ui-approvals.js`, `96-ui-nav.js`, view-local handlers)

| # | Key | Mock behaviour | Backend source | Status | Owner |
|---|---|---|---|---|---|
| 2.1 | `1` `2` `3` (outside fields, or in the empty composer) | answer the front question when armed (quiet 0.8 s) | `POST /api/questions/{qid}/answer` | DERIVE (bridge, VOCAB 9) | B1+C1 |
| 2.2 | Esc precedence: release hold → close modal → close inbox → (popovers own Esc) → close drawer → (slash menu) → answer 3 when armed → leave field → leave replay → interrupt the turn | as listed | interrupt: `POST .../interrupt` | DERIVE | B1+C1 |
| 2.3 | `alt+1..9` | nth session tab | none | PAGE | C1 |
| 2.4 | `ctrl+k` / `cmd+k` | palette | none | PAGE | C1 |
| 2.5 | `ctrl+t` / `alt+t` | Cache view | none | PAGE | C1 |
| 2.6 | `ctrl+g` / `alt+g` | Cockpit | none | PAGE | C1 |
| 2.7 | `ctrl+o` | expand/collapse tool output in the transcript (toast) | none | PAGE | C1 |
| 2.8 | `g` then `c r f h k a m b p s t e` | Cockpit, Radio, Files, Changes, Checkpoints, Cache, Mail, Board, Replay, Sessions, Tools, Settings | none | PAGE | C3 |
| 2.9 | `/` | composer with `/` (or palette on a phone / collapsed rail) | none | PAGE | C1 |
| 2.10 | `?` | Help sheet | none | PAGE | C1 |
| 2.11 | `o c m b r s , .` | Cockpit, Cache, Mail, Board, Replay, Sessions, Settings, Tools | none | PAGE | C1 |
| 2.12 | Space | over the chat or while held: pin/unpin the hold; elsewhere: pause/resume the view (replay) | none | PAGE | C1 |
| 2.13 | ← → (shift: a minute) | seek the view 10 s | none (the page's log) | PAGE | C1 |
| 2.14 | Home / End | replay from 0 / go live | none | PAGE | C1 |
| 2.15 | `+` `=` / `-` | replay speed up to 4x / down | none | PAGE | C1 |
| 2.16 | ↑ ↓ (outside lists) | select the next agent (cross-highlight); Enter opens its drawer | none | PAGE | C1 |
| 2.17 | Composer Enter | send (`\` at line end: newline) | `POST .../messages` | DERIVE (host queue: B1 reimplements `app/chat.go:696-731`) | B1+C1 |
| 2.18 | Composer alt+Enter, ctrl+j | newline | none | PAGE | C1 |
| 2.19 | Composer shift+Tab | cycle mode default → accept-edits → plan (never bypass/yolo) | `POST .../mode` | REAL-NOW (`Perm.SetMode`, `internal/perm/engine.go:287`) | B1+C1 |
| 2.20 | Composer ↑ ↓ | history of sent lines | snapshot `hist` + local | DERIVE | B1+C1 |
| 2.21 | Composer ctrl+r | History sheet | as 2.20 | DERIVE | C1 |
| 2.22 | Composer ctrl+c | discard the line; twice at an empty prompt "mock: /exit would end the session here" | none | **DECISION NEEDED D-14** | C1 |
| 2.23 | Composer paste of 5+ lines | `[pasted text #N +L lines]` chip in the input | none; the page sends the expanded text and the chip as `display` | PAGE | C1 |
| 2.24 | Slash menu ↑ ↓ Tab Enter Esc | choose and run/insert | `D.slash` per tab | DERIVE | B1+C1 |
| 2.25 | `@` completion ↑ ↓ Tab Enter | insert a path | `GET .../complete` / Workspace index | DERIVE | B3+C1 |
| 2.26 | Mode menu keys ↑ ↓ Home End Esc Tab | menu navigation | none | PAGE | C1 |
| 2.27 | Mode button ↑/↓ | opens the menu | none | PAGE | C1 |
| 2.28 | Stalls arrow keys | move between stalls (4 per row) | none | PAGE | C1 |
| 2.29 | Cache view ↑ ↓ | choose the agent | none | PAGE | C1 |
| 2.30 | Replay tape ← → | seek 10 s | none | PAGE | C1 |
| 2.31 | Workspace list ↑ ↓ | move between rows | none | PAGE | C2 |
| 2.32 | Settings nav ↑ ↓; Enter in budget/rule/try/verify/confirm fields | navigate / submit | as each page | PAGE | C3 |
| 2.33 | Runner form Enter | run | `POST /api/runs` | DERIVE | B4+C3 |
| 2.34 | Modal Tab trap; Esc or `esc · close` closes | | none | PAGE | C1 |
| 2.35 | Talk transcript Space | pin the hold | none | PAGE | C1 |
| 2.36 | Question "tell instead" field Enter / Esc | send / back | `POST .../answer {choice:3, note}` | DERIVE | B1+C1 |

## 3. Cockpit (`83-ui-cockpit.js`, `81-ui-hero.js`, `82-ui-board.js`)

| # | Element | Mock behaviour | Backend source | Status | Owner |
|---|---|---|---|---|---|
| 3.1 | Horse: eight legs = first eight workers by start order; ninth shares leg 1 with `+1`; gait per state (think/tool/edit/wait/ask/idle/done/stuck/free); halo on ask (amber pulse), stuck (red); body bob, tail, head follow the governor | roster legs, `m.ag[id].state` | roster (`k`, `leg`, VOCAB 8.1) from `agent.spawn` order; `state` events (VOCAB 8.3) | DERIVE | B2 |
| 3.2 | Leg labels: id, `· free`, glyph + word, `+N` | as 3.1 | as 3.1 | DERIVE | B2 |
| 3.3 | Leg/label click or Enter | agent drawer | none | PAGE | C1 |
| 3.4 | Prefix bar G0..G5 with token sizes, `one prefix · G0–G2 Nk tok`, `shared · warm m:ss`, flash on a cache break, cold veil (Cache details: Full only) | `D.layers`, warm clock, `m.flash` | `layers` events (VOCAB 6.5: `state.Agent.Stack` split, `cacheview.go:187` exported as `state.LayerSplit`), `warm`, `break` | DERIVE (G6 folded into G5: **D-16**) | B2+C1 |
| 3.5 | Manager card: id, role, glyph+word, doing (typed while streaming), `goal`/`single agent` chip, note, tokens, cost, hit (Full), sparkline of request ratios | model | `state`, `use`, `req`, `stream`/`more` (manager prose is `say`; the card types the manager's stream when the mock did) | DERIVE | B2 |
| 3.6 | Worker stalls (one per roster worker): as 3.5 plus task chip (merged style), leg and scope `leg N · read-only` or globs, cache-break mark for 8 s (90 s in Quiet), mini 60 s timeline, `data-state` | model | roster `scope` (task file globs, `state.Task.Files`), `task`, `break` | DERIVE | B2 |
| 3.7 | Ghost stalls `leg N free` with `/swarm N` hint (fewer than 8 workers) | static | roster size | DERIVE | B1 |
| 3.8 | Solo panel (single agent): text and the last six tool calls | `m.chan.mgr` tools | `tool` events | DERIVE | B2 |
| 3.9 | Stall/card click | drawer | none | PAGE | C1 |
| 3.10 | Swarm gantt: rows mgr + workers, last 60 s, segments think/tool/edit/wait/ask/done/idle/stuck, marks req/mail/compact/break/merge/ask/refuse, axis `now -10s …`, open segments grow | `A.segs`, `m.marks` | every `state` event (never coalesced), `req`, `mail`, `compact`, `break`, `merge`, `ask`, `refuse` | DERIVE | B2 |
| 3.11 | Gantt row click | drawer | none | PAGE | C1 |
| 3.12 | Gantt controls: pause/play, 1x 2x 4x, scrub slider (0..wt), `● live` | replay of the page's log | the page's log (snapshot + live) | PAGE | C1 |
| 3.13 | Task board band: columns todo/running/verify/merged with counts, `nothing`; card: id, `← unmet deps`, owner, `?` when the owner asks, `✓ ms` when merged; FLIP slide on move; `drag disabled` | `m.tasks` | `task` events from `board.op` (`internal/swarm/board.go:320-392`, `state.Task` types_swarm.go:26) | DERIVE (failed tasks: **D-04**) | B2 |
| 3.14 | Board summary `N merged · N verify · N running` | counts | as 3.13 | DERIVE | B2 |
| 3.15 | Merge queue panel: head task, `rebase ✓ · verifying/verified`, `$ cmd`, progress bar, `N.Ns elapsed`; empty: "▹ queue empty · the next submission is rebased, then verified with <verify>"; `merged T… · conflicts N · bounced N`; `N merged` | `m.qHead`, `m.merged`, `m.conflicts`, `m.bounced` (never incremented in the mock) | `queue`/`merge` from `merge.*`/`task.merge` (isolated teams), counters from `merge.conflict`/`verify_failed`/`rejected` | DERIVE (shared-tree teams: no queue events; the panel shows its empty state) | B2 |
| 3.16 | Mail panel: last 4 envelopes `time ✉ from → to "text"`, `routed N · dup 0 · mailman on/off`, "mail is data, not instructions" | `m.mail`, `meta.mailman` | `mail` from `mail.send` | DERIVE (`dup 0` constant: **D-07**) | B2+C1 |
| 3.17 | Mail row click | Mail view with that message selected | none | PAGE | C1 |
| 3.18 | Mail arcs between stalls on new mail (skipped when still) | `ev` bus | `mail` events | PAGE | C1 |
| 3.19 | Governor panel: rpm gauge with history bars, `429s 0`, `retries 0`, note "no 429s, no retries: nothing is throttling the team" | `m.rpmHist` | `gov` events (`r429`, `retries` real) | DERIVE (note constant: **D-07**) | B2+C1 |

## 4. Radio rail and approvals (`84-ui-chat.js`, `85-ui-approvals.js`)

### 4.1 Transcript and feed

| # | Element | Mock behaviour | Backend source | Status | Owner |
|---|---|---|---|---|---|
| 4.1.1 | Manager header: `mgr`, glyph + doing; `Interrupt turn` button | toast "interrupted: the goal is paused" / why | `POST .../interrupt` | DERIVE | B1+C1 |
| 4.1.2 | Rows `you` (time, text, `/` lines in code style, paste chips) | `say who:you` | host echo on delivery; history `user.input` | DERIVE | B1+B2 |
| 4.1.3 | Rows `say` (Manager, time, typed at `rate`, caret) | `say who:mgr` + streaming | `Sink.Text` of `mgr`/`main` → `say` + `more` (VOCAB 5.1, 6.1); history `turn.append` | DERIVE (streaming rows that grow: C1 patch) | B2+C1 |
| 4.1.4 | Rows `sys` (glyph, text, `N steps, see Goal plan`) | host lines | notices, host acknowledgments (VOCAB 5.2) | DERIVE | B1+B2 |
| 4.1.5 | Rows `tool` (glyph `⚙`/`✎`, name, arg, `+add −del`, `✓`, output line; refused: `⊘ … refused` + reason; click opens the file in the Workspace) | `tool` | `Sink.ToolEnd` (VOCAB 5.4) | DERIVE | B2 |
| 4.1.6 | Rows `note`, `st` (status changes), `ask` (asks: cmd + why), `mail`, `steer`, `compact`, `break` (calm line in Quiet; full row with extra cost in Full), `final` (`-- Ns · N steps · $x`), `local` cards, `scouts` | as kinds | VOCAB 5; `scouts` NOT-PRODUCED (the scouts' results arrive as their own tool rows and mail) | DERIVE | B2 |
| 4.1.7 | Team activity feed (read-only, collapsible): worker tool calls, mail, asks, notes, breaks, compactions, digest rows; click opens the agent's drawer | `isFeed` entries | as 4.1.6 | DERIVE | B2+C1 |
| 4.1.8 | Row cap 220 (feed 90), fold marker "N earlier rows folded (the log keeps them)", empty texts | DOM budget | none | PAGE | C1 |
| 4.1.9 | Stick to the edge; `N new ↓` pill when scrolled up | | none | PAGE | C1 |
| 4.1.10 | Hold: pointer over the transcript or focus inside or pinned → the view clock stops; chip `◔ holding/pinned · N new`, `▸▸ catching up · Ns`; click/Space pins | governor | none (VOCAB 13) | PAGE | C1 |
| 4.1.11 | Digest row after a long hold: `◆ while held · N min: merged, submitted, tool calls, mail, breaks, compactions, questions · N events`, expandable list (60 rows) | `collapse()` | none (the page's log) | PAGE | C1 |
| 4.1.12 | `/verbose off`: the feed hides tool calls | `S.ui.verbose` | none | UI-LOCAL (per tab, memory) | C1 |
| 4.1.13 | Resumed history at the top of the chat | short scripted recap | history translation (VOCAB 12) | DERIVE (t=0 placement: **D-15**) | B2 |

### 4.2 Plan box and composer

| # | Element | Mock behaviour | Backend source | Status | Owner |
|---|---|---|---|---|---|
| 4.2.1 | Plan box: `Goal plan n/N`, progress segments, toggle; list with glyphs `✓ ▸ ✎ ? ◌` and words; judge verdict line | `S.plan`, `m.plan`, `m.verdict` | `plan` (full form, from the `plan` tool of the main agent, `internal/plan/plan.go`), `verdict` (host judge, `goal.judge`) | DERIVE + NEW-HARNESS (`goal.judge` event, B1) | B1+B2 |
| 4.2.2 | Composer: `❯`, textarea autosize, placeholder, queued lines above it, mode button, hint, send `⏎` | | `POST .../messages`; `meta.queued` | DERIVE | B1+C1 |
| 4.2.3 | Send while a turn runs → queued line shown, delivered after the turn | `S.ui.queued`, `pump` | host queue (VOCAB, CONTRACT 8) | DERIVE | B1 |
| 4.2.4 | Send in replay → toast "go live to talk: a replay never changes the session" | | none | PAGE | C1 |
| 4.2.5 | Send while held → the hold is released | | none | PAGE | C1 |
| 4.2.6 | Mode button → mode menu (11.3.1) | | | PAGE | C1 |
| 4.2.7 | `/` menu (14 matches), `@` menu (8 paths) | | `D.slash`; Workspace index / `complete` | DERIVE | B1+B3+C1 |

### 4.3 Approval question and inbox

| # | Element | Mock behaviour | Backend source | Status | Owner |
|---|---|---|---|---|---|
| 4.3.1 | Question strip: `agent · role · task`, "wants to run a command", `waiting for you: Ns`, `+N waiting: asked one at a time`, `cwd $ cmd`, scope, why | from `m.q` | `ask` via the bridge (`perm.Prompter`, `internal/perm/perm.go:266`; fields VOCAB 9; `Request.Why`/`RememberRules` NEW-HARNESS B3) | DERIVE + NEW-HARNESS | B1+B3 (header for non-command kinds: **D-09**) |
| 4.3.2 | Options 1 Yes / 2 Yes, and don't ask again for <what> this session / 3 No, and tell Sleipnir what to do instead (esc); inert until the keyboard is quiet 0.8 s; meter; texts "your typing goes to the prompt until you pause", "ready: press 1, 2 or 3 (esc is 3)", "replay: go live to answer" | quiet rule | server floor 350 ms (`defaultAnswerAfter`, `internal/tui/app/chat_dialog.go:49`) | DERIVE (client 0.8 s kept: **D-17**) | B1+C1 |
| 4.3.3 | Click/key 1 or 2 | toast "answered N: agent runs the command"; choice 2 adds a rule with origin "don't ask again" | Decision table (`chat_dialog.go:68,123`); `Engine.remember` adds the rule (`internal/perm/ask.go:206`) | REAL-NOW (via bridge) | B1 |
| 4.3.4 | 3 / Esc → "tell instead" field + Send/Back; sends the note | `say you` + `steer` rows | Decision `{Allow:false, Reason: perm.DeclinedWith(note)}` (NEW-HARNESS B3); host emits `say you` and `steer {quiet}` | NEW-HARNESS | B1+B3 |
| 4.3.5 | Answered strip `✓ N <choice text> · agent · task · cmd` for 8 s | | `answer` event | DERIVE | B2 |
| 4.3.6 | Inbox popover: every tab's open question with the same options and quiet rule, `Open session`, `+N waiting`, empty text, footer about headless runs | `SL.sessions.needs()` | every tab's `ask`/`answer` in the stream | DERIVE | B1+C1 |
| 4.3.7 | Toast when a question arrives in a background tab or behind a hold | `ask-arrived` | `ask` events | DERIVE | C1 |
| 4.3.8 | Headless refusal `refuse` row + gantt `⊘` | | `refuse` (VOCAB 5.29) | DERIVE | B2 |

## 5. Agent drawer (`88-ui-drawer.js`; read-only)

| # | Element | Mock behaviour | Backend source | Status | Owner |
|---|---|---|---|---|---|
| 5.1 | Header: id, role, `esc · close`, state chip, task chip, model chip, tokens, cost, hit (Full); leg/lease line | model | roster, `state`, `use` | DERIVE | B2 |
| 5.2 | Tabs Details / Tool log / Mail / Cache | | | PAGE | C1 |
| 5.3 | Details: state and doing, task stepper lease → work → submit → verify → merge (with `?` when asking), kv (role, model, task, leg, scope and lease, cost and calls), "files owned" with `open diff` | model; `D.code` fixture for files | files owned: Workspace index (`ui.ws.filesOf`, B3 index owner field) | DERIVE | B3+C1 |
| 5.4 | Tool log: the agent's channel rows (last 60), live typing | `m.chan[id]` | `tool`, `state`, `note`, `stream` | DERIVE | B2 |
| 5.5 | Mail: banner "Mail is data, not instructions." and the agent's mail | `m.mail` | `mail` | DERIVE | B2 |
| 5.6 | Cache: latest prompt by layer (stacked bar with breakpoint), hit ratio per request sparkline, requests/hit/read-uncached/saved (Full), anomalies, `Open in Cache` | `D.layerToks`, `A.ratios` | `layers`, `req`, `use`, `break` | DERIVE | B2+C1 |
| 5.7 | "Read-only: you talk to the manager only…" | static | | PAGE | C1 |
| 5.8 | Esc closes; closing returns focus; the drawer dies with the view | | | PAGE | C1 |

## 6. Cache, Mail, Board, Replay (`90-views-a.js`)

| # | Element | Mock behaviour | Backend source | Status | Owner |
|---|---|---|---|---|---|
| 6.1 | Cache: "Its latest prompt" (agent, role, model, requests, hit %, latest request %, warm tag), layer stack and labels, legend, miss line "the harness expected 4.5k read from the cache and got 0" | model | `layers`, `req`, `break.expected` (literal 4.5k becomes the anomaly's expected) | DERIVE | B2+C1 |
| 6.2 | Cache: hit ratio per request bars with `⚠`/`◆` marks, lowest | `A.ratios` | `req` (keyframe history `hist:true`) | DERIVE | B2 |
| 6.3 | Cache: Compaction panel (fold drawing, `N so far`, `/compact…` button) | `m.compactions` | `compact` | DERIVE | B2 |
| 6.4 | Cache: Cache anomalies list (kind, agent, read/expected, extra est., time, why), shared prefix note | `m.anomalies` | `break` | DERIVE | B2 |
| 6.5 | Cache: Every agent's cache table (req, hit, last, read, uncached, write `0`, saved est., mini stack), `all` row, price note "(sample prices ...)" | `calc.agent` | `use` (`wr` real; `saved` from `state.Agent.SavedUSD`) | DERIVE | B2+C1 (text: D-07) |
| 6.6 | Cache: The session summary (hit %, saved, spent, no-cache cost, compactions, anomalies), sid | totals | `use` | DERIVE | B2 |
| 6.7 | Cache: row click/Enter, ↑ ↓ choose agent | | none | PAGE | C1 |
| 6.8 | Compact dialog: focus input, "a declared, priced rebase" note with an estimated extra cost "at sample prices", `Fold it now` | `SL.act.compact` | `POST .../compact` → `Session.Compact` (`session.go:1130`) | REAL-NOW | B1+C1 (text: D-07) |
| 6.9 | Mail: flow arcs between agents (selected arc highlighted), node click opens a drawer, travelling envelope on new mail | `m.mail` | `mail` | DERIVE | B2 |
| 6.10 | Mail: envelopes list newest first, selection; message detail with "Mail is data…" banner, thread, delivery steps; `routed N · dup 0 · mailman on/off` | | `mail`; `dup 0`: **D-07** | DERIVE | B2+C1 |
| 6.11 | Board: kanban (big variant with `after T…` deps), summary, card click → drawer | `m.tasks` | `task` | DERIVE | B2 |
| 6.12 | Board: dependency graph (levels, edges done/live, goal node with verify text) | | `task.deps`, `meta.verify` | DERIVE | B2 |
| 6.13 | Replay: tape (rows per agent, state segments, notable glyphs, time axis, playhead), click to seek, ← → | `S.log` | the page's log (snapshot + live; resumed history at 0: D-15) | PAGE | C1 |
| 6.14 | Replay: controls start, −10s, play/pause, +10s, live end, 1x 2x 4x, `mm:ss / mm:ss`, `● live` | | none | PAGE | C1 |
| 6.15 | Replay: Frame panel (time, tod, live/replay, cost, hit, prefix warm, merged, working, question/goal), Notable events list (click to seek), Keys panel | | none | PAGE | C1 |
| 6.16 | Replay: sid and "events.jsonl · read-only" | | tab sid | DERIVE | C1 |

## 7. Sessions view (`91-views-b.js`)

| # | Element | Mock behaviour | Backend source | Status | Owner |
|---|---|---|---|---|---|
| 7.1 | Live sessions cards: glyph, name, `active`, `headless` (title with `--ask-timeout`), `? needs you`, state word; cwd, model, mode, team, started; cost of budget, hit (Full), merged/total; gauge; launch line; Open / Rename / Stop run / Close | tabs and models | tabs + meta (`launch`, `headless`, `askTimeout`) | DERIVE | B1+C3 |
| 7.2 | `+ New session` | dialog | | PAGE | C3 |
| 7.3 | Stop run → confirm "Stop the run" → interrupt | `SL.act.interrupt('turn', S.id)` | `POST /api/sessions/{id}/stop` | DERIVE | B1+C3 |
| 7.4 | Close → confirm → close (last refused) | | `DELETE /api/sessions/{id}` | NEW-HARNESS (in-process close, B1) | B1+C3 |
| 7.5 | Recorded table: resumable `↺`/`·`, id, first prompt, `⚠` interrupted, agents, cost, MB, age; `↺ Resume`, `Replay` | `SL.sessions.recorded` | `GET /api/recorded` (B4 extracts `summarize`/`printSessions`, `cmd/sleipnir/setup.go:263-381`; `session.Resumable`, `resume.go:91`) | EXTRACT | B4+C3 |
| 7.6 | `↺ --continue` | resume latest | `POST /api/sessions/resume {from:"latest"}` (`ResolveResume`, `resume.go:23`) | REAL-NOW | B1 |
| 7.7 | `↺ Resume` row | resume | `POST /api/sessions/resume {from: sid}` | REAL-NOW (+ in-process host, B1) | B1 |
| 7.8 | `Replay` row | shows the current session's Replay with toast "(mock)" | `GET /api/recorded/{sid}/events` | **DECISION NEEDED D-10** | B4+C3 |
| 7.9 | Prune panel: text, `--older-than`, `--keep` inputs, preview "would delete N sessions (MB): …  nothing is deleted without --yes", `Prune with --yes…` → confirm listing ids → "deleted N sessions, freed MB" + toast | client arithmetic | `POST /api/recorded/prune` (B4 extracts `planPrune`, `cmd/sleipnir/sessions_prune.go:50`; deletion `session.RemoveStoredSession`, `internal/session/prune.go:20`) | EXTRACT | B4+C3 |
| 7.10 | Cross-session inbox panel: rows with `Answer…` → inbox | needs | as 4.3.6 | DERIVE | C3 |
| 7.11 | Counts `N running`, `N · MB on disk`, `N open` | | | DERIVE | C3 |
| 7.12 | Delete selected sessions (PLAN feature) | not in the mock | `POST /api/recorded/delete` | **DECISION NEEDED D-02** | B4 |

## 8. Workspace (`97-ui-workspace.js`, `96b-ws-data.js`; Files / Changes / Checkpoints)

| # | Element | Mock behaviour | Backend source | Status | Owner |
|---|---|---|---|---|---|
| 8.1 | Tabs Files / Changes / Checkpoints (also from the rail, chords, palette) | | none | PAGE | C2 |
| 8.2 | Time-travel scrubber 0..n with ticks (base id, checkpoint ids), `at` label, `● live` | the project at any checkpoint | `GET .../ws/index` checkpoints (`Store.List`, `internal/checkpoint/store.go:798`) + `ContentAt` (NEW-HARNESS B3) | NEW-HARNESS | B3+C2 |
| 8.3 | Summary chips `N files changed`, `+a −d`, isolation word | | index | DERIVE | B3 |
| 8.4 | Files: filter input, `changed only`, tree with dirs (owner dots, change count, collapse), files with ownership stripe, name, deny lock (title rule, origin, why), lease lock (agent, task, glob), ask `?` marker, `✎` writing, `⟲ N` reverted, `+a −d`, owner, status tag A/M/D/R, `✓` reviewed | `SL.ws.rows` | index tree: status, last writer (`FileState.LastWriter` / write journal, NEW-HARNESS B3), lease (`Swarm.Leases`/task scopes + `workspace.Match`, `internal/workspace/scope.go:486`), protected/ask (`Engine.Classify`, NEW-HARNESS B3) | NEW-HARNESS | B3+C2 |
| 8.5 | Changes: group by task / by agent; range since start / only cN / cN to now; `unreviewed` filter; group heads (task title, verification tag, `+/−`); rows with dir/name, `✎ writing`, counts, owner, tag, reviewed toggle `○/✓`; `↺ restored` group | `changeRows` | index change sets per checkpoint (`Store.Changes`, NEW-HARNESS B3) + `ws/diff` counts | NEW-HARNESS | B3+C2 |
| 8.6 | Checkpoints list (newest first): id, time, label, `● newest`, `skipped: nothing to put back` or files/+−/agents/tasks; `Diff` and `Restore` buttons; help text | `I.cps` | index (`Store.List` + `Changes`) | NEW-HARNESS | B3+C2 |
| 8.7 | Checkpoint click → pin time travel there; `Diff` → Changes `cN to now` | | none | PAGE | C2 |
| 8.8 | File header: path, status tag, owner chip, task chip + verification tag, checkpoint chip, `+a −d`; seg Diff / Whole file; Mark reviewed / ✓ Reviewed; Copy path; range note | | index + `ws/file` | DERIVE | B3+C2 |
| 8.9 | Diff body: hunks with header `@@ … @@ section`, `Revert hunk` (or `⟲ reverted by you · nothing is written in this mock` + `Undo`), lines with attribution gutter (who wrote each `+` line, title with task and checkpoint), line numbers, syntax colouring (escaped), live caret while being written; "No difference in this view" + Show the whole file | `SL.ws.hunks/blame` | `ws/diff` (`Store.ContentAt` + line diff; isolated: `gitx` diff), blame from the write journal or `git blame` (NEW-HARNESS B3: `Store.Writes`, `Repo.Blame`) | NEW-HARNESS | B3+C2 (text: D-07) |
| 8.10 | Whole file view with per-line owner gutter (first line of each run named) | | `ws/file` blame runs | NEW-HARNESS | B3+C2 |
| 8.11 | Denied path card: lock, "Denied path", rule/origin/tier/why, "This page does not show it either…" | `rawRow.protected` | `Engine.Classify` (Read) | NEW-HARNESS | B3 |
| 8.12 | "No content for this file is kept in the sample data." / "This file does not exist at this point in time." | | `ws/file exists:false` | DERIVE | C2 (first text: D-07) |
| 8.13 | Being-written preview of a tester's new file (typed) | hard-coded `items_test.go` | `stream {code, file}` from `ToolStart` of `write` (VOCAB 5.22) | DERIVE | B2+C2 |
| 8.14 | Revert hunk dialog: preview of the reversed lines, `Revert the hunk` / Cancel | `SL.act.revertHunk` (records only) | `POST .../ws/revert` (gitx reverse apply, `internal/gitx/apply.go:38`; checkpoint around it) | NEW-HARNESS | B3+C2 |
| 8.15 | Undo a reverted hunk | `unrevertHunk` | `POST .../ws/revert/{rid}/undo` | NEW-HARNESS | B3 |
| 8.16 | Restore dialog: label, time, file count, "The agents are told to read them again", "A safety checkpoint is taken first, so this can be undone"; table file / what happens (put back / removed) / lines / result; `Restore the files` | records only | dry run `Store.Restore(id, {DryRun:true})` (`internal/checkpoint/restore.go:182`); apply with an undo capture (`Store.CaptureUndo`, NEW-HARNESS B3) and the rewind note (`cmd/sleipnir/chat.go:744`) | REAL-NOW (preview) + NEW-HARNESS (undo) | B3+C2 |
| 8.17 | Notes: Time travel "the project as it was after cN… Return to live"; Restored "N files put back… safety checkpoint… (mock: nothing is written)" + `Undo the restore` | | index `restore` | NEW-HARNESS | B3+C2 (text: D-07) |
| 8.18 | Verify and merge strip: `--verify "cmd"`, head line and bar, task chips with state glyph (`?` when asking; click filters Changes by task), `merged a/b · conflicts · bounced · reviewed r/n` | `m.qHead`, tasks, reviewed | `queue`/`merge`/`task` events; reviewed from the sidecar | DERIVE | B2+B3+C2 |
| 8.19 | Reviewed marks | `S.ws.reviewed` (memory) | `PUT .../ws/reviewed` → session sidecar `web.json` (B4 `session.UpdateMeta`) | NEW-HARNESS (sidecar) | B3+B4 |
| 8.20 | Sessions without recorded history: list of files the tool calls touched, "what the agents did to this file" table, "The sample data keeps file contents for the shop and orders-api sessions only…" | `derived` | every real session has a checkpoint store: this path becomes the loading state | DERIVE | C2 (text: D-13) |
| 8.21 | Copy path | clipboard | none | PAGE | C2 |
| 8.22 | Worktrees list, merge queue detail, verify output, accept verified → commit (PLAN feature) | not in the mock | `ws/worktrees`, `ws/queue`, `ws/verify/{task}`, `ws/accept` (`Session.AcceptVerified`, NEW-HARNESS B3) | **DECISION NEEDED D-03** | B3 |

## 9. Settings (`98-ui-settings.js`): eleven pages

Navigation (left list in three groups with the active tab's name), header with title, description and "· <tab name>" / "· shared
by every session"; pages render from caches; focus and caret kept across re-renders.

### 9.1 Models

| # | Control | Mock behaviour | Backend source | Status | Owner |
|---|---|---|---|---|---|
| 9.1.1 | Filter words, `tools`, `reasoning`, `★ favourites`, max input $/M, min context, choose for (role) | client filter | `GET /api/models` (`internal/catalog` from `cmd/sleipnir/models.go:49-308`: `modelFilter.keep`, `fetchModels`, `usableSources`, `planModels`; `gateway.Fetch`, `internal/provider/gateway/catalog.go:222`) | EXTRACT | B4+C3 |
| 9.1.2 | Table: star, model, context, in $/M, out $/M (or "price unknown"), tags, runs (roles using it), `Use for <role>` / `current` | | as 9.1.1 + tab meta | EXTRACT | B4 |
| 9.1.3 | Star/unstar | `favModel` | `POST /api/models/fav` (`toggleFavorite`, `models.go:398` → `config.Save`) | EXTRACT | B4 |
| 9.1.4 | `Use for manager` | toast "a team starts again on X (mock: the scripted run continues)" | `POST .../model` → single agent `SwitchModel` (`internal/session/switch.go:22`), team restart | REAL-NOW / EXTRACT (`restartArgs`) | B1 (text: D-07) |
| 9.1.5 | `Use for <role>` | restart that role | `POST .../model {role}` → restart kind `roles` | EXTRACT | B1 |
| 9.1.6 | Note "N of M models … Prices are sample data; price unknown…" + `Run as CLI models --tools --max-price 5` | | | DERIVE | C3 (text: D-07) |

### 9.2 Roles & effort

| # | Control | Mock behaviour | Backend source | Status | Owner |
|---|---|---|---|---|---|
| 9.2.1 | Table: role, its agents, model select (+ `price unknown`), mailman row (on/off text) | | `ModelsView.roles/roleModels`, `Session.RoleModels()` (`switch.go:121`), meta `mailman` | REAL-NOW | B4 |
| 9.2.2 | Role model select change | toast "role runs on X (its workers restart)" | `POST .../model {ref, role}` | EXTRACT | B1 |
| 9.2.3 | Effort seg default … max; "Current: X" | `setEffort` | `POST .../effort` → `Session.SetEffort` (`internal/session/effort.go:13`), applied level `Session.Effort()` | REAL-NOW | B1 |
| 9.2.4 | `Run as CLI models` | runner | | PAGE | C3 |

### 9.3 Budget

| # | Control | Mock behaviour | Backend source | Status | Owner |
|---|---|---|---|---|---|
| 9.3.1 | Spent ("spent, sample prices"), budget, gauge, `% used` | ticks every 20 frames | `use.cost`, meta | DERIVE | B2 (text: D-07) |
| 9.3.2 | Set (number or off), Off; Enter | `setBudget` (pauses the goal when over) | `POST .../budget` → `Session.SetBudget` (`switch.go:79`; the swarm path NEW-HARNESS B1) | REAL-NOW + NEW-HARNESS (team budget at run time) | B1 |
| 9.3.3 | Cost by agent table + `all` row + saved est. (Full) | | `use` | DERIVE | B2 |

### 9.4 Permissions

| # | Control | Mock behaviour | Backend source | Status | Owner |
|---|---|---|---|---|---|
| 9.4.1 | Mode rows default / accept-edits / plan (current tag); note about shift+tab | `setMode` | `POST .../mode` (`Perm.SetMode`) | REAL-NOW | B1 |
| 9.4.2 | Dangerous modes bypass / yolo → type the name → `Set <mode>` / Cancel | typed confirm | `POST /api/confirm {mode}` + `POST .../mode {confirm}` | REAL-NOW + security gate (A2) | A2+B1 |
| 9.4.3 | The manager card + `See the refused Edit` (opens the manager's drawer) | | `PermissionsView.managerWrites` (swarm manager refusal text) | REAL-NOW | B4 |
| 9.4.4 | Rules in force table: effect tag, rule (+ note), from (origin + file, or `this session`/`don't ask again`), `locked` or `remove` | | `PermissionsView.rules` (`config.RuleOrigins`, NEW-HARNESS B4: per-rule origin; built-ins `internal/perm/builtin.go`) + session rules | NEW-HARNESS | B4 |
| 9.4.5 | Add for this session (effect select + rule; Enter) | toast "eff this session: rule (listed with the origin "this session")" | `POST .../rules` (`AllowForSession`, `switch.go:91`; deny/ask `Engine.AddRule`, `engine.go:300`) | REAL-NOW | B1 |
| 9.4.6 | `+ the tests preset` | toast "allowed this session: tests (N rules)" | `perm.TestsAllow` (`internal/perm/presets.go:6`) | REAL-NOW | B1 |
| 9.4.7 | `remove` | | `POST .../rules/remove` (`Engine.RemoveRule`, NEW-HARNESS B3) | NEW-HARNESS | B3+B1 |
| 9.4.8 | Would it ask? (tool Bash/Edit/Read, argument, Check, result tag + why; the order line) | the page's own JS judge | `POST .../permissions/check` → `Engine.Classify` (NEW-HARNESS B3; built on the engine's real evaluation, `engine.go:376` without prompting) | NEW-HARNESS | B3+C3 |

### 9.5 Trust

| # | Control | Mock behaviour | Backend source | Status | Owner |
|---|---|---|---|---|---|
| 9.5.1 | This project: directory, state (trusted on day, hashes unchanged / not used), digest, unlocks; files table (path, kind, bytes, hash 16) and covers note; `Run as CLI trust list` | | `GET .../trust`: `trust.Scan` (`internal/trust/trust.go:85`), `Ledger.Check` (`ledger.go:70`) | REAL-NOW | B4 |
| 9.5.2 | Every directory you decided about: dir, files, state tag; `Forget` / `Forget the entry` / `Trust these files` | `trustDir` (toast "trusted/forgot dir") | `POST /api/trust` (`Ledger.Remember` `ledger.go:89` after a fresh scan with confirmation; `Forget` `ledger.go:106`; `All` `ledger.go:126`) | REAL-NOW (+ per-file write serialization in the server) | B4 |
| 9.5.3 | Note with the session-start question options | | `TrustView.question.options` (the TUI's trust dialog options) | REAL-NOW | B4 |

### 9.6 Run settings

| # | Control | Mock behaviour | Backend source | Status | Owner |
|---|---|---|---|---|---|
| 9.6.1 | Workers stepper −/+ and number (0..12), team word | local | | PAGE | C3 |
| 9.6.2 | Now: team and active count | | roster, `state` | DERIVE | B2 |
| 9.6.3 | `Apply: start the team again` → confirm "Restart … The current run is closed (its checkpoints stay) and the chat starts empty." → toast | `restartTeam({swarm, force})` | `POST .../restart {kind:"swarm", fresh:true}` (in-process restart: `Session.Options()` NEW-HARNESS B1, `restartArgs`) | NEW-HARNESS + EXTRACT | B1 (fresh vs carried: **D-06**) |
| 9.6.4 | Isolation seg none/worktree | `setIsolation` (staged) | `PATCH .../launch` | DERIVE (staged in the host) | B1 |
| 9.6.5 | Verify input + Set (Enter) | `setVerify` toast "verify: cmd" | `PATCH .../launch {verify}` | DERIVE | B1 |
| 9.6.6 | Flags `--commit`, `--mailman`, `--no-mcp`, `--trust-project` | `setFlag` (staged) | `PATCH .../launch` | DERIVE | B1 |
| 9.6.7 | The same command line + Copy + `Run as CLI chat` | | meta `launch` | DERIVE | B1 |

### 9.7 MCP servers

| # | Control | Mock behaviour | Backend source | Status | Owner |
|---|---|---|---|---|---|
| 9.7.1 | Card per server: name, state tag, transport · origin, command line + server, describe note when it needs approval, error when failed, note, tools tags or "no tools: …", outcome line | | `GET .../mcp`: `session.MCPEntries` (`internal/session/mcp.go:422`), the tab's `MCPStatus()` (`mcp.go:249`) | REAL-NOW | B4 |
| 9.7.2 | `Approve` (needs approval) | "approved: its N tools are in the next request (the approval ends when the entry changes)" | `OpenMCPApprovals(home).Approve` (`mcp.go:393,401`) with confirmation; the tool list of a running session is frozen | REAL-NOW (approval) | B4 (**D-08**) |
| 9.7.3 | `Test` | "N tools answered ✓ (ms)" / nothing to test | extracted `mcpTest` (`cmd/sleipnir/mcp.go:159`) without creating a session dir | EXTRACT | B4 |
| 9.7.4 | `Reconnect` (disabled when off) | "reconnected ✓" / "reconnect refused again: …" | `Session.MCPReconnect` (`mcp.go:266`) | REAL-NOW | B4 |
| 9.7.5 | `Revoke` (disabled when it needs approval) | "revoked: the server stops and its tools leave the next request" | `Revoke` (`mcp.go:406`); effective at the next start | REAL-NOW | B4 (text: D-08) |
| 9.7.6 | Note "Every agent is sent the same tool list…" + session note | | `MCPView.sessionNote` | DERIVE | B4 |
| 9.7.7 | `Run as CLI mcp list` | | | PAGE | C3 |

### 9.8 Skills, commands & hooks

| # | Control | Mock behaviour | Backend source | Status | Owner |
|---|---|---|---|---|---|
| 9.8.1 | Skills table (name + argument hint, summary + `only you`, scope, source) + listing budget note | | `skills.Discover` (`internal/skills/skills.go:172`), budget from the session's shared layer | REAL-NOW | B4 |
| 9.8.2 | Your commands table + `Insert in the chat` | inserts `/name ` in the composer (toast) | `commands.Load` (`internal/commands/commands.go:198`) | REAL-NOW | B4+C3 |
| 9.8.3 | Hooks table (event, matcher, command · timeout, purpose + origin) + events list + trust note | | `hooks.ParseAs` (`internal/hooks/parse.go:49`), `hooks.Events` (`events.go:30`), redacted | REAL-NOW | B4 |

### 9.9 Providers & login

| # | Control | Mock behaviour | Backend source | Status | Owner |
|---|---|---|---|---|---|
| 9.9.1 | Table: provider (+ `default`), key tag (`✓ env VAR` / `✓ stored` / `no key`) + where, base URL + dialect, used by, `Sign in…` / `Sign out` | | `GET /api/providers`: `session.ProviderNames/LookupProvider/ProviderInfo/ProviderReady` (`internal/session/providers.go:139,225,236,336`), `config.StoredKeys` names only (`internal/config/auth.go:22`), key source from `harden.Process` status captured in main (EXTRACT B4), `chatgptauth.Connected` (`chatgptauth.go:171`) | EXTRACT | B4 |
| 9.9.2 | `Sign in…` modal: "Run this in a terminal…", `sleipnir login <id>`, Copy, `I ran it: mark as connected (mock)`, Cancel | | `POST /api/providers/recheck` | **DECISION NEEDED D-01** | B4+C3 |
| 9.9.3 | `Sign out` | toast "signed out of X" | `POST /api/providers/{name}/signout` (stored key removal + `harden.Provide(env, "")`; ChatGPT `Logout`) | EXTRACT | B4 |
| 9.9.4 | Note "The browser never sees an API key…" + provider note; `Run as CLI login`, `models` | | | DERIVE | B4 |

### 9.10 Config layers

| # | Control | Mock behaviour | Backend source | Status | Owner |
|---|---|---|---|---|---|
| 9.10.1 | Layers lowest to highest (kind, source, state tag, trust note) + precedence note; `Run as CLI config` | | `config.Load` `Report{Layers, Sources, Origins}` (`internal/config/load.go:45`) | REAL-NOW | B4 |
| 9.10.2 | Effective values: filter; key, value (session overrides "this session /mode … was X"), from (layer + file), notes, "below" | | `Report.Origins` + `config.Redact` (NEW-HARNESS B4) + tab meta overrides | NEW-HARNESS (redaction allow-list) | B4 |

### 9.11 Appearance & motion

| # | Control | Mock behaviour | Backend source | Status | Owner |
|---|---|---|---|---|---|
| 9.11.1 | Hover (both / chat / off), Motion (auto / reduce / full), Density (comfortable / compact), Cache details (Quiet / Full) | saved | `localStorage['sleipnir.web.settings']` | UI-LOCAL | C3 |
| 9.11.2 | Layout: Left rail (rail / wide), Radio rail (open / collapsed), Reset the width (344 px) | saved | `localStorage['sleipnir.web.dock']` | UI-LOCAL | C3 |
| 9.11.3 | Security semantics card (quiet period "this mock waits 0.8 s", modes, untrusted text, connection `127.0.0.1:6969 · loopback · launch token held in this tab`) | static | `hello.server.addr` | DERIVE | C3 (text: D-07) |
| 9.11.4 | Keys card | static + shortcuts | static | PAGE | C3 |

## 10. Tools, Doctor, Schedule, Runner, Kit (`98b-ui-tools.js`, `92-runner.js`, `95-kit.js`)

### 10.1 Tools catalogue (26 cards, four groups)

Filter input; `Open the runner · all commands`; each card: `sleipnir <cmd>`, purpose-built tag, summary, subcommand chips (first 8 +
"N more in the runner"), footer buttons. Source of cards: `D.spec` (`GET /api/cli`, B4 clispec). Status DERIVE, owner B4+C3.

| Group | Card | Purpose-built panel (`Open …`) | `Run as CLI` / `Open in the runner` | Runner mode (CONTRACT 18.2) |
|---|---|---|---|---|
| Run and chat | chat | none | runner | `tty_only` (**D-11**) |
| | run | none | runner | `net` (`priv` with bypass/yolo) |
| | swarm | none | runner | `net` (`priv` with bypass/yolo) |
| | demo | none | runner | `net` |
| | mock | none | runner | `server` (D-05) |
| Observe | sessions | Sessions view | runner | `run` (`sessions prune --yes`: `priv`) |
| | inspect | Cache view | runner | `run` with `--json` only (D-11) |
| | watch | Cockpit | runner | `tty_only` (D-11) |
| | replay | Replay view | runner | `run` with `--final`/`--record` (D-11) |
| | friction | none | runner | `run` |
| | recon | none | runner | `run` |
| Evaluate | doctor | Doctor view | runner | `net` |
| | sim | none | runner | `run` |
| | models | Settings › Models | runner | `net` (`fav add/rm`: `priv`) |
| | rl | none (13 subcommand chips + more) | runner | per subcommand (`net`/`run`/`server`) |
| Set up | init | none | runner | `priv` |
| | login | Settings › Providers | runner | `tty_only` (D-01) |
| | logout | Settings › Providers | runner | `priv` |
| | trust | Settings › Trust | runner | `run` (`add`/`forget`: `priv`) |
| | mcp | Settings › MCP servers | runner | `run` (`approve`/`revoke`: `priv`) |
| | schedule | Schedule view | runner | `run` (`add`/`rm`: `priv`) |
| | daemon | Schedule view | runner | `server` |
| | config | Settings › Config layers | runner | `run` |
| | update | none | runner | `net` (install: `priv`) |
| | version | none | runner | `run` |
| | help | none | runner | `run` |

### 10.2 Doctor

| # | Control | Mock behaviour | Backend source | Status | Owner |
|---|---|---|---|---|---|
| 10.2.1 | Endpoint select (configured models + `mock-1 (a loopback test server)` + `another endpoint …`), hint, custom `--base-url`/`--model`, `--deep` toggle, command line | | `GET /api/doctor/endpoints` | EXTRACT | B4+C3 |
| 10.2.2 | `Run the probe` (custom without both fields: toast) | steps appear over ~2.3 s from pack lines | `POST /api/doctor` → extracted `cmdDoctor` (`cmd/sleipnir/main.go:426`) → `internal/provider/probe` report with a step callback; `run` frames | EXTRACT | B4+C3 |
| 10.2.3 | Probe panel: status, meter, groups, step rows (✓/✗, name, ms, in/cached/out, hit bar for cache steps), warnings, error lines, verdict "What this endpoint does" (kv with ✓/✗ tone) + summary card | parse of pack lines | structured `step`/`verdict` frames | EXTRACT | B4+C3 |
| 10.2.4 | `Open in the runner` | runner with the flags | | PAGE | C3 |

### 10.3 Schedule

| # | Control | Mock behaviour | Backend source | Status | Owner |
|---|---|---|---|---|---|
| 10.3.1 | Daemon line: running pid/every/timeout or stopped; `Stop/Start the daemon` (toast); `Run what is due (--once)` (toast) | local flag | `POST /api/schedule/daemon` (`sched` daemon + lock, NEW-HARNESS B4; `daemonTick` `cmd/sleipnir/schedule.go:156` moved) | NEW-HARNESS + EXTRACT | B4 |
| 10.3.2 | Jobs table: id, cron, goal, where (+ model · mode · $), last run + exit, next, `Run now` / `Log` / `Remove` | | `sched.Store.List` (`internal/sched/store.go:34`), `sched.Next` (`store.go:135`) | REAL-NOW | B4 |
| 10.3.3 | `Run now`: streamed lines in the log pane, then toast "job ran: exit ok" | scripted lines | `POST .../jobs/{job}/run` → `RunJob` (`schedule.go:206` moved) streamed | EXTRACT | B4+C3 (D-05) |
| 10.3.4 | `Log`: log header and the job's last logs | | `GET .../jobs/{job}/log` | REAL-NOW (files in `<state>/schedule-logs`) | B4 |
| 10.3.5 | `Remove` → confirm → toast "removed jN" | | `DELETE .../jobs/{job}` (`Store.Remove`, `store.go:100`) | REAL-NOW | B4 |
| 10.3.6 | Add a job: cron (+ next run hint / invalid), goal, `--cwd` select, `--mode` (default/accept-edits/plan; "bypass and yolo cannot be scheduled from here"), `--budget-usd`, `Add the job` (validation toasts) → toast "added jN: next run …" | | `POST /api/schedule/jobs` (`Store.Add`, `store.go:77`), `GET /api/schedule/next` (`sched.ParseCron`, `cron.go:24`) | REAL-NOW | B4+C3 |
| 10.3.7 | Edit / pause a job (PLAN feature) | not in the mock | `PUT`/`pause` routes (`Store.Update`, `Job.Paused` NEW-HARNESS B4) | **DECISION NEEDED D-02** | B4 |

### 10.4 Runner (`92-runner.js`)

| # | Control | Mock behaviour | Backend source | Status | Owner |
|---|---|---|---|---|---|
| 10.4.1 | Commands list: filter, groups (sessions and runs / project and account / tools / rl lab / other), `N of 61`, selection | `D.spec` | `GET /api/cli` (B4 generated clispec, CONTRACT 18.3) | NEW-HARNESS (generator + drift test) | B4 |
| 10.4.2 | Form: summary, usage, positionals (required `*`), flags typed (bool toggle, number with step, text, repeatable comma list), defaults and notes as placeholders, `set` marks; source (`generatedFrom`) | | spec | DERIVE | C3 |
| 10.4.3 | Command line live; `Run` / `Copy` / `Reset`; `‹ Tools` | | | PAGE | C3 |
| 10.4.4 | Run: output pane streaming `$ cmd` + lines by kind; status `exit N · ms`; result card; recent runs list (click restores the form) | sample outputs | `POST /api/runs`; `run` frames; `GET /api/runs` | DERIVE (child process of the binary, CONTRACT 18.1; output kinds out/err only: **D-19**) | B4+C3 |
| 10.4.5 | Commands that cannot run here (chat, watch, login, replay without --final/--record, inspect without --json) | sample output | `403 tty_only` message | **DECISION NEEDED D-11** | B4 |
| 10.4.6 | Leaving the view / Reset / another command stops the streaming | timers die with the scope | `DELETE /api/runs/{id}` | **DECISION NEEDED D-05** | B4+C3 |
| 10.4.7 | Privileged commands (trust add, mcp approve, schedule add/rm, prune --yes, init, update install, logout, models fav add/rm, run/swarm --mode bypass/yolo) | run directly | confirmation token | NEW (security gate, A2) | A2+B4+C3 (confirm dialog: D-05) |

### 10.5 Kit

| # | Element | Mock behaviour | Backend source | Status | Owner |
|---|---|---|---|---|---|
| 10.5.1 | `?kit` view: the component catalogue with sample markup and a demo toast | static samples | none | **DECISION NEEDED D-20** | C1 |

## 11. Overlays, palette and toasts

### 11.1 Dialogs

| # | Dialog | Mock behaviour | Backend source | Status | Owner |
|---|---|---|---|---|---|
| 11.1.1 | New session: name, directory select, model select, workers (single agent / manager + N / share legs), isolation seg, verify; budget (number or off), flags (`--commit`, `--mailman`, `--no-mcp`, `--trust-project` checked), allow rules (+Add, tags ×), role models (role + model, Add, tags ×), first goal; mode radio group of five with dangerous warning text ("Nothing to type: choosing it here is the confirmation"); "the same command line" (bypass/yolo highlighted); Start / Copy the command / Cancel; budget validation toast; trust confirm for an untrusted directory; toast "started X: it runs in the background too" | `newSession` | `POST /api/sessions` (CONTRACT 6: shared chat-flag parser EXTRACT B1, in-process `session.New` `session.go:258`, trust step `trust.Scan`+`Ledger`, projects list) | EXTRACT + NEW-HARNESS (many sessions in one process, B1) | B1+C1 |
| 11.1.2 | Resume a session: `--continue`, table (↺/·, id, first prompt, `⚠ interrupted`, agents, cost, age, `↺ Resume` disabled when not resumable), note | `resumeSession` | `GET /api/recorded`, `POST /api/sessions/resume` (`ResolveResume`, `CheckResume` `resume.go:23,361`) | REAL-NOW + EXTRACT (listing) | B1+B4+C1 |
| 11.1.3 | Rename: input + Rename (Enter) → toast "renamed to X" | | `PATCH /api/sessions/{id}` (sidecar) | NEW-HARNESS | B1+B4 |
| 11.1.4 | Compact the thread (6.8) | | | | |
| 11.1.5 | Confirm (generic in-page): title, kicker, text, detail, OK (danger), Cancel; used by Close, Stop the run, Start again empty, Start the team again, Restore cN, Prune with --yes, Remove the job, Trust this project? | | per action | PAGE | C1 |
| 11.1.6 | Close the session confirm: last session warning, open question warning | | `DELETE /api/sessions/{id}` | NEW-HARNESS | B1 |
| 11.1.7 | Restore cN (Workspace) | 8.16 | | | |
| 11.1.8 | Revert this hunk | 8.14 | | | |
| 11.1.9 | Sign in to X | 9.9.2 | | | |

### 11.2 Sheets

| # | Sheet | Mock behaviour | Backend source | Status | Owner |
|---|---|---|---|---|---|
| 11.2.1 | Goal: objective (or "No goal set… /goal TEXT"), plan from the planner, the judge on evidence (verdict, evidence rows: tasks merged + literal "go test ./... passes on the merged result"), "State X · n/N steps · m/M tasks merged · judged on evidence at every turn boundary · continuation limit 20 turns, used N (sample)"; Pause the goal (esc) / Resume / Clear the goal; Set the goal input | | `goal` (objective, turns, max), `plan`, `verdict` (kind, left), `POST .../goal` | DERIVE + NEW-HARNESS (`goal.judge`) | B1+B2+C1 (evidence line and "(sample)": D-07) |
| 11.2.2 | Mode: note; five rows (current tag); bypass/yolo → typed confirm box (`Set it` enabled when the name matches) | | `POST .../mode` (+ confirm) | REAL-NOW | B1+C1 |
| 11.2.3 | Context: "Six layers… G0–G2 (Nk tokens) are one byte-prefix…", stacked bar, table layer/what/tokens (`0.5k–1.4k` for G5)/who reads it | `D.layers` | `layers` events of the tab's agents | DERIVE | B2+C1 |
| 11.2.4 | Help: keys (chords, alt+t/alt+g, alt+1…9, shortcuts) and commands list (slash) | | `D.slash` per tab | DERIVE | C1 |
| 11.2.5 | History (ctrl+r): sent lines newest first; click puts it in the composer | `S.hist` | snapshot `hist` | DERIVE | B1+C1 |
| 11.2.6 | Unreachable in v3 (no path opens them): model, effort, budget, permissions, trust, rewind, diff, login, mcp, skills, team | | none | deleted (UI-WIRING 13) | C1 |
| 11.2.7 | Views (phone More): every view + Command palette | | none | PAGE | C3 |

### 11.3 Popovers and transient UI

| # | Element | Mock behaviour | Backend source | Status | Owner |
|---|---|---|---|---|---|
| 11.3.1 | Mode menu: default / accept-edits / plan with descriptions and ✓; "Dangerous modes…" → Mode sheet with typed confirm; toast "mode: X" | | `POST .../mode` | REAL-NOW | B1+C1 |
| 11.3.2 | Session menu: Rename…, Start the team again… (Settings › Run settings), Stop the run… (confirm → interrupt), Close this session… | | as 1.2.3, 9.6.3, 7.3, 1.2.4 | | B1+C1 |
| 11.3.3 | Spend of every session (1.2.10) | | | | |
| 11.3.4 | Needs you inbox (4.3.6) | | | | |
| 11.3.5 | Hover cross-highlight (`data-ag`, `data-task`, `data-file`: hot/dimmed) and governor slow (30%) | | file owner from the Workspace index | DERIVE | C1+C2 |
| 11.3.6 | Toast stack (max 3, 3.6 s, announced) | | | PAGE | C1 |
| 11.3.7 | Chord hint, new pill, hold chip, question banner | as above | | PAGE | C1 |

### 11.4 Palette (`93-palette.js`; ctrl+k; 129 entries + one per live tab: 132 with the mock's three tabs)

All entries keep their group, name, description, `●` live mark and dispatch. Backend columns per entry.

**Slash commands (35, group as listed; dispatch `H[cmd]`)**

| # | Entry | Dispatch in the mock | Real | Status | Owner |
|---|---|---|---|---|---|
| P1 | `/goal TEXT` | pause/resume/clear words or set; opens Goal sheet | `POST .../goal` | DERIVE + NEW-HARNESS (goal loop in the web host, B1) | B1+C1 |
| P2 | `/new` | confirm "Start again, empty" → `newChat` | restart `new` | EXTRACT | B1 |
| P3 | `/clear` | same as /new | restart `clear` | EXTRACT | B1 |
| P4 | `/resume [id]` | resume id or Resume dialog | `POST /api/sessions/resume` | REAL-NOW | B1 |
| P5 | `/sessions` | Sessions view | | PAGE | C1 |
| P6 | `/compact [focus]` | Compact dialog prefilled | `POST .../compact` | REAL-NOW | B1 |
| P7 | `/rewind [id]` | with id: Restore dialog; else Workspace Checkpoints | Workspace | NEW-HARNESS (8.16) | B3+C2 |
| P8 | `/diff [id]` | Workspace Changes (`cN to now` or the newest only); toast "cN has nothing to diff" | Workspace | NEW-HARNESS | B3+C2 |
| P9 | `/exit` | Close confirm | `DELETE /api/sessions/{id}` | NEW-HARNESS | B1 |
| P10 | `/model [ref]` | with a known ref: set (toast "a team starts again on X"); else Settings › Models | `POST .../model` | REAL-NOW/EXTRACT | B1 |
| P11 | `/effort [level]` | set (toast "effort: X") or Settings › Roles | `POST .../effort` | REAL-NOW | B1 |
| P12 | `/fav [ref]` | star toggle (toast) or Settings › Models | `POST /api/models/fav` | EXTRACT | B4 |
| P13 | `/login [provider]` | Settings › Providers | | DERIVE | C1 (D-01) |
| P14 | `/budget [usd\|off]` | set (toasts) or Settings › Budget | `POST .../budget` | REAL-NOW | B1 |
| P15 | `/cost` | local card: input uncached, read, cache write `0`, output, hit, cost of budget (+ saved in Full) | model (`wr` real) | DERIVE | C1 |
| P16 | `/stats` | Cache view | | PAGE | C1 |
| P17 | `/context` | Context sheet | | DERIVE | C1 |
| P18 | `/status` | local card: model, mode, session, budget, cost, team | model, meta | DERIVE | C1 |
| P19 | `/mode <m>` | bypass/yolo → Mode sheet preset; else set (toast); no arg → Settings › Permissions | `POST .../mode` | REAL-NOW | B1 |
| P20 | `/plan [prompt]` | mode plan (toast "plan mode: read-only"), then send the prompt | `POST .../mode`, `POST .../messages` (the CLI's plan wrapper text, `cmd/sleipnir/chat.go` `/plan`) | REAL-NOW | B1 |
| P21 | `/allow <rule>` | allow this session (toasts) | `POST .../rules` | REAL-NOW | B1 |
| P22 | `/permissions` | Settings › Permissions | | PAGE | C1 |
| P23 | `/trust` | Settings › Trust | | PAGE | C1 |
| P24 | `/roles [role=m]` | set role model (toast) or Settings › Roles | `POST .../model {role}` | EXTRACT | B1 |
| P25 | `/swarm <n> [flags]` | confirm "Start the team again" → restart (toast; "already …") ; no n → Settings › Run settings | restart `swarm` | EXTRACT | B1 (D-06) |
| P26 | `/restart [flags]` | Settings › Run settings | restart `restart` (when flags are typed: CONTRACT 6) | EXTRACT | B1 |
| P27 | `/agents` | Cockpit | | PAGE | C1 |
| P28 | `/steer TEXT` | steer the manager (toasts) | `POST .../steer` | REAL-NOW (`Agent.Steer`, `agent.go:497`) | B1 |
| P29 | `/verbose [on\|off]` | feed tool calls on/off (toasts) | none | UI-LOCAL (per tab, memory) | C1 |
| P30 | `/anim [on\|off]` | motion setting (toast) | localStorage | UI-LOCAL | C1 |
| P31 | `/cwd` | local card: cwd, isolation | meta | DERIVE | C1 |
| P32 | `/recon` | runner `recon` and run | `POST /api/runs` | REAL-NOW (`session.BuildRecon`) | B4 |
| P33 | `/skills` | Settings › Skills | | PAGE | C1 |
| P34 | `/mcp` | Settings › MCP servers (`/mcp reconnect NAME` typed: server command) | `POST .../command` for reconnect | REAL-NOW | B1 |
| P35 | `/help` | Help sheet | | PAGE | C1 |
| P35+ | custom commands, skills, MCP prompts of the tab (not in the mock's list) | | `GET .../slash`; `POST .../command` (`expandSlash`, `chat.go:519`) | EXTRACT | B1 (listed with the same row look) |

**Go to (14)**: Cockpit, Cache, Mail, Board, Replay, Sessions, Settings, Tools, Doctor, Schedule, Run a command (runner) (the views
in registration order, Workspace excluded) + Files, Changes, Checkpoints (Workspace tabs). PAGE, C1.

**Settings (11)**: `Settings › Models`, `› Roles & effort`, `› Budget`, `› Permissions`, `› Trust`, `› Run settings`, `› MCP servers`,
`› Skills, commands & hooks`, `› Providers & login`, `› Config layers`, `› Appearance & motion` ("a real page, not a help text").
PAGE, C1.

**Radio (1)**: open the rail and focus the composer. PAGE, C1.

**Sessions (6 + one per live tab)**: New session…, Resume a session…, Needs you (inbox), Rename this session…, Close this session…,
Team… (Settings › Run settings); `Switch to <name>` with `cwd · state` per tab (dynamic). DERIVE (tabs), C1.

**Time (1)**: Pin the hold. PAGE, C1.

**The program (61)**: `sleipnir <path>` for every spec command with its summary; dispatch `ui.openCommand` (purpose-built panel for
sessions, doctor, schedule, daemon, models, trust, mcp, config, login, logout, replay, watch, inspect; else the runner with that
command). Source `GET /api/cli` (B4); each command's runner mode per CONTRACT 18.2. The 61 paths: `chat`, `config`, `daemon`, `demo`,
`doctor`, `friction`, `help`, `init`, `inspect`, `login`, `logout`, `mcp`, `mcp approve`, `mcp list`, `mcp revoke`, `mcp test`, `mock`,
`models`, `models fav add`, `models fav list`, `models fav rm`, `recon`, `replay`, `rl`, `rl compare`, `rl eval`, `rl expand`,
`rl export`, `rl report`, `rl reward`, `rl rollout`, `rl serve`, `rl show`, `rl taskgen`, `rl taskgen composite`, `rl taskgen fixture`,
`rl taskgen git`, `rl taskgen mutate`, `rl taskgen recall`, `rl tasks`, `rl tasks check`, `rl tasks filter`, `rl tasks split`,
`rl tasks stats`, `rl tasks validate`, `rl verify`, `run`, `schedule`, `schedule add`, `schedule rm`, `sessions`, `sessions prune`,
`sim`, `swarm`, `trust`, `trust add`, `trust forget`, `trust list`, `update`, `version`, `watch`. DERIVE, B4+C3.

Count: 35 + 14 + 11 + 1 + 6 + 1 + 61 = 129, plus one `Switch to` per live tab.

### 11.5 Toast-producing actions (every `ui.toast` call site)

| # | Trigger (file) | Toast text (mock) | Real source of the outcome | Owner |
|---|---|---|---|---|
| T1 | Stop the run (86, 91) | "stopped: the goal is paused" / why / "nothing was running" | `POST .../stop` result | B1+C1 |
| T2 | Interrupt turn button (84), Esc (94) | "interrupted: the goal is paused" / "interrupted: the turn is stopped and the goal is paused (/goal resume)" / "nothing is running to interrupt" | `POST .../interrupt` | B1+C1 |
| T3 | Send in replay (84); answer in replay (85); slash in replay (93) | "go live to talk…", "go live to answer…", "go live first…" | page | C1 |
| T4 | Send refused (84) | why | `POST .../messages` error | C1 |
| T5 | ctrl+c (84) | "line discarded…", "ctrl+c again to quit", "mock: /exit would end the session here" | page (D-14) | C1 |
| T6 | ctrl+o (94) | "tool output expanded (ctrl+o)" / "collapsed" | page | C1 |
| T7 | Question in a background tab or behind a hold (99) | "<tab>: agent wants to run cmd" / "a question is waiting behind the hold: …" | `ask` events | C1 |
| T8 | Background goal met (99) | "<tab>: goal met" | `goal {s:"met"}` / `final` | C1 |
| T9 | Close (80, 91) | "closed X" / last-session why | `DELETE` result | C1 |
| T10 | Mode menu / Mode sheet / Permissions page (80, 87, 98) | "mode: X", "mode: X (shift+tab will not leave it unasked)" (err), why | `POST .../mode` | C1 |
| T11 | Resume (87, 91, 93) | "resumed" / why | `POST .../resume` | C1 |
| T12 | Prune apply (91) | "pruned N sessions, M MB freed" | `POST /api/recorded/prune` | C3 |
| T13 | Recorded Replay (91) | "the recorded session shown is the one in this page (mock)" | D-10 | C3 |
| T14 | Start session (87) | "started X: it runs in the background too"; "the budget is a number of dollars, or off" (err) | `POST /api/sessions` | C1 |
| T15 | Copy command/path (87, 92, 97, 98) | "command copied" / "path copied" / "copy is not available here" | clipboard | C1/C2/C3 |
| T16 | Rename (87) | "renamed to X" | `PATCH` | C1 |
| T17 | Approvals (85) | "the buttons wake up when the keyboard has been quiet for a moment" (warm), "answered N: agent runs the command", "told agent what to do instead" | bridge | C1 |
| T18 | Compact (90) | "compacting the manager's thread: a declared, priced rebase" | `POST .../compact` | C1 |
| T19 | Settings › Models use (98) and `/model` (93) | "a team starts again on X (mock: the scripted run continues)" / "a team starts again on X" | `POST .../model` (D-07) | C3/C1 |
| T20 | Role select (98) | "role runs on X (its workers restart)" | `POST .../model {role}` | C3 |
| T21 | Trust toggle (98) | "trusted X" / "forgot X" | `POST /api/trust` | C3 |
| T22 | Insert command (98) | "inserted in the Radio composer" | page | C3 |
| T23 | Sign out (98) | "signed out of X" | `POST .../signout` | C3 |
| T24 | "I ran it" (98) | "mock: X now shows as connected" | D-01 | C3 |
| T25 | Budget (98, 93) | "budget: $x" / "budget: off" / why (err) | `POST .../budget` | C1/C3 |
| T26 | Rules (98, 93) | "eff this session: rule (listed with the origin "this session")", "allowed this session: tests (N rules)", "allowed this session: rule", "/allow needs a rule…" | `POST .../rules` | C1/C3 |
| T27 | Verify set (98) | "verify: cmd" | `PATCH .../launch` | C3 |
| T28 | Apply restart (98, 93) | "already …", "the team starts again: …", why | `POST .../restart` | C1/C3 |
| T29 | MCP actions (98) | "approved X", "revoked X", "X: reconnect failed again", "reconnected X ✓" | MCP routes (D-08) | C3 |
| T30 | Workspace (97) | "cN has nothing to put back", "files put back to before cN (mock: nothing is written)", "cN has nothing to diff" | Workspace routes (D-07) | C2 |
| T31 | Doctor (98b) | "a custom endpoint needs --base-url and --model" | page | C3 |
| T32 | Schedule (98b) | "job jN ran: exit ok", "daemon started", "daemon stopped: no job starts until it runs again", "ran N due jobs" / "nothing is due now (…)", "removed jN", "the cron expression is not valid", "a goal is required", "the budget is a number of dollars", "added jN: next run …" | schedule routes | C3 |
| T33 | Palette (93) | "effort: X", "starred/unstarred X", "plan mode: read-only", "X runs on Y", "/steer needs text…", "steer sent to the manager", verbose and motion texts, "started again, empty", "unknown command /x: / lists them", "X: no handler in this mock" | per entry; the last becomes the server command route (no toast) | C1 |

## 12. DECISION NEEDED

Each item: what the mock shows, why the real system cannot match it exactly, the options, and the recommendation this contract
builds unless the owner chooses otherwise.

| Id | Topic | Mock | Problem | Options | Recommended |
|---|---|---|---|---|---|
| D-01 | Provider sign-in | "Sign in…" shows the terminal command and "I ran it: mark as connected (mock)" | the button only fakes the state; the real choice is whether the browser may take a key | (a) terminal only: the button re-reads the providers and its text becomes "I ran it: check again"; no key route enabled (b) add a key form and the ChatGPT sign-in flow in the modal (new UI; routes of CONTRACT 14 enabled, priv) | (a) |
| D-02 | PLAN features without a v3 control: delete selected recorded sessions; schedule edit and pause | absent | building UI would change the approved look | (a) backend and API only, no control (b) add a selection column + "Delete selected…" (Sessions) and Edit/Pause buttons (Schedule) | (a) now; (b) when the owner approves a design |
| D-03 | PLAN feature without a v3 control: accept verified → commit; worktrees list; verify output | absent | as D-02 | (a) API only (b) controls in the Workspace strip | (a) |
| D-04 | Failed tasks | four columns: todo running verify merged | the harness has `failed` (and closures canceled/superseded) | (a) failed tasks stay in the todo column (no new style; closure in the title tooltip) (b) a fifth column (c) a ✗ mark on the card | (a) |
| D-05 | Lifetime of runner/doctor/run-now processes, and confirming privileged runner commands | streaming stops when the view unmounts; nothing asks | real processes and real side effects | (a) cancel the process when the view unmounts, Reset or another command; a standard `ui.confirm` before a `priv` command (b) keep processes running and reattach from "recent runs" | (a) |
| D-06 | `/swarm N`, Run settings Apply, footer "run it again" | "the chat starts empty" | `sleipnir chat`'s `/swarm` carries the manager's conversation (`restartArgs` fresh=false) | (a) fresh: new session, empty chat (mock text) (b) carry the conversation (CLI behaviour) and change the confirm text | (a) |
| D-07 | Mock-only wording | footer chip `MOCK · sample data`; "(sample prices)" titles; "Prices are sample data"; "spent, sample prices"; toast suffixes "(mock: …)", "(mock)"; "this mock waits 0.8 s"; "nothing is written in this mock"; "The sample data keeps file contents…"; governor note "no 429s, no retries: nothing is throttling the team" (static); `dup 0` (static); Goal sheet evidence literal "go test ./... passes on the merged result" and "(sample)"; compact estimate "at sample prices" | false with real data | (a) replace: chip removed; "(sample prices)" → "(list prices)"; "Prices are sample data;" → "Prices are the catalogue's;"; "spent, sample prices" → "spent"; "(mock: …)" suffixes dropped; "this mock waits 0.8 s" → "this page waits 0.8 s"; "nothing is written in this mock" → dropped; sample-data sentence → loading text (D-13); governor note shows "N 429s, N retries" when non-zero, else the mock's sentence; `dup` = mails dropped as duplicates when known, else hidden `dup` term; evidence rows = merged tasks + the judge's `left` items; "(sample)" dropped; estimate "at list prices" (b) keep verbatim | (a) |
| D-08 | MCP approve/revoke in a running session | "its tools are in the next request" | a session's tool list is frozen at start (`internal/session/mcp.go:210` comment) | (a) record the approval; it applies when the team starts again; texts say so (b) approving restarts the team so the tools appear | (a) |
| D-09 | Question box for non-command questions | header always "wants to run a command"; option 2 "don't ask again for <what> this session" | edits, web fetches (and, if they ever reach the page, trust and MCP) are asked too | (a) header word by `kind`: "wants to run a command" / "wants to edit a file" / "wants to fetch a web page" / "wants to use this project's files" / "wants to start a tool server"; option 2 for trust/MCP: "Yes, and remember until they change" (b) keep the fixed header | (a) |
| D-10 | Sessions view "Replay" of a recorded session | shows the current tab's Replay with a "(mock)" toast | real replay needs the recorded log | (a) open the recorded session read-only in a new tab kind "recorded" (no composer actions; the Replay view plays its log from `GET /api/recorded/{sid}/events`) (b) keep the button, toast "replay it with sleipnir replay <id> in a terminal" (c) remove the button | (a) |
| D-11 | Runner commands that need a terminal or serve their own page (`chat`, `watch`, `login`, `replay` without `--final`/`--record`, `inspect` without `--json`) | sample output | they cannot run in a pipe | (a) refuse with one line naming the web equivalent (b) map `chat` to the New session dialog, `inspect` to opening the inspector | (a) |
| D-12 | No tab | the mock always has sessions and refuses closing the last | a start without a model or key, or `--no-session` | (a) the server runs the first-run setup on the terminal; if no tab exists the page shows an empty cockpit and opens the New session dialog (b) refuse to serve without a first session | (a) |
| D-13 | States the mock does not have: not signed in (no cookie), disconnected/reconnecting, loading, server error | none | needed for a real server | (a) minimal: the committed sign-in page of `internal/web/auth.go` (`signInPage`: plain HTML, no script, no style) stays as it is; conn chip `○ 127.0.0.1:6969 · reconnecting` in `--warm` while the stream is down; loading = empty values and the views' own empty texts; errors as `err` toasts (b) the owner designs a styled sign-in page and the states | (a) |
| D-14 | ctrl+c twice at an empty prompt | toast "mock: /exit would end the session here" | in the web, quitting means closing the tab | (a) open the Close confirm (as `/exit`) (b) toast "ctrl+c twice ends a terminal chat; close this session with ×" | (a) |
| D-15 | History of a resumed session | a short recap at the top, elapsed from 00:00 | the real history is long and spans earlier runs | (a) earlier runs' transcript at t=0 with real times shown (`at`), elapsed and Replay cover the current run (VOCAB 12) (b) t=0 at the session's birth: elapsed and Replay span every run | (a) |
| D-16 | Prompt layers | six layers G0..G5 | the harness has seven (G6 hot tail) | (a) fold G6 into G5 (b) a seventh segment | (a) |
| D-17 | Approval quiet period | 0.8 s on the page | the harness uses 350 ms | (a) keep 0.8 s on the page, enforce 350 ms on the server (b) 350 ms on the page too | (a) |
| D-18 | Reviewed marks persistence | memory of the page | needs a home | (a) the session sidecar on the server (shared by pages, kept across reloads) (b) `localStorage` per browser | (a) |
| D-19 | Runner output colours | sample outputs with `head ok warn bad dim` kinds | real commands print plain stdout/stderr | (a) stdout `out`, stderr `err`, the result card as in the mock (b) per-command colourisers | (a) |
| D-20 | The `?kit` component catalogue | reachable with `?kit` (and listed in A3's `DEV-ONLY.md` as "removed or hidden when live") | it shows sample markup only (no data); it is a design reference | (a) keep it reachable with `?kit` only (no link anywhere; byte-identical module) (b) drop `95-kit.js` and the `?kit` branch of `99-app.js` from the shipped page (keep it in `uidev/`) | (b): `ui.popover` and `ui.ringSvg`, defined in 95-kit.js, have no other user (A3's inventory), so nothing else changes |
