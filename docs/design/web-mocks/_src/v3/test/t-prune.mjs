// t-prune.mjs: `sessions prune` arithmetic. For several (--older-than, --keep) pairs the registry's dry run is compared with an independent computation and, when the data pack
// is loaded, with the pack's own `sessions prune` output (count, ids, megabytes). Then --yes is applied and the list must shrink by exactly that many. Usage: node t-prune.mjs [file]
import { open } from './cdp.mjs';
const page = await open(process.argv[2] || '../dist/test.html', { query: 'manual' });
const r = await page.eval(`(async()=>{ const SL = __SL, T = SL.test, D = SL.D; T.manual(); T.run(1); const out = { pack: D.pack, cases: [], bad: 0 };
  const idsOf = x => x.lines.filter(l => l.k === 'out' && /^\\s+\\d{8}-\\d{6}-/.test(l.t)).map(l => l.t.trim().split(/\\s+/)[0]).sort();
  for (const [older, keep] of [['30d', 20], ['30d', 5], ['7d', 10], ['0', 20], ['2w', 0], ['90d', 20], ['30d', 100]]) {
    const r = SL.sessions.prune(older, keep, false), age = SL.sessions.parseAge(older), live = SL.sessions.list.length;
    const sorted = SL.sessions.recorded.slice().sort((a, b) => a.ageS - b.ageS), slots = Math.max(0, keep - live), want = sorted.filter((x, i) => i >= slots && x.ageS > age && x.ageS > 600).map(x => x.id).sort();
    const mine = r.list.map(x => x.id).sort(), c = { older, keep, n: mine.length, mb: +r.mb.toFixed(1), independent: JSON.stringify(mine) === JSON.stringify(want) };
    if (D.pack && D.outputs['sessions prune']) { const x = D.outputs['sessions prune']({ 'older-than': older, keep }, D.ctx()); const theirs = idsOf(x); c.pack = theirs.length; c.sameIdsAsPack = JSON.stringify(mine) === JSON.stringify(theirs); }
    if (!c.independent || (c.pack != null && !c.sameIdsAsPack)) out.bad++; out.cases.push(c); }
  const before = SL.sessions.recorded.length, r = SL.sessions.prune('30d', 20, true); out.applied = { deleted: r.list.length, before, after: SL.sessions.recorded.length, consistent: before - r.list.length === SL.sessions.recorded.length };
  const again = SL.sessions.prune('30d', 20, false); out.second = { again: again.list.length }; if (!out.applied.consistent || again.list.length) out.bad++;
  return out; })()`);
console.log(JSON.stringify(r, null, 1)); console.log('problems:', r.bad, 'errors:', page.errors.length); await page.close();
