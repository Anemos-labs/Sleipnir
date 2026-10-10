// t-input.mjs: pointer and keyboard semantics with REAL input events (CDP): hover hold/slow, pin, Esc precedence, the approval quiet period.
import { open } from './cdp.mjs';
const FILE = process.argv[2] || '../dist/test.html', out = {};
const page = await open(FILE, { query: 'manual' });
const ev = js => page.eval('(async()=>{ const SL = __SL, T = SL.test; ' + js + ' })()');
await ev(`T.manual(); T.run(1);`);
const rect = async sel => JSON.parse(await ev(`const r = document.querySelector(${JSON.stringify(sel)}).getBoundingClientRect(); return JSON.stringify({ x: r.x + r.width / 2, y: r.y + r.height / 2 })`));
/* 1. pointer over the transcript: rate eases to ~0 within 0.5 s; no append, no scroll; leaving: resume + catch-up */
let r = await rect('#talk'); await page.move(r.x, r.y);
out.hover = await ev(`const S = SL.sessions.active, rows0 = document.querySelectorAll('#talk .msg, #feed .msg').length, top0 = document.querySelector('#talk').scrollTop; const rates = []; for (let i = 0; i < 40; i++) { T.run(1 / 30); rates.push(+SL.time.rate.toFixed(3)); }
  const at05 = rates[14]; S.add([{ k: 'tool', id: 'be-1', name: 'Read', arg: 'a.go', out: 'x' }, { k: 'tool', id: 'be-1', name: 'Read', arg: 'b.go', out: 'x' }]); T.run(2);
  const held = { rateAt05s: at05, rateAfter1s: rates[29], hover: SL.time.T.hover, chip: document.querySelector('#holdChip').textContent, hidden: document.querySelector('#holdChip').hidden, rows: document.querySelectorAll('#talk .msg, #feed .msg').length - rows0, scrollMoved: document.querySelector('#talk').scrollTop - top0, gap: +(S.wt - S.vt).toFixed(2) }; return held`);
await page.move(5, 5);
out.leave = await ev(`const S = SL.sessions.active; const rates = []; let t = 0; while (t < 6 && !(S.wt - S.vt < 0.2 && !SL.time.T.catching)) { T.run(1 / 60); t += 1 / 60; rates.push(SL.time.rate); } return { seconds: +t.toFixed(2), maxRate: +Math.max(...rates, 0).toFixed(2), rowsAppended: document.querySelectorAll('#talk .msg, #feed .msg').length, gap: +(S.wt - S.vt).toFixed(2), chipHidden: document.querySelector('#holdChip').hidden }`);
/* 2. pointer over a stall: slow to 30%, never a jump */
r = await rect('.stall[data-ag="be-2"]'); await page.move(r.x, r.y);
out.slow = await ev(`const rates = []; for (let i = 0; i < 40; i++) { T.run(1 / 30); rates.push(+SL.time.rate.toFixed(3)); } return { hover: SL.time.T.hover, first: rates[0], at05: rates[14], at1: rates[29], max: Math.max(...rates), hotAgents: Array.from(document.querySelectorAll('.hot')).map(e => e.dataset.ag || e.dataset.task).slice(0, 12), dimmed: document.querySelectorAll('.dimmed').length }`);
await page.move(5, 5); await ev(`T.run(4)`);
/* 3. settings: hold on chat only / off */
out.modes = await ev(`const o = {}; for (const m of ['chat', 'off', 'both']) { SL.act.setHover(m); SL.time.setHover('linked'); T.run(1); o[m + ':linked'] = +SL.time.rate.toFixed(2); SL.time.setHover('chat'); T.run(1); o[m + ':chat'] = +SL.time.rate.toFixed(2); SL.time.setHover(null); T.run(2); } return o`);
/* 4. Space pins over the chat, Esc releases (and only then closes anything else) */
r = await rect('#talk'); await page.move(r.x, r.y); await ev(`T.run(1)`); await page.key('Space');
out.pin = await ev(`T.run(0.5); const a = { pinned: SL.time.T.pinned, chip: document.querySelector('#holdChip').textContent, rate: +SL.time.rate.toFixed(3) }; return a`);
await page.move(5, 5); out.pinSurvivesLeave = await ev(`T.run(1); return { pinned: SL.time.T.pinned, rate: +SL.time.rate.toFixed(3), held: SL.time.held }`);
await page.key('Escape'); out.escReleases = await ev(`T.run(0.1); return { pinned: SL.time.T.pinned, holdWanted: SL.time.holdWanted(), openQuestionStillOpen: !!SL.calc.openQuestion(SL.sessions.active.m) }`);
/* 5. the approval quiet period: typed-ahead text never answers; a pause then 1 answers */
await ev(`T.run(3)`);
await page.eval(`document.querySelector('#input').focus()`); for (const k of 'ab') await page.key(k); await page.key('1');
out.typeAhead = await ev(`const S = SL.sessions.active; return { composerValue: document.querySelector('#input').value, answered: S.m.qs[0].answered, wmAnswered: S.wm.qs[0].answered }`);
await page.eval(`document.querySelector('#input').value=''; document.querySelector('#input').blur()`); await ev(`T.run(0.4)`); await page.key('1');
out.tooSoon = await ev(`return { answered: SL.sessions.active.wm.qs[0].answered, meterReady: document.querySelector('.qmeter').classList.contains('ready') }`);
await ev(`T.run(1.0)`); out.armed = await ev(`return { meterReady: document.querySelector('.qmeter').classList.contains('ready'), btnEnabled: document.querySelector('.qopt').getAttribute('aria-disabled') }`);
await page.key('2'); out.answer2 = await ev(`T.run(0.5); const S = SL.sessions.active; return { answered: S.wm.qs[0].answered, rules: S.meta.rules.map(x => x.rule + ' | ' + x.origin), stripGone: !document.querySelector('.qstrip'), fe1: S.wm.ag['fe-1'].state }`);
out.afterAnswer = await ev(`T.run(8); const S = SL.sessions.active; return { fe1: S.wm.ag['fe-1'].state, doing: S.wm.ag['fe-1'].doing, T6: S.wm.tasks.T6.st, merged: S.wm.merged.join(','), goal: S.wm.goal.state }`);
/* 6. Esc: overlay first, then interrupt */
await ev(`SL.ui.sheets.help()`); await page.key('Escape'); out.escOverlay = await ev(`return { modalOpen: SL.ui.hasModal() }`);
out.errors = page.errors.slice(); await page.close();
console.log(JSON.stringify(out, null, 1));
