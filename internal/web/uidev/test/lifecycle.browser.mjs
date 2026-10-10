// lifecycle.browser.mjs: the mock's t-lifecycle.mjs on the REAL page (TEST-PLAN.md 6): nothing a view or a session starts outlives it.
// The page runs on internal/web/uidev/mockapi.mjs (the mock's own three sessions as API data) with hooks-real.js injected before its
// scripts; questions arrive through the live layer (SL.live.applyEv) while the views and the sessions switch.
//
//   node internal/web/uidev/test/lifecycle.browser.mjs
//
// It needs a Chromium (CHROME_PATH, or the Playwright headless shell the mock's tests use); it is not part of `node --test` and not of CI.
import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert/strict';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url)), repo = path.resolve(here, '../../../..');
const { open } = await import(path.join(repo, 'docs/design/web-mocks/_src/v3/test/cdp.mjs'));
const { start } = await import(path.join(here, '..', 'mockapi.mjs'));
const srv = await start({});
const p = await open('about:blank', { w: 1440, h: 900 });
await p.send('Page.addScriptToEvaluateOnNewDocument', { source: fs.readFileSync(path.join(here, '..', 'hooks-real.js'), 'utf8') });
await p.send('Page.navigate', { url: srv.url }); await p.sleep(2500);
const ev = js => p.eval('(async () => { const SL = window.__SL, T = SL.test, sleep = ms => new Promise(r => setTimeout(r, ms)); ' + js + ' })()');
let failed = 0;
const ok = (what, fn) => { try { fn(); console.log('ok', what); } catch (e) { failed++; console.log('FAIL', what + ': ' + e.message); } };
const zero = (d, keys) => keys.forEach(k => assert.equal(d[k], 0, k + ' changed by ' + d[k]));
try {
  const views = await ev(`const views = ['cockpit', 'cache', 'mail', 'board', 'replay', 'sessions', 'settings', 'runner'];
    SL.views.show('cockpit'); await sleep(300); await T.settle(); await T.gc(); await T.idle(); const c0 = T.counts();
    for (let i = 0; i < 30; i++) { SL.views.show(views[(i * 5 + 1) % views.length]); if (i % 3 === 0) { const S = SL.sessions.active; SL.live.applyEv(S, { seq: S.lastSeq + 1, t: S.wt, k: 'mail', from: 'be-2', to: 'ts-1', text: 'x' + i }); } await sleep(30); }
    SL.views.show('cockpit'); await sleep(400); await T.settle(); await T.gc(); await T.idle(); const c1 = T.counts(), d = {}; Object.keys(c0).forEach(k => { d[k] = c1[k] - c0[k]; }); return { d, c0, c1 };`);
  ok('30 view switches leave no timer, listener, observer, animation, frame hook, updater or scope behind', () => zero(views.d, ['ints', 'rafs', 'listeners', 'observers', 'frames', 'updaters', 'scopes', 'bus']));

  const drawer = await ev(`SL.views.show('cockpit'); await sleep(200); SL.ui.openDrawer('be-2'); await sleep(200); const open = document.querySelectorAll('.drawer').length; SL.views.show('mail'); await sleep(100); return { open, after: document.querySelectorAll('.drawer').length, sel: SL.link.sel };`);
  ok('the drawer dies with its view', () => { assert.equal(drawer.open, 1); assert.equal(drawer.after, 0); assert.equal(drawer.sel, null); });

  const sessions = await ev(`SL.views.show('cockpit'); const ids = SL.sessions.list.map(S => S.id); SL.act.switchSession(ids[0]); await sleep(300); await T.settle(); await T.gc(); await T.idle(); const base = T.counts(); let bad = 0;
    for (let i = 0; i < 50; i++) { SL.act.switchSession(ids[i % ids.length]); await sleep(40);
      if (i === 10 || i === 25) { const S = SL.sessions.get(ids[i === 10 ? 1 : 2] || ids[0]); SL.live.applyEv(S, { seq: S.lastSeq + 1, t: S.wt, k: 'ask', q: { id: 'q_life' + i, agent: 'mgr', cmd: 'git push origin main', cwd: '.', why: 'sends commits to a remote', what: 'this command' } }); }
      await sleep(20); const need = SL.sessions.needs(), badges = document.querySelectorAll('#sstrip .stab .nb').length, inbox = +document.querySelector('#sInbox b').textContent;
      if (badges !== SL.sessions.list.filter(S => SL.calc.openQuestion(S.wm)).length || inbox !== need.length) bad++; }
    SL.act.switchSession(ids[0]); await sleep(400); await T.settle(); await sleep(3800); await T.gc(); await T.idle(); const c1 = T.counts(), d = {}; Object.keys(base).forEach(k => { d[k] = c1[k] - base[k]; }); return { bad, d, title: document.title };`);
  ok('50 session switches while questions arrive: badges, the inbox and the title agree; nothing leaks', () => { assert.equal(sessions.bad, 0); zero(sessions.d, ['ints', 'rafs', 'listeners', 'observers', 'frames', 'updaters', 'scopes', 'bus']); assert.match(sessions.title, /^\(\? \d+\) /); });

  const remount = await ev(`SL.views.show('cockpit'); await sleep(300); await T.settle(); const res = {}; const cnt = () => ({ nodes: document.getElementsByTagName('*').length, c: T.counts() });
    for (const v of ['cockpit', 'cache', 'mail', 'board', 'replay', 'sessions', 'settings', 'runner']) { SL.views.show(v); await T.gc(); await T.idle(); const a = cnt(); for (let i = 0; i < 10; i++) { SL.views.show(v === 'board' ? 'mail' : 'board'); SL.views.show(v); } await T.gc(); await T.idle(); const b = cnt(); res[v] = { listeners: [a.c.listeners, b.c.listeners], scopes: [a.c.scopes, b.c.scopes], updaters: [a.c.updaters, b.c.updaters] }; }
    SL.views.show('cockpit'); return res;`);
  ok('a view mounted 10 times keeps its listener, scope and updater counts', () => Object.keys(remount).forEach(v => { const r = remount[v]; assert.equal(r.listeners[1], r.listeners[0], v + ' listeners'); assert.equal(r.scopes[1], r.scopes[0], v + ' scopes'); assert.equal(r.updaters[1], r.updaters[0], v + ' updaters'); }));
  const errs = p.errors.filter(e => !/^log: Failed to load resource: the server responded with a status of 404/.test(e));   /* a route the test server does not have is not a page error */
  if (errs.length) { failed++; console.log('FAIL page errors:\n' + errs.slice(0, 10).join('\n')); }
} finally { await p.close(); srv.close(); }
console.log(failed ? failed + ' failed' : 'all checks passed');
process.exit(failed ? 1 : 0);
