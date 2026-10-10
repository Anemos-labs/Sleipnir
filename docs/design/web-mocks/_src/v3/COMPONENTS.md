# Components

The component kit of the core. Every class below is defined once, in `src/css/72-components.css` (and the older panels, chat and HUD
files for the product-specific parts); colours come only from the tokens on `:root` (`src/css/10-tokens.css`). This file is generated
from the live `?kit` view (`node tools/gen-components.mjs > COMPONENTS.md`), so it cannot drift from what is on screen: open
`00-core.html?kit` to see every sample rendered.

Rules for every component:

* Interactive things are real elements (`<button>`, `<input>`, `<select>`, `<a>`) with a visible focus ring (`--focus`), never a styled `<div>`.
* Text from a tool, a file, a mail or a model is data: put it in with `textContent` or `SL.u.esc()`, never as markup, and label mail as data.
* A component that starts a timer, a listener or a transient element (popover, tooltip, toast) does it through the **scope** of the view or
  modal that owns it (see ARCHITECTURE.md, "the one rule"). The pure-CSS components below start nothing.
* No emoji: state is a text glyph (`✓ ✎ ? ◇ ◌ ⏹`) plus a word, never colour alone.

## Panel

`panel > .ph (h2 + .r right side) + .pb`

```html
<section class="panel" style="min-height:90px">
  <div class="ph">
    <h2>Panel title</h2>
    <div class="r"><span class="dim">meta</span></div>
  </div>
  <div class="pb">Body. Panels are the only container: a header row (.ph) and a body (.pb).</div>
</section>
```

## Buttons

`btn  btn pri  btn danger  btn sm; real <button>, visible focus ring`

```html
<button class="btn">Default</button>
<button class="btn pri">Primary</button>
<button class="btn danger">Danger</button>
<button class="btn sm">Small</button>
<button class="btn" disabled>Disabled</button>
```

## Segmented control

`seg > button[aria-pressed]`

```html
<div class="seg" role="group" aria-label="Mode">
  <button type="button" aria-pressed="true">default</button>
  <button type="button" aria-pressed="false">accept-edits</button>
  <button type="button" aria-pressed="false">plan</button>
</div>
```

## Toggle

`label.tgl > input[type=checkbox] + span.trk`

```html
<label class="tgl"><input type="checkbox" checked><span class="trk" aria-hidden="true"></span><span class="sr">--commit</span></label>
<span class="dim">--commit</span>
```

## Inputs with labels

`fld > label + div.fc > input|select|textarea + small.hint`

```html
<div class="fld">
  <label for="kit1">Budget</label>
  <div class="fc">
    <input id="kit1" type="text" value="5.00"> <small class="hint">a number of dollars, or off</small>
  </div>
</div>
<div class="fld">
  <label for="kit2">Model</label>
  <div class="fc"><select id="kit2"><option>anthropic/claude-sonnet-5-5</option><option>heimdall/demo-model</option></select></div>
</div>
<div class="fld">
  <label for="kit3">Goal</label>
  <div class="fc"><textarea id="kit3" rows="2">Build the shop</textarea></div>
</div>
```

## Form field group

`fieldset.fgroup > legend + .fld rows | .field-row (inline) | label.chk`

```html
<fieldset class="fgroup">
  <legend>Team</legend>
  <div class="fld">
    <label for="kit4">Workers</label>
    <div class="fc">
      <input id="kit4" type="text" value="8"> <small class="hint">--swarm N: the manager takes no leg</small>
    </div>
  </div>
  <div class="field-row">
    <label class="chk"><input type="checkbox" checked> --commit</label>
    <label class="chk"><input type="checkbox"> --mailman</label>
  </div>
</fieldset>
```

## Key and value list

`dl.kv > dt + dd (dl.kv.keys2 for key names in mono)`

```html
<dl class="kv">
  <dt>model</dt>
  <dd>anthropic/claude-sonnet-5-5</dd>
  <dt>mode</dt>
  <dd>default</dd>
  <dt>budget</dt>
  <dd>$5.00</dd>
</dl>
```

## Table

`tbl; td.r right-aligned; tr.sel selected`

```html
<table class="tbl">
  <thead>
    <tr>
      <th>agent</th>
      <th class="r">hit</th>
    </tr>
  </thead>
  <tbody>
    <tr>
      <td class="bright">be-1</td>
      <td class="r ok">89%</td>
    </tr>
    <tr class="sel">
      <td class="bright">be-2</td>
      <td class="r err">77%</td>
    </tr>
  </tbody>
</table>
```

## List rows

`mrow (.hot / .dimmed from hover linking)`

```html
<div class="lrows">
  <button class="mrow" type="button"><span class="mt">03:04:41</span><span class="mi">✉</span><span class="mf">ts-1 → be-1</span><span class="mx">“contract: page 0 …”</span></button>
  <button class="mrow hot" type="button"><span class="mt">03:04:36</span><span class="mi">✉</span><span class="mf">be-1 → fe-1</span><span class="mx">“catalogue: GET /items…”</span></button>
</div>
```

## Chips, tags, badges

`chip  chip task (--c)  tag ok|warm|err|fe  nb  kbd`

```html
<span class="chip">T4</span>
<span class="chip task" style="--c:var(--c-be)">T5</span>
<span class="tag ok">trusted</span>
<span class="tag warm">asks</span>
<span class="tag err">dangerous</span>
<span class="tag fe">tools</span>
<span class="nb">? 1</span>
<kbd>ctrl</kbd>
<kbd>k</kbd>
```

## Tabs

`tabbar > button[role=tab][aria-selected]`

```html
<div class="tabbar" role="tablist">
  <button role="tab" aria-selected="true">Transcript</button>
  <button role="tab" aria-selected="false">Cache</button>
  <button role="tab" aria-selected="false">Mail</button>
</div>
```

## Meter and gauge

`meter > i  gauge > i (width in %)`

```html
<div class="meter"><i style="width:62%"></i></div>
<div class="gauge wide"><i style="width:18%"></i></div>
```

## Sparkline

`ui.sparkBars(ratios, W, H): green >= 0.7, amber below, pink 0 with a 0 label`

```html
<svg viewBox="0 0 150 40" width="150" height="40" aria-hidden="true">
  <line x1="0" x2="150" y1="39.5" y2="39.5" stroke="var(--line2)" stroke-width="1"></line>
  <rect x="1.0" y="16.6" width="13.9" height="23.4" fill="var(--warm)" opacity="0.85"></rect>
  <rect x="19.8" y="5.8" width="13.9" height="34.2" fill="var(--ok)" opacity="0.85"></rect>
  <rect x="38.5" y="5.8" width="13.9" height="34.2" fill="var(--ok)" opacity="0.85"></rect>
  <rect x="57.3" y="5.8" width="13.9" height="34.2" fill="var(--ok)" opacity="0.85"></rect>
  <rect x="76.0" y="5.8" width="13.9" height="34.2" fill="var(--ok)" opacity="0.85"></rect>
  <rect x="94.8" y="6.2" width="13.9" height="33.8" fill="var(--ok)" opacity="0.85"></rect>
  <rect x="113.5" y="38.0" width="13.9" height="2.0" fill="var(--err)" opacity="1"></rect>
  <text x="120.4" y="34" font-size="10" fill="var(--err)" text-anchor="middle" font-family="var(--f-num)" font-weight="700">0</text>
</svg>
```

## Ring

`ui.ringSvg(pct, size, colour)`

```html
<svg class="kitring" width="56" height="56" viewBox="0 0 56 56" role="img" aria-label="85%">
  <circle cx="28" cy="28" r="23" fill="none" stroke="var(--line2)" stroke-width="4"></circle>
  <circle cx="28" cy="28" r="23" fill="none" stroke="var(--ok)" stroke-width="4" stroke-dasharray="144.5" stroke-dashoffset="21.7" transform="rotate(-90 28 28)"></circle>
  <text x="50%" y="55%" text-anchor="middle" class="rv" style="font-size:15.7px">85</text>
</svg>
<svg class="kitring" width="56" height="56" viewBox="0 0 56 56" role="img" aria-label="30%">
  <circle cx="28" cy="28" r="23" fill="none" stroke="var(--line2)" stroke-width="4"></circle>
  <circle cx="28" cy="28" r="23" fill="none" stroke="var(--warm)" stroke-width="4" stroke-dasharray="144.5" stroke-dashoffset="101.1" transform="rotate(-90 28 28)"></circle>
  <text x="50%" y="55%" text-anchor="middle" class="rv" style="font-size:15.7px">30</text>
</svg>
```

## Stack bar

`stack > span.ls[--lc] > i.rd (read) + i.pd (paid)`

```html
<div class="stack" style="height:26px;margin:0">
  <span class="ls" style="width:30%;--lc:var(--mgr)"><i class="rd" style="width:100%"></i></span>
  <span class="ls" style="width:40%;--lc:var(--be)"><i class="rd" style="width:100%"></i></span>
  <span class="ls" style="width:30%;--lc:var(--rv)"><i class="rd" style="width:60%"></i><i class="pd" style="left:60%"></i></span>
</div>
```

## Diff view

`diff > .ln.add|del|hunk > b.who + i (no) + s (sign) + span`

```html
<div class="diff" style="--ag:var(--c-be);--ag-a:var(--be-a)">
  <div class="ln hunk"><b class="who"></b><i></i><s></s><span>@@ -23,10 +23,10 @@</span></div>
  <div class="ln del"><b class="who"></b><i>24</i><s>−</s><span>func (c *Cart) Total() float64 {</span></div>
  <div class="ln add"><b class="who">be-2</b><i>24</i><s>+</s><span>func (c *Cart) Total() int64 {</span></div>
</div>
```

## Terminal output

`term > .tl.hd|out|ok|warn|bad|dim|head`

```html
<div class="term" style="max-height:130px">
  <div class="tl hd"><span class="prompt">$</span> sleipnir sessions prune</div>
  <div class="tl out">would delete 3 sessions (21.1 MB)</div>
  <div class="tl warn">  20251130-084418-90ac52  33d</div>
  <div class="tl ok">done</div>
  <div class="tl bad">error: …</div>
  <div class="tl dim">nothing was deleted</div>
</div>
```

## Tooltip (CSS only)

`[data-tip] + :hover/:focus-visible ::after`

```html
<span class="chip" data-tip="a tooltip needs no script, so it can never outlive its view" tabindex="0">hover or focus me</span>
```

## Overlays (script)

The overlays are created by functions, not by markup. All of them live under a scope and are removed by it.

| Call | What it makes | Notes |
| --- | --- | --- |
| `SL.ui.modal({title, kicker, desc, body, wide, color, cls, focus, onMount(body, scope, close), onClose})` | a dialog (or, with `wide: true`, a sheet): a titled panel over a scrim | focus-trapped, Esc and the scrim close it, the previous focus returns; it owns a scope: what it starts dies with it. View keys are off while it is open. Returns `{close, el, scope}`. |
| `SL.ui.confirm({title, text, detail, ok, danger, run})` | an in-page confirm | never `window.confirm`; `run()` is called on the confirm button. |
| `SL.ui.toast(message, kind)` | a toast in `#toasts` | `kind`: `ok`, `warm`, `err`, `quiet` (not announced). At most three; never takes focus and never answers anything. |
| `SL.ui.popover(scope, anchor, html, {role})` | a popover under `anchor`, in `scope.layer('pop')` | closes on outside press and Esc; removed with the scope (leave the view and it is gone). Returns `{el, close}`. |
| `[data-tip="text"]` | a CSS-only tooltip | shown on hover and on keyboard focus; no script, so it can never outlive its view. |

### Static markup of a dialog

```html
<div class="scrim">
  <div class="sheet" role="dialog" aria-modal="true" aria-label="Title" style="--c:var(--mgr)" tabindex="-1">
    <div class="sh-h"><h2>Title</h2><span class="k">kicker</span><span class="d">one line of description</span><button class="x" data-close aria-label="Close">×</button></div>
    <div class="sh-b"> ...body... </div>
  </div>
</div>
```

## Product components (not part of the generic kit)

These are specific to the Cockpit and are mounted by `SL.ui.*` functions; a variant restyles them in CSS rather than rewriting the markup
(the data attributes are what hover linking and the tests use).

| Component | Mounted by | Markup contract |
| --- | --- | --- |
| HUD (goal, budget, cache ring, prefix timer, team) | `SL.ui.shell.mount` | ids `#hud #hitTxt #vBudget` (see `src/html/shell.html`) |
| Session strip | `SL.ui.shell.mount` | `#sstrip`, one `.stab` per session, badge `.nb` for open questions |
| Drawn horse (eight worker legs) and prefix bar | `SL.ui.hero.mount(scope, svg, session)` | an `<svg>`; each leg and each leg label carries `data-ag` (the ids of the workers on that leg) |
| Stalls | `83-ui-cockpit.js` | `.stall[data-ag]`, `.mgr-card`, `.n-hit` (the hit percentage: one table) |
| Task board | `SL.ui.board.make(scope, host)` | `.bcard[data-task]`, columns todo / running / verify / merged |
| Radio rail (channels, transcript, composer) | `SL.chat.mount` | `#rail`, `#chans`, `#talk.talk[data-hold="chat"]`, `#composer` |
| Approval question | `SL.ui.approvals` | `.qbox` with three `.qopt` buttons and the quiet-period meter |
| Per-agent drawer | `SL.ui.openDrawer(id)` | `.drawer` in the current view's own layer |
| Layer stack (G0..G5, read/paid) | `SL.ui.layerStack(model, id, height, withBreaks)` | `.stack > .ls > i.rd + i.pd` |
| Spark bars of per-request hit | `SL.ui.sparkBars(ratios, W, H)` | returns SVG inner markup |

