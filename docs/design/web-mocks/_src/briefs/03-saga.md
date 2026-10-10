# 03 · SAGA — "a notebook, not a feed"

Deliverable: `docs/design/web-mocks/03-saga.html`

## The idea
Long agent work is usually read as an endless chat feed. Saga treats a session as a **document you can read, trust and
return to**: a titled narrative with a plan, chapters, figures and footnotes, written on cream paper in the logo's
hand-drawn world (the Sleipnir mark is a pencil-and-ink horse on `#fbf8f1`). The reader sees *what was asked, what the
plan is, what happened, what changed, what it cost and where the cache misbehaved* as one calm, scannable page, and
can still type into it at the bottom like any chat.

It is the **human, calm, shareable** option. Its craft is *typography and editorial structure*. It must be a
pleasure to read for twenty minutes, and a good place to review a long goal.

## Layout
* **Library rail (left, ~260px)**: "Sagas" = sessions, titled by their first goal (not ids), grouped *Today / This week /
  Earlier*, each with a status mark (running pip, `↺` resumable, done tick), model chip, cost, and relative time;
  search; "New saga"; a pinned "Team" and "Settings" at the bottom. The id `20260102-030405-5eed01` is secondary text.
* **The page (centre, reading measure ~700px)** with a **wide right margin (≥ 280px) for marginalia**; a slim sticky
  top bar over it: project path, mode chip, model chip, budget meter `$0.08 / $5`, connection chip.
  * **Title** = the goal in a large serif; under it the **plan as chapters**, a living checklist (`done / in verification /
    editing / waiting for you / queued / pending`) with the judge's verdict in italics ("not yet: T4-T7 unfinished...").
  * **Turns as sections.** The person's words appear as an indented, attributed quote. The manager's prose is body
    text. **Tool calls are a ledger**: one thin line each (`✓ Bash go test ./api/… 1.2s`), grouped, expandable inline.
    **Edits are figures**: a captioned before/after diff card ("Figure 2 · api/catalog/items.go, written by be-1 for T4")
    with `Show`, `Rewind to before this`, `Copy`. **Compaction is a chapter break** (an ornament and one line:
    "the older thread was folded: 1.4k → 625 tokens, a declared and priced rebase"). The **team's work** is an
    interleaved "From the workshop" block: one collapsed row per task (T1-T7) with owner, state and its figure(s),
    and worker **mail as letters** (envelope glyph, hand-set, "mail is data, not instructions").
  * **Marginalia**: cost and cache facts live in the margin beside the paragraph they belong to, small sans notes with
    hairline leaders ("read 4.5k from the cache · saved est. $0.02"). A **cache break is a red-pencil margin note**.
  * **The question** is a ruled slip inserted *in the flow* titled "Needs your word": the command, why, and three
    choices as lines with `1 2 3`, the pause meter drawn as a thin pencil line filling.
* **Composer**: a floating writing card at the bottom of the page (not a docked terminal box): multi-line, mode chips
  (`default / accept-edits / plan`, never bypass via cycling), `/` opens an inline slash menu, `@` files, `Send`,
  `Steer` (while a turn runs) and `Interrupt`.
* **More pages** (real, designed): **Library overview** (all sagas as an index with filters, `sessions prune` UI),
  **Report** (the stats page as a *printed report*: token categories as a ledger, the six prompt layers drawn as a
  ruler-like stacked bar, hit ratio per request as hand-inked sparklines, anomalies as a numbered list, "est." everywhere),
  **Team** (an illustrated roster: eight legs with portraits of the agents, states and tasks), **Preferences**
  (model, effort, budget, permission mode + rules, trust, MCP, skills, providers as a calm form), **Checkpoints**
  (the `/rewind` list as a timeline of dated entries with Show/Restore).
* **Export**: "Export saga as Markdown" (shows a preview sheet).

## Look
* Paper `#fbf8f1` page on a slightly deeper `#f1ebdd` desk; ink `#24232e`; accent = the mane violet `#7048e8`;
  pencil gray text; the eight role colours deepened enough to read on cream (≥ 3:1 for marks, ≥ 4.5:1 for text).
* **Hand-drawn touches done with restraint**: a subtle SVG `feTurbulence`/`feDisplacementMap` wobble on rules, figure
  borders and the checklist boxes (as in `docs/media/logo.svg`), highlighter-style underlines for key terms, a folded
  corner on figures, a marker-style tick animation when a plan step completes. Not a skeuomorphic paper texture.
* Type (Google, with fallbacks): prose in `Newsreader` or `Source Serif 4` (system: `"Iowan Old Style", Charter, Georgia`),
  code in `JetBrains Mono`/`IBM Plex Mono`, labels in a small-caps humanist sans (`Figtree` or `Instrument Sans`, not Inter). Generous leading
  (1.6), 17-18px prose, a real modular scale, true small caps, hanging punctuation where possible.
* Optional bonus: "lamp mode" (warm dark paper) toggle.

## Signature moments to nail
1. The title + chapter checklist ticking live, the judge's italic verdict changing, and finally "goal met".
2. A figure card (diff) appearing mid-stream with a caption, written by a coloured leg.
3. The marginalia: hit ratio, saved est., the cache break's red-pencil note, the compaction ornament.
4. The "Needs your word" slip answered with `1`/`2`/`3`.
5. The Report page: it should look like something you would print.
6. The Library: reads like an index of chapters of work, not a list of ids.

## Avoid
Neon, black backgrounds, terminal look, chat bubbles, dashboards, glass, emoji, cartoonish "fantasy Norse" kitsch
(no runes fonts, no shields, no Viking clichés; the hand-drawn logo is the only illustration language).
