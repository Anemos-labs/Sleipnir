# Round 2, Phase 2: the five Cockpit variants (shared process and acceptance)

Read `00-shared.md`, `10-v2-shared.md` (feedback, Team v2, engine requirements, **functionality floor**), then your concept
file. The core (`$S/work/core/`, built baseline `docs/design/web-mocks/v2/00-core.html`, `ARCHITECTURE.md` with the
component catalogue) and the data pack (`$S/work/data/`: `data.js`, `outputs.js`, `cli-spec.json`, `DATA.md`) are done and
verified. **You inherit them; you do not rewrite the engine**: time governor, View/scope lifecycle, store/actions,
sessions, runner, roster and numbers. If you find an engine bug, fix it in YOUR copy, keep the fix small, and name it in
your report so it can flow back.

The owner **loves Cockpit's look and animation** (HUD, drawn horse with eight legs, stalls, rings, gantt,
blueprint-dark, technical-condensed type, meaningful glow). Keep that identity. What differs between the five variants is
the **information architecture**: where things live, what is the centre of gravity, how the functionality from the other
round-1 mocks (Longhouse's sessions and per-member chat, Forge's Files/Changes/Checkpoints, Console's keyboard and
palette) is integrated. The owner's complaint about round 1 was *usability*: so the test of your variant is that a
person could run real work in it for a day. Depth beats decoration; but the beauty stays.

## Process
1. Set up: `cp -r $S/work/core $S/work/NN`; edit your copy of `manifest.json`; add your own `src/` files (your shell, your
   views, your CSS). Pull the data pack in through the core's adapter (`SLDATA`) and build with `build.mjs` to
   `docs/design/web-mocks/v2/NN-name.html` (NN and name are in your concept file). Touch no other repo file. No git. No Browser
   pane tools. Shot driver: `. ~/.local/sleipnir-toolchains.sh; node $S/shot.mjs <html> --w 1440 --h 900 --step ...`
   (see its header). Save review screenshots in `$S/shots/NN/`.
2. **Look at what you make** and iterate at least five rounds (every view, dialogs, the question answered, hold/release,
   session switch, 1280x720, 1024x700, 390x844). Critique like an art director and like a daily user: could I find it, could
   I undo it, does it tell me what happened?
3. **Run the acceptance checklist below in a scratch build (hooks allowed), fix, and report each line as PASS or the
   reason it is not.** Ship without hooks.
4. The file budget is <= 2.5 MB (inlined data included). Fonts: Google Fonts link only, with fallbacks (core already has it).
   No console errors. No horizontal scroll 390..1920.

## Acceptance checklist (report every line)
A. Feedback items: (1) hover hold: pointer over chat -> view eases to a stop in <= ~600 ms, linked parts highlighted and still;
   release -> bounded catch-up (45-min hold: <= 4 s, bounded DOM, state equals a never-held run); `Hover behaviour`
   setting works; chip + pin + Esc. (2) 30 rapid view switches with an arc in flight leave no orphan node/timer/listener.
   (3) three sessions, switch 50x with questions arriving: correct badges, nothing lost; New / Resume / Stop / Rename / Prune
   work; inbox answers a background question. (4) manager rides, **8 legs = 8 workers**, `manager + 8 workers`, ninth worker shares
   leg 1 with `+1`. (5) per-worker channel: transcript, Steer reaches only that worker, Interrupt only that worker. (6)
   Files/Changes/Checkpoints tracker: tree with ownership stripes + leases + protected paths, Changes by task/agent, Checkpoints
   Diff/Restore with preview + confirm, attribution gutter, hunk revert, scrubber. (7) Numbers agree between HUD, stalls,
   cache table, stats page, drawer at 5 sampled moments (script it).
B. Floor: items 1-12 of "Functionality floor" in `10-v2-shared.md`, each exercised once by script or by hand (state the
   action and the visible result). **No palette entry opens a help-text stub**: run through ALL palette entries and report any
   that is not a working screen/form/runner.
C. Safety semantics: quiet-period meter and inert buttons; typed-ahead never answers; shift+tab never reaches bypass/yolo;
   bypass/yolo need typed confirmation and look dangerous; mail/tool output rendered as text (try `<img onerror>` in a mail
   body in a scratch build: it must show as text); manager's refused edit shown.
D. Responsive/a11y: 390 / 768 / 1024 / 1280 / 1440 / 1920 clean; keyboard-only run of: open palette, switch session, answer
   the question, steer a worker, open a diff; visible focus; contrast >= 4.5:1 for text (script a DOM audit).
E. Performance sanity: CPU idle at rest < ~25% of one core on a hold (measure with the driver if you can); 10 minutes
   of simulated run (time-warp) does not grow the DOM beyond a bound.

## Final report (short)
The concept in two sentences; how to try it (the 8 things worth clicking, in order); what is real vs scripted; the
checklist as PASS/limit lines; web-only affordances beyond the CLI (so the owner can weigh them; anything that needs a
harness API the CLI lacks today, e.g. steering one worker, must be flagged "needs harness support"); engine fixes you made;
known flaws; screenshot paths.
