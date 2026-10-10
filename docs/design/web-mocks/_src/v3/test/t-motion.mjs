// t-motion.mjs: reduced motion. With prefers-reduced-motion the page carries body.still, runs no Web Animation and no looping CSS animation, the horse's legs
// do not move between frames, a mail arc is not drawn; the simulation still advances. Compared with the same page without the preference.
import { open } from './cdp.mjs';
const FILE = process.argv[2] || '../dist/test.html', out = {};
for (const reduced of [true, false]) {
  const page = await open(FILE, { query: 'manual', reduced });
  const r = await page.eval(`(async()=>{ const SL = __SL, T = SL.test; T.manual(); T.run(2); await T.idle();
    const svg = document.querySelector('.horsebox svg, .hero svg, #horse svg, svg.horse') || document.querySelector('[data-view=cockpit] svg'); const legs = () => svg ? svg.innerHTML.length + ':' + Array.from(svg.querySelectorAll('[data-ag]')).map(e => (e.getAttribute('transform') || '') + (e.getAttribute('d') || '') + (e.getAttribute('x2') || '') + (e.getAttribute('y2') || '')).join('|') : '';
    const l0 = legs(); T.run(0.4); const l1 = legs();
    const sig = SL.test.sig(SL.sessions.active.m); const before = SL.sessions.active.wt; T.run(5);
    SL.sessions.active.add({ k: 'mail', from: 'be-2', to: 'ts-1', text: 'motion probe' }); T.run(0.3); const arcs = document.querySelectorAll('.arcs g, .arc-env, .travel > *').length;
    const looping = document.getAnimations().filter(a => { const t = a.effect && a.effect.getComputedTiming ? a.effect.getComputedTiming() : null; return a.playState === 'running' && t && (t.iterations === Infinity || t.iterations > 3) && t.duration > 1; }).length;
    return { still: document.body.classList.contains('still'), uiStill: SL.ui.still(), legsMoved: l0 !== l1, legsSampled: l0.length > 0, animations: document.getAnimations().filter(a => a.playState === 'running').length, loopingAnimations: looping, mailArcNodes: arcs, simAdvanced: SL.sessions.active.wt > before + 4.9, stateChanged: SL.test.sig(SL.sessions.active.m) !== sig };
  })()`);
  out[reduced ? 'reduced' : 'full'] = r; out[(reduced ? 'reduced' : 'full') + 'Errors'] = page.errors; await page.close();
}
console.log(JSON.stringify(out, null, 1));
