# JavaScript inventory

Every file of `js/`, in load order, with its purpose and what a live API client does with it.

* **KEEP**: a renderer, a reducer or shared infrastructure. It draws from state and from `SL.D`, `SL.sessions`, `SL.act`
  and `SL.ws`, so it keeps working when those are fed by the API.
* **REPLACE-LIVE**: the simulation, a fixture or sample data. It is replaced by an API client or a feed.
* **SPLIT**: one file holds both; the boundary is stated.

The seams a live client must honour are listed after the table. A module reads another module's namespace when it is
evaluated (`const D = SL.D`), so a replacement loads at the position of the file it replaces.

| File | Purpose | Class |
| --- | --- | --- |
| `00-namespace.js` | Creates the one global, `window.SL`, before any module runs. | KEEP |
| `data.js` | Data pack `window.SLDATA`: sample projects, models, prices, sessions, file histories, formatters; parts are captures of the real binary's output. | REPLACE-LIVE |
| `outputs.js` | `SLDATA.outputs`: the text each `sleipnir` command prints, for the runner. | REPLACE-LIVE |
| `cli-spec.js` | `window.SLCLISPEC`: commands, flags, positionals, summaries generated from the CLI reference. | REPLACE-LIVE (served by the API; the generator is the real flag sets) |
| `00-util.js` | `SL.u` helpers (escaping, number and time formats, element builders, PRNG, bus) and `SL.bus`. | KEEP |
| `10-fixtures.js` | `SL.FX`: the built-in sample data (roles, roster, prices, models, providers, rules, skills, MCP servers, sample code and trees, commands). | REPLACE-LIVE |
| `11-data-adapter.js` | `SL.D`: the one object the engine reads catalogue data from, built from `SL.FX` and the data pack. | REPLACE-LIVE (the `SL.D` shape is the contract) |
| `20-clock.js` | `SL.time`: the view clock and the time governor (hold, slow, catch-up, quiet period). | KEEP |
| `30-model.js` | `SL.model` (the pure reducer from events to a session's model) and `SL.calc` (every derived number: hit, cost, savings, warm clock, tasks, questions). | KEEP |
| `40-scripts.js` | `SL.scripts`: the scripted timelines of the sample sessions, the generic new-run script, scripted chat and steer replies, the director that schedules verification and the end of the goal. | REPLACE-LIVE |
| `50-sessions.js` | `SL.sessions` and the `Session` class. Stays: the event log, the world and view clocks (`advanceWorld`, `advanceView`, `stepWorld`, `stepView`), `rebuild`, `collapse`, `seek`, `unseen`, `state`. Goes: `initRun` (roster and script from `SL.scripts`), the registry operations `create`, `make`, `close`, `rename`, `prune` and `pruneCandidates`, the recorded list, `PLANS`. | SPLIT |
| `60-actions.js` | `SL.act` (every action a person can take), `SL.settings` (per-viewer settings in `localStorage`), `SL.G` (state shared by every session) and `SL.actions.pump/deliver` (typed-ahead lines answered by the script). Stays: `SL.settings`, the action names and their `{ok, why}` results, the `action` bus event. Goes: each action's body (synthetic events and local state), `SL.G` initialisation from `SL.D`, `pump`/`deliver`. | SPLIT |
| `70-view.js` | Scopes and views (everything a view starts dies with it), hover cross-highlighting (`SL.link`), the one frame loop (`SL.loop`). | KEEP |
| `80-ui-shell.js` | The persistent chrome: HUD, session strip, replay banner, footer. | KEEP |
| `81-ui-hero.js` | The drawn horse whose eight legs are the eight workers, and the shared-prefix bar. Reads `SL.D.layers`. | KEEP |
| `82-ui-board.js` | The task board (todo, running, verify, merged) used by the cockpit and the Board view. | KEEP |
| `83-ui-cockpit.js` | The Cockpit view: horse, manager card, stalls, gantt, board, merge queue, mail, governor. | KEEP |
| `84-ui-chat.js` | `SL.chat`: the Radio rail (manager conversation, read-only team feed, plan box, hold chip, composer). | KEEP |
| `85-ui-approvals.js` | The approval question and the cross-session inbox, with the quiet-period rule for answering. | KEEP |
| `86-ui-overlays.js` | Modal sheets, toasts, in-page confirm, icons, the reduced-motion switch (`ui.still`). | KEEP |
| `87-ui-sheets.js` | Sheets (goal, model, mode, effort, budget, permissions, trust, context, help, history, rewind, diff, login, MCP, skills, team) and dialogs (new session, resume, rename). Reads the catalogues of `SL.D`; changes state only through `SL.act`. | KEEP |
| `88-ui-drawer.js` | The per-agent drawer (details, tool log, mail, cache) and the layer-stack drawing. Reads `SL.D.code`, `orderCode`, `layers`, `layerToks` (sample prompt layers and code). | KEEP |
| `90-views-a.js` | The Cache, Mail, Board and Replay views. | KEEP |
| `91-views-b.js` | The Sessions view: live sessions, recorded sessions, prune form. Reads `SL.sessions` (`list`, `recorded`, `prune`, `pruneCandidates`). | KEEP |
| `92-runner.js` | `SL.runner`: a form from the command's flags, the command line, a streaming output pane, a result card. Stays: form, command line, output pane, result card. Goes: `BUILTIN` simulated outputs and the use of `SLDATA.outputs` in `exec`. | SPLIT |
| `93-palette.js` | `SL.palette`: command registry (slash commands, views, sessions, `sleipnir` commands), the palette dialog, the `/` menu source, `@` file completion from the sample trees. | KEEP (file completion reads `SL.D.treeShop` and `treeOrders`: swap for the workspace API) |
| `94-keys.js` | Global keyboard handling and the Escape precedence. | KEEP |
| `95-kit.js` | The `?kit` component catalogue view; also defines `ui.popover` and `ui.ringSvg`, which no other module uses. | KEEP (development view; may be dropped with the `?kit` branch of `99-app.js`) |
| `96-ui-nav.js` | The navigation rail, the Radio rail's collapse and resize, the `g` chords, the phone bottom bar. | KEEP |
| `96b-ws-data.js` | `SL.ws`: file text, status, ownership, blame, tree rows and hunks at any checkpoint, derived from the data pack's change sets (`SL.D.extra.files`). | REPLACE-LIVE (the `SL.ws` methods are the contract of the workspace API) |
| `97-ui-workspace.js` | The Workspace view: Files, Changes and Checkpoints as three tabs, the time-travel scrubber, the diff with a per-line gutter, hunk revert, restore. Reads `SL.ws`; changes state through `SL.act`. | KEEP |
| `98b-ui-tools.js` | Tools: the command catalogue and the purpose-built Doctor and Schedule views. Reads `SL.D.spec`, `SL.D.schedule`, `SL.D.extra.cron`, `doctor`, `projects`. | KEEP |
| `98-ui-settings.js` | Settings, eleven pages in three groups. Reads `SL.D.models`, `roles`, `testsPreset` and `SL.D.extra` (permissions, skills, MCP, trust, providers, hooks, config); changes state through `SL.act`. | KEEP |
| `99-app.js` | Boot: settings, sessions, the shell scope, the first view, the frame loop; the `?view`, `?session` and `?kit` parameters. | KEEP (start-up gains the API handshake) |

## Seams

1. **Catalogues (`SL.D`)**: roles and role order, prices, models, providers, role models, modes, rules, tests presets,
   trust files, MCP servers, skills, shortcuts, config layers, schedule, the command spec, slash commands, prompt layers.
   `SL.D.extra` carries the data pack's richer sections used by Settings, Tools and the workspace. Every field the code reads
   is a `D.<name>` or `X.<name>` in the files above.
2. **Sessions (`SL.sessions`, `Session`)**: `list`, `active`, `get`, `activate`, `needs`, `recorded`, and the `Session` fields
   `id name kind meta roster log wt vt widx idx wm m ui hold replay hist`. Events enter only through `Session.add`;
   `{t, k, ...fields}` with `t` in session seconds and `k` one of the kinds listed in `30-model.js`.
3. **Actions (`SL.act.*`)**: the names are used by the renderers, the palette and the keyboard. A result is `{ok, why}` where an
   action can refuse. The `action` bus event follows every call.
4. **Workspace (`SL.ws`, `SL.fileOwner`)**: `info`, `setAt`, `textAt`, `touches`, `statusOf`, `blame`, `hunks`, `counts`, `rows`, `liveFile`, `pending`, `filesFrom`, `lineOps`, `lines`.
5. **Runner**: `exec(command, formState)` returns `{lines, exit, ms, card?}`; a line is `{k, t}` with `k` in `out err dim head ok warn bad`. The pane plays the lines back on a timer; a live result arrives as a stream of the same lines.
6. **Time**: the world clock advances one second per wall second for every session; the view clock is the governor's
   (`20-clock.js`). A live feed delivers events with their own `t`; the reducer, the hold and the catch-up logic do not change.

Text that came from a tool, a file, mail or a model is data: it reaches the page through `U.esc` (`00-util.js`) or
`textContent`, never as markup. A live client keeps that rule for every string the API returns.
