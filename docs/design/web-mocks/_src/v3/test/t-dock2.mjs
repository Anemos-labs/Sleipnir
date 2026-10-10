// t-dock2.mjs: lifecycle across ALL the Dock's views, numbers at five moments (incl. Settings > Budget), sessions through the UI (New, Rename, Resume, Stop,
// Close, Prune, the inbox), a keyboard-only run. Usage: node t-dock2.mjs ../dist/pack-test.html
import { open } from './cdp.mjs';
const FILE = process.argv[2] || '../dist/pack-test.html', out = {};
const page = await open(FILE, { query: 'manual' });
const ev = js => page.eval('(async()=>{ const SL = __SL, T = SL.test, $ = s => document.querySelector(s), $$ = s => Array.from(document.querySelectorAll(s)); ' + js + ' })()');
const nap = ms => new Promise(r => setTimeout(r, ms));
await ev(`T.manual(); T.run(1);`);

/* L. lifecycle: 60 rapid switches across every view, with things in flight; nothing is left behind */
out.life = await ev(`SL.views.show('cockpit'); T.run(0.5); await T.gc(); await T.idle(); const c0 = T.counts(); const S = SL.sessions.active;
  const views = [['cockpit'], ['workspace', { tab: 'files' }], ['workspace', { tab: 'changes' }], ['cache'], ['mail'], ['board'], ['settings'], ['tools'], ['doctor'], ['schedule'], ['runner'], ['sessions'], ['replay'], ['workspace', { tab: 'checkpoints' }]];
  S.add({ k: 'mail', from: 'be-1', to: 'ts-1', text: 'arc one' }); T.run(0.05); SL.views.show('workspace', { tab: 'files' });
  const arcsAfterFirst = $$('.arcs g, .arc-env, .ring-flash.pulse').length;
  for (let i = 0; i < 60; i++) { const v = views[i % views.length]; SL.views.show(v[0], v[1] || {}); if (v[0] === 'cockpit') { S.add({ k: 'mail', from: 'sc-1', to: 'be-2', text: 'arc ' + i }); T.run(0.04); SL.ui.openDrawer('be-1'); } if (v[0] === 'doctor') { const b = $('#dRun'); if (b) b.click(); } if (v[0] === 'schedule') { const b = $('[data-run="j2"]'); if (b) b.click(); } if (v[0] === 'runner') { const b = $('.rrun'); if (b) b.click(); } if (v[0] === 'settings') { SL.ui.settingsPage(['models', 'permissions', 'mcp', 'budget'][i % 4]); } if (i % 7 === 0) { const sb = $('#sBud'); if (sb) sb.click(); } T.run(0.03); }
  const strays = { arcs: $$('.arcs g').length, drawer: $$('.drawer').length, popovers: $$('.popover').length };
  $$('.budpop').forEach(p => p._off()); SL.views.show('cockpit'); T.run(2); await T.gc(); await T.idle(); await new Promise(r => setTimeout(r, 4500)); await T.gc(); const c1 = T.counts(); const diff = {}; Object.keys(c1).forEach(k => { if (typeof c1[k] === 'number') diff[k] = c1[k] - c0[k]; });
  return { arcsAfterFirstSwitch: arcsAfterFirst, strays, before: c0, after: c1, diff, mounts: SL.views.V.mounts, unmounts: SL.views.V.unmounts };`);

/* N. numbers, in BOTH cache modes: HUD (ring or chip), stalls, the Cache table, Settings > Budget, the strip, the Sessions card agree at five moments; every comparison reads the calculation fresh at the instant of the DOM read */
out.numbers = await ev(`const num = t => parseFloat(String(t).replace(/[^0-9.\\-]/g, '')); const rows = []; let bad = 0; const S = SL.sessions.active, vis = e => !!e && getComputedStyle(e).display !== 'none';
  const chk = (l, a, b, tol) => { if (Math.abs(a - b) > (tol || 0.51)) { bad++; rows.push(l + ' ' + a + ' vs ' + b); } };
  const tot = () => SL.calc.totals(S.m), ag = id => SL.calc.agent(S.m.ag[id]);
  let prev = 0;
  for (const t of [0, 4, 8, 20, 40]) { if (t > prev) { T.run(t - prev); prev = t; }
   for (const mode of ['quiet', 'full']) { SL.act.setCache(mode); const full = mode === 'full';
    SL.views.show('cockpit'); T.run(0.1); const hudHit = num($('#hitTxt').textContent), hudChip = num($('#hitChipN').textContent), hudCost = num($('#vCost').textContent), c1 = tot();
    chk(mode + ' hud ring vs calc', hudHit, c1.pct); chk(mode + ' hud chip vs calc', hudChip, c1.pct); chk(mode + ' hud cost vs calc', hudCost, c1.cost, 0.006);
    chk(mode + ' the ring shows in Full only', vis($('#ringHit > svg')) ? 1 : 0, full ? 1 : 0); chk(mode + ' the chip shows in Quiet only', vis($('#hitChip')) ? 1 : 0, full ? 0 : 1);
    chk(mode + ' stall hit figures shown only in Full', $$('.stall:not(.ghost) .n-hit').filter(vis).length > 0 ? 1 : 0, full ? 1 : 0); chk(mode + ' stall sparklines shown only in Full', $$('.stall:not(.ghost) .s-spark').filter(vis).length > 0 ? 1 : 0, full ? 1 : 0);
    $$('.stall:not(.ghost)').forEach(e => { chk(mode + ' stall ' + e.dataset.ag, num(e.querySelector('.n-hit').textContent), ag(e.dataset.ag).pct); });
    SL.ui.settingsPage('budget'); T.run(0.1); SL.loop.step(0.05); const bud = {}; $$('#budTbl tbody tr').forEach(r => { const td = r.querySelectorAll('td'); bud[td[0].textContent] = [num(td[2].textContent), num(td[4].textContent)]; }); const c2 = tot();
    chk(mode + ' budget all hit vs calc', bud.all[0], c2.pct); chk(mode + ' budget cost vs calc', bud.all[1], c2.cost, 0.00006); Object.keys(bud).forEach(id => { if (id !== 'all') chk(mode + ' budget ' + id, bud[id][0], ag(id).pct); });
    chk(mode + ' budget hit column shown only in Full', vis($('#budTbl thead th.qfull')) ? 1 : 0, full ? 1 : 0); chk(mode + ' budget savings line shown only in Full', vis($('.setpage .stubnote.qfull')) ? 1 : 0, full ? 1 : 0);
    SL.views.show('cache'); T.run(0.1); const cr = {}; $$('.ctable tbody tr, .ctable tfoot tr').forEach(r => { const td = r.querySelectorAll('td'); if (td.length > 3) cr[td[0].textContent.replace(/[^a-z0-9\\-]/gi, '')] = num(td[3].textContent); }); const c3 = tot(), strip = num($('[data-cost="shop"]').textContent);
    chk(mode + ' cache all vs calc', cr.all, c3.pct); chk(mode + ' strip cost vs calc', strip, c3.cost, 0.006); Object.keys(cr).forEach(id => { if (id !== 'all') chk(mode + ' cache ' + id, cr[id], ag(id).pct); });
    chk(mode + ' cache view saved column shown only in Full', vis($('.ctable thead th.qfull')) ? 1 : 0, full ? 1 : 0);
    rows.push(mode + ' t=' + t + ' calc ' + c3.pct + '% $' + c3.cost.toFixed(4) + ' | hud ' + hudHit + '/' + hudChip + '/' + hudCost + ' | budget all ' + bud.all.join('/') + ' | cache all ' + cr.all + ' | strip $' + strip); } }
  return { mismatches: bad, rows };`);

/* S. sessions through the UI */
out.sessions = await ev(`const o = {}; SL.views.show('cockpit'); T.run(0.2); $('#sNew').click(); T.run(0.3); o.dialog = !!$('.scrim .sheet'); o.fields = ['#nsName', '#nsCwd', '#nsModel', '#nsSwarm', '#nsVerify', '#nsBudget', '#nsRule', '#nsRole', '#nsGoal', '[data-k="commit"]', '[data-k="mailman"]', '[data-k="noMcp"]', '[data-k="trustProject"]', '[data-nsmode="bypass"]', '[data-seg="isolation"]'].map(s => $(s) ? 1 : 0).join('');
  $('#nsName').value = 'probe'; $('#nsName').dispatchEvent(new Event('input', { bubbles: true })); $('#nsSwarm').value = '2'; $('#nsSwarm').dispatchEvent(new Event('input', { bubbles: true })); $('#nsRole').value = 'tester'; $('#nsRoleM').value = 'anthropic/claude-haiku-5-5'; $('#nsAddRole').click(); T.run(0.1); o.cli = $('#nsCli').textContent; $('#nsStart').click(); T.run(0.5);
  const S = SL.sessions.active; o.created = { name: S.name, workers: S.roster.length - 1, tester: (S.roster.find(r => r.role === 'tester') || {}).model || '(no tester at 2 workers)', tabs: $$('.stab').map(b => b.querySelector('.sn').textContent) };
  $('.stab.sel').dispatchEvent(new MouseEvent('dblclick', { bubbles: true })); T.run(0.2); $('#rnIn').value = 'probe-2'; $('#rnGo').click(); T.run(0.2); o.renamed = $('.stab.sel .sn').textContent;
  $('#sRes').click(); T.run(0.3); o.resumeDialog = $$('.scrim tbody tr').length; $('[data-r="latest"]').click(); T.run(0.5); o.resumed = SL.sessions.active.name + ' / ' + SL.sessions.active.kind;
  $('#sOpt').click(); T.run(0.2); o.menu = $$('.sessmenu button').map(b => b.textContent.trim().slice(0, 18)); $('.sessmenu [data-a="stop"]').click(); T.run(0.3); o.stopConfirm = !!$('.scrim .sheet'); $('.scrim [data-no]').click(); T.run(0.2);
  const n0 = SL.sessions.list.length; $('.stabw.sel .stx').click(); T.run(0.3); o.closeAsk = !!$('.scrim .sheet'); $('.scrim [data-ok]').click(); T.run(0.3); o.closed = [n0, SL.sessions.list.length];
  const probe = SL.sessions.list.find(s => s.name === 'probe-2'); $('.stab[data-sid="' + probe.id + '"] ').click(); T.run(0.2); $('.stabw.sel .stx').click(); T.run(0.2); $('.scrim [data-ok]').click(); T.run(0.3); o.after = SL.sessions.list.map(s => s.name);
  /* prune through the Sessions view */
  SL.act.switchSession('shop'); SL.views.show('sessions'); T.run(0.3); const rec0 = SL.sessions.recorded.length; $('#pOlder').value = '30d'; $('#pOlder').dispatchEvent(new Event('input', { bubbles: true })); $('#pKeep').value = '20'; $('#pKeep').dispatchEvent(new Event('input', { bubbles: true })); o.dry = $('#pPre').textContent.split('\\n')[0]; $('#pYes').click(); T.run(0.3); $('.scrim [data-ok]').click(); T.run(0.3); o.prune = { before: rec0, after: SL.sessions.recorded.length, note: $('#pNote').textContent }; o.dry2 = $('#pPre').textContent.split('\\n')[0];
  return o;`);
out.inbox = await ev(`const o = {}; SL.act.switchSession('shop'); SL.views.show('cockpit'); T.run(0.3); $('#toasts').innerHTML = ''; SL.act.send('please commit this', 'orders-api'); T.run(0.5); await new Promise(r => setTimeout(r, 1700)); T.run(5); const need = SL.sessions.needs(); o.needs = need.map(x => x.S.name + ':' + x.q.agent); o.badge = $('#sInbox b').textContent; o.tabBadges = $$('.stabw .nb').map(b => b.closest('.stabw').querySelector('.sn').textContent + ' ' + b.textContent);
  o.toasts = $$('#toasts .toast').map(t => t.textContent.slice(0, 60)); $('#sInbox').click(); T.run(1.2); const row = $('.ibx[data-sid="orders-api"]'); o.row = !!row; if (row) { await T.idle(); T.run(1); row.querySelector('[data-choice="1"]').click(); T.run(0.5); } const q = SL.sessions.get('orders-api').wm.qs[0]; o.answered = q && q.answered; o.shopStillActive = SL.sessions.active.name; o.needsAfter = SL.sessions.needs().map(x => x.S.name); return o;`);

/* K. keyboard only: palette, switch session, answer, talk to the manager, open a diff */
await ev(`SL.ui.inbox.close(); SL.ui.closeModal(true); SL.act.switchSession('shop'); SL.views.show('cockpit'); T.run(0.3); document.activeElement && document.activeElement.blur();`);
await page.key('k', 2); await page.sleep(200); for (const ch of 'diff') await page.key(ch); await page.key('Enter'); await page.sleep(300);
out.kbd = { palette: await ev(`T.run(0.3); return { view: SL.views.current.name, tab: SL.sessions.active.ui.ws && SL.sessions.active.ui.ws.tab, mode: SL.sessions.active.ui.ws && SL.sessions.active.ui.ws.mode, hasDiff: $$('.ws-body .ln').length > 5 }` ) };
await page.key('2', 1); await page.sleep(200); out.kbd.alt2 = await ev(`T.run(0.2); return SL.sessions.active.name`);
await page.key('1', 1); await page.sleep(200); out.kbd.alt1 = await ev(`T.run(0.2); return SL.sessions.active.name`);
await page.key('g'); await page.key('c'); await page.sleep(200);
for (let i = 0; i < 6; i++) await page.key('ArrowDown'); await page.key('Enter'); await page.sleep(300);
out.kbd.focusAgent = await ev(`T.run(0.3); const d = $('.drawer'); return { chan: SL.sessions.active.ui.chan, drawer: !!d, drawerOf: d && d.getAttribute('aria-label'), inputsInDrawer: d ? d.querySelectorAll('input,textarea,select').length : -1, buttonsInDrawer: d ? Array.from(d.querySelectorAll('button')).map(b => b.textContent.trim()).filter(t => /steer|interrupt|reassign|quick message/i.test(t)) : [], selected: SL.link.sel }`);
await page.key('Escape'); await page.sleep(200); out.kbd.drawerClosed = await ev(`T.run(0.2); return { drawer: !!$('.drawer'), focusBack: document.activeElement && document.activeElement.dataset ? (document.activeElement.dataset.ag || document.activeElement.id) : null }`);
await page.key('g'); await page.key('r'); await page.sleep(300);
for (const ch of 'use int64') await page.key(ch); await page.key('Enter'); await page.sleep(300);
out.kbd.talk = await ev(`T.run(2); const S = SL.sessions.active; const you = S.m.chan.mgr.filter(e => e.k === 'you').map(e => e.text), workerSteers = Object.keys(S.m.chan).filter(k => k !== 'mgr' && k !== 'mail' && (S.m.chan[k] || []).some(e => e.k === 'steer')); return { channel: S.ui.chan, youRows: you.slice(-1), workerSteers };`);
await ev(`T.run(1.2)`); await page.key('1'); out.kbd.answer = await ev(`T.run(0.3); const q = SL.sessions.active.wm.qs.find(x => x.id === 'q1'); return { answered: q && q.answered }`);
out.errors = page.errors; console.log(JSON.stringify(out, null, 1)); await page.close();
