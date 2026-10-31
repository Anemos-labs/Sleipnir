---
name: design-audit
description: Browser audit of a built page, no model: script errors, empty charts, hidden text, overflow, contrast, generated-UI tells.
license: MIT
author: Sleipnir; the rule ideas follow the anti-pattern detector of Impeccable by Paul Bakaus (github.com/pbakaus/impeccable, Apache-2.0); no code is copied
---

# Design audit

A screenshot shows you a page. You can still miss an empty chart, text pressed against the screen edge, or a card grey
on grey. A model reading the image misses them more often. This audit loads the page in a real browser and measures
what was rendered. It needs no model and runs in seconds, so run it every time, not once.

## Run it

From the project that has Playwright installed (`npm i -D playwright && npx playwright install chromium` if it has not):

```sh
npm run build && npx vite preview --port 4173 &   # or the project's own dev or preview server
node ${SLEIPNIR_SKILL_DIR}/audit.mjs http://localhost:4173/ http://localhost:4173/settings
node ${SLEIPNIR_SKILL_DIR}/audit.mjs http://localhost:4173/ --dark         # again in the dark theme, when there is one
node ${SLEIPNIR_SKILL_DIR}/audit.mjs site/index.html --widths 390,768,1440 # a static file, other widths
```

The default widths are 390 and 1280 px. `--json` prints the findings as JSON. The exit status is 1 when a defect was
found, so the audit can gate a script or CI job. Audit every route you built or changed, in each theme.

## Defects: fix every one before you report the work done

| Rule | What it means |
|---|---|
| `script-error`, `load-failed` | An uncaught exception, a console error or a failed request. Fix this first: it often causes the defects below. |
| `scroll-incomplete` | The page is over 100 screens tall, so the audit stopped scrolling: what is revealed only further down was not checked. Check that part another way, or make the page shorter. |
| `empty-graphic` | A chart-sized `svg` or `canvas` that draws no data marks. Axes and labels with no bars count as empty. |
| `content-hidden-at-rest` | Over 20% of the text stays invisible after load and a full scroll: a reveal animation that never ran. |
| `horizontal-overflow` | The page is wider than the viewport, so the phone scrolls sideways. |
| `text-at-screen-edge` | Text painted closer than 16 px (12 px on a phone) to the screen edge: a missing container gutter. |
| `clipped-text` | Text cut off by `overflow: hidden` with no ellipsis. |
| `low-contrast` | Below WCAG AA: 4.5:1 for body text, 3:1 for large text. |
| `tiny-text` | Text under 12 px. |
| `unlabelled-control` | An input, select or button with no accessible name. |
| `skipped-heading` | A heading level skipped (h1 then h3). |
| `broken-image` | An image with no `src` or one that failed to load. |

## Tells: weigh each one, and keep it only if it was a deliberate choice

These are the patterns that generated interfaces fall into by default. Any one of them can be right. A page that has
several reads as a template.

- `overused-font`: Inter, Roboto, Geist, Poppins, Montserrat, Open Sans, Fraunces, Plus Jakarta Sans or Space Grotesk as
  the main face. Choose a face that suits the product's voice.
- `purple-gradient`, `gradient-text`, `glow`: violet-to-blue gradients, gradient-filled headings, coloured halo shadows.
  Use flat colour from a deliberate palette and neutral elevation.
- `side-stripe`: a thick coloured border on one side of a card. `nested-cards`: a bordered or shadowed card inside
  another. Use spacing, type and dividers instead.
- `icon-tile-stack`: a rounded square icon tile above every feature heading. Put the icon beside the heading, or drop it.
- `bounce-easing`: overshooting or elastic easing. Ease out instead (`cubic-bezier(0.22, 1, 0.36, 1)`).
- `tight-leading`, `long-lines`: body line height under 1.3, or lines longer than about 95 characters.
- `em-dash-overuse`, `filler-copy`: em-dash cadence and words like "supercharge", "seamless" or "cutting-edge". Say
  what the product does, in plain words.

## Then

Fix the defects, rerun the audit until it reports none, take the screenshots again, and put the final audit summary
in your result: the URLs, the widths and themes, and the line `0 defects`. A reviewer should run the audit too, not
take the summary on trust. The audit does not replace looking at the page. It covers what a quick look misses.
