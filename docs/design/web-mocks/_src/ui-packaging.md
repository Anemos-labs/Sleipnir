# UI packaging and screenshot parity

`internal/web/ui/` is the approved single-file mock (`docs/design/web-mocks/v3/sleipnir-web.html`) split into plain files that
`sleipnir web` embeds and serves. There is no build step: what is in the directory is what the browser loads. This note records
how the split was made, what the Content-Security-Policy requires of the page, how to serve it without the Go server, and how
`scripts/web-parity.mjs` proves that a page equals the mock, pixel for pixel.

## 1. Layout

```
internal/web/ui/
  index.html            the shell of the mock, linked CSS, classic scripts; no inline script or style
  css/*.css             the mock's 22 CSS parts, byte for byte, one <link> each, in the mock's order
  js/00-namespace.js    window.SL = {}
  js/data.js, outputs.js, cli-spec.js     the data pack
  js/NN-*.js            the 30 modules of the mock, byte for byte, in the mock's manifest order
  fonts/                14 woff2 files, fonts.css, the three OFL licences
  DEV-ONLY.md           the simulation and sample-data modules a live client replaces
  js-inventory.md       one line per module: purpose, KEEP or REPLACE-LIVE
```

| Part | Source | Relation |
| --- | --- | --- |
| `css/*.css` | `_src/v3/src/css/*.css` listed in `_src/v3/manifest.pack.json` | identical files |
| `js/NN-*.js` | `_src/v3/src/js/*.js` listed in the manifest | identical files |
| `js/data.js`, `js/outputs.js` | `_src/data/data.js`, `outputs.js` | identical files |
| `js/cli-spec.js` | `_src/data/cli-spec.json` | the JSON as `window.SLCLISPEC = {...};`, as `build.mjs` inlined it |
| `fonts/*` | `_src/fonts/*` | identical files |
| `index.html` | `_src/v3/src/html/shell.html` and `favicon.svg` | the shell between a head with links and a body end with script tags |

The mock was one function holding `const SL = {}`, the data pack, the JSON and the modules. As separate scripts `SL` must
outlive each file, so `00-namespace.js` creates `window.SL` and every module, which keeps its own function scope, reads it as a
free variable. The load order is the order of the mock's concatenation: namespace, `data.js`, `outputs.js`, `cli-spec.js`, then the
modules by manifest (`96-ui-nav.js` before `96b-ws-data.js`). Nothing from the mock's test build (`zz-test-hooks.js`) is included.

## 2. Content-Security-Policy

The server sends, on every response (`contentSecurityPolicy` in `internal/web/envelope.go`; `scripts/web-ui-dev.mjs` sends the same
string):

```
default-src 'none'; script-src 'self'; style-src 'self'; style-src-attr 'unsafe-inline'; font-src 'self';
img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; form-action 'none'; base-uri 'none'
```

What that forbids and what the packaging changed:

| Violation in the single file | Change | Visible effect |
| --- | --- | --- |
| One inline `<style>` holding all CSS | 22 `<link rel="stylesheet">` to `css/*.css`, same order, same bytes | none: the ordered CSSOM rules are identical |
| One inline `<script>` holding data, modules and the CLI JSON | 34 `<script src>` (classic, in order, at the end of `<body>`), `SL` as `window.SL` | none |
| Google Fonts: a stylesheet from `fonts.googleapis.com` (outside `style-src 'self'`), two preconnect hints, font files from `fonts.gstatic.com` (outside `font-src 'self'`), all of them requests to another host | `fonts/fonts.css` with relative `url(...)` to the embedded woff2 files; the preconnects are gone | none: same family names, weights, styles, unicode ranges and `font-display` (the same 14 `FontFace` entries); the embedded files are the ones Google served |
| `favicon` as a `data:` URI in `<link rel="icon">` | kept: `img-src` allows `data:` | none |
| `window.SLCLISPEC = {...}` assigned inside the one script | a file, `js/cli-spec.js`, with the same assignment | none |

Checked and found compliant, so unchanged: no `eval`, `new Function`, string `setTimeout`/`setInterval`; no inline event-handler
attributes and no `javascript:` URLs; no `<style>` element created from script; no `Blob`, `blob:` or object URL; no
`fetch`, `XMLHttpRequest`, `WebSocket`, `EventSource`, `importScripts`, `window.open` or iframe; the one `<form>` (the composer) is submitted by script (`preventDefault`), and `form-action 'none'` would refuse a native submission; no remote
URL in markup, CSS or script (the SVG namespace string is an identifier, not a request); SVG paint references are
document-local (`url(#id)`). Style *attributes* (`style="--c:var(--fe)"`, set through markup or `setAttribute`) and
`element.style`/CSSOM are allowed by `style-src-attr 'unsafe-inline'` and are used widely. The clipboard helper calls
`navigator.clipboard` and swallows a refusal.

Rules for code added to the UI (a live client included):

* no inline `<script>`, `<style>`, event-handler attribute or `javascript:` URL; no `eval` or `new Function`;
* requests go to the page's own origin (`connect-src 'self'`): `fetch` and `EventSource`, no other scheme;
* images are same-origin files or `data:` URIs; fonts are same-origin;
* style through classes, style attributes, `element.style` or constructed stylesheets; never a generated `<style>` element.

`web-parity.mjs` installs a `securitypolicyviolation` listener before the page's own scripts, and reports a violation (and
every console error, uncaught exception and failed request) of the page under test as a failure of the scene.

## 3. Serving without the Go server

```
node scripts/web-ui-dev.mjs [--port 8686] [--dir internal/web/ui]
```

A dependency-free static server on loopback with the CSP above and exact content types (`font/woff2`, `image/svg+xml`,
`text/javascript`, ...), `nosniff`, `no-store`; `/api/*` answers a JSON 404; only GET and HEAD; a path is resolved inside the
directory. It has no token, cookie or API. It is for loading and screenshotting the UI, not for security testing of the Go server.

## 4. Screenshot parity

```
node scripts/web-parity.mjs                          # A = the mock, B = internal/web/ui; exits 1 above a threshold
node scripts/web-parity.mjs --b http://127.0.0.1:6969/   # B = a running server
node scripts/web-parity.mjs --self                   # A against A: the method's own noise, must be 0
node scripts/web-parity.mjs --only settings --viewport 390x844 --images all
node scripts/web-parity.mjs --selftest               # PNG codec and comparison on synthetic pictures
```

Options: `--scenes`, `--only a,b` (name contains), `--viewport WxH`, `--threshold PCT` (overrides every scene), `--tolerance N`
(a channel may differ by up to N), `--profile NAME` (adds `maskProfiles.NAME` to every mask), `--jobs N`, `--out DIR`
(default `dist/web-parity`), `--images diff|all|none`, `--json FILE`, `--fonts DIR`, `--chrome PATH`, `--list`.
Chromium is the headless shell of the Playwright cache (or `CHROME_PATH`).

### What it does

For every scene and viewport, in a fresh browser context for each page: open A and B, run the scene's steps in each, take a
viewport screenshot of each, decode both PNGs in the tool (zlib inflate and PNG filter reversal), and compare. Per scene it
reports the percentage of differing pixels, their count, the pixels masked, and the bounding boxes of the differing regions
(differing pixels are grouped when within 16 px of each other). For a scene with a difference it writes `NAME@WxH.a.png`,
`.b.png` and `.diff.png`: the diff is A dimmed, every differing pixel red, every region outlined in yellow. The exit status is 1
when a scene exceeds its threshold (default 0.5 %), errors, or when a page threw, logged a console error, requested a URL
outside the loopback, or (B) violated the CSP.

### Determinism

The mock is a simulation driven by `requestAnimationFrame`. The tool brings both pages to the same state with a **virtual
clock** injected before any page script (`Page.addScriptToEvaluateOnNewDocument`): it replaces `setTimeout`, `setInterval`,
`requestAnimationFrame`, `requestIdleCallback`, `performance.now`, `Date` (fixed epoch) and `Math.random` (seeded), and sets the
page visible. Virtual time stands still until a step says `wait MS`; then the page's own timers and frames run in order, one
frame per 1/60 s of virtual time, as fast as the CPU allows, with the frame timestamps as the argument. Both pages therefore see
the same sequence and the simulation reaches the same state at the same virtual second regardless of load. The shipped mock
has no test hook, and none is needed.

What the virtual clock cannot reach is controlled around it:

* **Real-time work** (fonts, first layout, `ResizeObserver` callbacks): the tool waits for the fonts and two real frames after
  load and after every step, and before the screenshot.
* **Animations** (CSS animations, transitions, Web Animations run on the browser's timeline): before the screenshot every animation
  is set to rate 1 and finished, or, if infinite, parked at its start, and the text caret is hidden by a constructed stylesheet.
  (The mock sets the rate of every animation to the governor's rate, which is 0 during a hold, and `finish()` refuses a stopped
  animation; the tool restores the rate first.)
* **Rendering**: headless Chromium runs with software raster, sRGB, no LCD text, no partial raster or checker imaging, no
  threaded animation or scrolling, all compositor stages before draw; one device pixel per CSS pixel; UTC and `en-US`.
* **Network**: the browser resolves no host but `127.0.0.1`. The mock's Google Fonts link is answered from `_src/fonts` through the
  DevTools Fetch domain, so the mock renders with its real fonts; any other external request fails and is reported.

`--self` runs A against A across all scenes: the result must be 0 differing pixels everywhere.

### Scenes

`scripts/web-parity-scenes.json` (its `_doc` array describes the format). A scene is a name, ordered steps, and optionally
`viewports` (default 1440x900 and 390x844), `threshold`, `mask`, `query`, `reducedMotion`, `each`. Steps work in both pages, so
they use selectors and keys, not the page's namespace: `wait`, `until` (a JS condition, advancing in slices; for state that
arrives over a network the virtual clock does not control), `click` (`el.click()` on the nth match, optionally by text),
`mouse` (a real pointer click), `hover`, `key`, `keys`, `type`, `eval`, `size`, `expect`, `sleep` (real time), `sweep`; any
step can be limited with `"when": "wide" | "narrow"` (narrow: width up to 760). A step that finds nothing is an error.

The set covers the cockpit (first paint, running, question pending, answered by click and by key, reduced motion), the Radio
rail, the palette, the new-session and resume dialogs, the inbox, the goal, help and team sheets, session switching, the
navigation rail, all eleven Settings pages, Tools (catalogue, filter, runner, Doctor, Schedule), the Workspace tabs, the Cache
view in Quiet and Full, Mail, Board, Replay, Sessions, the drawer, the mode menu, the slash menu, the hold chip (hover, pinned
with a backlog, released with a digest), linked hover, the component kit, and `sweep-*` scenes. A **sweep** clicks every
distinct control of a view (returning with the `back` selectors when a click leaves it), closes what each opens, and types into
the fields: it exercises every handler for CSP violations and script errors and compares what the handlers leave behind.

### Masks

`mask` lists selectors; the boxes of the matching elements in either page are painted over in both pictures before they are
compared, for content that legitimately differs (a live clock, a timestamp, an id). `maskProfiles.live` collects the selectors
of that kind for a comparison of the mock with the UI wired to a real session (`--profile live`); a scene may add more. A
comparison of the packaged mock-backed UI needs no mask.

### Acceptance

For the packaged UI that still runs the mock's simulation, every scene is identical to the mock (0 differing pixels,
tolerance 0), `--self` is identical, and no page reports a CSP violation, console error or uncaught exception. When the data
layer is replaced by the live API, `--b` points at the running server, scenes that depend on live data gain masks or
`until` conditions, and the remaining differences are reviewed from the diff images against the owner-approved mock.
