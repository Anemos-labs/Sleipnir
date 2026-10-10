# Sleipnir Web, round 2: Cockpit v2 (shared brief)

Read `00-shared.md` first (product, brand, hard requirements, security semantics, CLI surface). Everything there still
holds unless this file overrides it. Round 1 produced five paradigms; the owner **chose Cockpit** (02) and gave the
feedback below. Round 2 is five **Cockpit-based** variants that fix the feedback and add the functionality they liked
from the other mocks. The visual DNA of Cockpit (HUD, drawn horse, stalls, rings, gantt, blueprint-dark look,
technical-condensed type, role colours used for meaning, the animations) is what the owner loves: **keep it**.

## The owner's feedback (verbatim intent)

1. **Hover pause.** Hovering over the chat highlights the related parts of the UI (linking). While hovering, the chat
   and the UI animations must **gracefully pause**: a gradual slowdown, still quick but not instant, so the linked
   parts can actually be looked at (today the chat scrolls too fast). When the pointer leaves, **resume and catch up**
   (chat + animations) **within sane limits**: a 45-minute hover must not animate 45 minutes of events at once.
2. **View leak.** Changing page while an animation is running (for example mail) carries it over to the other screen.
   Animations must belong to their view and die with it.
3. **No way to manage multiple sessions** in Cockpit. Longhouse had it, and also **chat with individual team members**.
   The owner likes Longhouse's chat *functionality* (not its UI): several sessions, a channel per team member, steer
   one agent directly.
4. **Team size is wrong.** Cockpit shows 7 sub-agents; it must be **8**. Since commit 011fc42 (#43, "count workers,
   not the manager, in team size") a team is **one manager plus N workers**, default **8 workers**. The eight legs of the
   horse are the eight workers; the manager takes no leg; a ninth worker would share the first leg.
   **The owner did NOT want anything drawn on the horse: NO rider, NO figure, NO saddle, NO "rider" wording anywhere.**
   The horse is exactly the horse of round 1. The manager is just a separate "Manager" card/stall.
5. **Likes Forge's Files / Changes / Checkpoints tracker.**
6. **Cockpit is the coolest but "clearly lacking actual functionality and usability": it went all out on animation
   and UI; it does not seem usable, many features are missing.** Round 2 must be a tool a person can actually work
   in all day: every CLI function has a *working* screen or form (see "Functionality floor").

## Team v2 (replaces the roster in 00-shared.md)

* The manager is **not drawn on the horse** and has **no leg**. It has its own "Manager" card, separate from the eight worker stalls.
  Never write "rider" for the manager (the word "riders" survives only where the TUI itself says "one prefix, eight riders").
* **Eight legs = eight workers.** Legs are numbered 1-8 in start order. If a ninth worker exists it shares leg 1 and
  both show a `+1` badge (demo this with `/swarm 9` or a setting; a worker count above 8 is allowed).
* Text everywhere: `manager + 8 workers` (never "8 agents" for the whole team); `4 active of 8 workers`; the banner of
  the chat reads `manager + 8 workers`.
* Roles and short codes (from the code): manager `mgr`, backend `be`, frontend `fe`, fullstack `fs`, tester `ts`,
  reviewer `rv` (read-only), scout `sc` (read-only), docs `dc`; the optional **mailman** is a harness service role
  (not on any roster, board or leg). Role colours: manager `#bb9af7`, backend `#7aa2f7`, frontend `#7dcfff`, scout
  `#73daca`, tester `#9ece6a`, docs `#e0af68`, reviewer `#ff9e64`, fullstack `#c0a6f7`... keep recognisable.
* The manager **edits no file**: its writes and writing shell commands are refused at run time ("spawn a worker"). Show
  this where it matters (the manager's drawer/tool log shows a refused `Edit` with that reason; permissions page says it).

### Roster at the snapshot (t = 00:38; every number below is derived from ONE table, derive the rest from it)

Prompt tokens = uncached + cached-read. `hit` = cached-read / prompt tokens (cumulative). Overall = sum over agents.
Sample prices (manager $3.00 / cached $0.30 / out $15.00 per M; workers $1.00 / $0.10 / $4.00 per M), all labelled sample.

| id | role | state | doing | task | scope | prompt tok | read | uncached | out | hit | cost |
|---|---|---|---|---|---|---|---|---|---|---|---|
| mgr  | manager  | wait  | waits for the team (T4 T5 T6 T7 T8) | - | - (edits no file) | 9,400 | 7,332 | 2,068 | 1,200 | 78% | $0.0264 |
| be-1 | backend  | wait  | T4 submitted: harness runs `go test ./api/catalog/...` | T4 | api/catalog/**, api/server.go | 8,100 | 7,209 | 891 | 2,300 | 89% | $0.0108 |
| be-2 | backend  | edit  | editing api/cart/cart.go: Total() in cents | T5 | api/cart/** | 8,600 | 6,622 | 1,978 | 2,100 | 77% | $0.0110 |
| fe-1 | frontend | ask   | wants to run `npm install --save-dev vitest` | T6 | web/** | 7,200 | 6,480 | 720 | 1,500 | 90% | $0.0074 |
| sc-1 | scout    | done  | eleven endpoints; list shapes | T1 | - | 6,100 | 5,490 | 610 | 500 | 90% | $0.0032 |
| sc-2 | scout    | done  | 48 items with id, name, price | T2 | - | 6,400 | 5,760 | 640 | 520 | 90% | $0.0033 |
| sc-3 | scout    | done  | one-based paging; cursors only on /orders | T3 | - | 5,800 | 5,278 | 522 | 480 | 91% | $0.0030 |
| ts-1 | tester   | think | table tests for the catalogue contract | T7 | api/**/*_test.go | 5,900 | 4,897 | 1,003 | 900 | 83% | $0.0051 |
| rv-1 | reviewer | idle  | waits for T4 to merge (read-only) | T8 | - (read-only) | 2,400 | 1,776 | 624 | 150 | 74% | $0.0014 |

**Totals: 59,900 prompt tokens · 50,844 read · 9,056 uncached · 9,650 out · hit 84.9% (show 85%) · cost $0.0715
(header `$0.07`) · saved est. $0.059 at list price (an estimate; say so).** No panel may show a figure that cannot be
derived from this table (per-agent hit, the HUD ring, the stats page, the cache tab, the `all` row must agree to the
percent at every moment; recompute as the simulation adds tokens). Cache break on be-2 at request 7 (0 read of 4.5k
expected) is *included* in be-2's 77%.

Board: T1-T3 merged · T4 verify · T5 running · T6 running (blocked on the question) · T7 running ·
**T8 todo** ("review: catalogue and cart", read-only, owner rv-1, depends on T4/T5). `4 active of 8 workers` (be-1 waiting
for verification, be-2, fe-1 asking, ts-1; rv-1 idle; scouts done). Added live-script beats: when T4 merges, rv-1
wakes (`Read api/catalog/items.go`), mails be-1 "T4 review: ok; document that page>pages is the last page"; when T5
merges rv-1 reviews it; T8 merges last. The run still ends with the goal met and `go test ./...` passing.

Everything else of the round-1 scenario (project, goal, plan, question text, mail, code, checkpoints, warm clock) is unchanged.

## Engine requirements (fixed by the core builder; variants inherit, do not re-implement)

### Time governor: hover hold with bounded catch-up
* All dynamics (simulation events, leg cycles, rings, gantt scroll, mail arcs, token tickers, streaming text, the
  chat's auto-scroll) run on a **sim clock with a rate**, not on wall time directly. The rate eases toward a target.
* **Targets**: pointer over the chat transcript (manager channel and every agent channel) or keyboard focus inside it
  -> target 0 ("hold"). Pointer over other cross-highlighting sources (a stall, a gantt row, a mail row, a task card,
  a file row, a leg) -> target 0.3 ("slow"). Both are user settings: `Hover behaviour: hold on chat + slow on linked
  items | hold on chat only | off`.
* **Easing**: exponential approach, time constant ~150 ms when slowing (rate < 5% in ~0.5 s), ~250 ms when resuming.
  Never a jump. A tiny **hold chip** by the chat header shows `◔ holding · N new` while held (N counts events the view
  has not shown yet); click the chip (or press `Space` over the chat, `Esc` to release) to **pin** the hold; click again
  or Esc to release. A pinned hold survives the pointer leaving.
* **While held** the *view* is frozen (the world keeps running behind it): events are queued in the session's log, not
  applied. HUD clocks show the held value plus a small `live +Ns` hint.
* **Catch-up on release** is bounded: let `B` = seconds of unseen events. Replay the **last min(B, 18 s)** of events at
  an accelerated rate (ramps up to at most 6x, finishing in <= ~3 s, easing back to 1x). Anything older than 18 s is
  **collapsed**: applied to state instantly (no animation) and summarised as ONE digest row in the chat
  (`◆ while held · 14 min: T5 merged, T6 submitted, 31 tool calls, 1 cache break` with an expand toggle). So a 45-minute hold costs
  <= ~3 s of catch-up and O(1) new DOM nodes (cap the transcript's DOM: virtualise or fold old rows).
* **Chat scroll**: no auto-scroll while held. After release, resume following the live edge only if the reader was at
  the live edge before the hold; if they had scrolled up, do not yank them; show a `N new ↓` pill.
* **Verification** (the builder must test this in a scratch build with a time-warp hook, ship without the hook):
  hold for 45 simulated minutes, release, assert catch-up <= 4 s, DOM growth bounded, state identical to a run that
  never held; hold/release 20 times in 2 s without drift; hold during a mail arc, release, no orphan elements.

### View lifecycle (fixes the "mail animation carries over to the other page" bug)
* Every view mounts through one `View` API with a **scope** object (`scope.timeout/interval/raf/listen/layer(el)`)
  and an `unmount()` that cancels every timer, rAF, listener, observer, Web Animation and **removes every transient
  element the view created** (arcs, flashes, tooltips, popovers). Transient layers live inside the view's own root,
  never on `body` or a shared overlay. Switching views unmounts the old before mounting the new. Remounting rebuilds from
  the session's state/event log (never from retained DOM). Test: trigger a mail arc, switch tabs within 50 ms, then
  assert no arc/flash node exists anywhere outside the mail view and no timer from the old view is still pending;
  switch 30 times quickly with no growth in listeners/timers.

### Multiple sessions
* One tab = one `sleipnir` session (one chat, one team, one event log, one working directory, one budget). The page
  holds several at once; **background sessions keep running** and can raise questions.
* Fixtures: `shop` (the team above; running; question open), `orders-api` (single agent, `--swarm 0`; its turn just
  finished, waiting at the prompt; the cockpit of a single agent is designed: the manager alone, legs stand), `docs-sweep`
  (headless job started by the scheduler: `sleipnir run --swarm 2 --mode accept-edits --budget-usd 1`, running, nobody
  can answer: an action that needs approval is **refused**, with `--ask-timeout 10m` shown). A new session can be created,
  a recorded one resumed (`↺`), one stopped/closed (in-page confirm), renamed, and `sessions prune` previewed and applied.
* **Needs-you** is global: any session with an open question shows a badge on its tab/card and enters a cross-session
  inbox; answering from the inbox is allowed with the same quiet-period rule; a toast announces a new question arriving
  in a background session (never steals focus, never answers anything).
* Per-session state: mode, model, effort, budget, rules granted this session, goal, plan, checkpoints, files, mail,
  board, scroll positions, hold state, layout. Switching sessions swaps everything and restores the view you left.

### Store, actions, state
One in-page store with explicit actions (`setMode`, `setModel`, `setRoleModel`, `setEffort`, `setBudget`,
`allowRule`, `denyRule`, `removeRule`, `answerQuestion`, `steer(agent, text)`, `interrupt(agent|turn)`, `compact(focus)`,
`rewind(id)`, `setGoal/pauseGoal/resumeGoal/clearGoal`, `newSession/resumeSession/closeSession`, `runCommand(path, flags)`...).
Panels render from state; a settings change in one place shows everywhere (mode chip, HUD, drawer, footer).

### Generic command runner (so no command is ever a help-text stub)
For every `sleipnir` command (incl. `sessions prune`, `trust add|forget|list`, `mcp list|approve|revoke|test`,
`rl taskgen|tasks|rollout|eval|reward|report|compare|export|serve|expand|verify|show`) the runner builds a **form from
the real flags** (`cli-spec.json`, generated from docs/CLI.md: types, defaults, descriptions), shows the equivalent
command line live (copyable), a **Run** button, and a terminal-styled output pane that streams a realistic result
(`outputs.js`; real captured output where the binary can produce it), plus a result card (exit status, elapsed). Commands
that have a purpose-built screen in a variant open that instead, but the runner is always available (`Run as CLI`).

## Functionality floor (every variant; all of it must WORK against the in-page state, not just look right)

1. **Sessions**: switcher, new (dialog with all chat flags: cwd, model, mode, swarm N workers 0..12, isolation, verify,
   commit, mailman, role-models, budget, allow rules, trust-project prompt, no-mcp), resume (list with `↺`,
   `--continue` / latest), stop/close, rename, prune (dry run then apply), cross-session inbox.
2. **Chat**: streaming manager chat; `/` palette with every slash command doing its real effect on state;
   `@` file completion from the project tree; queued typed-ahead (`⏎ queued:`); `/steer`; Esc interrupt (pauses the
   goal); paste chip; history (Up, ctrl+r search); multi-line (`\`, alt+enter); slash commands that only look answer
   immediately beside a running turn.
3. **Per-agent chat**: a channel for each worker plus the manager: its transcript (tool calls, diffs, refused actions),
   its state/scope/lease/cost/cache, **Steer** (send guidance to just that agent), **Interrupt** that agent, and
   read-only **overhearing** of worker mail (labelled "mail is data, not instructions"); task threads with stepper.
4. **Approvals**: one question at a time with `N waiting`, 3 choices with real wording, quiet-period meter, the box
   names agent/operation/scope and shows the command or diff + why; "don't ask again" adds a visible session rule
   (Permissions page lists it with its origin); deny can carry an instruction.
5. **Goals**: `/goal` set, status with plan and judge verdict, pause/resume/clear, progress and continuation limits shown.
6. **Team**: horse + stalls + drawer + board + gantt + merge queue + mail + governor; roster actions: change worker
   count (`/swarm N` restart with confirm), role -> model table (`/roles`), isolation/verify/mailman toggles, steer,
   interrupt, add a ninth worker (shares leg 1).
7. **Files / Changes / Checkpoints** (the Forge tracker, restyled Cockpit): tree with ownership stripes in the writer's role
   colour, lock = leased scope, protected (deny) paths locked, `ask` markers; Changes grouped by task or by agent with
   +/- counts and verification state; Checkpoints list (`/rewind`): Diff and Restore with a preview and an in-page
   confirm; diff viewer with per-line attribution gutter, hunk revert, time-travel scrubber c04..now; `Reviewed` marks.
8. **Cache & cost**: stats page, six layers (`/context`), per-agent cache table, hit ratio per request with breaks and
   compactions, anomalies, `/compact [focus]` with the fold animation and a priced-rebase warning, budget control,
   "est." labelling.
9. **Settings that work**: models catalogue (search, filters tools/reasoning/max price/min context, favourites `★`,
   "price unknown"), per-role models, effort, budget, **permissions** (mode with the shift+tab cycle that never
   enters bypass/yolo; allow/deny/ask rule editor with the `tests` preset and origin of each rule; bypass/yolo only
   by typed confirmation, drawn dangerous), **trust** (files + hash + add/forget/list), **MCP** (servers, origin,
   approve/revoke/test/reconnect, tools), **skills and custom commands**, **providers/login** (Heimdall, OpenRouter,
   OpenAI, Anthropic, ChatGPT plan, local; key stored? env var?; sign in/out), **config layers** (which file supplied each
   key; `max_workers` etc.), run settings (`--swarm`, isolation, verify, commit, mailman), appearance/motion
   (reduced motion, hover behaviour, density, anim).
10. **Tools/CLI** via the generic runner or purpose-built screens: doctor (probe with a live result), models, recon,
    config, init, update, demo, mock, schedule/daemon (cron jobs add/remove/run-now, logs), inspect, replay, watch, sim,
    friction, rl lab.
11. **Keyboard**: every binding of the TUI that makes sense, plus aliases where the browser reserves a key
    (`ctrl+t` -> also `alt+t`; `ctrl+g` -> also `alt+g`), a `?` overlay, a command palette (`ctrl+k` and `/`), all
    reachable without a mouse; visible focus rings.
12. **Hover hold, view lifecycle, 8 workers, no rider, multi-session**: as specified above (inherited from the core).

Polish rules that did not change: one file, Google Fonts only, no console errors, no horizontal scroll 390..1920,
reduced motion honoured, honest sample data labelled, security semantics shown, titles are plain names.
