// t-fit.mjs: vertical fit (and, at 390 and 768 wide, that the blocks of the radio pane do not overlap). At desktop sizes the page never scrolls: the rail (question, plan, transcript, composer), the footer and the HUD all fit in the
// viewport, with a question open and without one, in every channel. Pass: no page or rail overflow, composer and footer visible, transcript >= 40 px, the question box
// clipped by at most 12 px (it scrolls inside its slot below that; the supported minimum is 640 px high). Reports the numbers per size. Usage: node t-fit.mjs [dist/test.html]
import { open } from './cdp.mjs';
const FILE = process.argv[2] || '../dist/test.html', out = [];
const sizes = [[390, 844], [768, 1024], [1024, 700], [1100, 640], [1280, 640], [1280, 720], [1366, 768], [1440, 900], [1920, 1080], [2560, 1300]];
for (const [w, h] of sizes) {
  const page = await open(FILE, { w, h, query: 'manual' });
  const r = await page.eval(`(async()=>{ const SL = __SL, T = SL.test, $ = s => document.querySelector(s), R = e => e.getBoundingClientRect(); T.manual(); T.run(3); await T.idle(); const rows = []; const phone = innerWidth <= 900; if (phone) { $('#app').dataset.pv = 'radio'; SL.loop.dirty = true; T.run(0.4); }
    const measure = tag => { const de = document.documentElement, rail = $('#rail'), q = $('.qstrip'), comp = R($('#composer')), foot = R($('#foot')), talk = R($('#talk'));
      const seq = ['#chHead', '#qSlot>*', '#planBox', '.talkwrap', '#feedWrap'].concat(phone ? [] : ['#composer']).map(s => $(s)).filter(e => e && e.offsetHeight > 0).map(e => [e.id || e.className, R(e)]); let overlap = ''; for (let i = 1; i < seq.length; i++) if (seq[i][1].top < seq[i - 1][1].bottom - 1) overlap += seq[i - 1][0] + '/' + seq[i][0] + ' ';
      return { tag, phone, overlap, docOver: de.scrollHeight - innerHeight, railOver: rail.scrollHeight - rail.clientHeight, composerBottomVsFoot: Math.round(comp.bottom - foot.top), composerVisible: comp.top >= 0 && comp.bottom <= foot.top + 1, talkH: Math.round(talk.height), qClip: q ? q.scrollHeight - q.clientHeight : null, hudOverStrip: Math.round(R($('#hud')).bottom - R($('#sstrip')).top), footInView: foot.bottom <= innerHeight + 1 }; };
    rows.push(measure('question open, manager'));
    $('#feedTog').click(); T.run(0.3); rows.push(measure('question open, Team activity collapsed')); $('#feedTog').click(); T.run(0.3);
    const q = SL.calc.openQuestion(SL.sessions.active.m); if (q) { SL.time.noteKey(); T.run(1.2); SL.act.answerQuestion(q.id, 1, undefined, 'shop'); T.run(3); }
    rows.push(measure('no question'));
    SL.views.show('cache'); T.run(0.2); rows.push(measure('cache view'));
    return rows; })()`);
  out.push({ size: w + 'x' + h, rows: r, errors: page.errors.length }); if (page.errors.length) console.error(w + 'x' + h, page.errors);
  await page.close();
}
let bad = 0;
const flag = o => { const p = []; if (o.overlap) p.push('overlap ' + o.overlap); if (o.phone) return p;   // the phone rail scrolls as a whole: only overlaps matter
   if (o.docOver > 1) p.push('document scrolls ' + o.docOver); if (o.railOver > 1) p.push('rail overflows ' + o.railOver); if (!o.composerVisible) p.push('composer hidden'); if (o.talkH < 40) p.push('transcript ' + o.talkH); if (o.qClip != null && o.qClip > 12) p.push('question clipped ' + o.qClip); if (o.hudOverStrip > 1) p.push('hud overlaps strip'); if (!o.footInView) p.push('footer off screen'); return p; };
out.forEach(s => s.rows.forEach(o => { o.problems = flag(o); if (o.problems.length) bad++; }));
console.log(JSON.stringify(out.map(s => ({ size: s.size, errors: s.errors, rows: s.rows.map(o => o.tag + ': ' + (o.problems.length ? 'PROBLEM ' + o.problems.join(', ') : 'ok') + ' (talk ' + o.talkH + 'px, question clip ' + o.qClip + ')') })), null, 1));
console.log('problems:', bad);
