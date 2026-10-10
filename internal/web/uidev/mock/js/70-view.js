/* 70-view.js: SL.makeScope / SL.views (the View lifecycle), SL.link (hover cross-highlighting) and SL.loop (the one frame loop).
 *
 * THE ONE RULE: a view owns everything it starts. Every timer, rAF, listener, observer, Web Animation, bus subscription and every
 * transient element (arc, flash, tooltip, popover) is created through its `scope`, and `unmount()` cancels and removes all of it.
 * Transient layers live inside the view's own root (`scope.layer()`), never on <body> or a shared overlay. A view is remounted from the
 * session's state/log, never from retained DOM. A view that obeys this cannot carry an animation over to another screen. */
(function (SL) {
  'use strict';
  const U = SL.u, { $, $$, mk } = U;

  /* ---------------- scope ---------------- */
  const live = new Set();       // every scope alive (the shell's and the current view's): tests read it
  function makeScope(root, label) {
    const sc = {
      label, root, alive: true, timers: new Set(), ints: new Set(), rafs: new Set(), frames: new Set(), listeners: [], observers: [], anims: new Set(), layers: [], subs: [], updaters: new Set(), hooks: [],
      /** setTimeout that dies with the scope. */
      timeout(fn, ms) { if (!sc.alive) return 0; const id = setTimeout(() => { sc.timers.delete(id); if (sc.alive) fn(); }, ms); sc.timers.add(id); return id; },
      interval(fn, ms) { if (!sc.alive) return 0; const id = setInterval(() => { if (sc.alive) fn(); }, ms); sc.ints.add(id); return id; },
      clear(id) { clearTimeout(id); clearInterval(id); sc.timers.delete(id); sc.ints.delete(id); },
      /** requestAnimationFrame that dies with the scope (for work that must follow the display, not the sim clock). */
      raf(fn) { if (!sc.alive) return 0; const id = requestAnimationFrame(t => { sc.rafs.delete(id); if (sc.alive) fn(t); }); sc.rafs.add(id); return id; },
      /** A per-frame callback on the SIM clock: fn(dtView, vt, session). Returns a remover. */
      frame(fn) { const rec = { fn, sc }; sc.frames.add(rec); SL.loop.frames.add(rec); return () => { sc.frames.delete(rec); SL.loop.frames.delete(rec); }; },
      /** Re-render hook: fn(session, model) runs when the model, the meta or the settings changed. */
      update(fn) { const rec = { fn, sc }; sc.updaters.add(rec); SL.loop.updaters.add(rec); return () => { sc.updaters.delete(rec); SL.loop.updaters.delete(rec); }; },
      listen(target, type, fn, opts) { if (!sc.alive) return; target.addEventListener(type, fn, opts); sc.listeners.push([target, type, fn, opts]); },
      observe(el, cb) { if (!sc.alive || !window.ResizeObserver) return null; const ro = new ResizeObserver(cb); ro.observe(el); sc.observers.push(ro); return ro; },
      /** Web Animation that is cancelled with the scope. */
      animate(el, kf, opts) { if (!sc.alive || !el.animate) return null; const a = el.animate(kf, opts); sc.anims.add(a); a.addEventListener('finish', () => sc.anims.delete(a)); return a; },
      /** A transient layer inside the view's root: arcs, flashes, tooltips, popovers go here. Removed on unmount. */
      layer(cls) { const el = mk('div', { class: 'layer ' + (cls || '') }); root.appendChild(el); sc.layers.push(el); return el; },
      on(topic, fn) { const off = SL.bus.on(topic, fn); sc.subs.push(off); return off; },
      onUnmount(fn) { sc.hooks.push(fn); },
      stats() { return { timers: sc.timers.size, ints: sc.ints.size, rafs: sc.rafs.size, frames: sc.frames.size, listeners: sc.listeners.length, observers: sc.observers.length, anims: sc.anims.size, layers: sc.layers.length, subs: sc.subs.length, updaters: sc.updaters.size }; },
      dispose() {
        if (!sc.alive) return; sc.alive = false; live.delete(sc);
        sc.hooks.splice(0).forEach(f => { try { f(); } catch (e) { console.error(e); } });
        sc.timers.forEach(clearTimeout); sc.timers.clear(); sc.ints.forEach(clearInterval); sc.ints.clear(); sc.rafs.forEach(cancelAnimationFrame); sc.rafs.clear();
        sc.frames.forEach(r => SL.loop.frames.delete(r)); sc.frames.clear(); sc.updaters.forEach(r => SL.loop.updaters.delete(r)); sc.updaters.clear();
        sc.listeners.splice(0).forEach(([t, ty, fn, o]) => t.removeEventListener(ty, fn, o));
        sc.observers.splice(0).forEach(ro => ro.disconnect()); sc.anims.forEach(a => { try { a.cancel(); } catch (e) { /* gone */ } }); sc.anims.clear();
        sc.layers.splice(0).forEach(l => l.remove()); sc.subs.splice(0).forEach(off => off());
      },
    };
    live.add(sc); return sc;
  }

  /* ---------------- views ---------------- */
  const V = { reg: {}, order: [], cur: null, host: null, mounts: 0, unmounts: 0 };
  /** Register a view: def = {name, title, key?, nav?: true, mount(scope, root, params, S) -> void}. */
  function register(def) { V.reg[def.name] = def; if (def.nav !== false) V.order.push(def.name); }
  function unmount() {
    if (!V.cur) return; const c = V.cur; V.cur = null; c.scope.dispose(); c.root.remove(); V.unmounts++; SL.bus.emit('view-unmounted', c.name);
  }
  /** Show a view: the old one is unmounted (everything it started dies) BEFORE the new one mounts. */
  function show(name, params) {
    const def = V.reg[name]; if (!def) return false;
    unmount();
    const S = SL.sessions.active, root = mk('section', { class: 'view v-' + name, 'data-view': name, 'aria-label': def.title });
    V.host.appendChild(root);
    const scope = makeScope(root, 'view:' + name); V.cur = { name, def, root, scope, params };
    if (S) S.ui.view = name;
    try { def.mount(scope, root, params || {}, S); } catch (e) { console.error(e); }
    V.mounts++; SL.bus.emit('view-mounted', name); SL.loop.dirty = true;
    return true;
  }
  const stats = () => ({ live: live.size, frames: SL.loop.frames.size, updaters: SL.loop.updaters.size, mounts: V.mounts, unmounts: V.unmounts, cur: V.cur ? V.cur.name : null, curStats: V.cur ? V.cur.scope.stats() : null, rootChildren: V.host ? V.host.children.length : 0, bus: SL.bus.count() });
  SL.makeScope = makeScope; SL.views = { V, register, show, unmount, stats, get current() { return V.cur; }, live };

  /* ---------------- hover cross-highlighting: SL.link ---------------- */
  const L = SL.link = { hover: null, sel: null, active: false };
  const split = s => s ? s.split(/\s+/).filter(Boolean) : [];
  function hotOf(el) {
    const ag = new Set(), task = new Set(), file = new Set(); let n = el;
    ag.add; split(n.getAttribute('data-ag')).forEach(x => ag.add(x)); split(n.getAttribute('data-task')).forEach(x => task.add(x)); split(n.getAttribute('data-file')).forEach(x => file.add(x));
    const S = SL.sessions.active, m = S && S.m;
    if (m) {
      task.forEach(t => { const T = m.tasks[t]; if (T && T.owner) ag.add(T.owner); });
      Array.from(ag).forEach(a => m.torder.forEach(t => { if (m.tasks[t].owner === a) task.add(t); }));
      file.forEach(f => { const o = SL.fileOwner && SL.fileOwner(f); if (o) ag.add(o); });
    }
    return { ag, task, file };
  }
  /** Apply .hot / .dimmed to every linkable element for the pointer target (or the selected agent). */
  function apply() {
    const app = document.getElementById('app'); if (!app) return;
    let hot = null; if (L.hover) hot = hotOf(L.hover); else if (L.sel) { const fake = { getAttribute: k => k === 'data-ag' ? L.sel : null }; hot = hotOf(fake); }
    if (!hot && !L.active) return; L.active = !!hot;
    $$('[data-ag],[data-task],[data-file]', app).forEach(el => {
      if (!hot) { if (el.classList.contains('hot')) el.classList.remove('hot'); if (el.classList.contains('dimmed')) el.classList.remove('dimmed'); return; }
      const is = split(el.getAttribute('data-ag')).some(x => hot.ag.has(x)) || split(el.getAttribute('data-task')).some(x => hot.task.has(x)) || split(el.getAttribute('data-file')).some(x => hot.file.has(x));
      el.classList.toggle('hot', is); el.classList.toggle('dimmed', !is);
    });
  }
  function setHover(el) { if (L.hover === el) return; L.hover = el; apply(); }
  function select(id) { L.sel = id || null; apply(); }
  /** Pointer classes for the governor: over a chat transcript -> hold; over a linkable source -> slow. */
  function bind(scope) {
    const app = document.getElementById('app');
    scope.listen(app, 'pointerover', e => {
      const t = e.target; if (!t.closest) return;
      const inChat = t.closest('.talk');
      const src = t.closest('[data-ag],[data-task],[data-file]');
      SL.time.setHover(inChat ? 'chat' : src ? 'linked' : null);
      setHover(src && !t.closest('.dr-f,.composer') ? src : null);
    });
    scope.listen(app, 'pointerleave', () => { SL.time.setHover(null); setHover(null); });
    scope.listen(document, 'pointerleave', () => { SL.time.setHover(null); setHover(null); });
    scope.listen(app, 'focusin', e => { const c = e.target.closest && e.target.closest('.talk'); if (c) SL.time.setFocus(true); const s = e.target.closest && e.target.closest('[data-ag]'); if (s && !c) setHover(s); });
    scope.listen(app, 'focusout', e => { if (e.target.closest && e.target.closest('.talk')) SL.time.setFocus(false); if (!(e.relatedTarget && e.relatedTarget.closest && e.relatedTarget.closest('[data-ag]'))) setHover(null); });
  }
  L.apply = apply; L.setHover = setHover; L.select = select; L.bind = bind;

  /* ---------------- the frame loop ---------------- */
  const loop = SL.loop = { frames: new Set(), updaters: new Set(), manual: false, running: false, last: 0, key: '', dirty: true, cssRate: 1, frameNo: 0 };
  function renderKey(S) { return S ? S.id + '|' + (S.m ? S.m.ver : 0) + '|' + S.metaVer + '|' + SL.settings.ver + '|' + (S.replay ? 1 : 0) + '|' + SL.sessions.list.length + '|' + SL.G.ver : ''; }
  /** One frame: wall time advances the world of every session; the governor turns it into view time for the active one. */
  function step(dtWall) {
    const T = SL.time.T; T.wall += dtWall * 1000; loop.frameNo++;
    const list = SL.sessions.list; for (let i = 0; i < list.length; i++) list[i].advanceWorld(dtWall);
    SL.actions.pump();
    const S = SL.sessions.active, rep = S && S.replay ? S.replay : null;
    const wasCatching = T.catching;
    const dtView = SL.time.tick(dtWall, S ? S.wt - S.vt : 0, rep);
    if (S && S.m) {
      if (T.catching && !wasCatching && !rep) S.collapse();
      S.advanceView(dtView);
      if (T.snap) { S.vt = S.wt; S.stepView(S.wt, true); }
      if (rep && rep.playing && S.vt >= S.wt - 1e-6) { S.replay = null; SL.bus.emit('replay-ended', S); }
    }
    // CSS keyframes (glows, carets) follow the governor too, capped at 1x
    const r = Math.min(1, T.rate);
    if (document.getAnimations && (Math.abs(r - loop.cssRate) > 0.004 || r < 0.995)) { loop.cssRate = r; document.getAnimations().forEach(a => { try { a.playbackRate = r; } catch (e) { /* not an animation we can scale */ } }); }
    const vt = S ? S.vt : 0;
    const key = renderKey(S);
    if (key !== loop.key || loop.dirty) { loop.key = key; loop.dirty = false; Array.from(loop.updaters).forEach(rec => { if (rec.sc.alive) { try { rec.fn(S, S && S.m); } catch (e) { console.error(e); } } }); if (L.active || L.sel) apply(); }
    Array.from(loop.frames).forEach(rec => { if (rec.sc.alive) { try { rec.fn(dtView, vt, S, dtWall); } catch (e) { console.error(e); } } });
  }
  function start() {
    if (loop.running) return; loop.running = true;
    const tick = now => { if (!loop.running) return; if (loop.manual) { loop.last = now; requestAnimationFrame(tick); return; } const dt = Math.min(0.1, Math.max(0, (now - loop.last) / 1000)); loop.last = now; if (!document.hidden) step(dt); requestAnimationFrame(tick); };
    requestAnimationFrame(t => { loop.last = t; requestAnimationFrame(tick); });
  }
  loop.step = step; loop.start = start;
})(SL);
