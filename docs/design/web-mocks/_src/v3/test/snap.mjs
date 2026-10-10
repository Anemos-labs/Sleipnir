// snap.mjs NAME W H [JS] [--file F] [--scale N] [--reduced]: open the scratch build in manual mode, run the page's JS (optional), screenshot to $SHOTS/NAME.png
import { open } from './cdp.mjs';
const a = process.argv.slice(2), flag = (k, d) => { const i = a.indexOf(k); if (i < 0) return d; const v = a[i + 1]; a.splice(i, 2); return v; };
const file = flag('--file', '../dist/test.html'), scale = +flag('--scale', 1), reduced = a.includes('--reduced'); if (reduced) a.splice(a.indexOf('--reduced'), 1);
const [name, w, h, js] = a;
const page = await open(file, { query: 'manual', w: +w, h: +h, scale, reduced });
const ev = code => page.eval('(async()=>{ const SL = __SL, T = SL.test, D = SL.D; ' + code + ' })()');
await ev('T.manual(); T.run(3);');
if (js) await ev(js);
await page.sleep(350); await page.shot((process.env.SHOTS || '.') + '/' + name + '.png');
if (page.errors.length) console.log('ERRORS', JSON.stringify(page.errors));
await page.close();
