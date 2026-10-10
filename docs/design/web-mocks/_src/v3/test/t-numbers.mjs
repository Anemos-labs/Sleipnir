// t-numbers.mjs: every number derives from ONE table. At five moments, read the hit % from every view that shows it and compare.
import { open } from './cdp.mjs';
const FILE = process.argv[2] || '../dist/test.html';
const page = await open(FILE, { query: 'manual' });
const res = await page.eval(`(async()=>{ const SL = __SL, T = SL.test, calc = SL.calc; T.manual(); const out = []; let bad = 0, checks = 0;
  const num = s => parseInt(String(s).replace(/[^0-9]/g, ''), 10);
  const chk = (label, got, want, moment) => { checks++; if (got !== want) { bad++; out.push({ moment, label, got, want }); } };
  const F = () => { const mm = SL.sessions.active.m, a = {}; mm.order.forEach(id => { a[id] = calc.agent(mm.ag[id]).pct; }); return { tot: calc.totals(mm), ag: a }; };   // fresh at the instant of each DOM read: the sim keeps stepping between views
  const moments = [0, 4, 8, 20, 40]; let prev = 0; const table = [];
  for (const mt of moments) {
    T.run(mt - prev); prev = mt; await T.idle(); const S = SL.sessions.active, m = S.m, tot = calc.totals(m), ag = {}; m.order.forEach(id => { ag[id] = calc.agent(m.ag[id]).pct; });
    for (const mode of ['quiet', 'full']) {
    const full = mode === 'full', L = (l) => l + ' [' + mode + ']'; SL.act.setCache(mode); T.run(0.1); const shown = el => !!el && getComputedStyle(el).display !== 'none' && !el.closest('[hidden]');
    // cockpit: the HUD (the ring in Full, the small chip in Quiet; both read the same sum), the stalls and the manager card (Full only)
    SL.views.show('cockpit'); T.run(0.1); const hud = num(document.querySelector('#hitTxt').textContent); chk(L('hud ring'), hud, F().tot.pct, mt); chk(L('hud chip'), num(document.querySelector('#hitChipN').textContent), F().tot.pct, mt);
    chk(L('hud: the ring shows in Full only'), shown(document.querySelector('#ringHit > svg')), full, mt); chk(L('hud: the chip shows in Quiet only'), shown(document.querySelector('#hitChip')), !full, mt);
    document.querySelectorAll('.stall:not(.ghost)').forEach(el => { const id = el.dataset.ag; chk(L('stall ' + id), num(el.querySelector('.n-hit').textContent), F().ag[id], mt); chk(L('stall ' + id + ' hit/spark shown'), shown(el.querySelector('.n-hit')) && shown(el.querySelector('.s-spark')), full, mt); });
    chk(L('manager card'), num(document.querySelector('.mgr-card .n-hit').textContent), F().ag.mgr, mt); chk(L('manager card hit shown'), shown(document.querySelector('.mgr-card .n-hit')), full, mt);
    // cache tab: rows, all row, header (the Cache view shows the hit ratio in both modes; the saved column in Full only)
    SL.views.show('cache'); T.run(0.1); const rows = {}; document.querySelectorAll('.ctable tbody tr').forEach(tr => { const c = tr.children; const id = tr.dataset.pick; const read = num(c[5].textContent), un = num(c[6].textContent); rows[id] = { hit: num(c[3].textContent), calc: Math.round(100 * read / (read + un)), read, un }; chk(L('cache row ' + id), rows[id].hit, F().ag[id], mt); chk(L('cache row ' + id + ' read/(read+un)'), rows[id].calc, F().ag[id], mt); });
    const foot = document.querySelector('.ctable tfoot tr').children, fr = num(foot[5].textContent), fu = num(foot[6].textContent); chk(L('cache all row'), num(foot[3].textContent), F().tot.pct, mt); chk(L('cache all row read/(read+un)'), Math.round(100 * fr / (fr + fu)), F().tot.pct, mt); chk(L('cache all row = sum of rows'), fr, Object.values(rows).reduce((s, r) => s + r.read, 0), mt);
    chk(L('cache session line'), num(document.querySelector('.c-sumtxt b').textContent), F().tot.pct, mt);
    const hdr = document.querySelector('.c-who .hit-hi,.c-who .hit-mid,.c-who .hit-lo'); chk(L('cache header'), num(hdr.textContent), F().ag[SL.ui.cacheAgent], mt);
    chk(L('cache view: the saved column shows in Full only'), shown(document.querySelector('.ctable thead th.qfull')), full, mt); chk(L('cache view: the saved estimate shows in Full only'), /saved/.test(Array.from(document.querySelectorAll('.c-sumtxt > span')).filter(shown).map(x => x.textContent).join('|')), full, mt);
    if (full) { const sv = Math.round(F().tot.saved * 1e4) / 1e4; chk(L('cache all row saved = calc'), parseFloat(document.querySelector('.ctable tfoot tr').children[8].textContent.replace('$', '')), sv, mt); }
    // drawer: the chip in the header (Full only) and the Cache tab (both modes)
    for (const id of ['mgr', 'be-2']) { SL.views.show('cockpit'); T.run(0.1); SL.ui.openDrawer(id); T.run(0.1); const chip = Array.from(document.querySelectorAll('.drawer .r2 .chip')).find(c => /% hit/.test(c.textContent)); chk(L('drawer chip ' + id + ' present'), !!chip, full, mt); if (chip) chk(L('drawer chip ' + id), num(chip.textContent), F().ag[id], mt); document.querySelector('.drawer [data-tab="cache"]').click(); T.run(0.1); const kv = document.querySelector('.drawer .kv dd:nth-of-type(2)'); chk(L('drawer cache tab ' + id), num(kv.textContent), F().ag[id], mt); SL.ui.closeDrawer(); }
    // sessions view and replay frame
    SL.views.show('sessions'); T.run(0.1); const sh = document.querySelector('.scard.cur [data-c="hit"]'); chk(L('sessions card'), num(sh.textContent), F().tot.pct, mt); chk(L('sessions card hit shown'), shown(sh), full, mt);
    SL.views.show('replay'); T.run(0.3); const fr2 = document.querySelector('.rpFrame b:nth-of-type(2)'); chk(L('replay frame'), num(fr2 ? fr2.textContent : -1), F().tot.pct, mt);
    }
    table.push({ t: mt, overall: tot.pct, overall1: tot.hit1, mgr: ag.mgr, 'be-2': ag['be-2'], prompt: tot.prompt, read: tot.read, un: tot.un, out: tot.out, cost: +tot.cost.toFixed(4), saved: +tot.saved.toFixed(4) });
  }
  return { checks, mismatches: bad, detail: out.slice(0, 12), table } })()`);
res.errors = page.errors; await page.close();
console.log(JSON.stringify(res, null, 1));
