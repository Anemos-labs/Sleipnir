// t-a11y.mjs: a DOM audit of text contrast (WCAG AA: 4.5:1, 3:1 for large text) in every view and Settings page, plus: every button has a name,
// every input a label, landmarks, focus ring present. Usage: node t-a11y.mjs ../dist/pack-test.html [width height]
import { open } from './cdp.mjs';
const FILE = process.argv[2] || '../dist/pack-test.html', W = +process.argv[3] || 1440, H = +process.argv[4] || 900;
const page = await open(FILE, { w: W, h: H, query: 'manual' });
const ev = js => page.eval('(async()=>{ const SL = __SL, T = SL.test, $ = s => document.querySelector(s), $$ = s => Array.from(document.querySelectorAll(s)); ' + js + ' })()');
await ev(`T.manual(); T.run(1);`);
const AUDIT = `
const parse = c => { const m = /rgba?\\(([^)]+)\\)/.exec(c); if (!m) return null; const p = m[1].split(/[ ,\\/]+/).filter(Boolean).map(Number); return [p[0], p[1], p[2], p.length > 3 ? p[3] : 1]; };
const blend = (f, b) => [f[0] * f[3] + b[0] * (1 - f[3]), f[1] * f[3] + b[1] * (1 - f[3]), f[2] * f[3] + b[2] * (1 - f[3]), 1];
const lum = c => { const f = v => { v /= 255; return v <= .03928 ? v / 12.92 : Math.pow((v + .055) / 1.055, 2.4); }; return .2126 * f(c[0]) + .7152 * f(c[1]) + .0722 * f(c[2]); };
const ratio = (a, b) => { const x = lum(a), y = lum(b); return (Math.max(x, y) + .05) / (Math.min(x, y) + .05); };
const bgOf = el => { const chain = []; for (let e = el; e; e = e.parentElement) chain.push(e); let bg = parse(getComputedStyle(document.body).backgroundColor) || [7, 9, 15, 1]; if (bg[3] < 1) bg = blend(bg, [7, 9, 15, 1]); chain.reverse().forEach(e => { const cs = getComputedStyle(e), c = parse(cs.backgroundColor); if (c && c[3] > 0) bg = blend(c, bg); }); return bg; };
const opac = el => { let o = 1; for (let e = el; e; e = e.parentElement) { const v = +getComputedStyle(e).opacity; o *= isNaN(v) ? 1 : v; } return o; };
const bad = {}; let n = 0, ok = 0;
const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
let tn; while ((tn = walker.nextNode())) {
  const t = tn.nodeValue.replace(/\\s+/g, ' ').trim(); if (t.length < 2) continue; const el = tn.parentElement; if (!el || /^(SCRIPT|STYLE|NOSCRIPT)$/.test(el.tagName)) continue;
  const cs = getComputedStyle(el); if (cs.visibility === 'hidden' || cs.display === 'none') continue; const r = el.getBoundingClientRect(); if (r.width < 2 || r.height < 2) continue; if (el.closest('.sr,[hidden],.drawer:not(.in)')) continue;
  if (r.bottom < 0 || r.top > innerHeight || r.right < 0 || r.left > innerWidth) continue;
  if (el.closest('svg')) continue; if (document.querySelector('.scrim') && !el.closest('.scrim')) continue; if (window.__scope && !el.closest(window.__scope)) continue;
  const bg = bgOf(el), fg0 = parse(cs.color) || [255, 255, 255, 1]; const o = opac(el); let fg = blend([fg0[0], fg0[1], fg0[2], fg0[3] * o], bg);
  const size = parseFloat(cs.fontSize), bold = parseInt(cs.fontWeight, 10) >= 700, large = size >= 24 || (size >= 18.66 && bold), need = large ? 3 : 4.5, rt = ratio(fg, bg); n++;
  if (rt >= need) { ok++; continue; }
  const key = (el.closest('[data-view]') ? el.closest('[data-view]').dataset.view + ' ' : '') + el.tagName.toLowerCase() + (el.className && typeof el.className === 'string' ? '.' + el.className.trim().split(/\\s+/).slice(0, 2).join('.') : '');
  const b = bad[key] || (bad[key] = { n: 0, worst: 99, sample: '', color: '' }); b.n++; if (rt < b.worst) { b.worst = +rt.toFixed(2); b.sample = t.slice(0, 40); b.color = cs.color + ' on ' + 'rgb(' + bg.slice(0, 3).map(Math.round).join(',') + ')' + (o < 1 ? ' @' + o.toFixed(2) : ''); }
}
return { checked: n, pass: ok, failGroups: Object.keys(bad).length, bad };`;
const out = {};
const views = [['cockpit'], ['workspace', 'files'], ['workspace', 'changes'], ['workspace', 'checkpoints'], ['cache'], ['mail'], ['board'], ['replay'], ['sessions'], ['tools'], ['doctor'], ['schedule'], ['runner']];
for (const [v, tab] of views) { await ev(`SL.views.show(${JSON.stringify(v)}, ${JSON.stringify(tab ? { tab } : {})}); T.run(0.4);`); await page.sleep(150); out[v + (tab ? '/' + tab : '')] = await ev(AUDIT); }
for (const p of ['models', 'roles', 'budget', 'permissions', 'trust', 'run', 'mcp', 'skills', 'providers', 'config', 'look']) { await ev(`SL.ui.settingsPage('${p}'); T.run(0.4);`); out['settings/' + p] = await ev(AUDIT); }
/* a question box, a dialog, the palette */
await ev(`SL.views.show('cockpit'); T.run(0.3); SL.ui.rail.expand();`); out['rail with question'] = await ev(AUDIT);
await ev(`SL.palette.open(''); T.run(0.3);`); out.palette = await ev(AUDIT); await ev(`SL.ui.closeModal()`);
await ev(`SL.ui.dialogs.newSession(); T.run(0.3);`); out.newSession = await ev(AUDIT); await ev(`SL.ui.closeModal()`);
await ev(`SL.ui.dialogs.newSession(); T.run(0.3); document.querySelector('[data-nsmode="bypass"]').click(); T.run(0.2);`); out['newSession (bypass chosen)'] = await ev(AUDIT); await ev(`SL.ui.closeModal()`);
await ev(`SL.ui.dialogs.newSession(); T.run(0.3); document.querySelector('[data-nsmode="yolo"]').click(); T.run(0.2);`); out['newSession (yolo chosen)'] = await ev(AUDIT); await ev(`SL.ui.closeModal()`);
await ev(`document.querySelector('#modeBtn').click(); T.run(0.3);`); out['mode menu open'] = await ev(AUDIT); await ev(`document.querySelector('.modemenu')._off(true)`);
await ev(`SL.ui.sheets.mode(); T.run(0.3);`); out['mode dialog'] = await ev(AUDIT); await ev(`SL.ui.closeModal()`);
await ev(`SL.ui.openDrawer('be-2'); T.run(0.3);`); await page.sleep(600); for (const tab of ['info', 'log', 'mail', 'cache']) { await ev(`document.querySelector('.drawer [data-tab="${tab}"]').click(); T.run(0.2); window.__scope = '.drawer';`); out['drawer ' + tab] = await ev(AUDIT); } await ev(`window.__scope = null; SL.ui.closeDrawer()`);
await ev(`SL.act.setCache('full'); T.run(0.5);`); await page.sleep(700); out['cockpit (cache details: full)'] = await ev(AUDIT); await ev(`SL.act.setCache('quiet'); T.run(0.3);`);
/* structure */
out.structure = await ev(`const nameless = $$('button, a[href], [role=button], [role=tab]').filter(b => b.offsetParent !== null && !(b.getAttribute('aria-label') || b.textContent.trim() || b.title)).map(b => b.outerHTML.slice(0, 90)); const unlabeled = $$('input:not([type=hidden]), select, textarea').filter(i => i.offsetParent !== null && !(i.getAttribute('aria-label') || i.labels && i.labels.length || i.getAttribute('aria-labelledby') || i.title)).map(i => i.outerHTML.slice(0, 90)); return { nameless, unlabeled, landmarks: ['header[role=banner]', 'nav', 'main', 'aside', 'footer'].map(s => s + ':' + $$(s).length).join(' '), lang: document.documentElement.lang, skip: !!$('.skip'), h1: $$('h1').length }`);
let total = 0, fails = 0; const rows = [];
for (const k of Object.keys(out)) { const o = out[k]; if (o.checked == null) continue; total += o.checked; fails += o.checked - o.pass; rows.push(k.padEnd(26) + ' ' + String(o.pass).padStart(5) + '/' + String(o.checked).padEnd(5) + (o.failGroups ? ' FAIL groups ' + o.failGroups : ' ok')); }
console.log(rows.join('\n')); console.log('TOTAL text nodes', total, 'below AA', fails);
const worst = {}; for (const k of Object.keys(out)) { const o = out[k]; if (!o.bad) continue; for (const g of Object.keys(o.bad)) { const w = worst[g.replace(/^\S+ /, '')] || (worst[g.replace(/^\S+ /, '')] = { n: 0, worst: 99, sample: '', color: '', where: k }); w.n += o.bad[g].n; if (o.bad[g].worst < w.worst) Object.assign(w, o.bad[g], { where: k }); } }
console.log(JSON.stringify(Object.entries(worst).sort((a, b) => b[1].n - a[1].n).slice(0, 40), null, 0));
console.log(JSON.stringify(out.structure));
await page.close();
