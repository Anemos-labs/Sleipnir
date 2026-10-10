// Runs the real `sleipnir sim` over a grid of flags and stores text (and JSON for the defaults) in build/sim.json.
import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
const SL = process.env.SL;
const run = (...a) => execFileSync(SL, ['sim', ...a], { encoding: 'utf8' });
const text = {}, json = {}, head = {};
const providers = ['anthropic', 'marketplace'];
for (const p of providers) {
  for (const ag of [20, 50]) {
    for (const seed of (ag === 20 ? [1, 2, 3] : [1])) text[`compare|${p}|${ag}|${seed}`] = run('--mode', 'compare', '--provider', p, '--agents', String(ag), '--seed', String(seed));
    text[`scenarios|${p}|${ag}|1`] = run('--mode', 'scenarios', '--provider', p, '--agents', String(ag));
    text[`pins|${p}|${ag}|1`] = run('--mode', 'pins', '--provider', p, '--agents', String(ag));
  }
  text[`agents|${p}|20|1`] = run('--mode', 'agents', '--provider', p);
  for (const m of (p === 'anthropic' ? ['compare', 'scenarios', 'pins', 'agents'] : ['compare'])) json[`${m}|${p}`] = JSON.parse(run('--mode', m, '--provider', p, '--json'));
}
// dedupe identical texts: map key -> index into a pool
const pool = [], idx = {};
for (const [k, t] of Object.entries(text)) { let i = pool.indexOf(t); if (i < 0) { pool.push(t); i = pool.length - 1; } idx[k] = i; }
const out = { grid: { providers, agents: [20, 50], seeds: [1, 2, 3], note: 'compare: seeds 1-3 at 20 workers, seed 1 at 50; scenarios and pins: seed 1; agents mode: one table per provider; any other flag value falls back to the nearest precomputed case' }, idx, pool, json };
fs.writeFileSync('build/sim.json', JSON.stringify(out));
console.log('sim.json', fs.statSync('build/sim.json').size, 'pool', pool.length, 'of', Object.keys(text).length);
