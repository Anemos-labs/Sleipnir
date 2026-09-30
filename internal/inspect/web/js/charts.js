// Canvas and SVG charts. Colors come from CSS custom properties at draw time, so
// the same code serves light and dark themes. Marks follow one spec: 2px lines,
// hairline solid gridlines, a 2px surface gap between stacked fills, markers of
// at least 8px with a surface ring, and labels only where they earn their place.

import { cssVar, niceTicks, tok, clock, clamp, showTip, hideTip, h, s } from './lib.js';

const DPR = () => Math.min(3, window.devicePixelRatio || 1);

function palette() {
  return {
    surface: cssVar('--surface'), ink: cssVar('--ink'), ink2: cssVar('--ink-2'), muted: cssVar('--muted'),
    grid: cssVar('--grid'), axis: cssVar('--axis'), accent: cssVar('--accent'), crit: cssVar('--crit'),
    read: cssVar('--c-read'), write: cssVar('--c-write'), fresh: cssVar('--c-fresh'),
    layers: [0, 1, 2, 3, 4, 5, 6].map(i => cssVar('--g' + i)),
  };
}

/** Binary search: index of the value in sorted `xs` nearest to x. */
function nearest(xs, x) {
  let lo = 0, hi = xs.length - 1;
  if (hi < 0) return -1;
  while (lo < hi) {
    const mid = (lo + hi) >> 1;
    if (xs[mid] < x) lo = mid + 1; else hi = mid;
  }
  if (lo > 0 && Math.abs(xs[lo - 1] - x) <= Math.abs(xs[lo] - x)) lo--;
  return lo;
}

/**
 * TimelineChart: context size as a stacked area (by cache class, or by prompt
 * layer) with compaction commits and cache anomalies marked, above the hit-ratio
 * line. Two aligned panels with their own axes, not one dual-axis plot.
 */
export class TimelineChart {
  /**
   * opts: compact, height, tooltip(row) -> Node, onSelect(row), onZoom(domain|null), onStep(dir)
   */
  constructor(host, opts = {}) {
    this.opts = opts;
    this.host = host;
    this.canvas = h('canvas', { role: 'img', tabindex: 0, 'aria-label': 'Context size and cache hit ratio over time' });
    host.appendChild(this.canvas);
    this.ctx = this.canvas.getContext('2d');
    this.rows = [];
    this.xs = [];
    this.commits = [];
    this.ticks = [];
    this.mode = 'time';
    this.color = 'cache';
    this.domain = null;
    this.selId = null;
    this.hover = -1;
    this.brush = null;
    this.W = 0;
    this.ro = new ResizeObserver(() => this.draw());
    this.ro.observe(host);
    const c = this.canvas;
    c.addEventListener('pointermove', e => this.onMove(e));
    c.addEventListener('pointerleave', () => { if (!this.brush) { this.hover = -1; hideTip(); this.draw(); } });
    c.addEventListener('pointerdown', e => this.onDown(e));
    c.addEventListener('pointerup', e => this.onUp(e));
    c.addEventListener('pointercancel', () => { this.brush = null; this.draw(); });
    c.addEventListener('dblclick', () => opts.onZoom && opts.onZoom(null));
    c.addEventListener('keydown', e => {
      if (e.key === 'ArrowLeft') { e.preventDefault(); opts.onStep && opts.onStep(-1); }
      if (e.key === 'ArrowRight') { e.preventDefault(); opts.onStep && opts.onStep(1); }
    });
  }

  destroy() { this.ro.disconnect(); this.canvas.remove(); hideTip(); }

  /** rows: main requests (done), oldest first. commits/ticks carry their own x in both modes. */
  setData({ rows, commits, ticks, mode, color, domain, selId }) {
    this.rows = rows;
    this.commits = commits || [];
    this.ticks = ticks || [];
    this.mode = mode;
    this.color = color;
    this.domain = domain || null;
    this.selId = selId || null;
    this.xs = rows.map((r, i) => (mode === 'time' ? r.t : i));
    this.draw();
  }

  setSelection(id) { this.selId = id; this.draw(); }

  layout() {
    const compact = !!this.opts.compact;
    const top = compact ? 62 : 190, bot = compact ? 28 : 96;
    const padL = 46, padR = 14, padT = compact ? 8 : 12, gap = compact ? 12 : 16, axis = compact ? 0 : 22;
    const H = padT + top + gap + bot + axis + (compact ? 4 : 0);
    return { compact, top, bot, padL, padR, padT, gap, axis, H, plotW: Math.max(10, this.W - padL - padR) };
  }

  xDomain() {
    if (this.domain) return this.domain;
    if (!this.xs.length) return [0, 1];
    let a = this.xs[0], b = this.xs[this.xs.length - 1];
    if (this.mode === 'req') { a -= 0.5; b += 0.5; }
    else if (a === b) { a -= 500; b += 500; }
    else { const pad = (b - a) * 0.01; a -= pad; b += pad; }
    return [a, b];
  }

  draw() {
    const host = this.host;
    const W = Math.floor(host.clientWidth);
    if (W < 40) return;
    this.W = W;
    const L = this.layout();
    const dpr = DPR();
    const c = this.canvas;
    if (c.width !== Math.round(W * dpr) || c.height !== Math.round(L.H * dpr)) {
      c.width = Math.round(W * dpr); c.height = Math.round(L.H * dpr);
      c.style.height = L.H + 'px';
    }
    const g = this.ctx;
    g.setTransform(dpr, 0, 0, dpr, 0, 0);
    g.clearRect(0, 0, W, L.H);
    const P = palette();
    const [d0, d1] = this.xDomain();
    const X = x => L.padL + (x - d0) / (d1 - d0 || 1) * L.plotW;
    const inView = x => x >= d0 - (d1 - d0) * 0.001 && x <= d1 + (d1 - d0) * 0.001;
    const rows = this.rows;
    const yTop0 = L.padT, yTop1 = L.padT + L.top;
    const yBot0 = yTop1 + L.gap, yBot1 = yBot0 + L.bot;

    g.font = '11px ' + cssVar('--sans');
    g.textBaseline = 'middle';

    // ---- bands ----
    const layers = this.color === 'layer';
    const nb = layers ? 7 : 3;
    const bandColor = layers ? P.layers : [P.read, P.write, P.fresh];
    const val = (r, b) => layers ? r.layers[b] : (b === 0 ? r.read : b === 1 ? r.write : r.in);
    const total = r => layers ? r.layers.reduce((a, v) => a + v, 0) : r.read + r.write + r.in;
    let vis = [];
    let max = 0;
    for (let i = 0; i < rows.length; i++) {
      const x = this.xs[i];
      if (x < d0 - (d1 - d0) * 0.05 || x > d1 + (d1 - d0) * 0.05) continue;
      vis.push(i);
      max = Math.max(max, total(rows[i]), rows[i].prompt);
    }
    const ticks = niceTicks(max * 1.02 || 1, L.compact ? 2 : 4);
    const ymax = ticks[ticks.length - 1] || 1;
    const Yt = v => yTop1 - v / ymax * L.top;

    // gridlines and y labels (top panel)
    g.lineWidth = 1;
    g.strokeStyle = P.grid; g.fillStyle = P.muted; g.textAlign = 'right';
    for (const t of ticks) {
      const y = Math.round(Yt(t)) + 0.5;
      g.beginPath(); g.moveTo(L.padL, y); g.lineTo(L.padL + L.plotW, y); g.stroke();
      if (!L.compact || (t !== 0 && t === ticks[ticks.length - 1])) g.fillText(tok(t), L.padL - 8, y);
    }

    if (vis.length) {
      const cum = vis.map(i => {
        const out = [0]; let a = 0;
        for (let b = 0; b < nb; b++) { a += val(rows[i], b); out.push(a); }
        return out;
      });
      const single = vis.length === 1;
      const halfW = 6;
      for (let b = 0; b < nb; b++) {
        g.beginPath();
        if (single) {
          const x = X(this.xs[vis[0]]);
          g.rect(x - halfW, Yt(cum[0][b + 1]), halfW * 2, Yt(cum[0][b]) - Yt(cum[0][b + 1]));
        } else {
          vis.forEach((i, k) => { const x = X(this.xs[i]), y = Yt(cum[k][b + 1]); k ? g.lineTo(x, y) : g.moveTo(x, y); });
          for (let k = vis.length - 1; k >= 0; k--) g.lineTo(X(this.xs[vis[k]]), Yt(cum[k][b]));
          g.closePath();
        }
        g.fillStyle = bandColor[b];
        g.globalAlpha = 0.92;
        g.fill();
        g.globalAlpha = 1;
      }
      // surface gap between bands, then the outline of the whole stack
      if (!single) {
        g.lineJoin = 'round'; g.lineCap = 'round';
        for (let b = 1; b < nb; b++) {
          g.beginPath();
          vis.forEach((i, k) => { const x = X(this.xs[i]), y = Yt(cum[k][b]); k ? g.lineTo(x, y) : g.moveTo(x, y); });
          g.strokeStyle = P.surface; g.lineWidth = 1.5; g.stroke();
        }
        g.beginPath();
        vis.forEach((i, k) => { const x = X(this.xs[i]), y = Yt(cum[k][nb]); k ? g.lineTo(x, y) : g.moveTo(x, y); });
        g.strokeStyle = P.ink; g.lineWidth = 1.5; g.stroke();
      }
      // end label: the current context size (a direct label, sparingly)
      const lastI = vis[vis.length - 1];
      const lx = X(this.xs[lastI]), ly = Yt(cum[cum.length - 1][nb]);
      g.fillStyle = P.ink; g.textAlign = lx > L.padL + L.plotW - 44 ? 'right' : 'left';
      g.font = '600 11px ' + cssVar('--sans');
      g.fillText(tok(rows[lastI].prompt), lx + (g.textAlign === 'right' ? -6 : 6), Math.max(yTop0 + 6, ly - 8));
      g.font = '11px ' + cssVar('--sans');
    }

    // ---- compaction commits: a hairline through both panels and a diamond on top ----
    g.textAlign = 'center';
    for (const cm of this.commits) {
      const x = this.mode === 'time' ? cm.t : cm.idx;
      if (!inView(x)) continue;
      const px = Math.round(X(x)) + 0.5;
      g.beginPath(); g.moveTo(px, yTop0); g.lineTo(px, yBot1); g.strokeStyle = P.muted; g.lineWidth = 1; g.stroke();
      diamond(g, px, yTop0 + 1, 5, P.ink2, P.surface);
    }
    // compactor calls: a rug of ticks at the foot of the top panel
    for (const tk of this.ticks) {
      const x = this.mode === 'time' ? tk.t : tk.idx;
      if (!inView(x)) continue;
      const px = Math.round(X(x)) + 0.5;
      g.beginPath(); g.moveTo(px, yTop1); g.lineTo(px, yTop1 - 6); g.strokeStyle = P.ink2; g.lineWidth = 1.5; g.stroke();
    }

    // ---- hit ratio panel ----
    g.strokeStyle = P.grid; g.fillStyle = P.muted; g.textAlign = 'right'; g.lineWidth = 1;
    const hitTicks = L.compact ? [0, 1] : [0, 0.5, 1];
    for (const t of hitTicks) {
      const y = Math.round(yBot1 - t * L.bot) + 0.5;
      g.beginPath(); g.moveTo(L.padL, y); g.lineTo(L.padL + L.plotW, y); g.stroke();
      g.fillText(Math.round(t * 100) + '%', L.padL - 8, y);
    }
    if (vis.length) {
      const Yh = v => yBot1 - clamp(v, 0, 1) * L.bot;
      const pts = vis.map(i => [X(this.xs[i]), Yh(rows[i].hit), rows[i]]);
      if (pts.length > 1) {
        g.beginPath();
        pts.forEach(([x, y], k) => k ? g.lineTo(x, y) : g.moveTo(x, y));
        g.lineTo(pts[pts.length - 1][0], yBot1); g.lineTo(pts[0][0], yBot1); g.closePath();
        g.fillStyle = P.read; g.globalAlpha = 0.10; g.fill(); g.globalAlpha = 1;
        g.beginPath();
        pts.forEach(([x, y], k) => k ? g.lineTo(x, y) : g.moveTo(x, y));
        g.strokeStyle = P.read; g.lineWidth = 2; g.lineJoin = 'round'; g.lineCap = 'round'; g.stroke();
      }
      const dots = pts.length <= 90;
      for (const [x, y, r] of pts) {
        if (r.anomaly) { ring(g, x, y, 5, P.crit, P.surface); continue; }
        if (dots) dot(g, x, y, 3.5, P.read, P.surface);
      }
    }

    // anomaly flags on the top panel (drawn last so they stay visible)
    for (const i of vis) {
      const r = rows[i];
      if (!r.anomaly && !(r.undeclared && !r.rebase)) continue;
      const x = X(this.xs[i]);
      triangle(g, x, yTop0 + 3, 6, r.anomaly ? P.crit : cssVar('--warn'), P.surface);
    }

    // ---- x axis ----
    if (!L.compact) {
      g.fillStyle = P.muted; g.textAlign = 'center'; g.strokeStyle = P.axis;
      const yAx = yBot1 + 0.5;
      g.beginPath(); g.moveTo(L.padL, yAx); g.lineTo(L.padL + L.plotW, yAx); g.stroke();
      const nt = Math.max(2, Math.floor(L.plotW / 90));
      for (let k = 0; k <= nt; k++) {
        const x = d0 + (d1 - d0) * k / nt;
        const label = this.mode === 'time' ? clock(x) : '#' + Math.round(x + 1);
        g.textAlign = k === 0 ? 'left' : k === nt ? 'right' : 'center';
        g.fillText(label, L.padL + L.plotW * k / nt, yBot1 + 14);
      }
    }

    // ---- selection and hover ----
    const mark = (i, strong) => {
      if (i < 0 || i >= rows.length) return;
      const x = X(this.xs[i]);
      if (x < L.padL - 1 || x > L.padL + L.plotW + 1) return;
      const px = Math.round(x) + 0.5;
      g.beginPath(); g.moveTo(px, yTop0); g.lineTo(px, yBot1);
      g.strokeStyle = strong ? P.accent : P.ink2; g.lineWidth = strong ? 2 : 1; g.stroke();
      dot(g, x, yBot1 - clamp(rows[i].hit, 0, 1) * L.bot, 4.5, strong ? P.accent : P.ink, P.surface);
    };
    const selI = this.selId ? rows.findIndex(r => r.id === this.selId) : -1;
    if (selI >= 0) mark(selI, true);
    if (this.hover >= 0 && this.hover !== selI) mark(this.hover, false);

    if (this.brush && this.brush.active) {
      const a = Math.min(this.brush.x0, this.brush.x1), b = Math.max(this.brush.x0, this.brush.x1);
      g.fillStyle = P.accent; g.globalAlpha = 0.12; g.fillRect(a, yTop0, b - a, yBot1 - yTop0); g.globalAlpha = 1;
      g.strokeStyle = P.accent; g.lineWidth = 1; g.strokeRect(a + 0.5, yTop0 + 0.5, b - a, yBot1 - yTop0);
    }
    this.geom = { L, d0, d1, X };
  }

  localX(e) {
    const r = this.canvas.getBoundingClientRect();
    return e.clientX - r.left;
  }

  pick(px) {
    if (!this.geom || !this.xs.length) return -1;
    const { L, d0, d1 } = this.geom;
    const x = d0 + (px - L.padL) / L.plotW * (d1 - d0);
    return nearest(this.xs, x);
  }

  onMove(e) {
    if (!this.geom) return;
    const px = this.localX(e);
    if (this.brush) {
      this.brush.x1 = px;
      this.brush.active = Math.abs(this.brush.x1 - this.brush.x0) > 5;
      this.draw();
      return;
    }
    const i = this.pick(px);
    if (i !== this.hover) { this.hover = i; this.draw(); }
    if (i >= 0 && this.opts.tooltip) showTip(this.opts.tooltip(this.rows[i], i), e.clientX, e.clientY);
  }

  onDown(e) {
    if (e.button !== 0 || !this.geom) return;
    this.canvas.setPointerCapture(e.pointerId);
    const px = this.localX(e);
    this.brush = { x0: px, x1: px, active: false };
  }

  onUp(e) {
    if (!this.brush) return;
    const b = this.brush;
    this.brush = null;
    try { this.canvas.releasePointerCapture(e.pointerId); } catch { /* not captured */ }
    if (!b.active) {
      const i = this.pick(b.x0);
      if (i >= 0 && this.opts.onSelect) this.opts.onSelect(this.rows[i], i);
      this.draw();
      return;
    }
    const { L, d0, d1 } = this.geom;
    const toX = px => d0 + (clamp(px, L.padL, L.padL + L.plotW) - L.padL) / L.plotW * (d1 - d0);
    const a = toX(Math.min(b.x0, b.x1)), z = toX(Math.max(b.x0, b.x1));
    if (this.opts.onZoom && z > a) this.opts.onZoom([a, z]);
    this.draw();
  }
}

function dot(g, x, y, r, fill, ring) {
  g.beginPath(); g.arc(x, y, r + 2, 0, 7); g.fillStyle = ring; g.fill();
  g.beginPath(); g.arc(x, y, r, 0, 7); g.fillStyle = fill; g.fill();
}
function ring(g, x, y, r, stroke, surface) {
  g.beginPath(); g.arc(x, y, r + 2, 0, 7); g.fillStyle = surface; g.fill();
  g.beginPath(); g.arc(x, y, r, 0, 7); g.fillStyle = stroke; g.fill();
  g.beginPath(); g.arc(x, y, r - 2, 0, 7); g.fillStyle = surface; g.fill();
}
function diamond(g, x, y, r, fill, surface) {
  g.beginPath(); g.moveTo(x, y - r - 1.5); g.lineTo(x + r + 1.5, y); g.lineTo(x, y + r + 1.5); g.lineTo(x - r - 1.5, y); g.closePath(); g.fillStyle = surface; g.fill();
  g.beginPath(); g.moveTo(x, y - r); g.lineTo(x + r, y); g.lineTo(x, y + r); g.lineTo(x - r, y); g.closePath(); g.fillStyle = fill; g.fill();
}
function triangle(g, x, y, r, fill, surface) {
  g.beginPath(); g.moveTo(x, y - r - 1.5); g.lineTo(x + r + 2, y + r); g.lineTo(x - r - 2, y + r); g.closePath(); g.fillStyle = surface; g.fill();
  g.beginPath(); g.moveTo(x, y - r); g.lineTo(x + r, y + r - 1); g.lineTo(x - r, y + r - 1); g.closePath(); g.fillStyle = fill; g.fill();
}

/**
 * LineChart: a few series over a shared x (elapsed ms or an index) with a
 * crosshair tooltip that lists every series at that x. Used for the cumulative
 * cost race; one axis, never two.
 */
export class LineChart {
  /** opts: height, xLabel(x)->string, yLabel(y)->string, tip(i)->Node */
  constructor(host, opts = {}) {
    this.host = host; this.opts = opts;
    this.canvas = h('canvas', { role: 'img', 'aria-label': opts.label || 'Line chart' });
    host.appendChild(this.canvas);
    this.ctx = this.canvas.getContext('2d');
    this.series = []; this.xs = []; this.hover = -1;
    this.ro = new ResizeObserver(() => this.draw());
    this.ro.observe(host);
    this.canvas.addEventListener('pointermove', e => this.onMove(e));
    this.canvas.addEventListener('pointerleave', () => { this.hover = -1; hideTip(); this.draw(); });
  }
  destroy() { this.ro.disconnect(); this.canvas.remove(); hideTip(); }

  /** series: [{name, color(P)->css, width, dash, values[]}] with values aligned to xs. */
  setData(xs, series) { this.xs = xs; this.series = series; this.draw(); }

  draw() {
    const W = Math.floor(this.host.clientWidth);
    if (W < 40) return;
    const H = this.opts.height || 240;
    const dpr = DPR(), c = this.canvas;
    if (c.width !== Math.round(W * dpr) || c.height !== Math.round(H * dpr)) {
      c.width = Math.round(W * dpr); c.height = Math.round(H * dpr); c.style.height = H + 'px';
    }
    const g = this.ctx, P = palette();
    g.setTransform(dpr, 0, 0, dpr, 0, 0); g.clearRect(0, 0, W, H);
    g.font = '11px ' + cssVar('--sans'); g.textBaseline = 'middle';
    g.font = '600 11px ' + cssVar('--sans');
    const padL = 54, padR = Math.min(Math.max(...this.series.map(sr => g.measureText(sr.short || sr.name).width), 40) + 18, Math.floor(W * 0.4)), padT = 10, padB = 24;
    g.font = '11px ' + cssVar('--sans');
    const pw = W - padL - padR, ph = H - padT - padB;
    const xs = this.xs;
    if (!xs.length) return;
    const x0 = xs[0], x1 = xs[xs.length - 1] === xs[0] ? xs[0] + 1 : xs[xs.length - 1];
    let max = 0;
    for (const sr of this.series) for (const v of sr.values) max = Math.max(max, v);
    const ticks = niceTicks(max * 1.05 || 1, 4), ymax = ticks[ticks.length - 1] || 1;
    const X = x => padL + (x - x0) / (x1 - x0) * pw, Y = v => padT + ph - v / ymax * ph;
    g.strokeStyle = P.grid; g.fillStyle = P.muted; g.lineWidth = 1; g.textAlign = 'right';
    for (const t of ticks) {
      const y = Math.round(Y(t)) + 0.5;
      g.beginPath(); g.moveTo(padL, y); g.lineTo(padL + pw, y); g.stroke();
      g.fillText(this.opts.yLabel ? this.opts.yLabel(t) : String(t), padL - 8, y);
    }
    g.strokeStyle = P.axis;
    g.beginPath(); g.moveTo(padL, padT + ph + 0.5); g.lineTo(padL + pw, padT + ph + 0.5); g.stroke();
    const nt = Math.max(2, Math.floor(pw / 100));
    g.fillStyle = P.muted;
    for (let k = 0; k <= nt; k++) {
      const x = x0 + (x1 - x0) * k / nt;
      g.textAlign = k === 0 ? 'left' : k === nt ? 'right' : 'center';
      g.fillText(this.opts.xLabel ? this.opts.xLabel(x) : String(Math.round(x)), padL + pw * k / nt, H - 8);
    }
    // series: later ones are drawn first so the emphasised one (first) sits on top
    const order = this.series.map((_, i) => i).reverse();
    for (const i of order) {
      const sr = this.series[i];
      g.beginPath();
      xs.forEach((x, k) => k ? g.lineTo(X(x), Y(sr.values[k])) : g.moveTo(X(x), Y(sr.values[k])));
      g.strokeStyle = sr.color(P); g.lineWidth = sr.width || 2; g.lineJoin = 'round'; g.lineCap = 'round';
      g.setLineDash(sr.dash || []);
      g.stroke(); g.setLineDash([]);
    }
    // direct labels at the line ends, nudged apart if they would overlap
    const ends = this.series.map((sr, i) => ({ i, y: Y(sr.values[sr.values.length - 1]) })).sort((a, b) => a.y - b.y);
    for (let k = 1; k < ends.length; k++) if (ends[k].y - ends[k - 1].y < 13) ends[k].y = ends[k - 1].y + 13;
    g.textAlign = 'left';
    for (const e of ends) {
      const sr = this.series[e.i];
      g.fillStyle = P.ink;
      g.font = '600 11px ' + cssVar('--sans');
      g.fillText(sr.short || sr.name, padL + pw + 8, e.y);
    }
    g.font = '11px ' + cssVar('--sans');
    if (this.hover >= 0) {
      const x = X(xs[this.hover]);
      g.beginPath(); g.moveTo(Math.round(x) + 0.5, padT); g.lineTo(Math.round(x) + 0.5, padT + ph); g.strokeStyle = P.ink2; g.lineWidth = 1; g.stroke();
      for (const sr of this.series) dot(g, x, Y(sr.values[this.hover]), 4, sr.color(P), P.surface);
    }
    this.geom = { padL, pw, x0, x1 };
  }

  onMove(e) {
    if (!this.geom) return;
    const r = this.canvas.getBoundingClientRect();
    const { padL, pw, x0, x1 } = this.geom;
    const x = x0 + (e.clientX - r.left - padL) / pw * (x1 - x0);
    const i = nearest(this.xs, x);
    if (i !== this.hover) { this.hover = i; this.draw(); }
    if (i >= 0 && this.opts.tip) showTip(this.opts.tip(i), e.clientX, e.clientY);
  }
}

/** A small line with an end dot, for tiles and cards. y is scaled to [lo,hi] (default: the data's range). */
export function sparkline(values, { w = 120, hgt = 28, lo, hi, title } = {}) {
  const svg = s('svg', { viewBox: `0 0 ${w} ${hgt}`, width: w, height: hgt, role: 'img', 'aria-label': title || 'trend' });
  if (!values || values.length < 2) return svg;
  const mn = lo != null ? lo : Math.min(...values), mx = hi != null ? hi : Math.max(...values);
  const span = mx - mn || 1;
  const px = i => 2 + i / (values.length - 1) * (w - 4);
  const py = v => hgt - 3 - (clamp(v, mn, mx) - mn) / span * (hgt - 6);
  const d = values.map((v, i) => (i ? 'L' : 'M') + px(i).toFixed(1) + ' ' + py(v).toFixed(1)).join('');
  svg.appendChild(s('path', { class: 'sp-line', d }));
  const last = values.length - 1;
  svg.appendChild(s('circle', { class: 'sp-dot', cx: px(last), cy: py(values[last]), r: 3.5 }));
  return svg;
}

/** Per-minute bars: one rect per value, with a native title as a supplement to the numbers next to it. */
export function bars(values, labels, { w = 300, hgt = 34 } = {}) {
  const svg = s('svg', { viewBox: `0 0 ${w} ${hgt}`, preserveAspectRatio: 'none', role: 'img', 'aria-label': 'activity per minute' });
  const n = values.length;
  if (!n) return svg;
  const mx = Math.max(1, ...values);
  const bw = Math.max(1.5, Math.min(14, (w - 2) / n - 1.5));
  const step = n > 1 ? (w - bw - 2) / (n - 1) : 0;
  values.forEach((v, i) => {
    const bh = v ? Math.max(2, v / mx * (hgt - 2)) : 0;
    const r = s('rect', { x: 1 + i * step, y: hgt - bh, width: bw, height: bh, rx: 1.5 });
    r.appendChild(s('title', null, (labels && labels[i] ? labels[i] + ': ' : '') + v));
    svg.appendChild(r);
  });
  return svg;
}
