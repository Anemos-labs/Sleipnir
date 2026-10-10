// t-perf.mjs: CPU at rest and on a hold (real time, CDP Performance metrics: seconds of main-thread task time per wall second), and the DOM bound over ten simulated minutes.
import { open } from './cdp.mjs';
const FILE = process.argv[2] || '../dist/pack-test.html', out = {};
const page = await open(FILE, {});
const metric = async () => { const r = await page.send('Performance.getMetrics'); const m = {}; r.metrics.forEach(x => { m[x.name] = x.value; }); return m; };
await page.send('Performance.enable'); await page.sleep(2500);
const cpu = async (label, ms) => { const a = await metric(); await page.sleep(ms); const b = await metric(); out[label] = { taskPct: +(((b.TaskDuration - a.TaskDuration) / ((b.Timestamp - a.Timestamp)) * 100)).toFixed(1), scriptPct: +(((b.ScriptDuration - a.ScriptDuration) / (b.Timestamp - a.Timestamp) * 100)).toFixed(1), layoutPct: +(((b.LayoutDuration - a.LayoutDuration) / (b.Timestamp - a.Timestamp) * 100)).toFixed(1), nodes: b.Nodes }; };
const ev = js => page.eval('(async()=>{ const SL = __SL, $ = s => document.querySelector(s); ' + js + ' })()');
await cpu('cockpit at rest', 4000);
await page.move(1240, 720); await page.sleep(900); await cpu('cockpit, pointer over the chat (hold)', 4000);
out.holdRate = await ev(`return { rate: +SL.time.rate.toFixed(3), held: SL.time.held, hover: SL.time.T.hover }`);
await page.move(5, 5); await page.sleep(300);
await ev(`SL.ui.nav.go('changes')`); await cpu('workspace (changes) at rest', 4000);
await ev(`SL.ui.settingsPage('permissions')`); await cpu('settings at rest', 3000);
await ev(`SL.views.show('cache')`); await cpu('cache at rest', 3000);
await ev(`SL.views.show('cockpit')`); await page.send('Emulation.setCPUThrottlingRate', { rate: 1 });
out.errors = page.errors; console.log(JSON.stringify(out, null, 1)); await page.close();
