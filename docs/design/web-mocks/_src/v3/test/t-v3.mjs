// t-v3.mjs: the six changes of Sleipnir Web over the Dock, one section each.
//   1 eight workers by default   2 the horse has no dots and dotted lines   3 the person talks to the manager only
//   4 bypass and yolo in the New session dialog   5 the permission mode is a dropdown   6 Cache details: Quiet | Full
import { open } from './cdp.mjs';
const FILE = process.argv[2] || '../dist/pack-test.html', out = {}, probs = [];
const need = (ok, msg) => { if (!ok) probs.push(msg); };
const page = await open(FILE, { query: 'manual' });
const ev = js => page.eval('(async()=>{ const SL = __SL, T = SL.test, $ = s => document.querySelector(s), $$ = s => Array.from(document.querySelectorAll(s)), fire = (e, t) => e.dispatchEvent(new Event(t, { bubbles: true })); ' + js + ' })()');
await ev(`T.manual(); T.run(1);`);

/* ---------------- 1. eight workers by default ---------------- */
out.workers = await ev(`const o = {}; SL.ui.dialogs.newSession(); T.run(0.3); o.dialog = $('#nsSwarm').value; o.cli = /--swarm 8( |$)/.test($('#nsCli').textContent); o.label = $('#nsSw').textContent.trim();
  const f = $('#nsSwarm'); f.value = '0'; fire(f, 'input'); o.zero = /--swarm 0( |$)/.test($('#nsCli').textContent) && /single agent/.test($('#nsSw').textContent); f.value = '12'; fire(f, 'input'); o.twelve = /--swarm 12( |$)/.test($('#nsCli').textContent); f.value = '99'; fire(f, 'input'); o.clamped = /--swarm 12( |$)/.test($('#nsCli').textContent); f.value = '8'; fire(f, 'input'); $('#nsName').value = 'eight'; fire($('#nsName'), 'input');
  $('#nsStart').click(); T.run(1); const S = SL.sessions.active; o.started = { name: S.name, workers: S.roster.length - 1, meta: S.meta.swarm, launch: /--swarm 8( |$)/.test(S.meta.launch || '') };
  SL.act.switchSession('shop'); T.run(0.3); $('#sNew').click(); T.run(0.3); o.stripNew = $('#nsSwarm').value; SL.ui.closeModal(true); SL.palette.run(SL.palette.filter('New session', false)[0], ''); T.run(0.3); o.palette = $('#nsSwarm') && $('#nsSwarm').value; SL.ui.closeModal(true);
  const d = SL.act.newSession({ name: 'no-flag' }); T.run(0.5); o.noSpec = d.roster.length - 1;
  const rs = SL.sessions.recorded.filter(x => x.resumable); const r = SL.act.resumeSession(rs[0].id); T.run(0.5); o.resumeAsNew = SL.sessions.active.roster.length - 1 + '/' + SL.sessions.active.meta.swarm;
  SL.ui.settingsPage('run'); T.run(0.3); o.runPage = $('#rn').value; o.chatFlag = (SL.D.spec.commands.find(c => c.path.join(' ') === 'chat').flags.find(f => f.name === 'swarm') || {}).default;
  SL.act.switchSession('shop'); T.run(0.3); SL.ui.closeModal(true); return o;`);
need(out.workers.dialog === '8' && out.workers.cli, 'the New session dialog does not default to --swarm 8');
need(out.workers.zero && out.workers.twelve && out.workers.clamped, 'the worker field must still accept 0..12');
need(out.workers.started.workers === 8 && out.workers.started.launch, 'a session started from the dialog has 8 workers');
need(out.workers.stripNew === '8' && out.workers.palette === '8', '+ New and the palette entry default to 8');
need(out.workers.noSpec === 8, 'SL.act.newSession without a count defaults to 8');
need(/^8\/8$/.test(out.workers.resumeAsNew), 'resume as new defaults to 8 workers');
need(out.workers.chatFlag == 8, 'the chat flag --swarm default in the CLI spec is 8');

/* ---------------- 2. the horse: no dots, no dotted lines ---------------- */
out.horse = {};
for (const mode of ['quiet', 'full']) {
  out.horse[mode] = await ev(`SL.act.setCache('${mode}'); SL.views.show('cockpit'); T.run(3); const svg = $('svg.heroSvg'), o = { nodes: svg.querySelectorAll('.h-nodes,.conns,.conn,.h-conn,circle.node,circle.ring,circle.pulse').length, dashed: svg.querySelectorAll('[stroke-dasharray]').length, legs: svg.querySelectorAll('g.leg').length, labels: svg.querySelectorAll('g.hlab').length, halos: svg.querySelectorAll('.h-halo').length, circles: svg.querySelectorAll('circle').length, animated: svg.querySelectorAll('animate,animateMotion,animateTransform').length, prefix: !!svg.querySelector('.h-prefix') && getComputedStyle(svg.querySelector('.h-prefix')).display !== 'none' };
    /* a pulse on a hip node was the only thing that listened for events */ const be = svg.querySelector('g.leg[data-ag~="be-2"]'); o.legBe2 = !!be; return o;`);
  const h = out.horse[mode];
  need(h.nodes === 0 && h.dashed === 0 && h.animated === 0, mode + ': the horse still draws hip nodes, connectors or dotted lines');
  need(h.legs === 8 && h.labels === 8 && h.halos === 8, mode + ': the horse must keep its eight legs, labels and halos');
  need(h.prefix === (mode === 'full'), mode + ': the shared-prefix bar shows in Full only');
}
out.horseHover = await ev(`SL.act.setCache('quiet'); SL.views.show('cockpit'); T.run(3); const leg = $('svg.heroSvg g.leg[data-ag~="be-2"]'); leg.dispatchEvent(new PointerEvent('pointerover', { bubbles: true })); T.run(0.3); const o = { hotStall: $('.stall[data-ag="be-2"]').classList.contains('hot'), dimmedOther: $('.stall[data-ag="sc-1"]').classList.contains('dimmed'), hotLabel: $('svg.heroSvg .hlab[data-ag~="be-2"]').classList.contains('hot'), hover: SL.time.T.hover }; $('#app').dispatchEvent(new PointerEvent('pointerleave')); T.run(0.2); return o;`);
need(out.horseHover.hotStall && out.horseHover.dimmedOther && out.horseHover.hotLabel, 'hovering a leg must still link to its stall and its label');
out.horseOpen = await ev(`$('svg.heroSvg g.leg[data-ag~="be-2"]').dispatchEvent(new MouseEvent('click', { bubbles: true })); T.run(0.4); const d = $('.drawer'); const o = { drawer: !!d, label: d && d.getAttribute('aria-label'), inputs: d ? d.querySelectorAll('input,textarea,select').length : -1 }; SL.ui.closeDrawer(); return o;`);
need(out.horseOpen.drawer && out.horseOpen.inputs === 0, 'a leg opens a read-only drawer');

/* ---------------- 3. the person talks to the manager only ---------------- */
out.rail = await ev(`SL.views.show('cockpit'); T.run(0.5); const rail = $('#rail'); return { h2: $('#rail h2').textContent, chips: $$('#rail [data-chan], #rail .chan, #chans').length, chHead: $('#chHead').textContent.replace(/\\s+/g, ' ').trim(), buttons: $$('#chHead button').map(b => b.textContent.trim()), placeholder: $('#input').placeholder, send: $('#composer .send').textContent, feed: { present: !!$('#feed'), label: $('#feed').getAttribute('aria-label'), inputs: $$('#feedWrap input, #feedWrap textarea, #feedWrap select').length, buttons: $$('#feedWrap button').map(b => b.textContent.trim().replace(/\\s+/g, ' ')), rows: $$('#feed .msg').length, below: $('#feedWrap').getBoundingClientRect().top >= $('.talkwrap').getBoundingClientRect().bottom - 1 }, chatKinds: [...new Set($$('#talk .msg').map(e => e.className.replace(/\\bmsg\\b/, '').trim().split(' ')[0]))] };`);
need(out.rail.h2 === 'Manager', 'the Radio rail header is Manager');
need(out.rail.chips === 0, 'no per-worker channel chips');
need(out.rail.chHead.indexOf('Interrupt turn') >= 0 && out.rail.buttons.length === 1, 'the manager head has one button: Interrupt turn');
need(out.rail.feed.present && out.rail.feed.inputs === 0 && out.rail.feed.below, 'Team activity is a read-only section below the manager chat');
need(!out.rail.chatKinds.includes('live') && !out.rail.chatKinds.includes('break'), 'worker activity is not in the manager chat');
/* a whole-page sweep: nothing but the manager's "Interrupt turn" (and Stop the run) offers Steer / Interrupt / Reassign / Quick message */
out.sweep = await ev(`const BAD = /steer|interrupt|reassign|quick message|add (a |another )?(ninth |new )?worker|tell (be|fe|ts|rv|sc)-/i, found = [], scan = tag => { $$('button, [role=button], [role=menuitem], [role=tab], a, input, textarea, select, summary, [aria-label], [title]').forEach(e => { if (e.closest('svg')) return; const lab = e.matches('button, [role=button], [role=menuitem], [role=tab], a, summary') ? (e.textContent || '') : ''; const t = (lab + ' ' + (e.getAttribute('aria-label') || '') + ' ' + (e.getAttribute('title') || '') + ' ' + (e.getAttribute('placeholder') || '')).replace(/\\s+/g, ' ').trim(); if (BAD.test(t) && !/^Interrupt turn$/.test((e.textContent || '').trim())) found.push(tag + ': ' + (e.id || e.className || e.tagName) + ' = ' + t.slice(0, 70)); }); };
  const views = ['cockpit', 'cache', 'mail', 'board', 'replay', 'sessions', 'tools', 'settings', 'checkpoints', 'changes', 'files'];
  for (const v of views) { SL.ui.nav.go(v); T.run(0.5); scan(v); }
  for (const id of ['models', 'roles', 'budget', 'permissions', 'trust', 'run', 'mcp', 'skills', 'providers', 'config', 'look']) { SL.ui.settingsPage(id); T.run(0.3); scan('settings/' + id); }
  SL.views.show('cockpit'); T.run(0.3); for (const id of SL.sessions.active.m.order) { SL.ui.openDrawer(id); T.run(0.2); for (const tab of ['info', 'log', 'mail', 'cache']) { $('.drawer [data-tab="' + tab + '"]').click(); T.run(0.1); scan('drawer ' + id + '/' + tab); } SL.ui.closeDrawer(); }
  SL.ui.sessionMenu($('#sOpt')); scan('session menu'); document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })); $$('.popover').forEach(p => p.remove());
  { const S = SL.sessions.active; if (S.replay) { S.goLive(); S.touch(); } SL.views.show('cockpit'); T.run(0.5); }
  return { found, scanned: views.length + 11 + SL.sessions.active.m.order.length * 4 };`);
need(out.sweep.found.length === 0, 'steer/interrupt/reassign offered somewhere: ' + JSON.stringify(out.sweep.found));
out.palette = await ev(`SL.palette.build(); const all = SL.palette.filter('', false), bad = all.filter(c => /steer|interrupt|reassign|quick message/i.test(c.name + ' ' + (c.d || ''))).map(c => c.name + ' | ' + c.d); const before = SL.sessions.active.m.chan.mgr.length;
  SL.palette.runLine('/steer be-2 do it now'); T.run(2); const S = SL.sessions.active, steers = S.m.chan.mgr.filter(e => e.k === 'steer').map(e => e.to + ':' + e.text), workerSteers = Object.keys(S.m.chan).filter(k => k !== 'mgr' && k !== 'mail' && S.m.chan[k].some(e => e.k === 'steer' && !/^your answer/.test(e.text) && e.t > S.wt - 5));
  return { n: all.length, bad, steers, workerSteers, handler: !!SL.palette.H['/steer'], steerAct: SL.act.steer('be-2', 'x').ok, intrAct: SL.act.interrupt('fe-1').ok };`);
need(out.palette.bad.every(b => /^\/steer \|/.test(b)) && out.palette.bad.length <= 1, 'palette entries that steer or interrupt: ' + JSON.stringify(out.palette.bad));
need(out.palette.steers.every(s => s.startsWith('mgr:')) && out.palette.workerSteers.length === 0, '/steer must reach the manager only');
need(out.palette.steerAct === false && out.palette.intrAct === false, 'act.steer / act.interrupt refuse a worker');
/* the drawer: read-only, with state, task, lease, files, cost, tool log (the manager's refused edit), mail */
out.drawer = await ev(`SL.views.show('cockpit'); T.run(0.3); const o = {}; SL.ui.openDrawer('be-1'); T.run(0.3); const d = $('.drawer'); o.tabs = $$('.drawer [data-tab]').map(b => b.textContent); o.info = d.querySelector('.dr-b').textContent.replace(/\\s+/g, ' ').slice(0, 300); o.inputs = d.querySelectorAll('input,textarea,select').length; o.buttons = $$('.drawer button').map(b => b.textContent.trim().replace(/\\s+/g, ' ')); o.ro = /Read-only/.test(d.textContent); o.hasThread = !!d.querySelector('.tsteps'); $('.drawer [data-tab="mail"]').click(); T.run(0.2); o.mail = d.querySelector('.dr-b').textContent.replace(/\\s+/g, ' ').slice(0, 120);
  SL.ui.closeDrawer(); SL.ui.openDrawer('mgr'); T.run(0.3); $('.drawer [data-tab="log"]').click(); T.run(0.2); o.mgrRefused = /refused/.test($('.drawer .dr-b').textContent) && /api\\/server\\.go/.test($('.drawer .dr-b').textContent); SL.ui.closeDrawer();
  const stall = $('.stall[data-ag="be-2"]'); stall.click(); T.run(0.3); o.stallOpens = !!$('.drawer') && /be-2/.test($('.drawer').getAttribute('aria-label')); SL.ui.closeDrawer(); return o;`);
need(out.drawer.tabs.join() === 'Details,Tool log,Mail,Cache', 'the drawer tabs are Details, Tool log, Mail, Cache');
need(out.drawer.inputs === 0 && out.drawer.ro && !out.drawer.buttons.some(b => /steer|interrupt|reassign/i.test(b)), 'the drawer is read-only');
need(out.drawer.mgrRefused, "the manager's refused edit is in its tool log");
need(out.drawer.stallOpens, 'clicking a stall opens the read-only drawer');
out.feedHold = await ev(`SL.views.show('cockpit'); T.run(0.5); $('#feed').dispatchEvent(new PointerEvent('pointerover', { bubbles: true })); T.run(1.2); const o = { hover: SL.time.T.hover, rate: +SL.time.rate.toFixed(2), rowsHeld: (() => { const n = $$('#feed .msg').length; T.run(3); return $$('#feed .msg').length - n; })() }; $('#app').dispatchEvent(new PointerEvent('pointerleave')); T.run(8); o.after = SL.time.T.hover; return o;`);
need(out.feedHold.hover === 'chat' && out.feedHold.rate === 0 && out.feedHold.rowsHeld === 0, 'hovering Team activity holds the view like the chat does');
/* the composer talks to the manager; @ stays for files and does not list agents */
await page.eval(`document.querySelector('#input').focus()`);
out.composer = await ev(`const S = SL.sessions.active, inp = $('#input'); inp.value = 'hello manager @'; inp.setSelectionRange(inp.value.length, inp.value.length); fire(inp, 'input'); T.run(0.1); const items = $$('#slash .cmd .cn').map(e => e.textContent); const o = { at: items.slice(0, 5), agentsInAt: items.some(t => /^@?(be|fe|ts|rv|sc)-\\d/.test(t)) }; inp.value = ''; fire(inp, 'input'); const n = S.m.chan.mgr.length; inp.value = 'please look at api/cart'; fire(inp, 'input'); $('#composer').dispatchEvent(new Event('submit', { cancelable: true })); T.run(3.5); o.sent = S.m.chan.mgr.slice(n).filter(e => e.k === 'you').map(e => e.text); o.queued = S.ui.queued.length; return o;`);
need(out.composer.at.length > 0 && !out.composer.agentsInAt, '@ completes files, not agents');
need(out.composer.sent.length === 1, 'the composer sends to the manager');

/* ---------------- 4. bypass and yolo in the New session dialog ---------------- */
out.danger = await ev(`const o = {}; SL.ui.dialogs.newSession(); T.run(0.3); const radios = $$('[data-nsmode]'); o.modes = radios.map(r => r.dataset.nsmode); o.group = $('#nsModes').getAttribute('role'); o.dng = radios.filter(r => r.classList.contains('dng')).map(r => r.dataset.nsmode); o.glyph = radios.filter(r => /⚠/.test(r.textContent)).map(r => r.dataset.nsmode); o.desc = radios.map(r => r.querySelector('.nd').textContent.length > 20);
  const hatch = r => /repeating-linear-gradient/.test(getComputedStyle(r).backgroundImage); o.hatched = radios.filter(hatch).map(r => r.dataset.nsmode);
  $('[data-nsmode="bypass"]').click(); T.run(0.2); o.bypassCli = /--mode bypass/.test($('#nsCli').textContent); o.bypassWarn = !$('#nsWarn').hidden && /will not ask/.test($('#nsWarn').textContent); o.typedBox = !!$('#cfIn') || !!$('.dangerbox'); o.noTypedConfirm = $$('.scrim input[type=text]').filter(i => /confirm/i.test(i.getAttribute('aria-label') || '')).length === 0;
  $('[data-nsmode="yolo"]').click(); T.run(0.2); o.yoloCli = /--mode yolo/.test($('#nsCli').textContent); o.yoloWarn = /never ask/.test($('#nsWarn').textContent); o.checked = $$('[data-nsmode][aria-checked="true"]').map(r => r.dataset.nsmode); $('[data-nsmode="bypass"]').click(); $('#nsName').value = 'risky'; fire($('#nsName'), 'input');
  $('#nsStart').click(); T.run(1); const S = SL.sessions.active; o.session = { name: S.name, mode: S.meta.mode }; o.tabChip = !!$('.stab.sel .dgm'); o.tabTitle = $('.stab.sel .dgm') && $('.stab.sel .dgm').title; o.hud = $('#govTxt .hmode').textContent; o.hudDng = $('#govTxt .hmode').classList.contains('dng'); o.composerBtn = $('#modeTxt').textContent; o.composerDng = $('#modeBtn').classList.contains('danger'); o.noQuestion = !SL.calc.openQuestion(S.wm);
  o.otherTabs = $$('.stab:not(.sel) .dgm').length; SL.act.switchSession('shop'); T.run(0.3); o.shopChip = $('#modeBtn').classList.contains('danger'); return o;`);
need(out.danger.modes.join() === 'default,accept-edits,plan,bypass,yolo', 'the dialog lists all five modes');
need(out.danger.dng.join() === 'bypass,yolo' && out.danger.glyph.join() === 'bypass,yolo' && out.danger.hatched.join() === 'bypass,yolo', 'bypass and yolo are drawn dangerous (hatched, with a warning sign)');
need(out.danger.desc.every(Boolean), 'each mode has a plain line');
need(out.danger.bypassCli && out.danger.bypassWarn && !out.danger.typedBox && out.danger.noTypedConfirm, 'bypass is selectable with an inline warning and no typed confirmation');
need(out.danger.yoloCli && out.danger.yoloWarn && out.danger.checked.join() === 'yolo', 'yolo is selectable too, one mode at a time');
need(out.danger.session.mode === 'bypass' && out.danger.tabChip && out.danger.hudDng && /⚠/.test(out.danger.hud) && out.danger.composerDng && /⚠/.test(out.danger.composerBtn), 'a created bypass session shows the warning on its tab, the HUD and the composer');
need(out.danger.shopChip === false && out.danger.otherTabs >= 0, 'other sessions are not marked');
/* keyboard in the mode radio group */
out.dangerKbd = await ev(`SL.ui.dialogs.newSession(); T.run(0.3); $('[data-nsmode="plan"]').click(); $('[data-nsmode="plan"]').focus(); return document.activeElement.dataset.nsmode`);
await page.key('ArrowDown'); await page.sleep(100);
out.dangerKbd2 = await ev(`const o = { mode: document.activeElement.dataset.nsmode, checked: $('[data-nsmode][aria-checked="true"]').dataset.nsmode }; SL.ui.closeModal(true); return o`);
need(out.dangerKbd2.mode === 'bypass' && out.dangerKbd2.checked === 'bypass', 'arrow keys move through the modes');
/* elsewhere unchanged: shift+tab never reaches them; /mode bypass keeps its typed confirmation */
out.dangerElsewhere = await ev(`SL.act.switchSession('shop'); SL.act.setMode('default'); const o = {}; for (let i = 0; i < 6; i++) { SL.act.cycleMode(); (o.cycle = o.cycle || []).push(SL.sessions.active.meta.mode); } o.refused = SL.act.setMode('bypass').ok; SL.palette.runLine('/mode bypass'); T.run(0.3); o.typed = !!$('#cfIn') && !$('#cfBox').hidden; o.goDisabled = $('#cfGo').disabled; o.stillDefault = SL.sessions.active.meta.mode; SL.ui.closeModal(true); return o;`);
need(!out.dangerElsewhere.cycle.some(m => m === 'bypass' || m === 'yolo') && out.dangerElsewhere.refused === false, 'shift+tab and setMode never reach bypass/yolo without confirmation');
need(out.dangerElsewhere.typed && out.dangerElsewhere.goDisabled && out.dangerElsewhere.stillDefault === 'default', '/mode bypass in a running chat still asks for the typed name');

/* ---------------- 5. the permission mode is a dropdown ---------------- */
out.menu = await ev(`SL.act.switchSession('shop'); SL.act.setMode('default'); SL.views.show('cockpit'); T.run(0.3); const b = $('#modeBtn'), o = { tag: b.tagName, haspopup: b.getAttribute('aria-haspopup'), expanded: b.getAttribute('aria-expanded'), text: $('#modeTxt').textContent, caret: !!b.querySelector('.caret'), noChip: !$('#modeChip') };
  const r = b.getBoundingClientRect(); return Object.assign(o, { x: r.x + r.width / 2, y: r.y + r.height / 2 });`);
await page.click(out.menu.x, out.menu.y); await page.sleep(150);
out.menuOpen = await ev(`const b = $('#modeBtn'), m = $('.modemenu'), o = { mode: SL.sessions.active.meta.mode, expanded: b.getAttribute('aria-expanded'), menu: !!m }; if (m) { const r = m.getBoundingClientRect(), br = b.getBoundingClientRect(); o.role = m.getAttribute('role'); o.upward = r.bottom <= br.top + 1; o.inside = r.left >= 0 && r.right <= innerWidth && r.top >= 0; o.items = $$('.modemenu [role^="menuitem"]').map(i => i.getAttribute('role') + ':' + i.querySelector('.mn').textContent + (i.getAttribute('aria-checked') === 'true' ? '✓' : '')); o.separator = !!m.querySelector('hr[role="separator"]'); o.focus = document.activeElement.querySelector && document.activeElement.querySelector('.mn') ? document.activeElement.querySelector('.mn').textContent : document.activeElement.tagName; o.desc = $$('.modemenu .md').every(e => e.textContent.length > 10); } return o;`);
need(out.menu.haspopup === 'menu' && out.menu.expanded === 'false' && out.menu.caret && out.menu.noChip && out.menu.tag === 'BUTTON', 'the mode control is a menu button with a caret');
need(out.menuOpen.menu && out.menuOpen.mode === 'default' && out.menuOpen.expanded === 'true', 'a single click only opens the menu and never changes the mode');
need(out.menuOpen.role === 'menu' && out.menuOpen.upward && out.menuOpen.inside, 'the menu opens upward inside the window');
need(out.menuOpen.items.join('|') === 'menuitemradio:default✓|menuitemradio:accept-edits|menuitemradio:plan|menuitem:Dangerous modes…' && out.menuOpen.separator && out.menuOpen.desc, 'the menu lists default, accept-edits, plan with a check on the current one, a divider and Dangerous modes…');
out.menuOpen.focus === 'default' || need(false, 'focus starts on the checked item');
/* arrows, Esc, focus return */
await page.key('ArrowDown'); await page.key('ArrowDown'); await page.sleep(80);
out.menuKeys = await ev(`return { focus: document.activeElement.querySelector('.mn') && document.activeElement.querySelector('.mn').textContent, mode: SL.sessions.active.meta.mode }`);
await page.key('ArrowDown'); await page.sleep(60); out.menuKeys.wrap = await ev(`return document.activeElement.querySelector('.mn').textContent`);
await page.key('ArrowUp'); await page.key('Home'); await page.sleep(60); out.menuKeys.home = await ev(`return document.activeElement.querySelector('.mn').textContent`);
await page.key('Escape'); await page.sleep(120);
out.menuEsc = await ev(`return { open: !!$('.modemenu'), focus: document.activeElement.id, expanded: $('#modeBtn').getAttribute('aria-expanded'), mode: SL.sessions.active.meta.mode, modal: !!$('.scrim') }`);
need(out.menuKeys.focus === 'plan' && out.menuKeys.mode === 'default' && out.menuKeys.wrap === 'Dangerous modes…' && out.menuKeys.home === 'default', 'arrow keys, Home move through the menu without applying');
need(!out.menuEsc.open && out.menuEsc.focus === 'modeBtn' && out.menuEsc.expanded === 'false' && out.menuEsc.mode === 'default' && !out.menuEsc.modal, 'Esc closes the menu and returns the focus to the button');
/* click outside closes; clicking the button twice opens then closes; a pick applies and toasts */
out.menuMore = await ev(`const b = $('#modeBtn'), o = {}; b.click(); T.run(0.1); o.open1 = !!$('.modemenu'); b.click(); T.run(0.1); o.closedByButton = !$('.modemenu'); o.modeAfterTwoClicks = SL.sessions.active.meta.mode; b.click(); T.run(0.1); o.open2 = !!$('.modemenu'); return o;`);
await page.click(700, 889); await page.sleep(120);
out.menuOutside = await ev(`return { open: !!$('.modemenu'), mode: SL.sessions.active.meta.mode }`);
need(out.menuMore.open1 && out.menuMore.closedByButton && out.menuMore.modeAfterTwoClicks === 'default' && out.menuOutside.open === false && out.menuOutside.mode === 'default', 'the button toggles the menu; a click outside closes it; neither changes the mode');
out.menuPick = await ev(`const before = T.counts(); $('#toasts').innerHTML = ''; $('#modeBtn').click(); T.run(0.1); const item = $('.modemenu [data-mode="plan"]'); item.click(); T.run(0.3); return { mode: SL.sessions.active.meta.mode, text: $('#modeTxt').textContent, open: !!$('.modemenu'), toast: $$('#toasts .toast').map(t => t.textContent.trim()), focus: document.activeElement.id, expanded: $('#modeBtn').getAttribute('aria-expanded') };`);
need(out.menuPick.mode === 'plan' && out.menuPick.toast.some(t => /mode: plan/.test(t)) && !out.menuPick.open && out.menuPick.focus === 'modeBtn', 'picking plan applies it, toasts "mode: plan" and returns the focus');
out.menuCheck = await ev(`$('#modeBtn').click(); T.run(0.1); const o = $$('.modemenu [role^="menuitem"]').map(i => i.querySelector('.mn').textContent + (i.getAttribute('aria-checked') === 'true' ? '✓' : '')); $('.modemenu [data-mode="default"]').click(); T.run(0.2); return { o, mode: SL.sessions.active.meta.mode }`);
need(out.menuCheck.o.join('|') === 'default|accept-edits|plan✓|Dangerous modes…', 'the check follows the current mode');
out.menuDanger = await ev(`$('#modeBtn').click(); T.run(0.1); $('.modemenu [data-dangerous]').click(); T.run(0.3); const o = { dialog: !!$('.scrim'), title: $('.scrim h2, .scrim .mt') && ($('.scrim h2, .scrim .mt').textContent), modes: $$('.scrim [data-m]').map(b => b.dataset.m), box: !!$('#cfBox'), boxHidden: $('#cfBox') && $('#cfBox').hidden, mode: SL.sessions.active.meta.mode, menu: !!$('.modemenu') }; $$('.scrim [data-m="yolo"]')[0].click(); T.run(0.2); o.afterYoloClick = { box: !$('#cfBox').hidden, go: $('#cfGo').disabled, mode: SL.sessions.active.meta.mode }; SL.ui.closeModal(true); return o;`);
need(out.menuDanger.dialog && out.menuDanger.modes.length === 5 && out.menuDanger.afterYoloClick.box && out.menuDanger.afterYoloClick.go && out.menuDanger.afterYoloClick.mode === 'default' && !out.menuDanger.menu, 'Dangerous modes… opens the typed-confirmation dialog');
/* shift+tab in the composer still cycles; a menu leaves nothing behind */
await page.eval(`document.querySelector('#input').focus()`);
out.cycle = []; for (let i = 0; i < 4; i++) { await page.key('Tab', 8); out.cycle.push(await ev(`T.run(0.1); return SL.sessions.active.meta.mode`)); }
need(out.cycle.join() === 'accept-edits,plan,default,accept-edits', 'shift+tab in the composer cycles default, accept-edits, plan');
out.menuLeak = await ev(`SL.act.setMode('default'); T.run(0.2); await T.gc(); await T.idle(); const a = T.counts(); for (let i = 0; i < 8; i++) { $('#modeBtn').click(); T.run(0.05); if (i % 2) document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })); else $('#modeBtn').click(); T.run(0.05); } $('.modemenu') && $('.modemenu')._off && $('.modemenu')._off(false); await T.gc(); await T.idle(); const b = T.counts(); return { listeners: [a.listeners, b.listeners], nodes: [a.nodes, b.nodes], menus: $$('.modemenu').length }`);
need(out.menuLeak.listeners[0] === out.menuLeak.listeners[1] && out.menuLeak.menus === 0, 'the menu removes its listeners and its node on close');
out.menuNarrow = {};
for (const [w, h] of [[390, 844], [1024, 700], [1920, 1080]]) {
  await page.viewport(w, h); await page.sleep(200);
  out.menuNarrow[w] = await ev(`if (innerWidth <= 900) { $('#app').dataset.pv = 'radio'; SL.loop.dirty = true; } T.run(0.4); const b = $('#modeBtn'); b.click(); T.run(0.2); const m = $('.modemenu'), r = m.getBoundingClientRect(), br = b.getBoundingClientRect(); const o = { upward: r.bottom <= br.top + 1, inside: r.left >= 0 && r.right <= innerWidth && r.top >= 0, w: Math.round(r.width) }; $('.modemenu')._off(false); if (innerWidth <= 900) $('#app').dataset.pv = 'cockpit'; return o;`);
  need(out.menuNarrow[w].upward && out.menuNarrow[w].inside, 'the mode menu fits at ' + w + 'px');
}
await page.viewport(1440, 900); await page.sleep(200);

/* ---------------- 6. Cache details: Quiet (default) | Full ---------------- */
out.cache = {};
out.cache.default = await ev(`return { setting: SL.settings.cache, body: document.body.classList.contains('cq'), stored: (() => { try { return localStorage.getItem('sleipnir.web.settings') || localStorage.getItem('sleipnir.dock.settings') || Object.keys(localStorage).filter(k => /sleipnir/.test(k)).join() } catch (e) { return 'blocked' } })() }`);
need(out.cache.default.setting === 'quiet' && out.cache.default.body, 'Cache details defaults to Quiet');
out.cache.settings = await ev(`SL.ui.settingsPage('look'); T.run(0.3); const seg = $('[data-seg="cache"]') && $$('[data-seg="cache"]').map(b => b.textContent + (b.getAttribute('aria-pressed') === 'true' ? '✓' : '')); return { seg, label: $$('.setpage .fld label, .setpage .rowlab, .setpage dt').map(e => e.textContent.trim()).filter(t => /cache details/i.test(t)) }`);
need(out.cache.settings.seg && out.cache.settings.seg.join() === 'Quiet✓,Full', 'Settings > Appearance has Cache details: Quiet | Full');
for (const mode of ['quiet', 'full']) {
  out.cache[mode] = await ev(`SL.act.setCache('${mode}'); SL.act.switchSession('shop'); SL.views.show('cockpit'); T.run(0.5); const vis = e => !!e && getComputedStyle(e).display !== 'none' && !e.closest('[hidden]'), W = e => e ? Math.round(e.getBoundingClientRect().width) : 0, o = {};
    { const S = SL.sessions.active; o.dbg = { id: S.id, wt: Math.round(S.wt), vt: Math.round(S.vt), replay: !!S.replay, held: SL.time.holdWanted(), cost: SL.calc.totals(S.m).cost, wcost: SL.calc.totals(S.wm).cost, list: SL.sessions.list.map(x => x.id + ':' + Math.round(x.wt)).join() }; } o.ring = vis($('#ringHit > svg')); o.chip = vis($('#hitChip')) && $('#hitChip').textContent.replace(/\\s+/g, ' ').trim(); o.warmSize = W($('#warmSvg')); o.hitSize = W($('#ringHit')); o.warmNote = vis($('#warmNote')); o.warmLowOrCold = /\\b(low|cold)\\b/.test($('#ringWarm').className); o.warmTicks = vis($('#warmTicks'));
    o.stallHit = $$('.stall:not(.ghost) .n-hit').filter(vis).length; o.stallSpark = $$('.stall:not(.ghost) .s-spark').filter(vis).length; o.mgrHit = vis($('.mgr-card .n-hit')); o.mgrSpark = vis($('.mgr-card .s-spark'));
    o.prefix = vis($('.h-prefix')); o.viewBox = $('svg.heroSvg').getAttribute('viewBox'); o.horseH = Math.round($('svg.heroSvg').getBoundingClientRect().height);
    o.sessionsHit = (SL.views.show('sessions'), T.run(0.3), $$('.scard [data-c="hit"]').filter(vis).length); o.budget = (SL.ui.settingsPage('budget'), T.run(0.3), { hitCol: vis($('#budTbl thead th.qfull')), saved: vis($('.setpage .stubnote.qfull')) });
    o.cacheView = (SL.views.show('cache'), T.run(0.3), { savedCol: vis($('.ctable thead th.qfull')), sum: $$('.c-sumtxt > span').filter(vis).map(e => e.textContent).join(' ').replace(/\\s+/g, ' ').trim(), hitRows: $$('.ctable tbody tr').length, hitBars: $$('.hbar').length }); SL.views.show('cockpit'); T.run(0.3); return o;`);
}
const Q = out.cache.quiet, F = out.cache.full;
need(!Q.ring && /^cache \d+%$/.test(Q.chip) && Q.warmSize < 50 && (!Q.warmNote || Q.warmLowOrCold) && !Q.warmTicks, 'Quiet HUD: a small cache chip, a small warm clock');
need(F.ring && !F.chip && F.warmSize >= 60, 'Full HUD: the ring and the big warm clock');
need(Q.stallHit === 0 && Q.stallSpark === 0 && !Q.mgrHit && !Q.mgrSpark && Q.sessionsHit === 0, 'Quiet: no hit figures or sparklines on stalls, the Manager card or the session cards');
need(F.stallHit === 8 && F.stallSpark === 8 && F.mgrHit && F.sessionsHit >= 3, 'Full: the hit figures are back');
need(!Q.prefix && Q.horseH > 0, 'Quiet: no shared-prefix bar on the horse'); need(F.prefix, 'Full: the shared-prefix bar is on the horse');
need(!Q.budget.hitCol && !Q.budget.saved && !Q.cacheView.savedCol && !/saved|no cache would/.test(Q.cacheView.sum), 'Quiet: no savings figures on the budget page or in the Cache view');
need(F.budget.hitCol && F.budget.saved && F.cacheView.savedCol && /saved/.test(F.cacheView.sum), 'Full: the savings figures are back in the Cache view and the budget page');
need(Q.cacheView.hitRows === 9 && Q.cacheView.hitBars > 0, 'Quiet: the Cache view keeps the hit ratios');
/* a cache break: one calm line in Team activity and a small warning on the stall (Quiet); the v2 row in Full */
out.cache.brk = {};
for (const mode of ['quiet', 'full']) {
  const pre = await ev(`SL.act.setCache('${mode}'); SL.act.switchSession('shop'); SL.views.show('cockpit'); T.run(0.3); const S = SL.sessions.active; const n0 = $$('#feed .msg.break').length; S.add({ k: 'break', id: 'be-2', kind: 'low_hit', read: 0, expected: 4500, why: 'the prompt prefix did not change: the endpoint did not serve it' }); T.run(1.2); return { n0 };`);
  await page.sleep(500);
  out.cache.brk[mode] = await ev(`const stall = $('.stall[data-ag="be-2"]'), brk = stall.querySelector('.brk'), cs = getComputedStyle(stall), bcs = getComputedStyle(brk), rows = $$('#feed .msg.break'), last = rows[rows.length - 1], bx = brk.querySelector('.bx'), o = { added: rows.length - ${pre.n0}, calm: last && last.classList.contains('calm'), oneLine: !!last && last.getBoundingClientRect().height < 24, inChat: $$('#talk .msg.break').length, rowText: last ? last.textContent.replace(/\\s+/g, ' ').slice(0, 140) : '', dataBrk: stall.dataset.brk, brkShown: bcs.display !== 'none', brkBorder: bcs.borderTopWidth, brkVisibleText: brk.firstElementChild.textContent, bxClipped: bx.getBoundingClientRect().width <= 1, borderColor: cs.borderTopColor, errBorder: /247, 118, 142/.test(cs.borderTopColor), scopeHidden: getComputedStyle(stall.querySelector('.s-scope')).display === 'none' };
    return o;`);
}
const bq = out.cache.brk.quiet, bf = out.cache.brk.full;
need(bq.added === 1 && bq.calm && bq.oneLine && bq.inChat === 0 && !/est\.|\$/.test(bq.rowText), 'Quiet: a cache break is ONE calm line in Team activity, not in the chat, with no cost estimate');
need(bq.brkShown && bq.brkVisibleText === '⚠' && bq.bxClipped && bq.brkBorder === '0px' && !bq.errBorder && !bq.scopeHidden, 'Quiet: a small warning on the affected stall, no red border or boxed tag');
need(bf.added === 1 && !bf.calm && bf.errBorder && bf.brkShown && !bf.bxClipped && /extra cost/.test(bf.rowText), 'Full: the Dock v2 break row and the red stall');
out.errors = page.errors;
need(page.errors.length === 0, 'console errors: ' + JSON.stringify(page.errors));
console.log(JSON.stringify(out, null, 1));
console.log('problems: ' + probs.length + (probs.length ? '\n  ' + probs.join('\n  ') : ''));
await page.close();
process.exit(probs.length ? 1 : 0);
