// t-ten.mjs: ten simulated minutes (time warp) in the Cockpit, then five in the Workspace and Settings: the DOM stays bounded.
import { open } from './cdp.mjs';
const page = await open(process.argv[2] || '../dist/pack-test.html', { query: 'manual' }); const out = {};
const ev = js => page.eval('(async()=>{ const SL = __SL, T = SL.test, $ = s => document.querySelector(s), $$ = s => Array.from(document.querySelectorAll(s)); ' + js + ' })()');
await ev(`T.manual(); T.run(1);`);
const S0 = await ev(`const S = SL.sessions.active; SL.time.noteKey(); T.run(1); const q = SL.calc.openQuestion(S.wm); if (q) SL.act.answerQuestion(q.id, 1, undefined, 'shop'); T.run(40); return 'answered'`);
const sample = tag => ev(`await T.idle(); const c = T.counts(); return { at: ${JSON.stringify(tag)}, nodes: c.nodes, chatRows: $$('#talk .msg, #feed .msg').length, listeners: c.listeners, wt: Math.round(SL.sessions.active.wt), log: SL.sessions.active.log.length }`);
await ev(`const S = SL.sessions.active; T.flood(S, S.wt + 1, S.wt + 640, {}); T.run(2);`);
out.cockpit = [await sample('start')]; for (let m = 1; m <= 10; m++) { await ev(`T.run(60, 1 / 20)`); if ([1, 2, 5, 10].includes(m)) out.cockpit.push(await sample(m + ' min')); }
out.workspaceChanges = []; await ev(`SL.ui.nav.go('changes')`); for (let m = 1; m <= 4; m++) { await ev(`T.run(60, 1 / 20)`); out.workspaceChanges.push(await sample('ws +' + m + ' min')); }
await ev(`SL.ui.settingsPage('budget')`); await ev(`T.run(60, 1 / 20)`); out.settings = await sample('settings');
await ev(`SL.views.show('cockpit'); T.run(1)`); out.back = await sample('back to the cockpit');
out.errors = page.errors; console.log(JSON.stringify(out, null, 1)); await page.close();
