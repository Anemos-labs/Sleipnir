/* hooks-real.js: the instrumentation of the reference page's zz-test-hooks.js for the REAL page. Never shipped and never
 * loaded by index.html: a test driver injects it through the DevTools protocol before the page's own scripts run
 * (Page.addScriptToEvaluateOnNewDocument). It wraps the timer, frame, listener, observer and animation functions at once, and when
 * the page's namespace exists it exposes window.__SL and SL.test: counts(), idle(), settle(), gc(), listenerMap() and sig(m). The
 * time-warp hooks of the reference page (manual, step, run, flood) are not here: the real page runs on real time and its events come from
 * the server. */
(function () {
  const T = {};
  /* ---- instrumentation (installed before boot: this file is loaded before DOMContentLoaded fires 99-app) ---- */
  const live = { timers: new Set(), ints: new Set(), rafs: new Set(), obs: new Set(), anims: new Set() }, L = new WeakMap(), targets = new Set();   // listeners per target; targets held weakly so a collected node drops out of the counts
  const st = window.setTimeout, ct = window.clearTimeout, si = window.setInterval, ci = window.clearInterval, raf = window.requestAnimationFrame, caf = window.cancelAnimationFrame;
  window.setTimeout = function (fn, ms) { const a = Array.prototype.slice.call(arguments, 2); const id = st(function () { live.timers.delete(id); return fn.apply(this, a); }, ms); live.timers.add(id); return id; };
  window.clearTimeout = function (id) { live.timers.delete(id); return ct(id); };
  window.setInterval = function (fn, ms) { const a = Array.prototype.slice.call(arguments, 2); const id = si(fn, ms, ...a); live.ints.add(id); return id; };
  window.clearInterval = function (id) { live.ints.delete(id); return ci(id); };
  window.requestAnimationFrame = function (fn) { const id = raf(function (t) { live.rafs.delete(id); return fn(t); }); live.rafs.add(id); return id; };
  window.cancelAnimationFrame = function (id) { live.rafs.delete(id); return caf(id); };
  const add = EventTarget.prototype.addEventListener, rem = EventTarget.prototype.removeEventListener;
  const kk = (type, o) => type + '|' + (typeof o === 'boolean' ? o : o && o.capture ? 1 : 0);
  EventTarget.prototype.addEventListener = function (type, fn, o) { if (fn && !(o && o.once)) { let m = L.get(this); if (!m) { L.set(this, m = new Map()); targets.add(new WeakRef(this)); } const k = kk(type, o); let s = m.get(k); if (!s) m.set(k, s = new Set()); s.add(fn); } return add.call(this, type, fn, o); };
  EventTarget.prototype.removeEventListener = function (type, fn, o) { const m = L.get(this); if (m) { const s = m.get(kk(type, o)); if (s) s.delete(fn); } return rem.call(this, type, fn, o); };
  const RO = window.ResizeObserver; if (RO) window.ResizeObserver = class extends RO { constructor(cb) { super(cb); live.obs.add(this); } disconnect() { live.obs.delete(this); return super.disconnect(); } };
  const an = Element.prototype.animate; Element.prototype.animate = function () { const a = an.apply(this, arguments); live.anims.add(a); const done = () => live.anims.delete(a); a.addEventListener('finish', done); a.addEventListener('cancel', done); return a; };
  T.counts = () => { let ls = 0, detached = 0; targets.forEach(r => { const tgt = r.deref(); if (!tgt) { targets.delete(r); return; } if (tgt instanceof Animation) return; const m = L.get(tgt); let n = 0; m.forEach(s => { n += s.size; }); const attached = tgt === window || tgt === document || (tgt.isConnected !== false); if (attached) ls += n; else detached += n; }); return { timers: live.timers.size, ints: live.ints.size, rafs: live.rafs.size, listeners: ls, detachedListeners: detached, observers: live.obs.size, anims: Array.from(live.anims).filter(a => a.playState !== 'finished' && a.playState !== 'idle').length, nodes: document.getElementsByTagName('*').length, bus: SL.bus.count(), frames: SL.loop.frames.size, updaters: SL.loop.updaters.size, scopes: SL.views.live.size }; };
  /** force a garbage collection (the browser is started with --expose-gc); detachedListeners counts only nodes that survive it, i.e. real leaks. Call counts() in a LATER task. */
  T.gc = async () => { for (let i = 0; i < 4; i++) { if (window.gc) window.gc(); await new Promise(r => st(r, 40)); } };
  T.settle = () => new Promise(r => st(r, 800));
  T.idle = () => new Promise(r => raf(() => raf(() => st(r, 40))));          // let real frames, finishes and timers run
  T.listenerMap = () => { const o = {}; targets.forEach(r => { const tgt = r.deref(); if (!tgt || tgt instanceof Animation) return; if (!(tgt === window || tgt === document || tgt.isConnected !== false)) return; const m = L.get(tgt); const n = tgt === window ? 'window' : tgt === document ? 'document' : (tgt.tagName || '?').toLowerCase() + (tgt.id ? '#' + tgt.id : '') + (tgt.className && tgt.className.baseVal === undefined ? '.' + String(tgt.className).split(' ')[0] : ''); m.forEach((s, k) => { if (s.size) o[n + ' ' + k] = (o[n + ' ' + k] || 0) + s.size; }); }); return o; };
  T.sig = (m, noChan) => {
    const o = { ag: {}, tasks: {}, merged: m.merged.slice(), plan: m.plan.slice(), goal: m.goal.state, mail: m.mail.length, anom: m.anomalies.length, comp: m.compactions.length, steps: m.steps, refused: m.refused, lastReq: m.lastReq, rpm: m.rpm, qs: m.qs.map(q => q.id + ':' + q.answered), q: m.qHead ? m.qHead.task + m.qHead.step : null };
    m.order.forEach(id => { const a = m.ag[id]; o.ag[id] = [a.state, a.doing, a.task, Math.round(a.rd * 100) / 100, Math.round(a.un * 100) / 100, a.out, a.calls, a.nreq, a.ratios.map(r => Math.round(r * 1000) / 1000).join(','), a.segs.length]; });
    m.torder.forEach(id => { o.tasks[id] = m.tasks[id].st; });
    o.chan = {}; if (!noChan) Object.keys(m.chan).forEach(c => { const k = {}; m.chan[c].forEach(e => { if (e.k !== 'digest') k[e.k] = (k[e.k] || 0) + 1; }); o.chan[c] = k; });
    return JSON.stringify(o);
  };
  /* expose the hooks once the page's namespace has its views (00-namespace.js and 70-view.js have run) */
  (function attach() { if (window.SL && window.SL.views) { window.__SL = window.SL; window.SL.test = T; } else st(attach, 5); })();
})();
