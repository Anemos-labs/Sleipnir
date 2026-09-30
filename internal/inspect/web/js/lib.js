// Small DOM and formatting helpers.
//
// Every string that comes from a log (agent ids, tool names, notes, mail, hashes)
// is untrusted. The rule of this UI: text reaches the page only through
// textContent / createTextNode, and nothing here can build markup from a string.
// There is deliberately no way to build markup from a string here, no inline style
// attribute and no inline event handler.

const SVG_NS = 'http://www.w3.org/2000/svg';

function apply(node, props, ns) {
  if (props == null) return;
  for (const [k, v] of Object.entries(props)) {
    if (v == null || v === false) continue;
    if (k === 'class') node.setAttribute('class', v);
    else if (k === 'text') node.textContent = v;
    else if (k === 'on') for (const [ev, fn] of Object.entries(v)) node.addEventListener(ev, fn);
    else if (k === 'dataset') Object.assign(node.dataset, v);
    else if (k === 'css') Object.assign(node.style, v); // CSSOM assignment is not an inline style attribute
    else if (k === 'style') throw new Error('use css:{...}, not style=');
    else if (/^(inner|outer)HTML$|^on/i.test(k)) throw new Error('markup and inline handlers are not allowed: ' + k);
    else node.setAttribute(k, v === true ? '' : String(v));
  }
}

function append(node, kids) {
  for (const c of kids) {
    if (c == null || c === false) continue;
    if (Array.isArray(c)) append(node, c);
    else if (c instanceof Node) node.appendChild(c);
    else node.appendChild(document.createTextNode(String(c)));
  }
}

function isProps(x) {
  return x != null && typeof x === 'object' && !Array.isArray(x) && !(x instanceof Node);
}

/** h('div', {class:'x', on:{click:fn}}, 'text', child, ...) builds an element. */
export function h(tag, props, ...kids) {
  const node = document.createElement(tag);
  if (isProps(props)) apply(node, props); else kids.unshift(props);
  append(node, kids);
  return node;
}

/** s(...) builds an SVG element. */
export function s(tag, props, ...kids) {
  const node = document.createElementNS(SVG_NS, tag);
  if (isProps(props)) apply(node, props); else kids.unshift(props);
  append(node, kids);
  return node;
}

export function clear(node) { while (node.firstChild) node.removeChild(node.firstChild); return node; }
export function mount(node, ...kids) { clear(node); append(node, kids); return node; }

// ---- formatting ---------------------------------------------------------------------

export function tok(n) {
  if (n == null || isNaN(n)) return '–';
  const a = Math.abs(n);
  if (a < 1000) return String(Math.round(n));
  if (a < 100000) return (n / 1000).toFixed(a < 10000 ? 2 : 1).replace(/\.?0+$/, '') + 'k';
  if (a < 1e6) return Math.round(n / 1000) + 'k';
  return (n / 1e6).toFixed(2).replace(/\.?0+$/, '') + 'M';
}

export function int(n) { return n == null ? '–' : Math.round(n).toLocaleString('en-US'); }

export function usd(v) {
  if (v == null || isNaN(v)) return '–';
  const a = Math.abs(v);
  if (a === 0) return '$0';
  if (a >= 100) return '$' + v.toFixed(0);
  if (a >= 1) return '$' + v.toFixed(2);
  if (a >= 0.01) return '$' + v.toFixed(3);
  return '$' + v.toFixed(4);
}

export function pct(x, digits) {
  if (x == null || isNaN(x)) return '–';
  const v = x * 100;
  const d = digits != null ? digits : (Math.abs(v) >= 10 || v === 0 ? 0 : 1);
  return v.toFixed(d) + '%';
}

export function dur(ms) {
  if (ms == null || isNaN(ms)) return '–';
  if (ms < 1000) return Math.round(ms) + ' ms';
  const s = ms / 1000;
  if (s < 60) return s.toFixed(s < 10 ? 1 : 0) + ' s';
  const m = Math.floor(s / 60), r = Math.round(s % 60);
  if (m < 60) return m + 'm ' + String(r).padStart(2, '0') + 's';
  const hh = Math.floor(m / 60);
  return hh + 'h ' + String(m % 60).padStart(2, '0') + 'm';
}

/** Elapsed time as m:ss (or h:mm:ss) for axes. */
export function clock(ms) {
  const s = Math.max(0, Math.round(ms / 1000));
  const m = Math.floor(s / 60), r = s % 60;
  if (m < 60) return m + ':' + String(r).padStart(2, '0');
  return Math.floor(m / 60) + ':' + String(m % 60).padStart(2, '0') + ':' + String(r).padStart(2, '0');
}

export function timeOfDay(ts) {
  const d = new Date(ts);
  if (isNaN(d)) return '–';
  return d.toLocaleTimeString([], { hour12: false });
}

export function ago(ts, now = Date.now()) {
  const ms = now - new Date(ts).getTime();
  if (isNaN(ms)) return '–';
  if (ms < 5000) return 'just now';
  return dur(ms) + ' ago';
}

export function clamp(x, lo, hi) { return Math.max(lo, Math.min(hi, x)); }

/** Scrolls only `box` so that `el` shows inside it; unlike scrollIntoView it never moves the page. */
export function revealIn(box, el) {
  const b = box.getBoundingClientRect(), r = el.getBoundingClientRect();
  if (r.top < b.top) box.scrollTop -= b.top - r.top;
  else if (r.bottom > b.bottom) box.scrollTop += r.bottom - b.bottom;
}

/** Nice axis ticks: about `n` round values covering 0..max. */
export function niceTicks(max, n = 4) {
  if (!(max > 0)) return [0, 1];
  const raw = max / n;
  const p = Math.pow(10, Math.floor(Math.log10(raw)));
  const f = raw / p;
  const step = (f <= 1 ? 1 : f <= 2 ? 2 : f <= 5 ? 5 : 10) * p;
  const out = [];
  for (let v = 0; v <= max + step * 0.999; v += step) out.push(+v.toFixed(10));
  return out;
}

// ---- storage (per-viewer conveniences only; every access may throw) -------------------

export const store = {
  get(k, d) { try { const v = localStorage.getItem('sli.' + k); return v == null ? d : JSON.parse(v); } catch { return d; } },
  set(k, v) { try { localStorage.setItem('sli.' + k, JSON.stringify(v)); } catch { /* private mode */ } },
};

/** Reads a CSS custom property from the root, for canvas drawing. */
export function cssVar(name) {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
}

// ---- hash routing -----------------------------------------------------------------------

/** Parses "#/view?a=1&b=2" into {view, q}. */
export function parseHash(hash = location.hash) {
  const raw = hash.replace(/^#\/?/, '');
  const i = raw.indexOf('?');
  const view = decodeURIComponent(i < 0 ? raw : raw.slice(0, i));
  const q = new URLSearchParams(i < 0 ? '' : raw.slice(i + 1));
  return { view, q };
}

export function buildHash(view, q) {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(q || {})) if (v != null && v !== '') p.set(k, v);
  const qs = p.toString();
  return '#/' + encodeURIComponent(view) + (qs ? '?' + qs : '');
}

// ---- tooltip -------------------------------------------------------------------------------

const tipEl = () => document.getElementById('tip');

/** Shows the shared tooltip near a client point; content is a Node. */
export function showTip(node, x, y) {
  const t = tipEl();
  mount(t, node);
  t.hidden = false;
  const r = t.getBoundingClientRect();
  const vw = document.documentElement.clientWidth, vh = document.documentElement.clientHeight;
  let left = x + 14, top = y + 14;
  if (left + r.width > vw - 8) left = x - r.width - 14;
  if (top + r.height > vh - 8) top = y - r.height - 14;
  t.style.left = Math.max(8, left) + 'px';
  t.style.top = Math.max(8, top) + 'px';
}

export function hideTip() { tipEl().hidden = true; }
