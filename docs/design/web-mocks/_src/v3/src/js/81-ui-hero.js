/* 81-ui-hero.js: SL.ui.hero, the drawn Sleipnir: a horse whose EIGHT legs are the eight workers,
 * the shared prefix bar G0..G5 above it (Cache details: Full only; nothing joins the bar to the legs) and the leg labels.
 *
 * Legs are numbered 1-8 in worker start order; a ninth worker shares leg 1 (both show a `+1` badge). The manager takes no leg and is not drawn on the horse: it has its own card.
 * Motion is driven by dtView (the governor), so a hold freezes the stride and a catch-up speeds it. Glow only on state: a working leg is
 * faintly haloed, `ask` pulses amber, `stuck` red, `done` stands planted and bright, `idle` is dim, a free leg is a ghost. */
(function (SL) {
  'use strict';
  const U = SL.u, { sv, esc } = U, D = SL.D, calc = SL.calc, ui = SL.ui = SL.ui || {};
  const GROUND = 468, L1 = 112, L2 = 82, HIPX = [316, 346, 376, 406, 478, 508, 538, 568], KDIR = [-1, -1, -1, -1, 1, 1, 1, 1];
  const belly = x => 282 + 28 * Math.sin(Math.PI * (x - 300) / 282), hipY = x => belly(x) - 8;
  const GAIT = {
    idle: { speed: 0, amp: 0, lift: 0, op: .34 }, free: { speed: 0, amp: 0, lift: 0, op: .16 }, done: { speed: 0, amp: 0, lift: 0, op: 1 },
    wait: { speed: .42, amp: .5, lift: .5, op: .8 }, think: { speed: .8, amp: .55, lift: .62, op: 1 }, tool: { speed: 1.45, amp: 1, lift: 1, op: 1 },
    edit: { speed: 1.85, amp: 1.05, lift: 1.05, op: 1 }, ask: { speed: .5, amp: 0, lift: 0, op: 1, raise: 1 }, stuck: { speed: 3.4, amp: .22, lift: .4, op: 1 },
  };
  const PRIO = ['ask', 'stuck', 'edit', 'tool', 'think', 'wait', 'done', 'idle'];
  const GLYPH = { think: '◇', tool: '⚙', edit: '✎', wait: '✉', ask: '?', idle: '◌', done: '✓', stuck: '⚠', free: '·' };
  const WORD = { think: 'think', tool: 'tool', edit: 'edit', wait: 'wait', ask: 'ask', idle: 'idle', done: 'done', stuck: 'stuck', free: 'free' };
  const pick = states => { for (const p of PRIO) if (states.includes(p)) return p; return 'idle'; };

  function mount(sc, svg, S) {
    svg.innerHTML = ''; const roster = S.roster, workers = roster.filter(r => r.id !== 'mgr');
    const legs = []; for (let i = 0; i < 8; i++) legs.push({ i, ids: workers.filter(w => w.leg === i).map(w => w.id), phase: (i * .37) % 1, speed: 0, amp: 0, lift: 0, op: .16, raise: 0, st: 'free' });
    const HS = .6, HTX = -82, HTY = 58, BAR_Y = 26, BAR_H = 17, X0 = 10, W = 352;
    const el = (n, a, p, t) => sv(n, a, p || svg, t);
    const title = el('text', { x: 2, y: 11, class: 'hh' }, null, workers.length ? 'Manager + ' + workers.length + ' workers' : 'One agent, no workers');
    const pre = el('g', { class: 'h-prefix' });   /* the shared prefix G0..G5: drawn in Cache details: Full, absent in Quiet */
    el('text', { x: 370, y: 11, 'text-anchor': 'end', class: 'hs' }, pre, 'one prefix · G0–G2 ' + U.fmtK(D.layers.slice(0, 3).reduce((s, l) => s + l.tok, 0)) + ' tok');
    /* prefix bar */
    const tot = D.layers.reduce((s, l) => s + l.tok, 0), BB = BAR_Y + BAR_H, bar = el('g', { class: 'pbar' }, pre); let x = X0; const lay = [];
    D.layers.forEach((l, i) => { const w = W * l.tok / tot; el('rect', { x: x.toFixed(1), y: BAR_Y, width: (w - 1.5).toFixed(1), height: BAR_H, fill: l.col, opacity: i < 3 ? .88 : .5 }, bar); el('text', { x: (x + 1).toFixed(1), y: BB + 12, fill: l.col, 'font-size': i > 2 ? 9 : 10.5, 'font-weight': 700, 'font-family': 'var(--f-num)' }, bar, l.id); lay.push({ x0: x, w }); x += w; });
    const shX1 = lay[2].x0 + lay[2].w - 1.5, BR = BB + 18;
    el('path', { d: 'M' + X0 + ' ' + BR + ' V' + (BR + 4) + ' H' + shX1.toFixed(1) + ' V' + BR, fill: 'none', stroke: 'var(--mgr)', 'stroke-width': 1, opacity: .7 }, bar);
    el('path', { d: 'M' + lay[3].x0.toFixed(1) + ' ' + BR + ' V' + (BR + 4) + ' H' + (X0 + W) + ' V' + BR, fill: 'none', stroke: 'var(--dim)', 'stroke-width': 1, opacity: .6 }, bar);
    el('text', { x: X0 + W, y: BR + 16, fill: 'var(--dim)', 'font-size': 10, 'text-anchor': 'end', 'font-family': 'var(--f-num)' }, bar, 'per agent');
    const warmTxt = el('text', { x: X0, y: BR + 16, fill: 'var(--mgr)', 'font-size': 10.5, 'font-family': 'var(--f-num)' }, bar); const warmSpan = sv('tspan', { fill: 'var(--warm)', 'font-weight': 700 }); warmTxt.append('shared · ', warmSpan);
    const flash = el('rect', { x: X0 - 2, y: BAR_Y - 2, width: (shX1 - X0 + 4).toFixed(1), height: BAR_H + 4, fill: 'none', stroke: 'var(--err)', 'stroke-width': 2, opacity: 0 }, bar);
    const flashFill = el('rect', { x: X0 - 1, y: BAR_Y - 1, width: (shX1 - X0 + 2).toFixed(1), height: BAR_H + 2, fill: 'var(--err)', opacity: 0 }, bar);
    const coldR = el('rect', { x: X0 - 1, y: BAR_Y - 1, width: (shX1 - X0 + 2).toFixed(1), height: BAR_H + 2, fill: 'var(--bg)', opacity: 0 }, bar);

    /* horse */
    const hg = el('g', { transform: 'translate(' + HTX + ' ' + HTY + ') scale(' + HS + ')' });
    const g = el('g', { class: 'horse' }, hg);
    const defs = el('defs', {}, g), grad = el('linearGradient', { id: 'hBody', x1: 0, y1: 0, x2: 0, y2: 1 }, defs);
    el('stop', { offset: 0, 'stop-color': 'var(--horse-hi)' }, grad); el('stop', { offset: .62, 'stop-color': 'var(--horse)' }, grad); el('stop', { offset: 1, 'stop-color': 'var(--horse-lo)' }, grad);
    el('line', { x1: 120, x2: 780, y1: GROUND + 6, y2: GROUND + 6, class: 'h-ground' }, g);
    const ticks = el('line', { x1: 100, x2: 800, y1: GROUND + 20, y2: GROUND + 20, class: 'h-ticks' }, g);
    const shadow = el('ellipse', { cx: 440, cy: GROUND + 6, rx: 190, ry: 7, class: 'h-shadow' }, g);
    const lg = el('g', { class: 'h-legs' }, g);
    legs.forEach(L => {
      const col = L.ids.length ? SL.u.agCol(L.ids[0]) : 'var(--faint)';
      L.g = el('g', { class: 'leg', 'data-ag': L.ids.join(' '), tabindex: -1, role: 'button', 'aria-label': 'Leg ' + (L.i + 1) + (L.ids.length ? ': ' + L.ids.join(', ') : ': free') }, lg);
      L.halo = el('path', { class: 'h-halo', fill: 'none', stroke: col, 'stroke-width': 30, 'stroke-linecap': 'round', 'stroke-linejoin': 'round', opacity: 0 }, L.g);
      L.o1 = el('path', { fill: 'none', stroke: 'var(--ink)', 'stroke-width': 24, 'stroke-linecap': 'round' }, L.g); L.o2 = el('path', { fill: 'none', stroke: 'var(--ink)', 'stroke-width': 14, 'stroke-linecap': 'round' }, L.g);
      L.c1 = el('path', { fill: 'none', stroke: col, 'stroke-width': 14.5, 'stroke-linecap': 'round' }, L.g); L.c2 = el('path', { fill: 'none', stroke: col, 'stroke-width': 7.5, 'stroke-linecap': 'round' }, L.g);
      L.knee = el('circle', { r: 6.2, fill: col, stroke: 'var(--ink)', 'stroke-width': 3.2 }, L.g); L.hoof = el('path', { fill: 'var(--ink)', stroke: 'var(--ink)', 'stroke-width': 3, 'stroke-linejoin': 'round' }, L.g);
      L.hit = el('path', { fill: 'none', stroke: 'transparent', 'stroke-width': 34, 'stroke-linecap': 'round' }, L.g); L.col = col;
    });
    const bodyG = el('g', { class: 'h-body' }, g), tailG = el('g', { class: 'h-tail' }, bodyG);
    el('path', { d: 'M300 228 C250 222 196 246 160 300 C150 318 150 336 160 346 C170 322 190 300 214 290 C190 326 186 350 196 368 C214 340 236 318 262 308', fill: 'none', stroke: 'var(--mane)', 'stroke-width': 6, 'stroke-linecap': 'round', 'stroke-linejoin': 'round' }, tailG);
    el('path', { d: 'M296 244 C262 244 226 262 202 292', fill: 'none', stroke: 'var(--mane)', 'stroke-width': 3, 'stroke-linecap': 'round', opacity: .55 }, tailG);
    el('path', { d: 'M296 226 C310 192 380 184 450 192 C500 197 530 188 548 200 C580 160 604 118 634 92 C640 66 660 62 668 76 C690 88 720 120 738 150 C746 166 740 182 724 180 C704 178 684 168 664 156 C660 176 646 196 624 212 C606 236 594 256 582 280 C560 312 500 316 440 310 C380 304 318 300 300 270 C290 254 290 240 296 226 Z', fill: 'url(#hBody)', stroke: 'var(--ink)', 'stroke-width': 5.5, 'stroke-linejoin': 'round' }, bodyG);
    const det = { fill: 'none', stroke: 'var(--ink)', 'stroke-width': 2.4, 'stroke-linecap': 'round', opacity: .32 };
    ['M516 214 C538 232 546 262 538 296', 'M352 214 C336 236 334 262 346 292', 'M672 104 C680 126 678 146 666 158', 'M722 176 C706 174 692 168 682 160'].forEach(d => el('path', Object.assign({ d }, det), bodyG));
    const headG = el('g', {}, bodyG);
    el('path', { d: 'M548 200 C550 168 568 138 590 112 M560 194 C564 160 582 132 606 104 M574 184 C580 152 598 124 622 96 M538 208 C538 178 550 152 568 128', fill: 'none', stroke: 'var(--mane)', 'stroke-width': 6, 'stroke-linecap': 'round' }, bodyG);
    el('path', { d: 'M636 90 C634 76 638 62 646 54 C654 60 660 70 662 80 Z', fill: 'var(--horse)', stroke: 'var(--ink)', 'stroke-width': 5, 'stroke-linejoin': 'round' }, headG);
    el('path', { d: 'M642 80 C641 72 643 66 647 62 C651 67 654 72 655 78 Z', fill: 'var(--mane)', opacity: .85 }, headG);
    el('circle', { cx: 674, cy: 104, r: 5.4, fill: 'var(--ink)' }, headG); el('circle', { cx: 675.6, cy: 102.2, r: 1.5, fill: 'var(--horse-hi)' }, headG); el('circle', { cx: 731, cy: 166, r: 3.4, fill: 'var(--ink)' }, headG);

    /* the leg labels (nothing joins the legs to anything above) */
    const lgG = el('g', { class: 'labels' }), labels = [];
    legs.forEach(L => {
      const hx = HIPX[L.i] * HS + HTX, lx = 24 + L.i * 46.3, gy = (GROUND + 8) * HS + HTY;
      el('path', { d: 'M' + hx.toFixed(1) + ' ' + gy.toFixed(1) + ' L' + lx.toFixed(1) + ' ' + (gy + 14).toFixed(1), stroke: L.col, 'stroke-width': 1, opacity: .45, fill: 'none' }, lgG);
      const lab = el('g', { class: 'hlab', 'data-ag': L.ids.join(' ') || null, tabindex: -1, role: 'button', 'aria-label': 'leg ' + (L.i + 1) + (L.ids.length ? ', ' + L.ids.join(' and ') : ', free') }, lgG); if (!L.ids.length) lab.removeAttribute('data-ag');
      el('rect', { x: (lx - 22).toFixed(1), y: gy + 14, width: 44, height: 32, fill: 'transparent' }, lab);
      el('text', { x: lx.toFixed(1), y: (gy + 27).toFixed(1), 'text-anchor': 'middle', fill: L.col, class: 'id' }, lab, L.ids.length ? L.ids[0] : '·' + (L.i + 1));
      const badge = L.ids.length > 1 ? el('text', { x: (lx + 16).toFixed(1), y: (gy + 20).toFixed(1), class: 'plus', 'text-anchor': 'start' }, lab, '+' + (L.ids.length - 1)) : null;
      const sg = el('text', { x: lx.toFixed(1), y: (gy + 40).toFixed(1), 'text-anchor': 'middle', class: 'sg' }, lab, '· free');
      labels.push({ L, sg, badge });
    });
    svg.addEventListener('click', e => { const t = e.target.closest && e.target.closest('[data-ag]'); if (t) { const id = t.getAttribute('data-ag').split(' ')[0]; if (id) ui.focusAgent(id, e); } });
    svg.addEventListener('keydown', e => { if (e.key === 'Enter' || e.key === ' ') { const t = e.target.closest && e.target.closest('[data-ag]'); if (t) { e.preventDefault(); ui.focusAgent(t.getAttribute('data-ag').split(' ')[0], e); } } });

    let time = 0, mean = 0, gOff = 0;
    function pose(L, hx, still) {
      const u = L.phase % 1, A = 34 * L.amp, Hh = 46 * L.lift, SF = .58; let fx, fy = GROUND - 2;
      if (u < SF) { const p = u / SF; fx = A - 2 * A * p; } else { const p = (u - SF) / (1 - SF), e = p * p * (3 - 2 * p); fx = -A + 2 * A * e; fy -= Hh * Math.sin(Math.PI * p); }
      if (L.raise > 0) { const tap = still ? 0 : Math.sin(time * 5); fx = fx * (1 - L.raise) + (62 + 6 * tap) * L.raise; fy = fy * (1 - L.raise) + (GROUND - 86 - 7 * Math.max(0, tap)) * L.raise; }
      return [hx + fx, fy];
    }
    const sharedState = (m, ids) => { const sts = ids.map(id => (m.ag[id] && m.ag[id].spawned) ? m.ag[id].state : 'idle'); return sts.length ? pick(sts) : 'free'; };
    function frame(dt, vt, S) {
      const m = S.m; if (!m) return; const still = ui.still(); time += dt; let sum = 0, n = 0;
      legs.forEach(L => {
        const s = sharedState(m, L.ids); L.st = s; const T = GAIT[s] || GAIT.idle, k = Math.min(1, dt * 4);
        const tA = still ? 0 : T.amp, tL = still ? 0 : T.lift, tS = still ? 0 : T.speed;
        L.speed += (tS - L.speed) * k; L.amp += (tA - L.amp) * k; L.lift += (tL - L.lift) * k; L.op += (T.op - L.op) * k; L.raise += ((T.raise || 0) - L.raise) * k; L.phase += L.speed * dt;
        sum += L.speed * (L.amp > .01 ? 1 : 0); n += (L.amp > .01 ? 1 : 0);
        const hx = HIPX[L.i], hy = hipY(hx); let [fx, fy] = pose(L, hx, still); if (s === 'stuck' && !still) fx += Math.sin(time * 38 + L.i) * 2.4;
        let dx = fx - hx, dy = fy - hy, d = Math.hypot(dx, dy); d = Math.min(Math.max(d, 40), L1 + L2 - .5);
        const th = Math.atan2(dy, dx), al = Math.acos(Math.max(-1, Math.min(1, (L1 * L1 + d * d - L2 * L2) / (2 * L1 * d)))), ang = th + (KDIR[L.i] > 0 ? -al : al);
        const kx = hx + L1 * Math.cos(ang), ky = hy + L1 * Math.sin(ang), ex = hx + d * Math.cos(th), ey = hy + d * Math.sin(th), f = v => v.toFixed(1);
        const t1 = 'M' + f(hx) + ' ' + f(hy) + ' L' + f(kx) + ' ' + f(ky), t2 = 'M' + f(kx) + ' ' + f(ky) + ' L' + f(ex) + ' ' + f(ey);
        L.o1.setAttribute('d', t1); L.c1.setAttribute('d', t1); L.o2.setAttribute('d', t2); L.c2.setAttribute('d', t2); L.halo.setAttribute('d', t1 + ' L' + f(ex) + ' ' + f(ey)); L.hit.setAttribute('d', t1 + ' L' + f(ex) + ' ' + f(ey));
        L.knee.setAttribute('cx', f(kx)); L.knee.setAttribute('cy', f(ky)); L.hoof.setAttribute('d', 'M' + f(ex - 6) + ' ' + f(ey + 2) + ' L' + f(ex + 7) + ' ' + f(ey + 2) + ' L' + f(ex + 3.5) + ' ' + f(ey + 11) + ' L' + f(ex - 5) + ' ' + f(ey + 11) + ' Z');
        L.g.style.opacity = L.op.toFixed(2);
        let hc = L.col, ho = 0; if (s === 'tool' || s === 'edit') ho = .22; if (s === 'ask') { hc = 'var(--warm)'; ho = still ? .4 : .28 + .3 * (.5 + .5 * Math.sin(time * 4.2)); } if (s === 'stuck') { hc = 'var(--err)'; ho = .4; } if (still && s !== 'ask' && s !== 'stuck') ho = 0;
        L.halo.setAttribute('stroke', hc); L.halo.setAttribute('opacity', ho.toFixed(2));
      });
      mean = n ? sum / n : 0; const act = still ? 0 : Math.min(1, mean / 1.2), bob = Math.sin(time * 2 * Math.PI * (.9 + mean * .7)) * 2.6 * act;
      bodyG.setAttribute('transform', 'translate(0 ' + bob.toFixed(2) + ')'); tailG.setAttribute('transform', 'rotate(' + (Math.sin(time * 1.7) * (1.5 + 4 * act)).toFixed(2) + ' 300 232)'); headG.setAttribute('transform', 'rotate(' + (Math.sin(time * 1.2 + 1) * (.6 + 1.2 * act)).toFixed(2) + ' 640 120)');
      shadow.setAttribute('rx', (190 - bob * 2).toFixed(1)); gOff = (gOff + dt * 140 * mean * (still ? 0 : 1)) % 36; ticks.setAttribute('stroke-dashoffset', gOff.toFixed(1));
      const cc = calc.warmLeft(m, vt), cold = cc <= 0, fl = m.flash.t > -900 ? Math.max(0, 1 - (vt - m.flash.t) / 2.4) : 0;
      flash.setAttribute('opacity', fl.toFixed(2)); flashFill.setAttribute('opacity', (fl * .55).toFixed(2)); coldR.setAttribute('opacity', cold ? .6 : 0);
      const w = cold ? 'cold' : 'warm ' + U.clock(cc); if (warmSpan.textContent !== w) { warmSpan.textContent = w; warmSpan.setAttribute('fill', cold || cc < 8 ? 'var(--err)' : 'var(--warm)'); }
          }
    function update(S2, m) {
      if (!m) return; const quiet = SL.settings.cache === 'quiet'; if (pre.style.display !== (quiet ? 'none' : '')) { pre.style.display = quiet ? 'none' : ''; svg.setAttribute('viewBox', quiet ? '0 30 372 358' : '0 0 372 388'); title.setAttribute('y', quiet ? 46 : 11); svg.parentNode.classList.toggle('qhorse', quiet); }
      labels.forEach(({ L, sg, badge }) => { const s = sharedState(m, L.ids), t = GLYPH[s] + ' ' + WORD[s]; if (sg.textContent !== t) sg.textContent = t; sg.setAttribute('fill', s === 'ask' ? 'var(--warm)' : s === 'stuck' ? 'var(--err)' : s === 'done' ? 'var(--ok)' : 'var(--dim)'); });
    }
    sc.frame(frame); sc.update(update); update(S, S.m);
    return { legs, frame, update };
  }
  ui.hero = { mount, GLYPH, WORD, GAIT };
})(SL);
