// t-shipped.mjs: the SHIPPED file (no hooks): it carries no hook, runs on the real frame loop, every view opens from its tab, the palette and the help overlay open from the keyboard,
// a hold by real pointer movement works, nothing is logged as an error. Usage: node t-shipped.mjs [file]  (default: the repo's docs/design/web-mocks/v2/00-core.html)
import { open } from './cdp.mjs';
const FILE = process.argv[2] || '/home/thanos/Documents/Sleipnir/.claude/worktrees/sleipnir-web-interface-cc5a2f/docs/design/web-mocks/v2/00-core.html', out = [];
for (const [w, h] of [[1440, 900], [390, 844]]) {
  const page = await open(FILE, { w, h });
  const r = { size: w + 'x' + h };
  r.hooks = await page.eval(`({ __SL: typeof window.__SL, SL: typeof window.SL, test: typeof window.SL })`);
  await page.sleep(2500);
  r.hud = await page.eval(`({ ring: document.querySelector('#hitTxt').textContent, views: !!document.querySelector('.view'), stalls: document.querySelectorAll('.stall').length, legs: document.querySelectorAll('.leg').length })`);
  r.views = [];
  for (const v of ['cache', 'mail', 'board', 'replay', 'sessions', 'settings', 'cockpit']) {
    await page.eval(`(document.querySelector('.nvi[data-nav="${v}"]') || document.querySelector('#mnav [data-nav="${v}"]') || { click() {} }).click()`); await page.sleep(350);
    r.views.push(v + ':' + await page.eval(`(() => { const r = document.querySelector('#views').firstElementChild; return r ? (r.className.split(' ')[0] || r.tagName) + '/' + (document.querySelector('.nvi[aria-current="page"]') || { dataset: {} }).dataset.nav : 'none'; })()`));
  }
  await page.key('k', 2); await page.sleep(300); r.palette = await page.eval(`!!document.querySelector('.scrim .sheet')`); await page.key('Escape'); await page.sleep(200);
  await page.key('?'); await page.sleep(300); r.help = await page.eval(`!!document.querySelector('.scrim .sheet')`); await page.key('Escape'); await page.sleep(200);
  r.modalClosed = await page.eval(`!document.querySelector('.scrim')`);
  if (w > 900) { await page.move(1200, 600); await page.sleep(1200); r.holdChip = await page.eval(`(document.querySelector('#holdChip') || {}).hidden === false ? document.querySelector('#holdChip').textContent : 'no chip'`); await page.move(400, 300); await page.sleep(1500); r.released = await page.eval(`document.querySelector('#holdChip').hidden`); }
  await page.sleep(3000);
  r.errors = page.errors.slice(); r.fontMisses = page.fontMisses.length; out.push(r); await page.close();
}
console.log(JSON.stringify(out, null, 1));
