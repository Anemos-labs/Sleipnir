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
