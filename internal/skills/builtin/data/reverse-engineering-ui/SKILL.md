---
name: reverse-engineering-ui
description: Tear down a live site or web UI into an evidence-backed spec (layout, tokens, components, states, network) for an original rebuild. Use to clone or match one.
license: MIT
author: Sleipnir; the measured capture and comparison follow the website checks of Craft Anything (github.com/storytold/craft-anything, MIT OR Apache-2.0); no code is copied
---
# Reverse engineering a web UI

Read the `reverse-engineering` skill's boundaries first: rebuild behaviour and structure with your own code, assets,
name and copy. Never ship the site's images, icons, fonts, logos or text; never bypass logins or bot protection.
Visit the pages a normal visitor can, at a normal pace.

The output is a teardown folder in the project (for example `docs/research/<site>/`) with the capture, a tokens
file and a spec, and then an implementation measured against it.

## 1. Capture, with a real browser

`capture.mjs` (beside this file, Playwright only) records what a visitor sees, as measurements: run it from a project
that has Playwright installed (`npm i -D playwright && npx playwright install chromium` if it has not).

```sh
node ${SLEIPNIR_SKILL_DIR}/capture.mjs https://example.com docs/research/example/capture --states --har "$(mktemp -d)"
```

It visits the start page and up to six same-origin pages it links to (navigation links first; `--routes N` changes
the number) at 390, 768 and 1440 px (`--widths`), and writes for each page a full-page screenshot per width,
`aria.yml`, and `capture.json`: the layout measured at each width (page width, heading and body text sizes, and for
each section its padding, gap, items per row and content column), the design values the page uses most (colours in
hex, fonts, text sizes, spacing, radii, shadows, `:root` custom properties), the outline, headings, links, forms,
picture sizes, accessibility basics, the errors and failed requests it logs, and the parts that move by themselves.
`--states` opens each menu, pop-up, disclosure and tab one at a time at the widest and narrowest width and
photographs it into `states/`. `--dark` captures the dark scheme. Other pages go to `routes/<page>/`, listed in
`routes.json`. Raw HARs can hold cookies, tokens and personal data, so `--har` takes a folder outside the repository.

Read every screenshot it writes: images are returned as images, and measurements alone miss what a page looks like.
Then capture what the script cannot reach by itself: hover and focus on interactive elements, toasts, empty, loading
and error states, a filled and an invalid form, and reduced motion. Name each screenshot after the state.

## 2. Turn the measurements into design tokens

Measure, do not eyeball: take values from `capture.json`, not from a look at a screenshot. Its `design` lists are
ordered by use, and `vars` holds the custom properties declared on `:root`, which often are the design system.

From those lists, write `tokens.json` (or the project's token format): a colour palette with roles (background,
surface, text, muted, accent, border, danger), a type scale, a spacing scale (usually multiples of 4 or 8), radii,
shadows, breakpoints and motion durations and easings. Rare one-off values are usually accidents; leave them out.
Pick your own fonts with similar metrics instead of shipping theirs.

## 3. Inventory the components and layout

- Page regions and the grid: max content width, columns, gutters at each breakpoint, what reflows or hides.
- Components: buttons (sizes, variants, states), inputs, cards, navigation, tables, modals, tabs, toasts. For each:
  anatomy, variants, states, keyboard behaviour, and the accessibility role and name from the ARIA snapshot.
- Interactions: what each control does, transitions (duration, easing, what moves), scroll effects, focus
  management in dialogs, shortcuts.
- Content model: what data each view shows, its sort order, pagination, empty and limit cases.

## 4. Network and data

Open the raw HARs in the temporary directory (they are JSON) and never commit them; only the sanitized shapes go
into the teardown folder. For each user action list the requests it makes: method, path pattern, query
parameters, request and response shapes, status codes, caching headers, polling or WebSocket use. Write the shapes,
not the values, and strip anything personal. These become the API your rebuild serves; design it cleanly rather
than copying quirks you did not observe a reason for.

## 5. Spec, then build

Write `spec.md` from the evidence: pages and flows, components with states, tokens, responsive rules, data and API,
accessibility requirements, and acceptance checks (for example "the command palette opens on Ctrl+K and Cmd+K,
traps focus, filters as you type, Escape closes and restores focus"). Each item cites a screenshot or HAR entry.

Build it with the project's stack and the extracted tokens, using original copy, icons (an open icon set) and
imagery.

## 6. Compare

Serve the rebuild (a preview of its production build, or its built files), capture it with the same command and
options into another folder, and compare the two:

```sh
node ${SLEIPNIR_SKILL_DIR}/capture.mjs http://localhost:4173/ docs/research/example/build --states
node ${SLEIPNIR_SKILL_DIR}/compare.mjs docs/research/example/capture docs/research/example/build
```

`compare.mjs` pairs the pages by path and prints, per page, **claims** that must hold (the page exists; it does not
scroll sideways at a width where the original does not; it logs no errors and fails no requests the original does
not; it has the original's landmarks), **differences** with the exact values on both sides (text sizes, each
section's padding, items per row and content column, the colours, fonts, sizes and spacing the original uses most,
the order of the page's parts, accessibility basics) and what was **not compared** (a page or width missing from a
capture). It exits 1 while a claim fails or anything was not compared: a part that was never measured never passes.
Differences do not fail by themselves. Fix each one the spec says should match, with the value it gives; keep the
ones that make the rebuild your own, and say which in the spec.

Then put each pair of screenshots side by side (read both images) for what numbers do not show: imagery, icons,
alignment and the states. Parts listed as moving by themselves (videos, canvases, animations) hold one frame in a
screenshot: match how they look and move, not the frame. A pixel diff (`pixelmatch`, Playwright's
`toHaveScreenshot`) is useful for regressions in the rebuild, not as a target of identity with the original.

Run the comparison again after each round of fixes and keep its last report with the teardown. The work is done when
it exits 0 and every remaining difference is one you chose.
