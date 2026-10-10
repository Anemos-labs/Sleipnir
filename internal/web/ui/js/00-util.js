/* 00-util.js: helpers shared by every module (SL.u). No state, no DOM side effects. */
(function (SL) {
  'use strict';
  const NSV = 'http://www.w3.org/2000/svg';
  const $ = (s, r) => (r || document).querySelector(s);
  const $$ = (s, r) => Array.prototype.slice.call((r || document).querySelectorAll(s));
  /** HTML-escape a value: everything that came from a tool, a file, a mail or the model is data and goes through here. */
  const esc = s => String(s == null ? '' : s).replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
  const fmtK = n => n >= 1000 ? (n / 1000).toFixed(1).replace(/\.0$/, '') + 'k' : String(Math.round(n));
  const fmtN = n => Math.round(n).toLocaleString('en-US');
  const fmtUsd = (n, d) => '$' + n.toFixed(d == null ? (n < .1 ? 4 : 2) : d);
  const fmtMs = ms => ms < 1000 ? Math.round(ms) + 'ms' : (ms / 1000).toFixed(1) + 's';
  const pct = (a, b) => b > 0 ? Math.round(100 * a / b) : 0;
  /** m:ss from seconds, rounding up (a countdown shows 0:25 for 24.3 s). */
  const clock = s => { s = Math.max(0, Math.ceil(s - 1e-6)); return Math.floor(s / 60) + ':' + String(s % 60).padStart(2, '0'); };
  /** mm:ss elapsed. */
  const mmss = s => { s = Math.max(0, Math.floor(s)); return String(Math.floor(s / 60)).padStart(2, '0') + ':' + String(s % 60).padStart(2, '0'); };
  /** hh:mm:ss time of day for a session second `t`, given the session's start in seconds since midnight. */
  const tod = (t0, t) => { const s = Math.floor(t0 + t); return String(Math.floor(s / 3600) % 24).padStart(2, '0') + ':' + String(Math.floor(s / 60) % 60).padStart(2, '0') + ':' + String(s % 60).padStart(2, '0'); };
  const dur = s => s < 90 ? Math.round(s) + 's' : s < 5400 ? Math.floor(s / 60) + 'm ' + String(Math.round(s % 60)).padStart(2, '0') + 's' : Math.floor(s / 3600) + 'h ' + String(Math.floor(s / 60) % 60).padStart(2, '0') + 'm';
  const clamp = (x, a, b) => x < a ? a : x > b ? b : x;
  const hitCls = p => p >= 90 ? 'hit-hi' : p >= 70 ? 'hit-mid' : 'hit-lo';
  /** Create an element: mk('div', {class:'x'}, '<b>html</b>'). */
  const mk = (tag, attrs, html) => { const e = document.createElement(tag); for (const k in (attrs || {})) e.setAttribute(k, attrs[k]); if (html != null) e.innerHTML = html; return e; };
  /** Create an SVG element under `parent`. */
  const sv = (tag, attrs, parent, text) => { const e = document.createElementNS(NSV, tag); for (const k in (attrs || {})) e.setAttribute(k, attrs[k]); if (text != null) e.textContent = text; if (parent) parent.appendChild(e); return e; };
  /** html string -> first element (used by the transcript to build rows). */
  const frag = html => { const t = document.createElement('template'); t.innerHTML = html.trim(); return t.content.firstElementChild; };
  /** Deterministic PRNG (mulberry32): the same seed always gives the same sequence. */
  const rng = seed => { let a = seed >>> 0; return () => { a = (a + 0x6D2B79F5) >>> 0; let t = a; t = Math.imul(t ^ (t >>> 15), t | 1); t ^= t + Math.imul(t ^ (t >>> 7), t | 61); return ((t ^ (t >>> 14)) >>> 0) / 4294967296; }; };
  const hash = s => { let h = 2166136261; for (let i = 0; i < s.length; i++) { h ^= s.charCodeAt(i); h = Math.imul(h, 16777619); } return h >>> 0; };
  let _uid = 0;
  const uid = p => (p || 'u') + (++_uid).toString(36);
  const deepCopy = o => o == null ? o : JSON.parse(JSON.stringify(o));
  /** Binary search: first index whose item.t > t (items sorted by t). */
  const upperBound = (arr, t) => { let lo = 0, hi = arr.length; while (lo < hi) { const m = (lo + hi) >> 1; if (arr[m].t <= t) lo = m + 1; else hi = m; } return lo; };
  /** Pub/sub used inside the engine. Returns an unsubscribe function. */
  const makeBus = () => { const subs = {}; return { on(topic, fn) { (subs[topic] = subs[topic] || new Set()).add(fn); return () => subs[topic] && subs[topic].delete(fn); }, emit(topic, a, b) { const s = subs[topic]; if (s) Array.from(s).forEach(fn => { try { fn(a, b); } catch (e) { console.error(e); } }); }, count() { return Object.keys(subs).reduce((n, k) => n + subs[k].size, 0); } }; };
  /** Copy text to the clipboard; the promise rejection is swallowed (the page may not be allowed). */
  const copy = txt => { try { return navigator.clipboard.writeText(txt).then(() => true, () => false); } catch (e) { return Promise.resolve(false); } };
  /** The string with its first letter in upper case. */
  const ucfirst = s => s ? s[0].toUpperCase() + s.slice(1) : s;
  /**
   * Ids the server names (agents, tasks, roles) become object keys, data-* attributes, CSS variable names and words of markup. A plain
   * id is 1 to 64 letters, digits, `.`, `_`, `:` or `-` and is not a name every object already has (`constructor`); anything else is
   * BAD_ID, one placeholder that is safe everywhere. okId tells, safeId replaces (an absent value stays absent).
   */
  const ID_RE = /^[A-Za-z0-9._:-]{1,64}$/, BAD_ID = 'invalid-id';
  const okId = x => typeof x === 'string' && ID_RE.test(x) && !(x in Object.prototype);
  const safeId = x => x == null || x === '' ? x : okId(x) ? x : BAD_ID;
  /** The value of key k of a lookup table only when the table itself has it (never what every object inherits). */
  const own = (o, k) => o != null && Object.prototype.hasOwnProperty.call(o, k) ? o[k] : undefined;
  /** The first line of s, at most n characters, with … when anything was left out: for a toast or a one-line summary. */
  const clip1 = (s, n) => { s = String(s == null ? '' : s); const nl = s.indexOf('\n'), one = nl >= 0 ? s.slice(0, nl) : s; return one.length > n ? one.slice(0, n) + '…' : one + (nl >= 0 ? ' …' : ''); };
  /** Colour token of an agent: its own shade when the tokens define one (sc-1, be-2 ...), else its role colour. Only a plain id (letters,
   *  digits, `_` and `-`) is spliced into a variable name; any other value is the dim colour. */
  const agCol = id => id === 'you' ? 'var(--fg)' : id === 'mgr' ? 'var(--c-mgr)' : typeof id === 'string' && /^[A-Za-z0-9_-]{1,64}$/.test(id) ? 'var(--a-' + id + ',var(--c-' + id.split('-')[0] + ',var(--dim)))' : 'var(--dim)';
  /** Is this key event part of an input method's composition (a CJK candidate being chosen)? Its Enter picks a candidate: it never
   *  sends, submits or runs a shortcut. */
  const composing = e => !!e && (e.isComposing === true || e.keyCode === 229);
  SL.u = { composing, agCol, okId, safeId, BAD_ID, own, clip1, NSV, $, $$, esc, fmtK, fmtN, fmtUsd, fmtMs, pct, clock, mmss, tod, dur, clamp, hitCls, mk, sv, frag, rng, hash, uid, deepCopy, upperBound, makeBus, copy, ucfirst };
  SL.bus = makeBus();
})(SL);
