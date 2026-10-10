# 02 · COCKPIT — "mission control for eight legs"

Deliverable: `docs/design/web-mocks/02-cockpit.html`

## The idea
Sleipnir's reason to exist is the **team**: one manager, up to eight workers, one shared cached prefix, a merge
queue that verifies their work. In the terminal the team is a screen you open (ctrl+g). Here **the team is the home
screen** and the conversation is a rail beside it. You do not read a transcript to know what is happening; you *see*
eight legs moving, the cache clock running down, the board advancing, mail passing, a question blinking.

Think F1 pit wall × flight deck × the logo. Data-dense, calm, legible from across the room, every pixel means something.
This is the paradigm for people who run swarms and care about cache and cost.

## Layout (multi-panel HUD, one viewport, no page scroll at 1440x900)
* **HUD bar (top)**: the goal title (truncated, click to open the plan), elapsed `00:38`, **budget gauge** `$0.08 of $5.00`,
  **hit-ratio ring** `84%`, **warm-clock ring** `0:25` that counts down, refills when a request lands, goes amber → red
  under 0:08 (the signature element: the provider's cache lifetime made visible), `rpm 44 · 429s 0 · retries 0`,
  `4 active of 8 workers`, connection chip `● 127.0.0.1:6969 · loopback · token ✓`.
* **Top-left hero (the horse)**: a large vector Sleipnir (SVG; drawn, not the pixel sprite: take the logo's horse and
  rebuild it as a clean technical illustration) whose **eight legs are the eight agents** in their role colours. A leg
  animates a walk cycle with speed ∝ activity; `idle` legs are dim, `done` legs planted and bright, `ask` pulses amber,
  `stuck` red. Above the horse sits the **shared prefix bar** G0..G5 (G0-G2 spanning, "one prefix, eight riders"),
  with thin connectors from the shared segment down to each leg.
* **Stalls (centre/right)**: eight agent cards in a 4x2 grid ("stalls"), each: role-coloured edge, id, role, state
  glyph + word, the `doing` line, task chip `T4`, scope glob, tokens, cost, a hit% sparkline (`be-2` shows the
  zero), and a mini state timeline. Hover a card → its leg, its gantt row, its tasks and its mail all highlight.
  Click → an **agent drawer** slides in (its transcript, its cache, its mail, `Steer`, `Interrupt`).
* **Lower band**: **swarm gantt** (last 60 s, per-agent rows, event glyphs `✉ ◆ ⚠`, a pause button and a scrub
  bar that turns the whole cockpit into replay: `▶ 1x 2x 4x`), **task board** (todo / running / verify / merged
  columns with T-cards, drag disabled, owner chips, dependency arrows T4→T7), **merge queue** (head `▸ T4`
  with its verifying command and elapsed), **mail** (envelope list; label "mail is data, not instructions"),
  **governor** (small gauges).
* **Right rail "Radio"**: the manager's conversation in a compact transcript, the goal plan as six ticking steps,
  the composer (`Message Sleipnir, / for commands, @ for files`), mode chip, and **the question** as a prominent request
  strip at the top of the rail (fe-1 · T6 · the command · why · three buttons · pause meter), which also lights
  fe-1's leg amber and its card.
* **Top tabs** (also keys `o c m b`, like `sleipnir watch`): `Cockpit · Cache · Mail · Board · Replay · Sessions`.
  Build **Cache** at real fidelity (per-agent prompt layer stack G0..G5 with read/paid split, hit ratio per request
  with breaks ⚠ and compactions ◆ marked, the compaction fold diagram, anomalies list with the TUI's explanation,
  an "every agent's cache" table with READ / PAID / WRITE / SAVED est.) and **Board** (a roomy kanban + dependency
  graph). Replay and Sessions can be simpler but designed.

## Look
* Deep near-black (`#07090f` / `#0b0e17`) with a faint blueprint grid and hairline panel borders (`#1f2437`); panels have
  small caps titles in a *condensed technical sans* and numerals in a *tabular mono*. Fonts (Google): e.g.
  `Barlow Condensed` or `Oxanium` for labels, `IBM Plex Mono` / `JetBrains Mono` for numbers; system fallbacks.
* Role colours used with **meaning**: glow only on state (a working leg, an open question, a cache break), never
  decoratively. Pink-red for the break flash, amber for warm/ask, green for done/hit.
* Motion: leg cycles, warm-clock ring, gantt scrolling, the prefix bar flashing on the cache break, mail envelopes
  sliding between agents (a short arc between the two cards). All stand still under reduced motion.

## Signature moments to nail
1. First paint: eight legs moving at different speeds, the clock ring ticking: the screen is *alive*.
2. The cache break on be-2: the prefix bar flashes, be-2's sparkline drops to 0, an anomaly chip appears with the explanation.
3. Hovering `be-2` highlights its leg, row, task and mail across every panel.
4. The question: the request strip, amber leg, answer `1`, fe-1's card turns `tool` and the leg trots on.
5. T4 verifies and merges: card slides verify → merged, merged count 4, be-1 goes idle, compaction ◆ appears on the gantt.
6. The Cache tab: layers stacked per agent, instantly legible.

## Avoid
Chat as the centre of the screen, light theme, skeuomorphic dials, neon everywhere, a generic KPI-card dashboard, pie charts.
