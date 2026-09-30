// The layer stack of one request: the seven layers side by side in prefix order,
// the same tokens as the provider billed them (cache read, cache write, uncached),
// and what changed since the agent's previous request. A prefix cache breaks at
// the first changed byte, so a rebase shows up as a partial rewrite: the layers
// before the change stay cached, the changed layer and everything after it are
// hatched.

import { h, s, clear, tok, pct, showTip, hideTip } from './lib.js';

const SHORT = ['const', 'shared', 'role', 'notes', 'spine', 'thread', 'hot'];
const REBASE_LABEL = { commit: 'compaction commit', sync: 'shared-layer sync', epoch: 'shared epoch' };

const STATE_TEXT = {
  cached: 'cached', appended: 'appended', rewritten: 'rewritten', invalidated: 'invalidated',
  new: 'new', fresh: 'fresh every request', absent: 'absent', removed: 'removed',
};

const STATE_HELP = {
  cached: 'Same bytes as the previous request and nothing before it changed: served from cache.',
  appended: 'Previous turns kept byte for byte; only new turns were added at the end.',
  rewritten: 'Its own bytes changed, so the provider cannot reuse it.',
  invalidated: 'Same bytes, but a layer before it changed. A prefix cache matches from the start, so it is re-processed too.',
  new: 'Did not exist in the previous request.',
  fresh: 'The hot tail is regenerated on every request by design and sits after the last breakpoint.',
};

export function stateLabel(st) { return STATE_TEXT[st] || st; }

/** One sentence that says what the request paid for and why. */
export function verdict(rep, prevRow) {
  const L = rep.layers.filter(l => l.state !== 'absent');
  const changed = L.filter(l => l.changed && l.key !== 'G6').map(l => l.key);
  const inv = L.filter(l => l.state === 'invalidated').map(l => l.key);
  const cached = L.filter(l => l.state === 'cached' || l.state === 'appended').map(l => l.key);
  const list = a => a.length > 1 ? a.slice(0, -1).join(', ') + ' and ' + a[a.length - 1] : a[0];
  const r = rep.req;
  if (rep.req.kind !== 'main') return { tone: '', text: 'A compactor fork: it re-sends the agent’s prompt with one instruction appended, so the provider serves nearly all of it from cache.' };
  if (!rep.prev) {
    return {
      tone: '',
      text: 'First request of this agent: every layer is new to it.' +
        (rep.read > 0 ? ` The provider already held ${tok(rep.read)} tokens of the shared prefix, read from cache because another agent wrote them.`
          : ' Nothing was cached yet, so the whole prompt was processed and written at the write price.'),
    };
  }
  if (!changed.length) {
    const g5 = rep.layers.find(l => l.key === 'G5');
    const grew = prevRow && g5 ? g5.tokens - prevRow.layers[5] : null;
    return { tone: 'good', text: 'Prefix intact: nothing before the thread changed' + (grew != null && grew > 0 ? `, and the thread grew by about ${tok(grew)} tokens.` : '.') };
  }
  if (rep.rebase) {
    return {
      tone: '',
      text: `A ${REBASE_LABEL[rep.rebase] || rep.rebase} rewrote ${list(changed)}` +
        (inv.length ? `; ${list(inv)} ${inv.length > 1 ? 'were' : 'was'} invalidated behind it` : '') +
        '. ' + (cached.length ? `${list(cached)} stayed cached.` : 'Nothing stayed cached.') +
        ` ${tok(rep.fresh + rep.write)} tokens had to be processed again (${pct(r.hit)} of the prompt was still read from cache).`,
    };
  }
  return {
    tone: 'crit',
    text: `${list(changed)} changed with no declared rebase (no compaction commit or shared-layer sync). ` +
      `Everything from ${changed[0]} onward misses the cache: ${tok(rep.fresh + rep.write)} tokens were processed again.`,
  };
}

export class LayerStack {
  /** opts: onLayer(key), compact */
  constructor(host, opts = {}) {
    this.host = host; this.opts = opts; this.rep = null; this.sel = null; this.prevRow = null;
    this.box = h('div', { class: 'stack' });
    host.appendChild(this.box);
    this.ro = new ResizeObserver(() => this.draw());
    this.ro.observe(host);
  }

  destroy() { this.ro.disconnect(); this.box.remove(); hideTip(); }

  set(rep, prevRow, sel) { this.rep = rep; this.prevRow = prevRow; this.sel = sel || null; this.draw(); }

  draw() {
    const rep = this.rep;
    clear(this.box);
    if (!rep) return;
    const W = Math.floor(this.host.clientWidth);
    if (W < 120) return;
    const compact = !!this.opts.compact;
    const layers = rep.layers;
    const sumL = layers.reduce((a, l) => a + l.tokens, 0);
    const total = Math.max(rep.prompt, sumL, 1);
    const sc = (W - 2) / total;
    const yA = 22, hA = compact ? 34 : 42, yB = yA + hA + 30, hB = compact ? 18 : 22;
    const H = yB + hB + (compact ? 22 : 40);
    const svg = s('svg', { viewBox: `0 0 ${W} ${H}`, width: W, height: H, role: 'group', 'aria-label': `Prompt layers of ${rep.req.id}` });
    svg.appendChild(s('defs', null,
      s('pattern', { id: 'hatch', width: 7, height: 7, patternUnits: 'userSpaceOnUse', patternTransform: 'rotate(45)' }, s('rect', { class: 'hatch-bar', width: 3, height: 7 })),
      s('pattern', { id: 'hatchl', width: 9, height: 9, patternUnits: 'userSpaceOnUse', patternTransform: 'rotate(45)' }, s('rect', { class: 'hatch-bar', width: 1.6, height: 9 }))));

    svg.appendChild(s('text', { x: 0, y: 12 }, 'Layers, in prompt order (width = tokens)'));
    svg.appendChild(s('text', { x: 0, y: yB - 8 }, 'As billed by the provider: read from cache, written to cache, processed uncached'));

    // Row A: layers.
    let x = 0;
    for (const l of layers) {
      if (l.tokens <= 0) continue;
      const w = Math.max(l.tokens * sc, 3);
      const g = s('g', { class: 'seg', tabindex: 0, role: 'button', 'aria-label': `${l.key} ${l.title}, ${l.tokens} tokens, ${l.state}` });
      const gi = +l.key.slice(1);
      g.appendChild(s('rect', { class: 'lay g' + gi, x, y: yA, width: Math.max(w - 2, 1), height: hA, rx: 3 }));
      if (l.state === 'rewritten' || l.state === 'new') g.appendChild(s('rect', { class: 'hatch', x, y: yA, width: Math.max(w - 2, 1), height: hA, rx: 3 }));
      if (l.state === 'invalidated') g.appendChild(s('rect', { class: 'hatch light', x, y: yA, width: Math.max(w - 2, 1), height: hA, rx: 3 }));
      if (this.sel === l.key) g.appendChild(s('rect', { class: 'selring', x: x - 1, y: yA - 1, width: Math.max(w - 2, 1) + 2, height: hA + 2, rx: 4 }));
      const tcls = 'in' + (gi >= 3 ? ' g' + gi + 't' : '');
      const name = l.key + ' ' + SHORT[gi];
      if (w - 2 >= textW(name, true) + 16) {
        g.appendChild(s('text', { class: tcls, x: x + 8, y: yA + 16 }, name));
        if (!compact) g.appendChild(s('text', { class: tcls + ' val', x: x + 8, y: yA + 32 }, tok(l.tokens)));
      } else if (w - 2 >= textW(l.key, true) + 10) g.appendChild(s('text', { class: tcls, x: x + 6, y: yA + hA / 2 + 4 }, l.key));
      if (l.breakpoint) g.appendChild(s('path', { class: 'bp', d: `M${x + w - 2},${yA - 9} l4,4 l-4,4 l-4,-4 z` }));
      g.addEventListener('pointermove', e => showTip(layerTip(l), e.clientX, e.clientY));
      g.addEventListener('pointerleave', hideTip);
      g.addEventListener('focus', () => { const r = g.getBoundingClientRect(); showTip(layerTip(l), r.left + r.width / 2, r.top); });
      g.addEventListener('blur', hideTip);
      g.addEventListener('click', () => this.opts.onLayer && this.opts.onLayer(l.key));
      g.addEventListener('keydown', e => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); this.opts.onLayer && this.opts.onLayer(l.key); } });
      svg.appendChild(g);
      x += l.tokens * sc;
    }

    // Row B: billing.
    const parts = [['read', rep.read, 'read from cache'], ['write', rep.write, 'written to cache'], ['fresh', rep.fresh, 'processed uncached']];
    x = 0;
    for (const [cls, n, label] of parts) {
      if (n <= 0) continue;
      const w = Math.max(n * sc, 3);
      const g = s('g', { class: 'seg' });
      g.appendChild(s('rect', { class: 'bill ' + cls, x, y: yB, width: Math.max(w - 2, 1), height: hB, rx: 3 }));
      const long = `${label.split(' ')[0]} ${tok(n)} \u00b7 ${pct(n / total)}`, short = tok(n);
      if (w - 2 >= textW(long, true) + 16) g.appendChild(s('text', { class: 'in', x: x + 8, y: yB + hB / 2 + 4 }, long));
      else if (w - 2 >= textW(short, true) + 12) g.appendChild(s('text', { class: 'in', x: x + 6, y: yB + hB / 2 + 4 }, short));
      g.addEventListener('pointermove', e => showTip(h('div', null, h('b', null, tok(n) + ' tokens'), ' ' + label + ' (' + pct(n / total) + ')'), e.clientX, e.clientY));
      g.addEventListener('pointerleave', hideTip);
      g.appendChild(s('title', null, `${n} tokens ${label}`));
      svg.appendChild(g);
      x += n * sc;
    }

    // Guard expectation: where the harness expected the cached prefix to end.
    let expectBelow = false;
    if (rep.expected > 0 && rep.req.done) {
      const ex = Math.min(rep.expected * sc, W - 2);
      const label = `guard expected ${tok(rep.expected)}`;
      expectBelow = ex < 300 && !compact;
      svg.appendChild(s('line', { class: 'expect', x1: ex, x2: ex, y1: yA - 4, y2: yB + hB + 6 }));
      const right = ex > W - 130;
      svg.appendChild(s('text', { class: 'expect-label', x: right ? ex - 5 : ex + 5, y: expectBelow ? yB + hB + 34 : 12, 'text-anchor': right ? 'end' : 'start' }, label));
    }

    // Token axis.
    const yAx = yB + hB + 8;
    if (!compact) {
      svg.appendChild(s('line', { class: 'axis', x1: 0, x2: W - 2, y1: yAx, y2: yAx }));
      const step = niceStep(total / 6);
      for (let v = 0; v <= total; v += step) {
        const px = Math.min(v * sc, W - 2);
        svg.appendChild(s('line', { class: 'axis', x1: px, x2: px, y1: yAx, y2: yAx + 4 }));
        svg.appendChild(s('text', { x: px, y: yAx + 15, 'text-anchor': v === 0 ? 'start' : 'middle' }, tok(v)));
      }
    }
    this.box.appendChild(svg);
  }
}

/** Rough text width at the chart's 11px font: enough to decide whether a label fits its segment. */
function textW(str, bold) { return str.length * (bold ? 6.6 : 6.1); }

function niceStep(raw) {
  const p = Math.pow(10, Math.floor(Math.log10(Math.max(raw, 1))));
  const f = raw / p;
  return (f <= 1 ? 1 : f <= 2 ? 2 : f <= 5 ? 5 : 10) * p;
}

function layerTip(l) {
  const rows = [h('div', { class: 'h' }, h('b', null, l.key + ' ' + l.title), h('span', { class: 'muted' }, tok(l.tokens) + ' tokens'))];
  const r = (k, v) => rows.push(h('div', { class: 'r' }, h('span', { class: 'k' }, k), h('span', { class: 'v' }, v)));
  r('state', stateLabel(l.state));
  if (l.read || l.write || l.fresh) {
    r('read from cache', tok(l.read));
    if (l.write) r('written', tok(l.write));
    r('uncached', tok(l.fresh));
  }
  r('size source', { recorded: 'recorded estimate', blob: 'from blob size', derived: 'remainder of provider total', unknown: 'unknown' }[l.source] || l.source);
  if (l.hash) r('hash', l.hash);
  if (STATE_HELP[l.state]) rows.push(h('div', { class: 'flag' }, STATE_HELP[l.state]));
  return h('div', null, ...rows);
}

/** The per-layer table below the bars. */
export function layerTable(rep, prevRow, onLayer, sel) {
  const total = Math.max(rep.prompt, 1);
  const rows = rep.layers.filter(l => l.state !== 'absent' || l.key === 'G2').map(l => {
    const gi = +l.key.slice(1);
    const delta = prevRow && l.key !== 'G6' ? l.tokens - prevRow.layers[gi] : null;
    return h('tr', { class: 'clickable' + (sel === l.key ? ' sel' : ''), on: { click: () => onLayer && onLayer(l.key) } },
      h('td', null, h('span', { class: 'sw g' + gi }), h('b', null, l.key), ' ', l.title),
      h('td', { class: 'r' }, l.state === 'absent' ? '–' : tok(l.tokens), delta ? h('div', { class: 'sub' }, (delta > 0 ? '+' : '') + tok(delta)) : null),
      h('td', { class: 'r' }, l.state === 'absent' ? '–' : pct(l.tokens / total)),
      h('td', { title: STATE_HELP[l.state] || '' }, h('span', { class: 'state ' + l.state }, stateLabel(l.state), l.breakpoint ? ' ◆' : '')),
      h('td', null, l.state === 'absent' || !(l.read + l.write + l.fresh) ? '' : billBar(l)),
      h('td', { class: 'mono muted' }, l.hash || ''));
  });
  return h('div', { class: 'tblwrap' }, h('table', { class: 'tbl lt' },
    h('thead', null, h('tr', null, h('th', null, 'Layer'), h('th', { class: 'r' }, 'Tokens'), h('th', { class: 'r' }, 'Share'),
      h('th', null, 'Since previous request'), h('th', null, 'Provider billed'), h('th', null, 'Hash / turns'))),
    h('tbody', null, ...rows)));
}

function billBar(l) {
  const t = Math.max(l.tokens, 1);
  const seg = (cls, n) => n > 0 ? h('i', { class: cls, css: { flexGrow: String(n / t), flexBasis: '0' }, title: `${cls}: ${tok(n)}` }) : null;
  return h('div', { class: 'bar', role: 'img', 'aria-label': `read ${l.read}, written ${l.write}, uncached ${l.fresh} tokens` }, seg('read', l.read), seg('write', l.write), seg('fresh', l.fresh));
}

/** Legend for the layer bar, with the hatch meaning spelled out. */
export function stackLegend() {
  const it = (cls, text) => h('span', { class: 'it' }, h('span', { class: 'sw ' + cls }), text);
  return h('div', { class: 'legend' },
    it('read', 'read from cache'), it('write', 'written to cache'), it('fresh', 'processed uncached'),
    h('span', { class: 'it' }, h('span', { class: 'sw hatch' }), 'rewritten or new (dense hatch)'),
    h('span', { class: 'it' }, h('span', { class: 'sw hatch light' }), 'invalidated behind a change (light hatch)'),
    h('span', { class: 'it' }, h('span', { class: 'sw diamond' }), 'cache breakpoint'),
    h('span', { class: 'it' }, h('span', { class: 'sw line', css: { background: 'var(--ink)' } }), 'guard’s expected cached prefix'));
}
