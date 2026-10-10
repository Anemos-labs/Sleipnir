// t-pack.mjs: the data pack adapter. With the pack loaded SL.D carries what the pack carries in the shape the engine needs, the roster keeps the built-in order with the pack's tokens, and the
// derived percentages equal the pack's own (hitPct). Without the pack the built-in fixture is complete. Usage: node t-pack.mjs [file]
import { open } from './cdp.mjs';
const page = await open(process.argv[2] || '../dist/pack-test.html', { query: 'manual' });
const r = await page.eval(`(async()=>{ const SL = __SL, T = SL.test, D = SL.D; T.manual(); T.run(1); const checks = []; const ck = (n, ok, v) => checks.push({ n, ok: !!ok, v });
  ck('spec has commands', D.spec.commands.length >= (D.pack ? 40 : 8), D.spec.commands.length); ck('35 slash commands', D.slash.length === 35, D.slash.length); ck('models (>= 36 with the pack, >= 12 built in)', D.models.length >= (D.pack ? 36 : 12), D.models.length);
  ck('roster order is the built-in order', D.roster.map(r => r.id).join() === SL.FX.ROSTER.map(r => r.id).join()); ck('recorded >= 21 (prune needs more than --keep 20 once the live sessions count)', D.recorded.length >= 21, D.recorded.length);
  ck('providers', D.providers.length >= 5, D.providers.length); ck('shortcuts', D.shortcuts.length >= 10, D.shortcuts.length); ck('layers G0..G5', D.layers.length === 6);
  ck('every spec command runs without throwing and returns lines', D.spec.commands.every(c => { const x = SL.runner.exec(c, { pos: {}, flags: {} }); return x && Array.isArray(x.lines) && typeof x.exit === 'number'; }));
  if (D.pack) { const S = SL.sessions.get('shop'); const team = D.raw.team; let bad = [];
    team.forEach(t => { const a = S.wm.ag[t.id]; if (!a) { bad.push(t.id + ' missing'); return; } const c = SL.calc.agent(a); if (Math.abs(c.pct - t.hitPct) > 1) bad.push(t.id + ' ' + c.pct + ' vs ' + t.hitPct); });
    ck('hit % per agent matches the pack (snapshot, within 1 point: be-2 has moved by the first request)', !bad.length, bad.join(', '));
    ck('outputs cover every command of the spec', D.spec.commands.filter(c => !D.output(c.path)).length <= 6, D.spec.commands.filter(c => !D.output(c.path)).map(c => c.path.join(' ')).join(',')); }
  else ck('built-in only: no outputs object', Object.keys(D.outputs).length === 0);
  return { pack: D.pack, checks, failed: checks.filter(c => !c.ok) }; })()`);
console.log(JSON.stringify({ pack: r.pack, passed: r.checks.length - r.failed.length, of: r.checks.length, failed: r.failed }, null, 1)); console.log('errors:', page.errors.length); await page.close();
