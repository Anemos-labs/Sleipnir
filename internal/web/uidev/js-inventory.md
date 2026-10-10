# JavaScript inventory

Every file of `js/` in the order `index.html` loads it, with its purpose and its relation to the reference page
(`internal/web/uidev/mock`, a self-contained prototype with sample data and a scripted simulation; see `README.md`).

* **Shared**: a renderer, a reducer or shared infrastructure that exists in both pages with the same role. It draws from state and
  from `SL.D`, `SL.sessions`, `SL.act` and `SL.ws`; it differs from the reference page's file only where it reads the server's data
  or sends a request.
* **Live**: the file has a counterpart in the reference page that works on sample data or on the simulation; the shipped file is a
  client of the API and has a different body.
* **Shipped only**: the reference page has no counterpart.

The contracts between modules are listed after the table. A module reads another module's namespace when it is evaluated
(`const D = SL.D`), so a file loads after the files whose namespaces it reads.

| File | Purpose | Relation |
| --- | --- | --- |
| `00-namespace.js` | Creates the one global, `window.SL`, before any module runs. | Shared |
| `00-util.js` | `SL.u` helpers (escaping, id checks, number and time formats, element builders, PRNG, bus) and `SL.bus`. | Shared |
| `api.js` | `SL.api`, the page's only door to the server: requests under `/api/` with the session cookie and the `X-Sleipnir-Web` header, the confirmation flow for a `428`, single-use confirmation ids, the event stream with its reconnects. A call never throws. | Shipped only |
| `11-data-live.js` | `SL.D`, `SL.G` and `SL.data`: the catalogues (models, roles, providers, rules, skills, MCP servers, trust, configuration, schedule, the command spec) and the state shared by every session, as caches over the API that read as empty until they arrive; plus the page's own tables (the permission modes, the key list, the names and colours of the prompt layers, the built-in roles). | Live (counterpart: `10-fixtures.js`, `11-data-adapter.js`) |
| `20-clock.js` | `SL.time`: the view clock and the time governor (hold, slow, catch-up, quiet period). | Shared |
| `30-model.js` | `SL.model` (the pure reducer from events to a session's model, which first makes each server event plain) and `SL.calc` (every derived number: hit, cost, savings, warm clock, tasks, questions). | Shared |
| `50-sessions.js` | `SL.sessions` and the `Session` class: the registry of the tabs the page shows, the event log, the world and view clocks (`advanceWorld`, `advanceView`, `stepWorld`, `stepView`), `rebuild`, `collapse`, `seek`, `unseen`, `state`, the placeholder session of a server that hosts no tab, the recorded-session list and pruning. | Live (counterpart: `50-sessions.js`, `40-scripts.js`) |
| `60-actions.js` | `SL.act` (every action a person can take: a synchronous check that returns `{ok: false, why}`, else a request to the API whose result is `done`), `SL.settings` (per-viewer settings in `localStorage`) and `SL.actions`. The effect of an action reaches the screen as the events and meta the server sends back. | Live (counterpart: `60-actions.js`) |
| `live.js` | `SL.live`: the boot sequence (hello, stream, snapshots), the frames that become sessions, events, meta and rosters, the connection state, recorded sessions opened read-only or followed, the server's defaults for a new session. | Shipped only |
| `70-view.js` | Scopes and views (everything a view starts dies with it), hover cross-highlighting (`SL.link`), the one frame loop (`SL.loop`). | Shared |
| `80-ui-shell.js` | The persistent chrome: HUD, session strip, connection chip, replay banner, footer. | Shared |
| `81-ui-hero.js` | The drawn horse whose eight legs are the eight workers, and the shared-prefix bar. Reads `SL.D.layers`. | Shared |
| `82-ui-board.js` | The task board (todo, running, verify, merged) used by the cockpit and the Board view. | Shared |
| `83-ui-cockpit.js` | The Cockpit view: horse, manager card, stalls, gantt, board, merge queue, mail, governor. | Shared |
| `84-ui-chat.js` | `SL.chat`: the Radio rail (manager conversation, read-only team feed, plan box, hold chip, composer). | Shared |
| `85-ui-approvals.js` | The approval question and the cross-session inbox, with the quiet-period rule for answering. | Shared |
| `86-ui-overlays.js` | Modal sheets, toasts, in-page confirm, icons, the reduced-motion switch (`ui.still`). | Shared |
| `87-ui-sheets.js` | Sheets (goal, mode, context, help, history) and dialogs (new session, resume, rename, the trust step). Reads the catalogues of `SL.D`; changes state only through `SL.act`. | Shared |
| `88-ui-drawer.js` | The per-agent drawer (details, tool log, mail, cache) and the layer-stack drawing. Reads `SL.D.layers` and `layerToks`. | Shared |
| `90-views-a.js` | The Cache, Mail, Board and Replay views. | Shared |
| `91-views-b.js` | The Sessions view (live sessions, recorded sessions, prune, delete selected) and `SL.toolkit`, the toolkit that Settings, Tools, the Runner and Sessions share (API calls with their confirmations, page caches, one way to report a failed request). Reads `SL.sessions`. | Shared |
| `92-runner.js` | `SL.runner`: a form from the command's flags, the command line, a streaming output pane and a result card. A run is a child process of the server; the page starts it, follows its frames and stops it when the view goes away. | Live (counterpart: `92-runner.js`) |
| `93-palette.js` | `SL.palette`: command registry (slash commands, views, sessions, `sleipnir` commands), the palette dialog, the `/` menu source, `@` file completion from the server's walk of the project. | Shared |
| `94-keys.js` | Global keyboard handling and the Escape precedence. | Shared |
| `96-ui-nav.js` | The navigation rail, the Radio rail's collapse and resize, the `g` chords, the phone bottom bar. | Shared |
| `96b-ws-data.js` | `SL.ws`: file text, status, ownership, blame, tree rows, hunks, restore and revert at any checkpoint, filled from the workspace routes of the API. The `SL.ws` methods are the contract of the workspace screens. | Live (counterpart: `96b-ws-data.js`) |
| `97-ui-workspace.js` | The Workspace view: Files, Changes, Checkpoints and Merge as four tabs, the time-travel scrubber, the diff with a per-line gutter, hunk revert, restore. Reads `SL.ws`; changes state through `SL.act`. | Shared |
| `98-ui-settings.js` | Settings, eleven pages in three groups. Reads `SL.D.models`, `roles`, `testsPreset` and `SL.D.extra` (permissions, skills, MCP, trust, providers, hooks, config); changes state through `SL.act`. | Shared |
| `98b-ui-tools.js` | Tools: the command catalogue and the purpose-built Doctor and Schedule views. Reads `SL.D.spec` and `SL.D.extra` (`doctor`, `projects`). | Shared |
| `99-app.js` | Boot: settings, the live data, the shell scope, the first view, the frame loop; the `?view` and `?session` parameters, the tab-title badge. | Shared |

## Contracts

1. **Catalogues (`SL.D`)**: roles and role order, prices, models, providers, role models, modes, rules, tests presets, trust files,
   MCP servers, skills, shortcuts, config layers, schedule, the command spec, slash commands, prompt layers. `SL.D.extra` carries
   the richer sections used by Settings, Tools and the workspace. Every field the code reads is a `D.<name>` or `X.<name>` in the
   files above. A field that has not arrived reads as its empty shape.
2. **Sessions (`SL.sessions`, `Session`)**: `list`, `active`, `get`, `activate`, `needs`, `recorded`, and the `Session` fields
   `id name kind meta roster log wt vt widx idx wm m ui hold replay hist`. Events enter only through `Session.add`;
   `{t, k, ...fields}` with `t` in session seconds and `k` one of the kinds listed in `30-model.js`
   (`internal/web/wire/events.go` has their fields).
3. **Actions (`SL.act.*`)**: the names are used by the renderers, the palette and the keyboard. A result is `{ok, why}` where an
   action can refuse, and `done` (a promise of the API result) where it sent a request. The `action` bus event follows every call.
4. **Workspace (`SL.ws`, `SL.fileOwner`)**: `info`, `setAt`, `textAt`, `touches`, `statusOf`, `blame`, `hunks`, `counts`, `rows`,
   `liveFile`, `pending`, `filesFrom`, `lines`, with `ops` (restore, hunk revert, accept) and `merge` for the requests the screens make.
5. **Runner**: `SL.runner.buildRequest` builds the request for a command from its form state and `createRun` drives one run; the
   server answers with a run id and streams `out` and `err` lines and a result. A line is `{k, t}`.
6. **Time**: the world clock advances one second per wall second for every session; the view clock is the governor's
   (`20-clock.js`). The server's events carry their own `t`; the reducer, the hold and the catch-up logic do not depend on where
   the events come from.

Text that came from a tool, a file, mail or a model is data: it reaches the page through `U.esc` (`00-util.js`) or `textContent`,
never as markup. Every string the API returns follows that rule.
