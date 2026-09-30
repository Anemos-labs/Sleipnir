# The mark

![Sleipnir](logo.png)

Sleipnir is Odin's eight-legged horse, and the mark is exactly that, drawn a little silly on purpose: a galloping horse with
**eight legs, one for each rider on the shared prefix**, each leg in the colour of a role (manager violet, backend and frontend
blues, tester green, reviewer amber, docs orange). The mane and the tail are the harness's own violet. The same colours colour
the roles in the terminal UI (`docs/UX.md`).

| File | Use |
|---|---|
| `logo.svg`, `logo.png` | the mark alone, on light backgrounds |
| `logo-dark.svg` | the mark alone, on dark backgrounds |
| `logo-wordmark.svg`, `.png` / `-dark.svg` | the mark with the name and the tagline, for headers |
| `favicon.svg` | the reduction for 16 to 64 px: a pale horse over eight bold leg bars (the detailed mark cannot be read that small) |
| `social-preview.png` | 1280 × 640, for the GitHub repository's social preview (Settings, Social preview: upload it there) |

Colours: ink `#24232e`, paper `#fbf8f1`, violet `#7048e8` (light) and `#b79cf9` (dark); the role colours are in `make_logo.py`.

Rules of thumb: keep clear space of one leg-width around it; do not recolour the legs, stretch it, add a fifth pair of legs or
remove one (eight is the point); do not use the wobble filter on anything else; below 48 px wide use `favicon.svg`.

Regenerate: `python3 make_logo.py && node render.mjs logo.html wordmark.html social.html favicon-test.html` (Python 3 and headless
Chromium through Playwright; the SVGs are plain paths plus one SVG filter for the hand-drawn wobble).

## The gallery

The recordings of the terminal interface are not drawings: each is the program's own screen, played from the event log of one recorded
session, `showcase/events.jsonl` (the shop demo: `sleipnir demo --scenario shop`, with the paths of the machine that made it replaced).
`gallery.json` lists them (which screen, how big, which stretch of the session, at which second the still is taken), and the same
manifest is read by `sleipnir replay --gallery`, by `scripts/record-demo.sh` and by the test that checks the committed files.

| File | What it shows |
|---|---|
| `swarm.svg`, `swarm.png` | the swarm cockpit over the whole session: the shared prefix and its riders, mail, a stuck agent, the provider losing its cache, the merge queue sending a piece back |
| `cache.svg`, `cache.png` | the cache of one agent (be-2) from its start: layers, the clock, the hit ratio with its marks, a compaction while warm, the break and what it cost |
| `fold.svg`, `fold.png` | another agent's (be-1) compaction at a cold moment, after a build that outlasted the cache |

The SVGs are animated with CSS only (no script: they play in a README and in any browser), identical frames are merged, and the file is a
function of the log and the manifest, so `scripts/record-demo.sh --check` (and `go test ./internal/tui/app`) fail when a change to a
screen or a widget has not been recorded again. The PNG stills are taken from the SVGs with headless Chromium (`scripts/svg2png.mjs`).
To make a new session: `scripts/record-demo.sh --new-session` (about twenty seconds, no key), then read the diff of the pictures and commit.
