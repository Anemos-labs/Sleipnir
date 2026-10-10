# Core v2: the shared Cockpit engine (Phase 1 of round 2)

You are the author of `02-cockpit.html`. The owner loves it and wants five Cockpit-based variants. Five builders will
start from **your core**, so everything that must be right once (the engine) is yours now, and it must be modular,
documented and boringly reliable. You do not build the variants' layouts or the long tail of settings screens.

Read `10-v2-shared.md` completely (feedback, Team v2: eight workers, no drawn rider, the numbers, the engine
requirements: time governor, view lifecycle, multi-session, store/actions, generic command runner). A data pack is being
produced in parallel by another builder in `$S/work/data/` (`data.js`, `outputs.js`, `cli-spec.json`; schema in
`$S/brief/11-data-pack.md`). Do not wait for it: define what you need through a thin adapter (`core/src/js/data-adapter.js`) that
reads `window.SLDATA` when present and falls back to a small built-in fixture, so the core works with or without it.

## Deliverables
1. `$S/work/core/` : the modular source.
   * `src/css/*.css` (tokens, base, components, hud, horse, stalls, panels, ...), `src/js/*.js` (see modules below),
     `src/html/shell.html` (the default Cockpit shell), `build.mjs` (concatenates in the order of `manifest.json`, which lists
     css and js files and the shell; a variant can swap or add files by editing a copy of the manifest) and `go.sh`.
   * `ARCHITECTURE.md` : module map, load order, state shape, action list, event types, the **View/scope API**, the
     **governor API** (`SL.time`), the **session API**, the **runner API**, extension points ("how a variant adds a
     panel/view/screen"), the **component catalogue** (class names + a tiny HTML sample for: buttons, segmented control,
     toggle, input/select/textarea with labels, table, list rows, chip, badge, tabs, dialog, sheet, toast, tooltip,
     popover, meter, sparkline, ring, stack bar, diff view, terminal output pane, form field groups), the tokens
     and the type scale, and the test hooks.
   * Modules (names are yours but keep this separation): `clock.js` (sim clock + governor), `bus.js`/`store.js`
     (state, actions, subscribe), `sessions.js` (multi-session registry + the three scripted timelines),
     `view.js` (View mount/unmount + scope), `ui-*.js` (HUD, horse, stalls, gantt, board, queue, mail, governor gauge, rail),
     `chat.js` (transcript, composer, slash/@ menus, queue, steer, history, hold chip, digest rows), `approvals.js`,
     `palette.js`, `runner.js` (+ its form generator from `cli-spec.json`), `a11y.js`/`keys.js`.
2. `docs/design/web-mocks/v2/00-core.html` : the built baseline, a **working app** (it is the "Cockpit v2" you would
   hand to the owner today): all of the round-1 Cockpit screens (Cockpit, Cache, Mail, Board, Replay, Sessions) updated, plus
   a session strip with the three live sessions and New/Resume dialogs, the per-agent drawer with **Steer / Interrupt** and a
   channel list (manager + 8 workers) in the Radio rail, the approvals inbox, the generic runner reachable from the
   palette, a minimal Settings page (mode, model, budget, hover behaviour, motion) proving the store round trip, and the
   `?kit` view showing the component catalogue.
3. Screenshots of every state you verified under `$S/shots/core/`.

## Requirements (all verified by you, in scratch builds with hooks, shipped without hooks)
* **Roster v2**: manager card + 8 worker legs (nothing drawn on the horse) + the exact numbers (derive everything from one table; the Cache tab, stalls, drawer and
  HUD ring must agree to the percent at every moment, as the simulation adds tokens). Keep the hero exactly the horse of round 1 (NO rider, NO figure on it); eight legs in the eight workers' role colours (2x blue for the two backends is fine); `idle/done/ask/stuck`
  legs as before; the manager's own card separate; the ninth-worker leg-sharing badge works (`+1`).
* **Time governor** exactly as in the shared brief, including the hold chip + pin, digest rows, bounded catch-up, no
  auto-scroll while held, `Hover behaviour` setting, and the 45-minute test.
* **View lifecycle** exactly as in the shared brief, including the mail-arc test. Mail arcs, flashes, tooltips,
  popovers: all inside their view root. Document it as the one rule every variant must follow.
* **Multi-session**: the three fixtures simulated concurrently with independent event logs (deterministic scripts); the
  strip/tabs, switching without loss, background questions with badge + toast + inbox, New (all flags; creating a
  session starts a short scripted run: recon survey, plan, one worker), Resume (list with `↺`), Close/Stop (in-page confirm),
  Rename, prune dry-run/apply (if the data pack is absent use a built-in list). The single-agent cockpit (orders-api) is
  designed, not an afterthought; docs-sweep shows the **refused** approval (headless, nobody to ask).
* **Per-agent channels**: the Radio rail has a channel switcher (manager + 8 workers + a read-only Mail channel), each with its own
  transcript (tool calls, refused actions, diffs), Steer (sends guidance; the agent reacts in its next step), Interrupt
  (that agent only), state/scope/lease/cost/cache header. Hover linking from every message to legs, stalls, gantt rows,
  tasks, mail, files; hold-on-hover applies to every channel.
* **Store + actions** as listed in the shared brief; every action visible everywhere it should be. Include the
  `/` command table from `chatHelp` so each slash command dispatches its action (the long tail may open the runner).
* **Runner**: form from `cli-spec.json`, live command line, Run, streaming output pane, result card; works for every
  command in the spec when the data pack is present (outputs from `SLDATA.outputs`), and for at least `sim`, `sessions
  prune`, `trust`, `mcp list`, `doctor` with built-in sample outputs when it is not.
* **Keyboard**: ctrl+k and `/` palette, `?` overlay, alt+t/alt+g aliases, tab order, focus rings, `o c m b r s` view keys,
  question keys with the quiet period, Esc semantics (release hold, close overlay, interrupt: in that order of precedence).
* Everything of the round-1 Cockpit that was good stays: look, motion, hover linking, replay scrub, drawer, palette, phone layout.

## Verification you must do and report (numbers, not adjectives)
Governor: 45-min hold -> catch-up seconds, DOM node delta, state equality with a never-held run. 20 rapid hold/release
cycles: no drift. View lifecycle: 30 rapid switches: timers/listeners/nodes before vs after. Multi-session: switch among the
three 50 times while questions arrive: no leak, correct badges. Cache numbers agree at 5 sampled moments in every view.
Phone (390), 1024, 1440, 1920: no overflow. No console errors. `window.__sl`-style hooks absent in the shipped file.

Final message: short report: module map in 10 lines, how to start a variant from the core (3 commands), the verification
numbers, known gaps. Touch only: `$S/work/core/`, `docs/design/web-mocks/v2/00-core.html`, `$S/shots/core/`. No git. No Browser pane tools.
