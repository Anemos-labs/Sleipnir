/* zz-test-hooks.js: test hooks, appended by `build.mjs --test` ONLY. The shipped file never contains this.
 * - window.__SL            the engine namespace
 * - SL.test.manual()       stop the rAF loop; drive frames with SL.test.step(dt) / run(seconds, dt): a time warp
 * - SL.test.flood(S,...)   deterministic synthetic events (a long-running job) inserted through the normal S.add path
 * - SL.test.counts()       live timers / intervals / rAFs / listeners / observers / animations / DOM nodes (everything in the page,
 *                          not only what went through a scope), to prove nothing leaks across view and session switches
 * - SL.test.sig(m)         a state signature of a model (chat rows excluded except by kind), to compare a held and a never-held run */
window.__SL = SL;
(function () {
  const T = SL.test = {};
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
  /* ---- time warp ---- */
  T.manual = () => { SL.loop.manual = true; };
  if (/(^|[?&])manual/.test(location.search)) SL.loop.manual = true;      // ?manual: the page never steps by itself, so a run is deterministic from t = 0
  T.step = dt => SL.loop.step(dt);
  T.run = (sec, dt) => { dt = dt || 1 / 60; const n = Math.round(sec / dt); for (let i = 0; i < n; i++) SL.loop.step(dt); return n * dt; };
  /** step until pred() or maxSec of simulated wall time; returns the simulated seconds used (or -1 on timeout) */
  T.until = (pred, maxSec, dt) => { dt = dt || 1 / 60; let t = 0; while (t < maxSec) { if (pred()) return t; SL.loop.step(dt); t += dt; } return pred() ? t : -1; };
  /* ---- a long-running job: deterministic synthetic events through the normal insertion path ---- */
  T.flood = (S, t0, t1, opt) => {
    opt = opt || {};
    const ids = S.roster.filter(r => r.id !== 'mgr').map(r => r.id), rnd = SL.u.rng(1234), ev = []; let task = 100, n = 0;
    for (let t = t0; t < t1; t += 1) {
      const a = ids[n % ids.length], f = 'docs/part-' + (n % 37) + '.md'; n++;
      ev.push({ t, k: 'tool', id: a, name: n % 3 ? 'Read' : 'Edit', arg: f, out: (80 + n % 90) + ' lines', task: null });
      if (n % 25 === 0) ev.push({ t: t + 0.2, k: 'req', id: a, ratio: 0.8 + rnd() * 0.15, p: 1400, o: 280 });
      if (n % 90 === 0) ev.push({ t: t + 0.3, k: 'mail', from: a, to: ids[(n + 1) % ids.length], text: 'part ' + (n % 37) + ' done; links ok' });
      if (n % 120 === 0 && opt.tasks !== false) { const id = 'T' + (task++); ev.push({ t: t + 0.4, k: 'task', id, s: 'running', title: 'sweep part ' + task, owner: a, deps: [] }, { t: t + 5, k: 'task', id, s: 'merged' }); }
      if (n % 600 === 0) ev.push({ t: t + 0.5, k: 'break', id: a, kind: 'low_hit', read: 0, expected: 4500, why: 'the prompt prefix did not change: the endpoint did not serve it' });
      if (n % 150 === 0) ev.push({ t: t + 0.6, k: 'say', who: 'mgr', text: 'Part ' + (n % 37) + ' is in; moving on to the next file.' });
    }
    S.add(ev); return ev.length;
  };
  T.sig = (m, noChan) => {
    const o = { ag: {}, tasks: {}, merged: m.merged.slice(), plan: m.plan.slice(), goal: m.goal.state, mail: m.mail.length, anom: m.anomalies.length, comp: m.compactions.length, steps: m.steps, refused: m.refused, lastReq: m.lastReq, rpm: m.rpm, qs: m.qs.map(q => q.id + ':' + q.answered), q: m.qHead ? m.qHead.task + m.qHead.step : null };
    m.order.forEach(id => { const a = m.ag[id]; o.ag[id] = [a.state, a.doing, a.task, Math.round(a.rd * 100) / 100, Math.round(a.un * 100) / 100, a.out, a.calls, a.nreq, a.ratios.map(r => Math.round(r * 1000) / 1000).join(','), a.segs.length]; });
    m.torder.forEach(id => { o.tasks[id] = m.tasks[id].st; });
    o.chan = {}; if (!noChan) Object.keys(m.chan).forEach(c => { const k = {}; m.chan[c].forEach(e => { if (e.k !== 'digest') k[e.k] = (k[e.k] || 0) + 1; }); o.chan[c] = k; });
    return JSON.stringify(o);
  };
  T.dg = () => { const S = SL.sessions.active; return S.m.chan.mgr.filter(e => e.k === 'digest').length; };
})();
