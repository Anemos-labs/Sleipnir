#!/usr/bin/env node
// gen-components.mjs: write COMPONENTS.md from the live `?kit` view, so the catalogue in the document is exactly the one on screen.
//   node tools/gen-components.mjs [dist/test.html] > COMPONENTS.md      (needs the scratch build with hooks, see go.sh)
import { open } from '../test/cdp.mjs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
const here = path.dirname(fileURLToPath(import.meta.url)), FILE = process.argv[2] || path.join(here, '../dist/test.html');
const page = await open(FILE, { query: 'kit&manual' });
const secs = await page.eval(`(() => {
  const INL = new Set(['SPAN', 'B', 'I', 'S', 'KBD', 'CODE', 'EM', 'SMALL', 'A', 'INPUT', 'BUTTON', 'LABEL', 'STRONG', 'BR', 'TIME', 'SELECT', 'OPTION', 'TEXTAREA', 'svg', 'SVG']);
  const inl = e => INL.has(e.tagName) || INL.has(e.tagName.toUpperCase());
  const flat = e => Array.from(e.childNodes).every(c => c.nodeType === 3 || (c.nodeType === 1 && inl(c) && flat(c)));
  const kids = (e, ind) => { const lines = []; let run = []; const flush = () => { if (run.length) { if (run.join(' ').length > 110 && run.length > 1) run.forEach(r => lines.push(ind + r)); else lines.push(ind + run.join(' ')); run = []; } };
    Array.from(e.childNodes).forEach(c => { if (c.nodeType === 3) { if (c.textContent.trim()) run.push(c.textContent.trim()); } else if (c.nodeType === 1 && inl(c) && flat(c)) run.push(c.outerHTML); else if (c.nodeType === 1) { flush(); lines.push.apply(lines, pp(c, ind)); } }); flush(); return lines; };
  const pp = (e, ind) => { if (flat(e) && (e.outerHTML.length <= 110 || e.children.length < 2)) return [ind + e.outerHTML]; const o = e.outerHTML; return [ind + o.slice(0, o.indexOf('>') + 1)].concat(kids(e, ind + '  '), [ind + '</' + e.tagName.toLowerCase() + '>']); };
  return Array.from(document.querySelectorAll('.kit-grid > .kit-s')).map(s => { const d = s.querySelector('.kit-demo'); return { title: s.querySelector(':scope > .ph h2').textContent, html: s.querySelector('.kit-code') ? kids(d, '').join('\\n').replace(/=""/g, '') : '', note: (s.querySelector('.kit-note') || {}).textContent || '' }; });
})()`);
const out = [];
out.push(`# Components

The component kit of the core. Every class below is defined once, in \`src/css/72-components.css\` (and the older panels, chat and HUD
files for the product-specific parts); colours come only from the tokens on \`:root\` (\`src/css/10-tokens.css\`). This file is generated
from the live \`?kit\` view (\`node tools/gen-components.mjs > COMPONENTS.md\`), so it cannot drift from what is on screen: open
\`00-core.html?kit\` to see every sample rendered.

Rules for every component:

* Interactive things are real elements (\`<button>\`, \`<input>\`, \`<select>\`, \`<a>\`) with a visible focus ring (\`--focus\`), never a styled \`<div>\`.
* Text from a tool, a file, a mail or a model is data: put it in with \`textContent\` or \`SL.u.esc()\`, never as markup, and label mail as data.
* A component that starts a timer, a listener or a transient element (popover, tooltip, toast) does it through the **scope** of the view or
  modal that owns it (see ARCHITECTURE.md, "the one rule"). The pure-CSS components below start nothing.
* No emoji: state is a text glyph (\`✓ ✎ ? ◇ ◌ ⏹\`) plus a word, never colour alone.
`);
for (const s of secs.filter(x => x.html)) {
  out.push(`## ${s.title}\n\n\`${s.note.replace(/`/g, "'")}\`\n`);
  if (s.html) out.push('```html\n' + s.html.trim() + '\n```\n');
}
out.push(`## Overlays (script)

The overlays are created by functions, not by markup. All of them live under a scope and are removed by it.

| Call | What it makes | Notes |
| --- | --- | --- |
| \`SL.ui.modal({title, kicker, desc, body, wide, color, cls, focus, onMount(body, scope, close), onClose})\` | a dialog (or, with \`wide: true\`, a sheet): a titled panel over a scrim | focus-trapped, Esc and the scrim close it, the previous focus returns; it owns a scope: what it starts dies with it. View keys are off while it is open. Returns \`{close, el, scope}\`. |
| \`SL.ui.confirm({title, text, detail, ok, danger, run})\` | an in-page confirm | never \`window.confirm\`; \`run()\` is called on the confirm button. |
| \`SL.ui.toast(message, kind)\` | a toast in \`#toasts\` | \`kind\`: \`ok\`, \`warm\`, \`err\`, \`quiet\` (not announced). At most three; never takes focus and never answers anything. |
| \`SL.ui.popover(scope, anchor, html, {role})\` | a popover under \`anchor\`, in \`scope.layer('pop')\` | closes on outside press and Esc; removed with the scope (leave the view and it is gone). Returns \`{el, close}\`. |
| \`[data-tip="text"]\` | a CSS-only tooltip | shown on hover and on keyboard focus; no script, so it can never outlive its view. |

### Static markup of a dialog

\`\`\`html
<div class="scrim">
  <div class="sheet" role="dialog" aria-modal="true" aria-label="Title" style="--c:var(--mgr)" tabindex="-1">
    <div class="sh-h"><h2>Title</h2><span class="k">kicker</span><span class="d">one line of description</span><button class="x" data-close aria-label="Close">×</button></div>
    <div class="sh-b"> ...body... </div>
  </div>
</div>
\`\`\`

## Product components (not part of the generic kit)

These are specific to the Cockpit and are mounted by \`SL.ui.*\` functions; a variant restyles them in CSS rather than rewriting the markup
(the data attributes are what hover linking and the tests use).

| Component | Mounted by | Markup contract |
| --- | --- | --- |
| HUD (goal, budget, cache ring, prefix timer, team) | \`SL.ui.shell.mount\` | ids \`#hud #hitTxt #vBudget\` (see \`src/html/shell.html\`) |
| Session strip | \`SL.ui.shell.mount\` | \`#sstrip\`, one \`.stab\` per session, badge \`.nb\` for open questions |
| Drawn horse (eight worker legs) and prefix bar | \`SL.ui.hero.mount(scope, svg, session)\` | an \`<svg>\`; each leg and each leg label carries \`data-ag\` (the ids of the workers on that leg) |
| Stalls | \`83-ui-cockpit.js\` | \`.stall[data-ag]\`, \`.mgr-card\`, \`.n-hit\` (the hit percentage: one table) |
| Task board | \`SL.ui.board.make(scope, host)\` | \`.bcard[data-task]\`, columns todo / running / verify / merged |
| Radio rail (channels, transcript, composer) | \`SL.chat.mount\` | \`#rail\`, \`#chans\`, \`#talk.talk[data-hold="chat"]\`, \`#composer\` |
| Approval question | \`SL.ui.approvals\` | \`.qbox\` with three \`.qopt\` buttons and the quiet-period meter |
| Per-agent drawer | \`SL.ui.openDrawer(id)\` | \`.drawer\` in the current view's own layer |
| Layer stack (G0..G5, read/paid) | \`SL.ui.layerStack(model, id, height, withBreaks)\` | \`.stack > .ls > i.rd + i.pd\` |
| Spark bars of per-request hit | \`SL.ui.sparkBars(ratios, W, H)\` | returns SVG inner markup |
`);
console.log(out.join('\n'));
await page.close();
