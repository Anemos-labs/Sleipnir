// t-quiet-scan.mjs: in Quiet, no visible hit-% number or savings figure outside the Cache view and the drawer's Cache tab; in Full they come back
import { open } from './cdp.mjs';
const page = await open(process.argv[2], { w: +(process.env.W || 1440), h: +(process.env.H || 900) });
const ev = js => page.eval('(async()=>{ const SL = __SL, T = SL.test, $ = s => document.querySelector(s), $$ = s => Array.from(document.querySelectorAll(s)); ' + js + ' })()');
await ev(`T.manual(); T.run(1);`);
const SCAN = `const re = /(\\d+\\s*%\\s*(of\\s+)?(hit|read))|hit\\s*[:=]?\\s*\\d+\\s*%|\\bsaved\\b|saving|vs\\.? no cache|no cache|cache hit|\\best\\.\\)/i; const out = [];
  const walk = root => { const w = document.createTreeWalker(root, NodeFilter.SHOW_TEXT); let n; while ((n = w.nextNode())) { const t = n.nodeValue.trim(); if (!t || !re.test(t)) continue; const el = n.parentElement; if (!el) continue; const cs = getComputedStyle(el); if (cs.display === 'none' || cs.visibility === 'hidden') continue; let p = el, hidden = false; while (p && p !== document.body) { if (getComputedStyle(p).display === 'none' || p.hidden) { hidden = true; break; } p = p.parentElement; } if (hidden) continue; if (el.closest('.sr,#announcer')) continue; const r = el.getBoundingClientRect(); if (!r.width && !r.height) continue; out.push(t.slice(0, 90) + '  <' + (el.className || el.tagName) + '>'); } };
  walk(document.body); return out;`;
const out = {};
for (const mode of ['quiet', 'full']) {
  await ev(`SL.act.setCache('${mode}'); T.run(0.5);`);
  const views = ['cockpit', 'cache', 'mail', 'board', 'replay', 'sessions', 'tools', 'settings', 'checkpoints', 'changes', 'files', 'runner'];
  out[mode] = {};
  for (const v of views) {
    out[mode][v] = await ev(`try { SL.ui.nav.go('${v}'); } catch (e) { SL.views.show('${v}'); } T.run(0.6); ${SCAN}`);
  }
  out[mode].settingsBudget = await ev(`SL.ui.settingsPage('budget'); T.run(0.6); ${SCAN}`);
  out[mode].settingsAppearance = await ev(`SL.ui.settingsPage('look'); T.run(0.6); ${SCAN}`);
  out[mode].drawerInfo = await ev(`SL.views.show('cockpit'); T.run(0.4); SL.ui.openDrawer('be-2'); T.run(0.4); ${SCAN}`);
  out[mode].drawerCache = await ev(`document.querySelector('[data-tab="cache"]').click(); T.run(0.3); ${SCAN}`);
  out[mode].mgrDrawer = await ev(`SL.ui.closeDrawer(); SL.ui.openDrawer('mgr'); T.run(0.4); ${SCAN}`);
  out[mode].sessionsMenu = await ev(`SL.ui.closeDrawer(); const b = document.querySelector('#sBud'); b.click(); T.run(0.3); const r = ${'(() => { ' + SCAN.replace('return out;', 'return out;') + ' })()'}; b.click(); return r;`);
  out[mode].costCmd = await ev(`SL.ui.closeDrawer(); SL.views.show('cockpit'); SL.palette.runLine('/cost'); T.run(0.5); ${SCAN}`);
  out[mode].help = await ev(`SL.ui.sheets.help ? SL.ui.sheets.help() : 0; T.run(0.3); const r = (() => { ${SCAN} })(); SL.ui.closeModal(true); return r;`);
}
const ALLOWED = /cache hit ratio|saved in this browser|Quiet keeps the cache|the stats page|tokens, cost and cache hit/;   // command descriptions and the setting's own help text, not figures
const bad = [], leftQuiet = [];
for (const [mode, views] of Object.entries(out)) for (const [v, lines] of Object.entries(views)) for (const l of lines) {
  if (ALLOWED.test(l)) continue;
  if (mode === 'quiet') bad.push(mode + ' / ' + v + ': ' + l);
}
const fullShows = Object.values(out.full).flat().some(l => /saved est|saved  <k>|\d+% hit/.test(l));
console.log('quiet: ' + bad.length + ' savings or hit figures outside the Cache view and the drawer Cache tab' + (bad.length ? '\n  ' + bad.join('\n  ') : ''));
console.log('full: the figures are back: ' + fullShows);
await page.close();
process.exit(bad.length || !fullShows ? 1 : 0);
