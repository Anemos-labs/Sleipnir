// t-layout-dock.mjs: no horizontal page scroll and nothing wider than the viewport, in every Dock view, at 390, 768, 1024, 1280, 1440, 1920 (rail open and collapsed, left rail narrow and wide).
import { open } from './cdp.mjs';
const FILE = process.argv[2] || '../dist/pack-test.html', out = [];
for (const [w, h] of [[390, 844], [768, 1024], [1024, 700], [1280, 720], [1440, 900], [1920, 1080]]) {
  const page = await open(FILE, { w, h, query: 'manual' });
  const r = await page.eval(`(async()=>{ const SL = __SL, T = SL.test, $ = s => document.querySelector(s); T.manual(); T.run(2); await T.idle(); const o = [], de = document.documentElement;
    const chk = tag => { const wide = []; document.querySelectorAll('#app *').forEach(e => { if (e.closest('.drawer,.scrim,.popover,svg,.slash,#toasts,.chordhint')) return; const r = e.getBoundingClientRect(); if (r.width && r.right > innerWidth + 1 && getComputedStyle(e).position !== 'fixed' && !e.closest('.sstrip .stabs, .setnav2, .c-table, .gwrap, .diff, .ws-body, .settable, .ws-list, .talk, .term, .pre, .dsteps, .tbl')) wide.push(e.tagName.toLowerCase() + '.' + String(e.className).slice(0, 30) + ' ' + Math.round(r.right)); }); if (de.scrollWidth > innerWidth + 1 || document.body.scrollWidth > innerWidth + 1 || wide.length) o.push(tag + ': scrollWidth ' + de.scrollWidth + ' > ' + innerWidth + (wide.length ? ' wide: ' + wide.slice(0, 3).join(', ') : '')); };
    const phone = innerWidth <= 900; const go = (v, p) => { if (phone) document.getElementById('app').dataset.pv = 'main'; SL.views.show(v, p || {}); T.run(0.3); };
    for (const [v, p] of [['cockpit'], ['workspace', { tab: 'files' }], ['workspace', { tab: 'changes' }], ['workspace', { tab: 'checkpoints' }], ['cache'], ['mail'], ['board'], ['replay'], ['sessions'], ['tools'], ['doctor'], ['schedule'], ['runner'], ['settings']]) { go(v, p); chk(v + (p && p.tab ? '/' + p.tab : '')); }
    for (const pg of ['models', 'roles', 'budget', 'permissions', 'trust', 'run', 'mcp', 'skills', 'providers', 'config', 'look']) { SL.ui.settingsPage(pg); T.run(0.3); chk('settings/' + pg); }
    if (!phone) { SL.ui.rail.collapse(); T.run(0.3); go('cockpit'); chk('cockpit, rail collapsed'); go('workspace', { tab: 'changes' }); chk('changes, rail collapsed'); SL.ui.rail.expand(); SL.ui.nav.setWide(true); T.run(0.3); go('cockpit'); chk('cockpit, wide rail'); go('workspace', { tab: 'changes' }); chk('changes, wide rail'); SL.ui.nav.setWide(false); SL.ui.rail.setWidth(560); T.run(0.3); go('cockpit'); chk('cockpit, rail 560'); go('workspace', { tab: 'changes' }); chk('changes, rail 560'); SL.ui.rail.setWidth(344); }
    SL.ui.dialogs.newSession(); T.run(0.3); chk('new session dialog'); SL.ui.closeModal(); SL.palette.open(''); T.run(0.3); chk('palette'); SL.ui.closeModal();
    return o; })()`);
  out.push({ size: w + 'x' + h, problems: r, errors: page.errors.length }); if (page.errors.length) console.error(w + 'x' + h, page.errors); await page.close();
}
console.log(JSON.stringify(out, null, 1)); console.log('sizes with problems:', out.filter(o => o.problems.length || o.errors).length);
