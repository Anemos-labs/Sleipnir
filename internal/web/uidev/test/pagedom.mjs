// pagedom.mjs: the shipped page, booted on a small fake DOM, for the tests that press keys in it (keys-parity.test.mjs, ctrl-d.test.mjs,
// help-keys.test.mjs). index.html and every script of it run in order (no DOM library: the page's own key handlers, the composer's, the
// session tabs', the palette's and the views' all run) against a fake fetch and a fake event stream. Nothing waits for time: the page's
// timers and clock are moved by the test.
import fs from 'node:fs';
import path from 'node:path';
import vm from 'node:vm';
import { webcrypto } from 'node:crypto';
import { fileURLToPath } from 'node:url';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const UI = path.resolve(HERE, '..', '..', 'ui');

/** The keydown event a canonical key stands for, as a browser makes it. */
export function eventFor(spec) {
  let rest = spec, ctrl = false, alt = false, shift = false;
  for (;;) {
    if (rest.startsWith('ctrl+') && rest.length > 5) { ctrl = true; rest = rest.slice(5); continue; }
    if (rest.startsWith('alt+') && rest.length > 4) { alt = true; rest = rest.slice(4); continue; }
    if (rest.startsWith('shift+') && rest.length > 6) { shift = true; rest = rest.slice(6); continue; }
    break;
  }
  const NAME = { esc: 'Escape', enter: 'Enter', tab: 'Tab', space: ' ', backspace: 'Backspace', delete: 'Delete', insert: 'Insert', up: 'ArrowUp', down: 'ArrowDown', left: 'ArrowLeft', right: 'ArrowRight', home: 'Home', end: 'End', pgup: 'PageUp', pgdn: 'PageDown' };
  let key, code = '';
  if (NAME[rest]) { key = NAME[rest]; if (rest === 'space') code = 'Space'; }
  else if (/^f([1-9]|1[0-2])$/.test(rest)) key = rest.toUpperCase();
  else {
    key = rest;
    if (/^[a-z]$/.test(rest)) { code = 'Key' + rest.toUpperCase(); if (shift) key = rest.toUpperCase(); }
    else if (/^[0-9]$/.test(rest)) code = 'Digit' + rest;
    else if ('~!@#$%^&*()_+{}|:"<>?'.includes(rest)) shift = true; // a character a keyboard types with shift
  }
  return { key, code, ctrlKey: ctrl, altKey: alt, shiftKey: shift, metaKey: false };
}
/** Whether a key is a character that goes into a text box when nothing else claims it. */
export const typesText = spec => spec.length === 1 || spec === 'space' || /^shift\+[a-z]$/.test(spec);

// ---------------------------------------------------------------------------------------------------------------------------
// A DOM small enough to read, large enough for the page to mount on: elements, text, the selectors the page uses (a selector it
// does not understand throws, so a new one is noticed), events with capture and bubble, focus, and a log of what changed.

const VOID = new Set(['area', 'base', 'br', 'col', 'embed', 'hr', 'img', 'input', 'link', 'meta', 'source', 'track', 'wbr']);
const ENT = { amp: '&', lt: '<', gt: '>', quot: '"', apos: "'", nbsp: '\u00a0' };
const unent = s => s.replace(/&(#x[0-9a-f]+|#\d+|[a-z]+);/gi, (m, e) => e[0] === '#' ? String.fromCodePoint(e[1] === 'x' || e[1] === 'X' ? parseInt(e.slice(2), 16) : parseInt(e.slice(1), 10)) : (e.toLowerCase() in ENT ? ENT[e.toLowerCase()] : m));
const escText = s => String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
const escAttr = s => escText(s).replace(/"/g, '&quot;');
const kebab = k => k.replace(/[A-Z]/g, c => '-' + c.toLowerCase());
/** What changed on connected elements since the log was cleared: one line per change. */
export const LOG = { rows: [], errors: [] };
const label = el => el.localName + (el.id ? '#' + el.id : '') + (el._attrs.get('class') ? '.' + el._attrs.get('class').split(/\s+/)[0] : '');

function parseSelector(sel) {
  const lists = []; let i = 0; const s = String(sel).trim();
  const fail = () => { throw new Error('the fake DOM does not understand the selector ' + JSON.stringify(sel)); };
  const ident = () => { const m = /^[\w-]+/.exec(s.slice(i)); if (!m) fail(); i += m[0].length; return m[0]; };
  function compound() {
    const c = { tag: null, id: null, cls: [], attrs: [], not: [] }; let any = false;
    if (s[i] === '*') { i++; any = true; } else if (/\w/.test(s[i] || '')) { c.tag = ident().toLowerCase(); any = true; }
    for (;;) {
      const ch = s[i];
      if (ch === '#') { i++; c.id = ident(); any = true; } else if (ch === '.') { i++; c.cls.push(ident()); any = true; }
      else if (ch === '[') {
        i++; const name = ident(); let op = null, val = null;
        if (s[i] !== ']') {
          const m = /^(\^=|\$=|\*=|=)/.exec(s.slice(i)); if (!m) fail(); op = m[1]; i += op.length;
          if (s[i] === '"' || s[i] === "'") { const q = s[i++], e = s.indexOf(q, i); if (e < 0) fail(); val = s.slice(i, e); i = e + 1; } else { const m2 = /^[^\]\s]+/.exec(s.slice(i)); if (!m2) fail(); val = m2[0]; i += val.length; }
        }
        if (s[i] !== ']') fail(); i++; c.attrs.push({ name, op, val }); any = true;
      } else if (ch === ':') {
        i++; if (ident() !== 'not' || s[i] !== '(') fail(); i++; c.not.push(compound()); if (s[i] !== ')') fail(); i++; any = true;
      } else break;
    }
    if (!any) fail();
    return c;
  }
  function complex() {
    const parts = [compound()], combs = [];
    for (;;) {
      let sp = false; while (s[i] === ' ') { i++; sp = true; }
      if (i >= s.length || s[i] === ',') break;
      if (s[i] === '>') { i++; while (s[i] === ' ') i++; combs.push('>'); } else if (sp) combs.push(' '); else fail();
      parts.push(compound());
    }
    return { parts, combs };
  }
  for (;;) { while (s[i] === ' ') i++; lists.push(complex()); if (s[i] === ',') { i++; continue; } break; }
  if (i < s.length) fail();
  return lists;
}
function matchCompound(el, c) {
  if (c.tag && el.localName !== c.tag) return false;
  if (c.id && el.id !== c.id) return false;
  for (const k of c.cls) if (!el.classList.contains(k)) return false;
  for (const a of c.attrs) {
    const v = el.getAttribute(a.name); if (v == null) return false;
    if ((a.op === '=' && v !== a.val) || (a.op === '^=' && !v.startsWith(a.val)) || (a.op === '$=' && !v.endsWith(a.val)) || (a.op === '*=' && !v.includes(a.val))) return false;
  }
  return !c.not.some(n => matchCompound(el, n));
}
function matchComplex(el, cx, k = cx.parts.length - 1) {
  if (!matchCompound(el, cx.parts[k])) return false;
  if (k === 0) return true;
  if (cx.combs[k - 1] === '>') return !!el.parentElement && matchComplex(el.parentElement, cx, k - 1);
  for (let p = el.parentElement; p; p = p.parentElement) if (matchComplex(p, cx, k - 1)) return true;
  return false;
}
const SELS = new Map();
const compile = sel => { let v = SELS.get(sel); if (!v) { v = parseSelector(sel); SELS.set(sel, v); } return v; };
const matches = (el, sel) => compile(sel).some(cx => matchComplex(el, cx));

export class FakeEvent {
  constructor(type, init) { this.type = type; this.bubbles = false; this.cancelable = true; Object.assign(this, init || {}); this.defaultPrevented = false; this.target = null; this._stop = false; this._now = false; }
  preventDefault() { if (this.cancelable) this.defaultPrevented = true; }
  stopPropagation() { this._stop = true; }
  stopImmediatePropagation() { this._stop = true; this._now = true; }
}
class Target {
  constructor() { this._ls = []; }
  addEventListener(type, fn, opts) { const capture = typeof opts === 'boolean' ? opts : !!(opts && opts.capture); if (!this._ls.some(l => l.type === type && l.fn === fn && l.capture === capture)) this._ls.push({ type, fn, capture }); }
  removeEventListener(type, fn, opts) { const capture = typeof opts === 'boolean' ? opts : !!(opts && opts.capture); this._ls = this._ls.filter(l => !(l.type === type && l.fn === fn && l.capture === capture)); }
  _fire(ev, phase) {
    for (const l of this._ls.slice()) {
      if (l.type !== ev.type || (phase === 'capture' && !l.capture) || (phase === 'bubble' && l.capture)) continue;
      ev.currentTarget = this;
      try { l.fn.call(this, ev); } catch (err) { LOG.errors.push(String(err && err.stack || err).split('\n').slice(0, 3).join(' | ')); } // a browser reports a handler's error and goes on
      if (ev._now) return;
    }
  }
  dispatchEvent(ev) {
    ev.target = ev.target || this; const path = []; for (let n = this; n; n = n._up()) path.push(n);
    for (let i = path.length - 1; i >= 0 && !ev._stop; i--) path[i]._fire(ev, i === 0 ? 'target' : 'capture');
    if (ev.bubbles) for (let i = 1; i < path.length && !ev._stop; i++) path[i]._fire(ev, 'bubble');
    return !ev.defaultPrevented;
  }
  _up() { return null; }
}
class El extends Target {
  constructor(doc, tag, ns) {
    super(); this.ownerDocument = doc; this.localName = String(tag).toLowerCase(); this.namespaceURI = ns || null; this.nodeType = 1; this.tagName = ns ? String(tag) : String(tag).toUpperCase();
    this.parentNode = null; this.childNodes = []; this._attrs = new Map(); this._value = ''; this._sel = [0, 0]; const self = this; const styles = {};
    this.style = new Proxy({ setProperty(k, v) { self.style[k] = v; }, removeProperty(k) { const v = styles[k]; delete styles[k]; return v || ''; }, getPropertyValue: k => styles[k] || '' }, {
      get: (t, k) => k in t ? t[k] : (typeof k === 'string' ? styles[k] || '' : undefined),
      set: (t, k, v) => { if (styles[k] !== String(v)) { styles[k] = String(v); self._log('style ' + String(k)); } return true; },
    });
    this.classList = { add: (...c) => self._cls(s => c.forEach(x => s.add(x))), remove: (...c) => self._cls(s => c.forEach(x => s.delete(x))), contains: c => self._classes().has(c), toggle(c, force) { let on; self._cls(s => { on = force === undefined ? !s.has(c) : !!force; if (on) s.add(c); else s.delete(c); }); return on; } };
    this.dataset = new Proxy({}, { get: (_, k) => typeof k === 'string' && self._attrs.has('data-' + kebab(k)) ? self._attrs.get('data-' + kebab(k)) : undefined, set: (_, k, v) => { self.setAttribute('data-' + kebab(String(k)), v); return true; }, has: (_, k) => self._attrs.has('data-' + kebab(String(k))) });
    if (this.localName === 'template') this.content = new Frag(doc);
  }
  _up() { return this.parentNode; }
  _log(what) { if (this.isConnected) LOG.rows.push(label(this) + ' ' + what); }
  _classes() { return new Set((this._attrs.get('class') || '').split(/\s+/).filter(Boolean)); }
  _cls(fn) { const s = this._classes(), before = Array.from(s).join(' '); fn(s); const after = Array.from(s).join(' '); if (before !== after) { if (after) this._attrs.set('class', after); else this._attrs.delete('class'); this._log('class'); } }
  _bool(n, v) { if (v) this.setAttribute(n, ''); else this.removeAttribute(n); }
  get className() { return this._attrs.get('class') || ''; } set className(v) { this.setAttribute('class', v); }
  get id() { return this._attrs.get('id') || ''; } set id(v) { this.setAttribute('id', v); }
  get hidden() { return this._attrs.has('hidden'); } set hidden(v) { this._bool('hidden', v); }
  get disabled() { return this._attrs.has('disabled'); } set disabled(v) { this._bool('disabled', v); }
  get title() { return this._attrs.get('title') || ''; } set title(v) { this.setAttribute('title', v); }
  get type() { return this._attrs.get('type') || (this.localName === 'input' ? 'text' : this.localName === 'button' ? 'submit' : ''); } set type(v) { this.setAttribute('type', v); }
  get checked() { return !!this._checked; } set checked(v) { this._checked = !!v; }
  get isContentEditable() { return this._attrs.get('contenteditable') === 'true' || this._attrs.get('contenteditable') === ''; }
  get isConnected() { for (let n = this; n; n = n.parentNode) if (n === this.ownerDocument) return true; return false; }
  get value() { return this.localName === 'input' || this.localName === 'textarea' || this.localName === 'select' ? this._value : (this._attrs.get('value') || ''); }
  set value(v) { v = String(v == null ? '' : v); if (v !== this._value) { this._value = v; this._log('value'); } this._sel = [v.length, v.length]; }
  get selectionStart() { return Math.min(this._sel[0], this._value.length); } set selectionStart(v) { this._sel[0] = v; }
  get selectionEnd() { return Math.min(this._sel[1], this._value.length); } set selectionEnd(v) { this._sel[1] = v; }
  setSelectionRange(a, b) { this._sel = [a, b]; }
  select() { this._sel = [0, this._value.length]; }
  setRangeText(text, start, end, mode) { const s = start == null ? this.selectionStart : start, e = end == null ? this.selectionEnd : end; this.value = this._value.slice(0, s) + text + this._value.slice(e); const p = s + text.length; this._sel = mode === 'select' ? [s, p] : [p, p]; }
  get parentElement() { return this.parentNode && this.parentNode.nodeType === 1 ? this.parentNode : null; }
  get children() { return this.childNodes.filter(n => n.nodeType === 1); }
  get firstElementChild() { return this.children[0] || null; }
  get firstChild() { return this.childNodes[0] || null; }
  get nextSibling() { const p = this.parentNode; return p ? p.childNodes[p.childNodes.indexOf(this) + 1] || null : null; }
  get nextElementSibling() { let n = this.nextSibling; while (n && n.nodeType !== 1) n = n.nextSibling; return n; }
  get previousElementSibling() { const p = this.parentNode; if (!p) return null; let i = p.childNodes.indexOf(this) - 1; while (i >= 0 && p.childNodes[i].nodeType !== 1) i--; return i >= 0 ? p.childNodes[i] : null; }
  append(...ns) { ns.forEach(n => this.appendChild(typeof n === 'string' ? this.ownerDocument.createTextNode(n) : n)); }
  after(...ns) { const p = this.parentNode, nx = this.nextSibling; if (p) ns.forEach(n => p.insertBefore(typeof n === 'string' ? this.ownerDocument.createTextNode(n) : n, nx)); }
  insertAdjacentHTML(pos, html) {
    const t = this.ownerDocument.createElement('template'); t.innerHTML = html; const ns = t.content.childNodes.slice(), p = this.parentNode;
    if (pos === 'beforeend') ns.forEach(n => this.appendChild(n)); else if (pos === 'afterbegin') ns.reverse().forEach(n => this.insertBefore(n, this.firstChild)); else if (pos === 'beforebegin') ns.forEach(n => p.insertBefore(n, this)); else if (pos === 'afterend') { const nx = this.nextSibling; ns.forEach(n => p.insertBefore(n, nx)); }
  }
  get offsetWidth() { return this.getBoundingClientRect().width; } get clientWidth() { return this.offsetWidth; } get clientHeight() { return this.getBoundingClientRect().height; }
  getTotalLength() { return 0; }
  get textContent() { return this.childNodes.map(n => n.nodeType === 3 ? n.data : n.textContent).join(''); }
  set textContent(v) { if (this.textContent !== String(v == null ? '' : v)) this._log('text'); this._clear(true); if (v !== '' && v != null) this.appendChild(this.ownerDocument.createTextNode(String(v)), true); }
  get innerHTML() { return this.childNodes.map(n => n.nodeType === 3 ? escText(n.data) : n.outerHTML).join(''); }
  set innerHTML(html) { const t = this.localName === 'template' ? this.content : this; this._log('html'); t._clear(true); parseInto(this.ownerDocument, t, String(html)); }
  get outerHTML() { const a = Array.from(this._attrs).map(([k, v]) => ' ' + k + (v === '' ? '' : '="' + escAttr(v) + '"')).join(''); return '<' + this.localName + a + '>' + (VOID.has(this.localName) ? '' : this.innerHTML + '</' + this.localName + '>'); }
  _clear(quiet) { if (!quiet && this.childNodes.length) this._log('children'); this.childNodes.forEach(n => { n.parentNode = null; }); this.childNodes = []; }
  appendChild(n, quiet) { return this.insertBefore(n, null, quiet); }
  insertBefore(n, ref, quiet) {
    if (n instanceof Frag) { n.childNodes.slice().forEach(c => this.insertBefore(c, ref, quiet)); return n; }
    if (n.parentNode) n.parentNode.removeChild(n);
    n.parentNode = this; const i = ref ? this.childNodes.indexOf(ref) : -1; if (i < 0) this.childNodes.push(n); else this.childNodes.splice(i, 0, n);
    if (!quiet) this._log('children'); return n;
  }
  removeChild(n) { const i = this.childNodes.indexOf(n); if (i >= 0) { this._log('children'); this.childNodes.splice(i, 1); n.parentNode = null; } return n; }
  remove() { if (this.parentNode) this.parentNode.removeChild(this); }
  contains(n) { for (let x = n; x; x = x.parentNode) if (x === this) return true; return false; }
  getAttribute(n) { n = String(n).toLowerCase(); return this._attrs.has(n) ? this._attrs.get(n) : null; }
  setAttribute(n, v) { n = String(n).toLowerCase(); v = String(v); if (this._attrs.get(n) !== v) { this._attrs.set(n, v); if (n === 'value' && /^(input|textarea)$/.test(this.localName)) this._value = v; this._log('attr ' + n); } }
  removeAttribute(n) { n = String(n).toLowerCase(); if (this._attrs.delete(n)) this._log('attr ' + n); }
  hasAttribute(n) { return this._attrs.has(String(n).toLowerCase()); }
  matches(sel) { return matches(this, sel); }
  closest(sel) { for (let n = this; n && n.nodeType === 1; n = n.parentNode) if (matches(n, sel)) return n; return null; }
  _walk(fn) { for (const c of this.childNodes) { if (c.nodeType !== 1) continue; if (fn(c) === true || c._walk(fn)) return true; } return false; }
  querySelector(sel) { const l = compile(sel); let found = null; this._walk(n => { if (l.some(cx => matchComplex(n, cx))) { found = n; return true; } return false; }); return found; }
  querySelectorAll(sel) { const l = compile(sel), out = []; this._walk(n => { if (l.some(cx => matchComplex(n, cx))) out.push(n); return false; }); return out; }
  get _hid() { for (let n = this; n && n.nodeType === 1; n = n.parentNode) if (n.hidden || n.style.display === 'none') return true; return false; }
  getClientRects() { return this.isConnected && !this._hid ? [{ width: 100, height: 20 }] : []; }
  getBoundingClientRect() { const on = this.isConnected && !this._hid; return { x: 0, y: 0, top: 0, left: 0, right: on ? 100 : 0, bottom: on ? 20 : 0, width: on ? 100 : 0, height: on ? 20 : 0 }; }
  get offsetParent() { return this.isConnected && !this._hid ? this.parentNode : null; }
  get scrollHeight() { return 20; }
  focus() {
    const d = this.ownerDocument, prev = d.activeElement; if (prev === this) return; d.activeElement = this;
    if (prev && prev !== d.body) { prev.dispatchEvent(new FakeEvent('blur')); prev.dispatchEvent(new FakeEvent('focusout', { bubbles: true })); }
    this.dispatchEvent(new FakeEvent('focus')); this.dispatchEvent(new FakeEvent('focusin', { bubbles: true }));
  }
  blur() { const d = this.ownerDocument; if (d.activeElement !== this) return; d.activeElement = d.body; this.dispatchEvent(new FakeEvent('blur')); this.dispatchEvent(new FakeEvent('focusout', { bubbles: true })); }
  click() { this.dispatchEvent(new FakeEvent('click', { bubbles: true })); }
  scrollIntoView() {} scrollTo() {} setPointerCapture() {} releasePointerCapture() {}
}
class Text extends Target {
  constructor(doc, data) { super(); this.ownerDocument = doc; this.nodeType = 3; this.data = data; this.parentNode = null; }
  get textContent() { return this.data; } set textContent(v) { this.data = String(v); }
  _up() { return this.parentNode; }
}
class Frag extends Target {
  constructor(doc) { super(); this.ownerDocument = doc; this.nodeType = 11; this.childNodes = []; this.parentNode = null; }
  get firstElementChild() { return this.childNodes.find(n => n.nodeType === 1) || null; }
  _clear() { this.childNodes.forEach(n => { n.parentNode = null; }); this.childNodes = []; }
  appendChild(n) { return this.insertBefore(n, null); }
  insertBefore(n, ref) { if (n.parentNode) n.parentNode.removeChild(n); n.parentNode = this; const i = ref ? this.childNodes.indexOf(ref) : -1; if (i < 0) this.childNodes.push(n); else this.childNodes.splice(i, 0, n); return n; }
  removeChild(n) { const i = this.childNodes.indexOf(n); if (i >= 0) { this.childNodes.splice(i, 1); n.parentNode = null; } return n; }
}
/** The markup the page builds: well formed, with void elements and raw-text script and style. */
function parseInto(doc, root, html) {
  const stack = [root]; let i = 0; const top = () => stack[stack.length - 1];
  const text = s => { if (s) top().appendChild(doc.createTextNode(unent(s)), true); };
  while (i < html.length) {
    const lt = html.indexOf('<', i);
    if (lt < 0) { text(html.slice(i)); break; }
    if (lt > i) text(html.slice(i, lt));
    if (html.startsWith('<!--', lt)) { const e = html.indexOf('-->', lt + 4); i = e < 0 ? html.length : e + 3; continue; }
    if (html[lt + 1] === '!' || html[lt + 1] === '?') { const e = html.indexOf('>', lt); i = e < 0 ? html.length : e + 1; continue; }
    if (html[lt + 1] === '/') { const e = html.indexOf('>', lt), name = html.slice(lt + 2, e < 0 ? html.length : e).trim().toLowerCase(); for (let k = stack.length - 1; k > 0; k--) if (stack[k].localName === name) { stack.length = k; break; } i = e < 0 ? html.length : e + 1; continue; }
    const m = /^<([a-zA-Z][\w:-]*)((?:\s+[^\s"'<>/=]+(?:\s*=\s*(?:"[^"]*"|'[^']*'|[^\s"'>]+))?)*)\s*(\/?)>/.exec(html.slice(lt));
    if (!m) { text('<'); i = lt + 1; continue; }
    const name = m[1], ns = name === 'svg' || stack.some(n => n.namespaceURI) ? 'http://www.w3.org/2000/svg' : null, el = doc.createElementNS(ns, name);
    const re = /\s+([^\s"'<>/=]+)(?:\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'>]+)))?/g; let a;
    while ((a = re.exec(m[2]))) el.setAttribute(a[1], unent(a[2] != null ? a[2] : a[3] != null ? a[3] : a[4] != null ? a[4] : ''));
    top().appendChild(el, true); i = lt + m[0].length; const lname = name.toLowerCase();
    if (m[3] || VOID.has(lname)) continue;
    if (lname === 'script' || lname === 'style') { const e = html.toLowerCase().indexOf('</' + lname, i), body = html.slice(i, e < 0 ? html.length : e); if (body) el.appendChild(doc.createTextNode(body), true); const gt = html.indexOf('>', e < 0 ? html.length : e); i = gt < 0 ? html.length : gt + 1; continue; }
    stack.push(el);
  }
}
class Doc extends Target {
  constructor(win) {
    super(); this.nodeType = 9; this.readyState = 'complete'; this.hidden = false; this.title = 'Sleipnir Web'; this.parentNode = null; this.childNodes = []; this.win = win;
    this.documentElement = this.createElement('html'); this.documentElement.parentNode = this; this.childNodes.push(this.documentElement);
    this.body = this.createElement('body'); this.documentElement.appendChild(this.body, true); this.activeElement = this.body;
  }
  _up() { return this.win; }
  createElement(t) { return new El(this, t, null); }
  createElementNS(ns, t) { return new El(this, t, ns || null); }
  createTextNode(s) { return new Text(this, String(s)); }
  createDocumentFragment() { return new Frag(this); }
  getElementById(id) { return this.documentElement.querySelector('#' + id); }
  querySelector(s) { return this.documentElement.querySelector(s); }
  querySelectorAll(s) { return this.documentElement.querySelectorAll(s); }
  contains(n) { return this.documentElement.contains(n); }
}

// ---------------------------------------------------------------------------------------------------------------------------
// The page, booted: index.html's markup and scripts in order, a fake server (fetch and the event stream), a clock the test moves.

const MARKUP = fs.readFileSync(path.join(UI, 'index.html'), 'utf8');
const FILES = Array.from(MARKUP.matchAll(/<script src="([^"]+)"><\/script>/g)).map(m => path.join(UI, m[1]));
const SOURCES = FILES.map(f => fs.readFileSync(f, 'utf8'));
const SCRIPTS = FILES.map((f, i) => new vm.Script(SOURCES[i], { filename: f }));
/** For a test of the test: edits to the source of the page's files (by name) that a boot applies; none in a real run. */
export const PATCH = {};
const ev = (seq, t, k, f) => Object.assign({ seq, t, k }, f || {});
const ROSTER = [
  { id: 'mgr', role: 'manager', code: 'mgr', nth: 0, k: 0, leg: -1, scope: '-', ro: false, model: 'm', spawn: 0 },
  { id: 'be-1', role: 'backend', code: 'be', nth: 1, k: 1, leg: 0, scope: 'api/**', ro: false, model: 'm', spawn: 1 },
  { id: 'fe-1', role: 'frontend', code: 'fe', nth: 2, k: 2, leg: 1, scope: 'web/**', ro: false, model: 'm', spawn: 1 },
];
const snapshot = (id, order, seq) => ({ tab: { id, sid: 's-' + id, name: id, cwd: '/p/' + id, gen: 1, order }, gen: 1, seq, now: 10, meta: { model: 'm', mode: 'default', startedAt: 1760000000000, swarm: 2 }, roster: ROSTER,
  keyframe: [ev(0, 1, 'use', { id: 'be-1', rd: 10, un: 5, out: 1 })], events: [ev(1, 2, 'say', { who: 'you', text: 'hi' }), ev(2, 3, 'state', { id: 'be-1', s: 'edit', doing: 'Edit x', task: 'T1' })], hist: ['earlier line', 'a later line'], questions: [] });

/** A page, booted and idle. `two` gives it a second session tab. */
export async function boot(o) {
  o = o || {};
  LOG.errors = [];
  const win = new Target(), doc = new Doc(win), calls = [], errors = [], store = new Map(), timers = { now: 0, n: 0, list: [] }, streams = [];
  const body = /<body[^>]*>([\s\S]*)<\/body>/i.exec(MARKUP)[1].replace(/<script[\s\S]*?<\/script>/gi, '');
  parseInto(doc, doc.body, body); LOG.rows = [];
  const setT = (fn, ms, rep) => { const t = { id: ++timers.n, at: timers.now + Math.max(0, +ms || 0), fn, rep: rep ? Math.max(1, +ms || 1) : 0 }; timers.list.push(t); return t.id; };
  const clr = id => { timers.list = timers.list.filter(t => t.id !== id); };
  const advance = ms => { const end = timers.now + ms; for (;;) { timers.list.sort((a, b) => a.at - b.at || a.id - b.id); const t = timers.list[0]; if (!t || t.at > end) break; timers.list.shift(); timers.now = t.at; if (t.rep) { t.at += t.rep; timers.list.push(t); } t.fn(); } timers.now = end; };
  const snaps = { a: snapshot('a', 0, 2) }; if (o.two) snaps.b = snapshot('b', 1, 2);
  const route = (method, p, init) => {
    if (p === '/api/hello') return { boot: 'b1', streamAfter: 7, server: { addr: '127.0.0.1:7777' }, tabs: Object.values(snaps).map(s => s.tab), active: 'a', ui: {} };
    let m = /^\/api\/sessions\/([^/]+)\/snapshot$/.exec(p); if (m) return snaps[m[1]] || { _status: 404, error: 'no', code: 'not_found' };
    if (/\/complete\?/.test(p)) return { paths: ['api/cart.go', 'web/app.js', 'README.md'] };
    if (/\/ws\/index$/.test(p)) return { key: 'k1', head: 'c1', files: [{ path: 'api/cart.go', size: 10 }, { path: 'web/app.js', size: 10 }, { path: 'README.md', size: 10 }], checkpoints: [], tasks: [], reviewed: [], reverted: [] };
    if (p === '/api/cli') return { commands: [], chatSlash: [{ cmd: '/goal', args: 'TEXT', desc: 'a goal', group: 'session' }, { cmd: '/help', args: '', desc: 'this text', group: 'session' }, { cmd: '/cost', args: '', desc: 'what it cost', group: 'session' }] };
    if (p === '/api/models') return { models: [{ ref: 'm', ctx: 1, in: 1, out: 2, cached: 0.1 }], favs: [], roles: [], roleOrder: [], roleModels: {} };
    if (p === '/api/providers') return { providers: [] };
    if (p === '/api/projects') return { projects: [{ dir: '/p/a', trust: 'trusted', default: true }] };
    if (p === '/api/recorded') return { recorded: [], mb: 0 };
    if (p === '/api/recorded/watching') return { tabs: [] };
    if (method !== 'GET') return { ok: true };
    return { _status: 404, error: 'not found', code: 'not_found' };
  };
  const fetch = async (p, init) => {
    init = init || {}; const method = init.method || 'GET'; if (method !== 'GET') calls.push(method + ' ' + p); const r = route(method, p, init), status = r && r._status || 200;
    return { ok: status < 300, status, headers: { get: () => null }, json: async () => JSON.parse(JSON.stringify(r)) };
  };
  class ES extends Target { constructor(u) { super(); this.url = u; this.readyState = 0; this.fns = {}; streams.push(this); setT(() => { this.readyState = 1; if (this.onopen) this.onopen({}); }, 0); } addEventListener(t, f) { (this.fns[t] = this.fns[t] || []).push(f); } emit(type, data, id) { (this.fns[type] || []).forEach(f => f({ data: JSON.stringify(data), lastEventId: String(id || '') })); } close() { this.readyState = 2; } }
  const ctx = {
    console: { log() {}, info() {}, warn() {}, error: (...a) => errors.push(a.map(String).join(' ')) }, setTimeout: setT, clearTimeout: clr, setInterval: (f, ms) => setT(f, ms, true), clearInterval: clr,
    Promise, TextEncoder, AbortController, DOMException, URLSearchParams, URL, crypto: webcrypto, performance, document: doc, fetch, EventSource: ES, Event: FakeEvent, CustomEvent: FakeEvent,
    location: { reload() {}, search: '', href: 'http://127.0.0.1:7777/', host: '127.0.0.1:7777', hash: '' },
    localStorage: { getItem: k => store.has(k) ? store.get(k) : null, setItem: (k, v) => store.set(k, String(v)), removeItem: k => store.delete(k) },
    navigator: { clipboard: { writeText: () => Promise.resolve() } }, requestAnimationFrame: () => 1, cancelAnimationFrame() {}, innerWidth: 1280, innerHeight: 800,
    matchMedia: () => ({ matches: false, addEventListener() {}, addListener() {} }), getComputedStyle: el => ({ display: el._hid ? 'none' : 'block', visibility: 'visible' }),
    getSelection: () => ({ toString: () => '' }), CSS: { escape: s => String(s).replace(/[^\w-]/g, c => '\\' + c) },
    addEventListener: (...a) => win.addEventListener(...a), removeEventListener: (...a) => win.removeEventListener(...a), confirm: () => false,
  };
  ctx.window = ctx; ctx.globalThis = ctx; vm.createContext(ctx);
  FILES.forEach((f, i) => { const edit = PATCH[path.basename(f)]; (edit ? new vm.Script(edit(SOURCES[i]), { filename: f }) : SCRIPTS[i]).runInContext(ctx); });
  ctx.SL.palette.clock = () => ctx.SL.time.T.wall; // the age of an answer is the page's own clock, not the machine's
  const settle = async () => { for (let i = 0; i < 12; i++) { await new Promise(r => setImmediate(r)); advance(0); } };
  await settle();
  const SL = ctx.SL, $ = s => doc.querySelector(s);
  let seq = 10, id = 100;
  if (errors.length || LOG.errors.length) throw new Error('the page failed to boot: ' + errors.concat(LOG.errors)[0]);
  const L = {
    SL, doc, $, calls, errors, snaps, settle, advance,
    /** The page's own clock, moved by `s` seconds (the quiet period of a question, the window of a chord). */
    async step(s) { SL.loop.step(s); advance(s * 1000); await settle(); },
    /** A frame of the event stream, as the server sends it. */
    async frame(type, data) { streams[streams.length - 1].emit(type, data, ++id); await settle(); },
    async event(k, f) { await L.frame('ev', { tab: 'a', ev: ev(++seq, 12 + seq / 100, k, f) }); },
    focus(sel) { const e = $(sel); e.focus(); return e; },
    async type(text) { const i = $('#input'); i.focus(); for (const c of text) { i.setRangeText(c, i.selectionStart, i.selectionEnd, 'end'); i.dispatchEvent(new FakeEvent('input', { bubbles: true })); } await settle(); },
    /** One key, pressed on the focused element: the event, and what a browser does with a character nobody claimed. */
    press(spec) {
      const t = doc.activeElement || doc.body, e = new FakeEvent('keydown', Object.assign({ bubbles: true }, eventFor(spec)));
      t.dispatchEvent(e);
      if (!e.defaultPrevented && typesText(spec) && /^(input|textarea)$/.test(t.localName) && !t.disabled) { t.setRangeText(spec === 'space' ? ' ' : e.key, t.selectionStart, t.selectionEnd, 'end'); t.dispatchEvent(new FakeEvent('input', { bubbles: true })); }
      return e;
    },
  };
  return L;
}
