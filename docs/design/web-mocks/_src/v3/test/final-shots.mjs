// final-shots.mjs: the verified states as PNGs under $SHOTS (default shots/core). Serial: one browser at a time. Usage: node final-shots.mjs [only-prefix]
import { open } from './cdp.mjs';
import fs from 'node:fs';
const OUT = process.env.SHOTS, ONLY = process.argv[2] || '', FILE = process.env.FILE || '../dist/test.html';
fs.mkdirSync(OUT, { recursive: true });
const done = [];
async function scene(name, w, h, fn, o = {}) {
  if (ONLY && !name.startsWith(ONLY)) return;
  const page = await open(FILE, { w, h, query: 'manual', reduced: !!o.reduced, scale: o.scale || 1 });
  const ev = js => page.eval('(async()=>{ const SL = __SL, T = SL.test, S = () => SL.sessions.active; ' + js + ' })()');
  try { await ev('T.manual(); T.run(3);'); await fn(ev, page); await page.sleep(300); await page.shot(OUT + '/' + name + '.png'); done.push(name + (page.errors.length ? ' ERRORS ' + JSON.stringify(page.errors) : '')); }
  finally { await page.close(); }
}
const key = async (ev, k) => ev(`document.dispatchEvent(new KeyboardEvent('keydown', { key: ${JSON.stringify(k)}, bubbles: true }));`);

await scene('01-cockpit-1440x900', 1440, 900, async () => {});
await scene('02-cache-break-flash', 1440, 900, async ev => { await ev(`T.until(() => S().m.anomalies.length > 0, 20); T.run(0.35);`); });
await scene('03-question-answered', 1440, 900, async ev => { await ev(`T.run(1.5); const q = SL.calc.openQuestion(S().m); SL.act.answerQuestion(q.id, 2, undefined, 'shop'); T.run(9);`); });
await scene('04-hold-pinned-while-events-pile-up', 1440, 900, async ev => { await ev(`SL.time.pin(true); T.run(50); const S_ = S(); return 0;`); });
await scene('05-after-release-digest-row', 1440, 900, async ev => { await ev(`SL.time.pin(true); T.run(40); SL.time.pin(false); T.run(6); const c = document.querySelector('.digest'); if (c) c.scrollIntoView({ block: 'center' });`); });
await scene('06-channel-be-2-transcript', 1440, 900, async ev => { await ev(`T.run(4); document.querySelector('[data-chan="be-2"]').click(); T.run(0.5);`); });
await scene('07-drawer-be-2-steer', 1440, 900, async ev => { await ev(`T.run(3); SL.ui.openDrawer('be-2'); T.run(0.4); const t = document.querySelector('.drawer [data-tab="steer"]'); if (t) t.click(); T.run(0.3);`); });
await scene('08-interrupt-be-2', 1440, 900, async ev => { await ev(`T.run(2); SL.act.interrupt('be-2'); T.run(1.5); document.querySelector('[data-chan="be-2"]').click(); T.run(0.5);`); });
for (const v of ['cache', 'mail', 'board', 'replay', 'sessions', 'settings', 'runner', 'kit']) await scene('10-view-' + v, 1440, 900, async ev => { await ev(`T.run(8); SL.views.show('${v}'); T.run(0.6);`); });
await scene('11-runner-sim-output', 1440, 900, async ev => { await ev(`SL.views.show('runner', { path: ['sim'], flags: { agents: 8, turns: 12 } }); T.run(0.4); const b = document.querySelector('.runv .rrun, .runv [data-run]'); if (b) b.click(); T.run(6);`); });
await scene('12-dialog-new-session', 1440, 900, async ev => { await ev(`SL.ui.dialogs.newSession(); T.run(0.3);`); });
await scene('13-dialog-resume', 1440, 900, async ev => { await ev(`SL.ui.dialogs.resume(); T.run(0.3);`); });
await scene('14-sheet-team-restart', 1440, 900, async ev => { await ev(`SL.ui.sheets.team(); T.run(0.3);`); });
await scene('15-inbox-cross-session', 1440, 900, async ev => { await ev(`T.run(40); SL.ui.inbox.toggle(); T.run(0.4);`); });
await scene('16-orders-api-single-agent', 1440, 900, async ev => { await ev(`SL.act.switchSession('orders-api'); SL.views.show('cockpit'); T.run(4);`); });
await scene('17-docs-sweep-refused', 1440, 900, async ev => { await ev(`SL.act.switchSession('docs-sweep'); SL.views.show('cockpit'); T.until(() => S().m.refused > 0, 400); T.run(1);`); });
await scene('18-ninth-worker-badge', 1440, 900, async ev => { await ev(`SL.act.restartTeam({ swarm: 9 }); SL.views.show('cockpit'); T.run(12);`); });
await scene('19-palette', 1440, 900, async ev => { await ev(`SL.palette.open(''); T.run(0.3);`); });
await scene('20-help-overlay', 1440, 900, async ev => { await ev(`SL.ui.sheets.help(); T.run(0.3);`); });
await scene('30-1024x700', 1024, 700, async () => {});
await scene('31-1280x720', 1280, 720, async () => {});
await scene('32-1920x1080', 1920, 1080, async () => {});
await scene('33-phone-390-cockpit', 390, 844, async () => {});
await scene('34-phone-390-radio', 390, 844, async ev => { await ev(`const b = document.querySelector('#mnav [data-pv="radio"], #mnav button:nth-child(2)'); if (b) b.click(); T.run(0.4);`); });
await scene('35-phone-390-cache', 390, 844, async ev => { await ev(`SL.views.show('cache'); T.run(0.5);`); });
await scene('36-tablet-768', 768, 1024, async () => {});
await scene('40-reduced-motion', 1440, 900, async () => {}, { reduced: true });
if (/pack/.test(FILE)) {   // the data-pack build: FILE=../dist/pack-test.html node final-shots.mjs 50
  await scene('50-pack-cockpit', 1440, 900, async () => {});
  for (const [n, path, flags] of [['recon', ['recon'], {}], ['rl-taskgen-git', ['rl', 'taskgen', 'git'], { repo: '.' }], ['sessions-prune', ['sessions', 'prune'], {}], ['config', ['config'], {}], ['models', ['models'], {}]])
    await scene('51-pack-runner-' + n, 1440, 900, async ev => { await ev(`SL.views.show('runner', { path: ${JSON.stringify(path)}, flags: ${JSON.stringify(flags)} }); T.run(0.5); const b = document.querySelector('.runv .rrun, .runv [data-run]'); if (b) b.click(); T.run(6);`); });
  await scene('52-pack-sessions-prune-panel', 1440, 900, async ev => { await ev(`SL.views.show('sessions'); T.run(0.6);`); });
  await scene('53-pack-settings', 1440, 900, async ev => { await ev(`SL.views.show('settings'); T.run(0.6);`); });
}
console.log(done.join('\n'));
