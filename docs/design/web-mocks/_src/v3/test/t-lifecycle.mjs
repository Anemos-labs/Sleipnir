// t-lifecycle.mjs: the View lifecycle (no animation, timer, listener or element carries over) and the multi-session registry.
import { open } from './cdp.mjs';
const FILE = process.argv[2] || '../dist/test.html', out = {};
const page = await open(FILE, { query: 'manual' });
const ev = js => page.eval('(async()=>{ const SL = __SL, T = SL.test; ' + js + ' })()');
const diffObj = (a, b) => { const o = {}; Object.keys(a).forEach(k => { if (typeof a[k] === 'number') o[k] = b[k] - a[k]; }); return o; };
await ev(`T.manual(); T.run(1);`);
/* 1. a mail arc, switch within 50 ms, assert nothing of it exists anywhere */
out.arc = await ev(`const S = SL.sessions.active; SL.views.show('cockpit'); T.run(0.3);
  S.add({ k: 'mail', from: 'be-1', to: 'fe-1', text: 'lifecycle test' }); T.until(() => document.querySelectorAll('.arcs g').length > 0, 3);
  await T.idle(); const before = { arcs: document.querySelectorAll('.arcs g').length }; const old = SL.views.current.scope, c0 = T.counts();
  T.run(0.05); SL.views.show('cache'); const afterStats = old.stats();
  const nodes = document.querySelectorAll('.arcs,.arc-env,.travel,.drawer,.layer').length, c1 = T.counts();
  await T.idle(); return { before, oldScopeAlive: old.alive, oldScopeStats: afterStats, arcOrFlashNodesAnywhere: nodes, countsBeforeAfter: [c0, c1] }`);
/* 2. 30 rapid switches across every view: counts before vs after */
out.switches = await ev(`const views = ['cockpit', 'cache', 'mail', 'board', 'replay', 'sessions', 'settings', 'runner', 'kit']; SL.views.show('cockpit'); T.run(0.5);
  for (let i = 0; i < 9; i++) { SL.views.show(views[i]); T.run(0.1); } SL.views.show('cockpit'); T.run(0.3);
  await T.idle(); const part = () => { const n = s => document.querySelector(s).getElementsByTagName('*').length; return { views: n('#views'), rail: n('#rail'), outsideViewsAndRail: document.getElementsByTagName('*').length - n('#views') - n('#rail') }; }; const p0 = part(); const c0 = T.counts(), lm0 = T.listenerMap(), s0 = SL.views.stats(); for (let i = 0; i < 30; i++) { SL.views.show(views[(i * 5 + 1) % views.length]); if (i % 3 === 0) { SL.sessions.active.add({ k: 'mail', from: 'be-2', to: 'ts-1', text: 'x' + i }); } T.run(0.03); }
  SL.views.show('cockpit'); T.run(0.5); await T.settle(); await T.gc(); await T.idle(); const c1 = T.counts(), lm1 = T.listenerMap(), s1 = SL.views.stats(); const lmd = {}; Object.keys(lm1).forEach(k => { if (lm1[k] !== (lm0[k] || 0)) lmd[k] = (lm0[k] || 0) + ' -> ' + lm1[k]; }); Object.keys(lm0).forEach(k => { if (!(k in lm1)) lmd[k] = lm0[k] + ' -> 0'; });
  const diff = {}; Object.keys(c0).forEach(k => { diff[k] = c1[k] - c0[k]; }); return { listenerDiff: lmd, before: c0, after: c1, diff, nodeParts: [p0, part()], mounts: s1.mounts - s0.mounts, unmounts: s1.unmounts - s0.unmounts, viewRootChildren: s1.rootChildren }`);
/* 3. the drawer and a popover die with their view */
out.drawer = await ev(`SL.views.show('cockpit'); T.run(0.3); SL.ui.openDrawer('be-2'); T.run(0.2); const open = document.querySelectorAll('.drawer').length; SL.views.show('mail'); T.run(0.1); const k = document.querySelectorAll('.drawer').length; SL.views.show('kit'); T.run(0.2); document.querySelector('[data-o="popover"]').click(); const pops = document.querySelectorAll('.popover').length; SL.views.show('cockpit'); T.run(0.1); return { drawerOpen: open, drawerAfterSwitch: k, popoverOpen: pops, popoverAfterSwitch: document.querySelectorAll('.popover').length, linkSel: SL.link.sel }`);
/* 4. sessions: 50 switches among the three while questions arrive; badges; no leak */
out.sessions = await ev(`SL.views.show('cockpit'); T.run(0.5); const ids = ['shop', 'orders-api', 'docs-sweep']; SL.act.switchSession('shop'); T.run(0.3); await T.idle(); const base = T.counts(); const log = []; let bad = 0;
  for (let i = 0; i < 50; i++) { const id = ids[i % 3]; SL.act.switchSession(id); T.run(0.05);
    if (i === 10) SL.sessions.get('orders-api').add({ k: 'ask', q: { id: 'qa', agent: 'mgr', task: null, cmd: 'git push origin main', cwd: '.', why: 'sends commits to a remote', scope: 'git push', cont: 'generic' } });
    if (i === 25) SL.sessions.get('docs-sweep').add({ k: 'ask', q: { id: 'qb', agent: 'dc-1', task: 'T3', cmd: 'npm run lint:md', cwd: 'docs/', why: 'runs a script from the network', scope: 'npm run', cont: 'generic' } });
    T.run(0.05); const need = SL.sessions.needs(), badges = document.querySelectorAll('#sstrip .stab .nb').length, inbox = document.querySelector('#sInbox b').textContent;
    const expectedBadges = SL.sessions.list.filter(S => SL.calc.openQuestion(S.wm)).length; if (badges !== expectedBadges || +inbox !== need.length) bad++; if (i % 10 === 9) log.push({ i, active: SL.sessions.active.id, badges, inbox, need: need.map(n => n.S.id + ':' + n.q.id) }); }
  SL.act.switchSession('shop'); T.run(0.5); await T.settle(); await new Promise(r => setTimeout(r, 3800)); /* the last toast (3.6 s) is gone */ await T.gc(); await T.idle(); const c1 = T.counts(); const diff = {}; Object.keys(base).forEach(k => { diff[k] = c1[k] - base[k]; });
  return { badgeMismatches: bad, samples: log, base, after: c1, diff, activeViewMountedFor: SL.views.current.name }`);
/* 5. create and close sessions: no residue */
out.newclose = await ev(`SL.act.switchSession('shop'); T.run(0.3); await T.idle(); const c0 = T.counts(), lm0 = T.listenerMap(), n0 = SL.sessions.list.length;
  for (let i = 0; i < 8; i++) { const S = SL.act.newSession({ name: 'tmp-' + i, swarm: i % 5, goalText: i % 2 ? 'x' : '' }); T.run(0.4); SL.act.closeSession(S.id); T.run(0.1); }
  SL.act.switchSession('shop'); T.run(0.5); await T.settle(); await T.gc(); await T.idle(); const c1 = T.counts(), d = {}, lm1 = T.listenerMap(), lmd = {}; Object.keys(c0).forEach(k => { d[k] = c1[k] - c0[k]; }); Object.keys(lm1).forEach(k => { if (lm1[k] !== (lm0[k] || 0)) lmd[k] = (lm0[k] || 0) + ' -> ' + lm1[k]; }); Object.keys(lm0).forEach(k => { if (!(k in lm1)) lmd[k] = lm0[k] + ' -> 0'; }); return { listenerDiff: lmd, sessionsBefore: n0, sessionsAfter: SL.sessions.list.length, diff: d, ids: SL.sessions.list.map(S => S.id) }`);
/* 6. a background question raises a toast and a badge, answers from the inbox, and never auto-answers */
out.inbox = await ev(`SL.act.switchSession('shop'); T.run(0.3); const S = SL.sessions.get('orders-api'); const q = SL.calc.openQuestion(S.wm); const before = q ? q.id : null;
  const toast = Array.from(document.querySelectorAll('#toasts .toast')).map(t => t.textContent);
  const r1 = SL.act.answerQuestion('qa', 2, undefined, 'orders-api'); T.run(0.3); const after = SL.calc.openQuestion(S.wm); const rule = S.meta.rules.map(r => r.rule + '|' + r.origin);
  return { openBefore: before, toastCount: toast.length, toasts: toast.slice(0, 3), answered: r1.ok, openAfter: after ? after.id : null, rules: rule, shopStillWaiting: !!SL.calc.openQuestion(SL.sessions.get('shop').wm) }`);
/* 6. re-mount at a frozen sim time: every view's DOM node and listener counts are identical after 10 away-and-back cycles (nothing accumulates in the view) */
out.remount = await ev(`SL.act.switchSession('shop'); SL.views.show("cockpit"); T.run(2); await T.settle(); await T.settle(); const res = {}; const cnt = () => ({ nodes: document.getElementsByTagName('*').length, c: T.counts() });
  for (const v of ['cockpit', 'cache', 'mail', 'board', 'replay', 'sessions', 'settings', 'runner', 'kit']) { SL.views.show(v); await T.gc(); await T.idle(); const a = cnt(); for (let i = 0; i < 10; i++) { SL.views.show(v === 'kit' ? 'cockpit' : 'kit'); SL.views.show(v); } await T.gc(); await T.idle(); const b = cnt(); res[v] = { nodes: [a.nodes, b.nodes], listeners: [a.c.listeners, b.c.listeners], timers: [a.c.timers + a.c.ints + a.c.rafs, b.c.timers + b.c.ints + b.c.rafs], scopes: [a.c.scopes, b.c.scopes], same: a.nodes === b.nodes && a.c.listeners === b.c.listeners && a.c.scopes === b.c.scopes }; }
  SL.views.show('cockpit'); return res`);
out.errors = page.errors.slice(); await page.close();
console.log(JSON.stringify(out, null, 1));
