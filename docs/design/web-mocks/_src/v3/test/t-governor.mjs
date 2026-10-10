// t-governor.mjs: the time governor. A 45-simulated-minute hold, released; the same run never held; 20 hold/release cycles in 2 s;
// a hold during a mail arc. Prints numbers. Usage: node t-governor.mjs [dist/test.html]
import { open } from './cdp.mjs';
const FILE = process.argv[2] || '../dist/test.html';
const run = async (hold, tasks) => {
  const page = await open(FILE, { query: 'manual' });
  const r = await page.eval(`(async()=>{ const SL = __SL, T = SL.test; T.manual(); SL.act.switchSession('docs-sweep'); const S = SL.sessions.active; S.endgamed = true; T.run(2);
    const W0 = S.wt, END = W0 + 2760, HOLD = ${hold}; const n = T.flood(S, W0 + 0.5, W0 + 2730, { tasks: ${tasks} });
    T.run(1); const base = T.counts(), rows0 = document.querySelectorAll('#talk .msg, #feed .msg').length, res = { events: n, nodes0: base.nodes, rows0, counts0: base };
    if (HOLD) {
      S.hold.pinned = true; SL.time.pin(true); T.run(1); const vt0 = S.vt; T.until(() => S.wt >= W0 + 2700, 3000, 0.25);
      res.held = { vtMoved: +(S.vt - vt0).toFixed(3), gapS: Math.round(S.wt - S.vt), unseen: S.unseen(), rate: SL.time.rate, nodes: T.counts().nodes, chip: document.querySelector('#holdChip').textContent, hint: document.querySelector('#liveHint').textContent };
      S.hold.pinned = false; SL.time.pin(false); let maxNodes = 0, maxRate = 0, t = 0; const dt = 1 / 60;
      while (t < 30) { SL.loop.step(dt); t += dt; const c = T.counts().nodes; if (c > maxNodes) maxNodes = c; if (SL.time.rate > maxRate) maxRate = SL.time.rate; if (!SL.time.T.catching && S.wt - S.vt < 0.2 && t > 0.1) break; }
      res.catchUpSeconds = +t.toFixed(2); res.maxRate = +maxRate.toFixed(2); res.maxNodesDuringCatchUp = maxNodes; res.digestRows = T.dg();
      const dg = document.querySelector('#feed .digest .dtog'); res.digestText = dg ? dg.textContent.replace(/\\s+/g, ' ') : null;
    }
    T.until(() => S.wt >= END, 3000, 0.25); T.until(() => S.wt - S.vt < 0.2 && S.idx === S.widx, 20);
    res.final = { nodes: T.counts().nodes, rows: document.querySelectorAll('#talk .msg, #feed .msg').length, gap: +(S.wt - S.vt).toFixed(3), idxEqWidx: S.idx === S.widx, wt: Math.round(S.wt), sigView: T.sig(S.m), sigWorldNoChat: T.sig(S.wm, true), sigViewNoChat: T.sig(S.m, true) };
    return res })()`);
  r.errors = page.errors.slice(); await page.close(); return r;
};
const out = {};
for (const [name, tasks] of [['withTasks', true], ['noTasks', false]]) {
  const B = await run(false, tasks), A = await run(true, tasks);
  out[name] = { never: { rows: B.final.rows, nodes: B.final.nodes, errors: B.errors.length }, held: { ...A, final: { ...A.final, sigView: undefined, sigWorldNoChat: undefined, sigViewNoChat: undefined }, counts0: undefined },
    stateEqualToNeverHeld: A.final.sigViewNoChat === B.final.sigViewNoChat, viewEqualsWorld: A.final.sigViewNoChat === A.final.sigWorldNoChat, chatCountsEqualExceptDigest: A.final.sigView === B.final.sigView,
    domDeltaVsBeforeHold: A.final.nodes - A.nodes0, domDeltaVsNeverHeld: A.final.nodes - B.final.nodes };
  if (!out[name].stateEqualToNeverHeld) { const a = JSON.parse(A.final.sigViewNoChat), b = JSON.parse(B.final.sigViewNoChat); for (const k of Object.keys(a)) if (JSON.stringify(a[k]) !== JSON.stringify(b[k])) console.error('DIFF', name, k, JSON.stringify(a[k]).slice(0, 400), '\n   vs never-held', JSON.stringify(b[k]).slice(0, 400)); }
  if (!out[name].chatCountsEqualExceptDigest) { const a = JSON.parse(A.final.sigView).chan, b = JSON.parse(B.final.sigView).chan; for (const k of Object.keys(a)) if (JSON.stringify(a[k]) !== JSON.stringify(b[k])) console.error('chan diff', name, k, JSON.stringify(a[k]), JSON.stringify(b[k])); }
}
// 20 rapid hold/release cycles in 2 s, and a hold during a mail arc
{
  const page = await open(FILE, { query: 'manual' });
  out.cycles = await page.eval(`(async()=>{ const SL = __SL, T = SL.test; T.manual(); SL.act.switchSession('docs-sweep'); const S = SL.sessions.active; S.endgamed = true; T.run(2);
    const gaps = [], c0 = T.counts();
    for (let i = 0; i < 20; i++) { SL.time.setHover('chat'); T.run(0.05); SL.time.setHover(null); T.run(0.05); gaps.push(+(S.wt - S.vt).toFixed(3)); }
    const during = { maxGap: Math.max(...gaps), rateNow: +SL.time.rate.toFixed(2) };
    T.run(3); const after = { gap: +(S.wt - S.vt).toFixed(3), rate: +SL.time.rate.toFixed(3), catching: SL.time.T.catching, idxEqWidx: S.idx === S.widx, viewEqualsWorld: T.sig(S.m, true) === T.sig(S.wm, true), held: SL.time.held };
    const c1 = T.counts(); return { during, after, nodes: [c0.nodes, c1.nodes], listeners: [c0.listeners, c1.listeners], timers: [c0.timers, c1.timers] } })()`);
  out.arc = await page.eval(`(async()=>{ const SL = __SL, T = SL.test; SL.views.show('cockpit'); const S = SL.sessions.active; T.run(0.5);
    S.add({ k: 'mail', from: 'dc-1', to: 'dc-2', text: 'arc test' }); T.until(() => document.querySelectorAll('.arcs g').length > 0, 3);
    const arcs0 = document.querySelectorAll('.arcs g').length; SL.time.pin(true); S.hold.pinned = true; T.run(2); const arcsHeld = document.querySelectorAll('.arcs g').length;
    SL.time.pin(false); S.hold.pinned = false; T.until(() => document.querySelectorAll('.arcs g').length === 0, 6); const arcsAfter = document.querySelectorAll('.arcs g').length, envs = document.querySelectorAll('.arc-env').length;
    S.add({ k: 'mail', from: 'dc-2', to: 'dc-1', text: 'second' }); T.until(() => document.querySelectorAll('.arcs g').length > 0, 3); SL.views.show('cache'); T.run(0.1);
    return { arcs0, arcsHeld, arcsAfter, envsAfter: envs, anywhereAfterSwitch: document.querySelectorAll('.arcs,.arc-env,.travel').length } })()`);
  out.errors = page.errors.slice(); await page.close();
}
console.log(JSON.stringify(out, null, 1));
