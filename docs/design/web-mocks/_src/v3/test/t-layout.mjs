// t-layout.mjs: no horizontal overflow at 390 / 768 / 1024 / 1280 / 1440 / 1920 in every view; overlap check on text nodes is visual (see screenshots)
import { open } from './cdp.mjs';
const FILE = process.argv[2] || '../dist/test.html', out = [];
const sizes = [[390, 844], [768, 1024], [1024, 700], [1280, 720], [1440, 900], [1920, 1080]];
const views = ['cockpit', 'cache', 'mail', 'board', 'replay', 'sessions', 'settings', 'runner', 'kit'];
for (const [w, h] of sizes) {
  const page = await open(FILE, { w, h, query: 'manual' });
  const r = await page.eval(`(async()=>{ const SL = __SL, T = SL.test; T.manual(); T.run(1); const o = []; for (const v of views) { SL.views.show(v); T.run(0.2); await T.idle(); const de = document.documentElement; const wide = Array.from(document.querySelectorAll('body *')).filter(e => { const r = e.getBoundingClientRect(); return r.right > innerWidth + 1 && r.width > 0 && getComputedStyle(e).position !== 'fixed' && !e.closest('svg') && !e.closest('.view,.layer,.drawer,#sstrip,.tabs') ; }).slice(0, 3).map(e => e.tagName + '.' + String(e.className).slice(0, 24) + ' ' + Math.round(e.getBoundingClientRect().right)); o.push({ v, sw: de.scrollWidth, iw: innerWidth, over: de.scrollWidth > innerWidth, wide }); } return o })()`.replace('views', JSON.stringify(views)));
  out.push({ size: w + 'x' + h, overflow: r.filter(x => x.over || x.wide.length).map(x => ({ view: x.v, scrollWidth: x.sw, innerWidth: x.iw, wide: x.wide })), errors: page.errors.length });
  await page.close();
}
console.log(JSON.stringify(out, null, 1));
