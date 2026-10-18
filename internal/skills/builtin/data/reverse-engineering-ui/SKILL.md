---
name: reverse-engineering-ui
description: Tear down a live website or web UI into an evidence-backed spec (layout, design tokens, components, states, interactions, network calls) and rebuild it as original code. Use when asked to clone or match a site or interface.
---
# Reverse engineering a web UI

Read the `reverse-engineering` skill's boundaries first: rebuild behaviour and structure with your own code, assets,
name and copy. Never ship the site's images, icons, fonts, logos or text; never bypass logins or bot protection.
Visit the pages a normal visitor can, at a normal pace.

The output is a teardown folder in the project (for example `docs/research/<site>/`) with screenshots, a tokens
file and a spec, and then an implementation checked against it.

## 1. Capture, with a real browser

Use Playwright (or the project's browser tooling). A headless Chromium is enough; save everything to the teardown
folder so it can be read back and cited.

```js
// capture.mjs: node capture.mjs https://example.com docs/research/example
import { chromium } from "playwright";
import fs from "node:fs/promises";
const [url, out] = process.argv.slice(2);
await fs.mkdir(out, { recursive: true });
const browser = await chromium.launch();
for (const [name, width] of [["mobile", 375], ["tablet", 768], ["desktop", 1440]]) {
  const ctx = await browser.newContext({ viewport: { width, height: 900 }, recordHar: { path: `${out}/${name}.har`, content: "omit" } });
  const page = await ctx.newPage();
  await page.goto(url, { waitUntil: "networkidle" });
  await page.screenshot({ path: `${out}/${name}.png`, fullPage: true });
  await fs.writeFile(`${out}/${name}.aria.yml`, await page.locator("body").ariaSnapshot());
  await ctx.close(); // writes the HAR
}
await browser.close();
```

Read every screenshot you take: images are returned as images. Text dumps alone miss what a page looks like.

Capture the states, not just the first paint: hover and focus on interactive elements, open menus, dialogs,
toasts, empty, loading and error states, a filled and an invalid form, dark mode
(`colorScheme: "dark"` in the context), and reduced motion. Name each screenshot after the state.

## 2. Extract design tokens from computed styles

Measure, do not eyeball. Collect the values the page actually uses and cluster them into a scale:

```js
// in page.evaluate: count computed values across visible elements
const props = ["color", "background-color", "border-color", "font-family", "font-size", "font-weight",
  "line-height", "letter-spacing", "border-radius", "box-shadow", "padding-top", "padding-left", "gap"];
const counts = Object.fromEntries(props.map(p => [p, {}]));
for (const el of document.querySelectorAll("body *")) {
  const r = el.getBoundingClientRect();
  if (!r.width || !r.height) continue;
  const cs = getComputedStyle(el);
  for (const p of props) { const v = cs.getPropertyValue(p); counts[p][v] = (counts[p][v] || 0) + 1; }
}
// also read the custom properties declared on :root, which often are the design system
const root = getComputedStyle(document.documentElement);
const vars = [...document.styleSheets].flatMap(s => { try { return [...s.cssRules]; } catch { return []; } })
  .filter(r => r.selectorText === ":root").flatMap(r => [...r.style]).filter(n => n.startsWith("--"))
  .map(n => [n, root.getPropertyValue(n).trim()]);
return { counts, vars };
```

From the counts, write `tokens.json` (or the project's token format): a colour palette with roles (background,
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

Open the HARs (they are JSON). For each user action list the requests it makes: method, path pattern, query
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

Screenshot the rebuild with the same script, the same viewports and states, and put each pair side by side (read
both images). Fix differences in layout, spacing, type and states that the spec says should match; keep the
differences that make it your own. A pixel diff (`pixelmatch`, Playwright's `toHaveScreenshot`) is useful for
regressions in the rebuild, not as a target of identity with the original.
