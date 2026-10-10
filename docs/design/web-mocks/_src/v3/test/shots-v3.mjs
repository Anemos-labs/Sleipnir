// shots-v3.mjs: the "after" pictures of the six changes under $SHOTS (shots/v3). Serial. Usage: node shots-v3.mjs [only-prefix]
import { open } from './cdp.mjs';
import fs from 'node:fs';
const OUT = process.env.SHOTS, FILE = process.env.FILE || '../dist/pack-test.html', ONLY = process.argv[2] || '';
fs.mkdirSync(OUT, { recursive: true });
async function scene(name, w, h, scale, setup, clipSel) {
  if (ONLY && !name.startsWith(ONLY)) return;
  const page = await open(FILE, { w, h, query: 'manual', scale });
  const ev = js => page.eval('(async()=>{ const SL = __SL, T = SL.test, $ = s => document.querySelector(s), $$ = s => Array.from(document.querySelectorAll(s)), fire = (e, t) => e.dispatchEvent(new Event(t, { bubbles: true })); ' + js + ' })()');
  try {
    await ev('T.manual(); T.run(1);'); await setup(ev, page); await page.sleep(450);
    let clip;
    if (clipSel) { const r = await ev(`const e = ${typeof clipSel === 'function' ? '(' + clipSel.toString() + ')()' : '$(' + JSON.stringify(clipSel) + ')'}; const b = e.getBoundingClientRect(); return { x: Math.max(0, b.x), y: Math.max(0, b.y), width: b.width, height: b.height }`); clip = { ...r, scale: 1 }; }
    const { data } = await page.send('Page.captureScreenshot', { format: 'png', ...(clip ? { clip } : {}) });
    fs.writeFileSync(OUT + '/' + name + '.png', Buffer.from(data, 'base64')); console.log(name + (page.errors.length ? ' ERRORS ' + JSON.stringify(page.errors) : ' ok'));
  } finally { await page.close(); }
}
await scene('after-1-new-session', 1440, 900, 1, async ev => { await ev(`$('#sNew').click(); T.run(0.3); $('[data-nsmode="bypass"]').click(); T.run(0.2);`); });
await scene('after-1b-new-session-default', 1440, 900, 1, async ev => { await ev(`$('#sNew').click(); T.run(0.3);`); });
await scene('after-2-horse', 1440, 900, 2, async ev => { await ev(`T.run(0.5)`); }, '.panel.hero');
await scene('after-2b-horse-full-bar-no-connectors', 1440, 900, 2, async ev => { await ev(`SL.act.setCache('full'); T.run(0.5)`); }, '.panel.hero');
await scene('after-3-radio', 1440, 900, 2, async ev => { await ev(`T.run(0.5)`); }, '#rail');
await scene('after-3b-radio-after-answer', 1440, 900, 2, async ev => { await ev(`T.run(1.5); const q = SL.calc.openQuestion(SL.sessions.active.m); SL.act.answerQuestion(q.id, 2, undefined, 'shop'); T.run(9);`); }, '#rail');
await scene('after-3c-drawer-read-only', 1440, 900, 1, async ev => { await ev(`T.run(0.5); SL.ui.openDrawer('be-2'); T.run(0.4);`); });
await scene('after-5-mode-dropdown-open', 1440, 900, 2, async ev => { await ev(`T.run(0.5); $('#modeBtn').click(); T.run(0.2);`); }, () => { const m = document.querySelector('.modemenu').getBoundingClientRect(), c = document.querySelector('#composer').getBoundingClientRect(); const e = document.createElement('div'); e.style.cssText = 'position:fixed;pointer-events:none;left:' + (c.left - 4) + 'px;top:' + (m.top - 6) + 'px;width:' + (c.width + 8) + 'px;height:' + (c.bottom - m.top + 12) + 'px'; document.body.appendChild(e); return e; });
await scene('after-6-hud-stalls-quiet', 1440, 900, 2, async ev => { await ev(`T.run(0.5)`); }, () => { const e = document.createElement('div'); e.style.cssText = 'position:fixed;pointer-events:none;left:0;top:0;width:1100px;height:480px'; document.body.appendChild(e); return e; });
await scene('after-6-hud-stalls-full', 1440, 900, 2, async ev => { await ev(`SL.act.setCache('full'); T.run(0.5)`); }, () => { const e = document.createElement('div'); e.style.cssText = 'position:fixed;pointer-events:none;left:0;top:0;width:1100px;height:520px'; document.body.appendChild(e); return e; });
await scene('after-6b-cache-break-quiet', 1440, 900, 2, async ev => { await ev(`T.until(() => SL.sessions.active.m.anomalies.length > 0, 40); T.run(1.2);`); }, () => { const e = document.createElement('div'); e.style.cssText = 'position:fixed;pointer-events:none;left:64px;top:0;width:1376px;height:900px'; document.body.appendChild(e); return e; });
await scene('after-6c-cache-break-full', 1440, 900, 2, async ev => { await ev(`SL.act.setCache('full'); T.until(() => SL.sessions.active.m.anomalies.length > 0, 40); T.run(0.6);`); }, () => { const e = document.createElement('div'); e.style.cssText = 'position:fixed;pointer-events:none;left:64px;top:0;width:1376px;height:900px'; document.body.appendChild(e); return e; });
await scene('after-6d-settings-appearance', 1440, 900, 1, async ev => { await ev(`SL.ui.settingsPage('look'); T.run(0.4);`); });
