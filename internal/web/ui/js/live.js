/* live.js: SL.live, the page's connection to the server: the boot sequence, the one stream, the snapshots,
 * and the frames that become sessions, events, meta and rosters.
 *
 * Boot: GET /api/hello; open the stream at hello.streamAfter and buffer its frames; fetch the snapshot of every tab and the shell's
 * caches in parallel; build the sessions; apply the buffered frames whose seq is newer than the snapshot; then the page mounts.
 * An event of a tab is dropped when its seq is not newer than the tab's lastSeq; otherwise the world clock moves up to its time (an
 * event is never held back by a slow page clock), it is inserted into the log and the world model steps (questions, needs-you and the
 * toasts react at once, whatever the view does). A tab whose snapshot is in flight buffers its frames (at most BUFFER per tab; beyond
 * that its snapshot is fetched again). A `gap` refetches every snapshot; a `reset` rebuilds its tab from a new snapshot; either one
 * arriving while that tab's snapshot is in flight marks it stale, and the snapshot is fetched again once it lands. A snapshot that
 * fails is fetched again after 1, 2, 4 and 8 s, then every 15 s, while the tab shows that it could not be loaded; its frames wait
 * meanwhile (they are never applied over a missing base). A live tab whose log grows past LOG_CAP events takes a fresh snapshot (the
 * server's own bounds and keyframe) at the next moment it is not in a replay. Boot waits for the snapshots, not for the catalogues.
 *
 * Connection states: `open` (the stream is up), `reconnecting` (the browser reconnects with Last-Event-ID, or the page reopens
 * the stream after asking /api/hello every 2 s), `down` (the server said bye, or the topic closed). A new boot id means a new server
 * run: the page reloads (the server answers with its sign-in page). */
(function (SL) {
  'use strict';
  const BUFFER = 10000, RETRY_MS = 2000, LOG_CAP = 75000;
  /** The waits before a failed snapshot is fetched again: 1, 2, 4, 8 s, then every 15 s (a test may shorten them). */
  const RETRY = { steps: [1000, 2000, 4000, 8000], later: 15000 };
  const L = SL.live = { hello: null, state: 'connecting', addr: '127.0.0.1:6969', stream: null, booted: false, pending: {}, gen: {}, started: false, tabs: {}, RETRY, LOG_CAP, ver: {}, stopped: false };
  /** The versions of the meta and the roster applied to tab id (a frame's `v`, a snapshot's metaV and rosterV): an older one never
   *  overwrites a newer one, whichever arrives first. */
  const verOf = id => L.ver[id] || (L.ver[id] = { meta: 0, roster: 0 });
  const api = () => SL.api;
  const ss = () => SL.sessions;

  /** The derived clock fields of a meta patch: t0 (seconds since local midnight of startedAt) and started (hh:mm:ss). */
  function metaOf(patch) { const m = Object.assign({}, patch || {}); return m; }
  /** An event from the server as the page keeps it: a copy, made plain (SL.model.clean: plain ids, numeric clocks, no markup). */
  const inbound = e => SL.model.clean(Object.assign({}, e));
  const keyframeOf = list => (Array.isArray(list) ? list : []).filter(e => e && typeof e === 'object').map(e => Object.assign(inbound(e), { seq: 0 }));
  const roster = list => SL.model.cleanRoster(list);

  /** Build or rebuild a session from a TabSnapshot. The session object (and its UI state) is kept when it exists. */
  function fromSnapshot(snap) {
    const t = snap.tab || {}, id = t.id; let S = ss().get(id);
    /* the snapshot's meta and roster count only when they are not older than a meta or roster frame already applied */
    const V = verOf(id), mv = +snap.metaV || 0, rv = +snap.rosterV || 0, newMeta = !S || mv >= V.meta, newRoster = !S || rv >= V.roster;
    const meta = newMeta ? metaOf(snap.meta) : {};
    if (newMeta) V.meta = Math.max(V.meta, mv); if (newRoster) V.roster = Math.max(V.roster, rv);
    if (!S) S = ss().make({ id, name: t.name || id, sid: t.sid, gen: snap.gen || t.gen, cwd: t.cwd, headless: t.headless, order: t.order, meta, roster: roster(snap.roster), recorded: t.kind === 'recorded', follow: !!t.follow });
    else { S.name = t.name || S.name; S.sid = t.sid || S.sid; S.gen = snap.gen || t.gen || S.gen; S.order = t.order || S.order; Object.assign(S.meta, meta); S.roster = snap.roster && newRoster ? roster(snap.roster) : S.roster; if (t.headless) S.meta.headless = true; }
    ss().setStart(S);
    if (meta.queued) S.ui.queued = meta.queued.map(q => ({ id: q.id, text: q.text }));
    const log = keyframeOf(snap.keyframe).concat((Array.isArray(snap.events) ? snap.events : []).filter(e => e && typeof e === 'object').map(inbound));
    log.sort((a, b) => a.t - b.t || (a.seq || 0) - (b.seq || 0));
    S.reset(log, snap.seq || 0, snap.now || 0);
    if (Array.isArray(snap.hist)) S.hist = snap.hist.slice(-200);
    L.gen[id] = snap.gen || t.gen || 0;
    return S;
  }

  /** A tab the server made to follow a session another process writes (POST /api/recorded/{sid}/watch): it has no
   *  snapshot; its history is the recorded session's events, and its frames continue them. */
  const isWatch = id => /^w-/.test(String(id || ''));
  /**
   * Fetch a tab's snapshot; frames of the tab that arrive meanwhile are buffered and applied after it (the newer ones only). opt.fresh
   * (a reset or a gap): a snapshot already in flight is stale, and the tab is fetched again once it lands. A failed fetch keeps the
   * buffered frames and tries again (RETRY), with the tab marked as not loaded (S.loading) meanwhile; a 404 drops the tab.
   */
  function loadTab(id, opt) {
    if (isWatch(id)) return loadWatch(L.tabs[id] || { id, sid: String(id).slice(2), name: 'watching ' + String(id).slice(2), headless: true });
    const prev = L.pending[id];
    if (prev && prev.loading) { if (opt && opt.fresh) prev.stale = true; return prev.loading; }
    if (prev && prev.timer) clearTimeout(prev.timer);
    const rec = L.pending[id] = { frames: prev ? prev.frames : [], metas: prev ? prev.metas : [], rosters: prev ? prev.rosters : [], loading: null, overflow: !!(prev && prev.overflow), stale: false, tries: prev ? prev.tries : 0, timer: null };
    rec.loading = api().get(api().tab(id) + '/snapshot').then(r => {
      if (L.pending[id] !== rec) return null;
      if (!r.ok) {
        if (r.status === 404) { delete L.pending[id]; if (ss().get(id)) ss().drop(id); return null; }
        return failed(id, rec, r);
      }
      const S = fromSnapshot(r.data || {});
      delete L.pending[id]; if (S.loading) { S.loading = null; S.touch(); }
      if (rec.overflow || rec.stale) return loadTab(id);   /* a reset or a gap came while it was in flight, or frames overflowed: what landed is old */
      (rec.metas || []).forEach(f => H.meta(f)); (rec.rosters || []).forEach(f => H.roster(f));
      rec.frames.forEach(f => applyEv(S, f));
      S.stepWorld(S.wt); SL.bus.emit('roster-changed', S); SL.bus.emit('sessions-changed'); SL.bus.emit('needs-changed', S);
      if (SL.loop) SL.loop.dirty = true;
      return S;
    });
    return rec.loading;
  }
  /** A snapshot that failed: the tab stays (or is shown, from its tab record) as not loaded, its frames wait, and the fetch is tried
   *  again after the next wait of RETRY. The first failure of a tab toasts once. Resolves null. */
  function failed(id, rec, r) {
    rec.loading = null; const wait = RETRY.steps[rec.tries] != null ? RETRY.steps[rec.tries] : RETRY.later; rec.tries++;
    let S = ss().get(id); const t = L.tabs[id];
    if (!S && t) S = ss().make({ id, name: t.name || id, sid: t.sid, cwd: t.cwd, headless: t.headless, order: t.order, meta: {} });
    if (S) { S.loading = { why: r.message || 'the server did not answer', next: Date.now() + wait, tries: rec.tries }; S.touch(); SL.bus.emit('sessions-changed'); }
    if (rec.tries === 1 && SL.ui && SL.ui.toast) SL.ui.toast('could not load ' + ((S && S.name) || id) + ': retrying (' + (r.message || 'the server did not answer') + ')', 'warm');
    rec.timer = setTimeout(() => { if (L.pending[id] === rec) loadTab(id); }, wait);
    if (SL.loop) SL.loop.dirty = true;
    return null;
  }

  /** Build (or rebuild) a watch tab: the recorded events so far, then the frames that arrived meanwhile. */
  function loadWatch(t) {
    const id = t.id; if (L.pending[id] && L.pending[id].loading) return L.pending[id].loading;
    const rec = L.pending[id] = { frames: [], loading: null, overflow: false };
    rec.loading = recordedEvents(t.sid, 0).then(got => {
      if (L.pending[id] !== rec) return null;
      if (!got.ok && !(got.events || []).length) return failed(id, rec, got.r || {});
      const events = (got.events || []).filter(e => e && typeof e === 'object').map(inbound).sort((a, b) => a.t - b.t || (a.seq || 0) - (b.seq || 0));
      let S = ss().get(id);
      if (!S) S = ss().make({ id, name: t.name || id, sid: t.sid, cwd: t.cwd, headless: t.headless, order: t.order != null ? t.order : 1000, recorded: true, follow: true, meta: { headless: !!t.headless }, roster: rosterOf(events) });
      else S.roster = rosterOf(events);
      const last = events.length ? events[events.length - 1] : null;
      S.reset(events, events.reduce((n, e) => Math.max(n, e.seq || 0), 0), last ? last.t : 0); S.truncated = got.cut ? events.length : 0; S.loading = null;
      delete L.pending[id];
      rec.frames.forEach(f => applyEv(S, f));
      SL.bus.emit('roster-changed', S); SL.bus.emit('sessions-changed'); if (SL.loop) SL.loop.dirty = true;
      return S;
    });
    return rec.loading;
  }
  /** A followed session's roster grows with the agents its events name (no roster frame comes with a watch tab). */
  function ensureAgents(S, ev) {
    const id = ev.k === 'ask' ? ev.q && ev.q.agent : ev.k === 'mail' ? null : ev.id;
    if (!id || S.roster.some(r => r.id === id) || !/^[a-z]+-\d+$/.test(id)) return;
    const r = rosterOf(S.log.concat([ev])).find(x => x.id === id); if (!r) return;
    S.roster = S.roster.concat([r]); SL.model.addAgent(S.wm, r); if (S.m) SL.model.addAgent(S.m, r); SL.bus.emit('roster-changed', S);
  }
  /** One UI event of a tab: dropped when not newer; the world clock moves up to it; the world steps. */
  function applyEv(S, ev) {
    if (!ev || typeof ev !== 'object') return;
    ev = inbound(ev);
    if (ev.seq && ev.seq <= S.lastSeq) return;
    if (S.follow) ensureAgents(S, ev);
    S.catchUp(ev.t || 0);
    S.add([ev]);
    if (ev.seq) S.lastSeq = ev.seq;
    S.stepWorld(S.wt);
    if (S.log.length > LOG_CAP && !S.recorded && !S.replay && !L.pending[S.id]) loadTab(S.id, { fresh: true });   /* the server's bounds and keyframe instead of an ever longer log */
    if (SL.loop) SL.loop.dirty = true;
  }

  /* ---------------- frames ---------------- */
  const H = {
    ev(d) { const id = d.tab; if (L.pending[id]) { const p = L.pending[id]; if (p.frames.length >= BUFFER) p.overflow = true; else p.frames.push(d.ev); return; } const S = ss().get(id); if (S) applyEv(S, d.ev); },
    meta(d) {
      if (L.pending[d.tab] && !ss().get(d.tab)) { (L.pending[d.tab].metas = L.pending[d.tab].metas || []).push(d); return; }   /* applied after its snapshot, by version */
      const S = ss().get(d.tab); if (!S) return; const p = d.patch || {}, V = verOf(d.tab);
      if (d.v) { if (+d.v <= V.meta) return; V.meta = +d.v; }   /* already in what this tab holds */
      if (Array.isArray(p.queued)) S.ui.queued = p.queued.map(q => ({ id: q.id, text: q.text }));
      S.setMeta(p);
      if (p.rules && SL.data && SL.data.loaded && Object.keys(SL.data.loaded).some(k => k.indexOf('permissions|') === 0)) SL.data.load('permissions', { tab: S.id });
    },
    roster(d) {
      if (L.pending[d.tab] && !ss().get(d.tab)) { (L.pending[d.tab].rosters = L.pending[d.tab].rosters || []).push(d); return; }
      const S = ss().get(d.tab); if (!S) return; const V = verOf(d.tab);
      if (d.v) { if (+d.v <= V.roster) return; V.roster = +d.v; }
      S.roster = roster(d.roster);
      S.roster.forEach(r => { SL.model.addAgent(S.wm, r); if (S.m) SL.model.addAgent(S.m, r); });
      SL.bus.emit('roster-changed', S); S.touch();
    },
    tab(d) {
      const t = d.tab || {}; if (!t.id) return;
      if (d.op !== 'remove') L.tabs[t.id] = t;
      if (d.op === 'remove') { if (SL.data) SL.data.forget(t.id); delete L.pending[t.id]; if (ss().get(t.id)) ss().drop(t.id); return; }
      const S = ss().get(t.id);
      if (d.op === 'update' && S) { S.name = t.name || S.name; S.sid = t.sid || S.sid; S.order = t.order != null ? t.order : S.order; if (t.headless) S.meta.headless = true; else if (isWatch(t.id)) S.meta.headless = false; S.touch(); SL.bus.emit('sessions-changed'); return; }
      if (!S || d.op === 'add') loadTab(t.id);
    },
    reset(d) { const S = ss().get(d.tab); if (!S && !L.pending[d.tab]) return; L.gen[d.tab] = d.gen; loadTab(d.tab, { fresh: true }); },
    recorded() { if (SL.data) SL.data.load('recorded', { force: true }); },
    run(d) { SL.bus.emit('run', d); },
    toast(d) { if (SL.ui && SL.ui.toast && d && d.text) SL.ui.toast(d.text, d.kind || ''); },
    ping(d) { const now = (d && d.now) || {}; Object.keys(now).forEach(id => { const S = ss().get(id); if (S && typeof now[id] === 'number') { S.catchUp(now[id]); S.stepWorld(S.wt); } }); },
    gap() { refetchAll(); },
    lagged() { /* the browser reconnects with Last-Event-ID */ },
    bye(d) { L.stopped = true; setState('down', (d && d.reason) || 'the server stopped'); },   /* the last frame of a stream: the server stopped (Ctrl-C) */
    closed() { setState('down', 'the server closed the stream'); },
  };
  function refetchAll() { ss().list.slice().forEach(S => { if (!S.recorded || isWatch(S.id)) loadTab(S.id, { fresh: true }); }); }

  /** Frames that arrive before the sessions exist (during boot) wait in a list. */
  let early = [];
  function onFrame(type, data) {
    if (!L.booted) { early.push([type, data]); return; }
    const h = H[type]; if (h) { try { h(data || {}); } catch (e) { console.error('frame ' + type, e); } }
  }

  /* ---------------- connection state ---------------- */
  function setState(s, why) {
    if (L.state === s) return; const was = L.state; L.state = s; L.why = why || ''; if (s === 'open') L.stopped = false;
    SL.bus.emit('conn', s);
    if (SL.loop) SL.loop.dirty = true;
    const an = typeof document !== 'undefined' && document.getElementById('announcer');
    if (an && was !== 'connecting') an.textContent = s === 'open' ? 'connected to ' + L.addr : s === 'down' ? 'disconnected: ' + (why || 'the server is gone') : 'reconnecting to ' + L.addr;
    if (s === 'down' && L.stream) { L.stream.close(); L.stream = null; scheduleRedial(); }
  }
  /** After a bye or a closed topic: ask /api/hello every 2 s; a new boot reloads the page, the same one reopens the stream. */
  let redial = null;
  function scheduleRedial() {
    if (redial) return;
    redial = setTimeout(async () => { redial = null; const h = await api().get('/api/hello', { noRetry: true }); if (!h.ok) { scheduleRedial(); return; } if (L.hello && h.data.boot !== L.hello.boot) { reload(); return; } openStream(L.lastId || h.data.streamAfter); refetchAll(); }, RETRY_MS);
  }
  const reload = () => { try { location.reload(); } catch (e) { /* tests */ } };
  async function beforeReopen() {
    const h = await api().get('/api/hello', { noRetry: true });
    if (!h.ok) return 'later';
    if (L.hello && h.data.boot !== L.hello.boot) { reload(); return false; }
    return true;
  }
  function openStream(after) {
    if (L.stream) L.stream.close();
    L.stream = api().stream(after, {
      frame: (type, data, id) => { if (id) L.lastId = id; onFrame(type, data); },
      state: s => { if (s === 'open') setState('open'); else if (s === 'reconnecting' && L.state !== 'down') setState('reconnecting'); },
      beforeReopen,
    });
  }

  /* ---------------- boot ---------------- */
  /** Resolve the server's hello, retrying every 2 s while the server is not reachable (the page shows the disconnected chip). */
  async function hello() {
    for (;;) {
      const r = await api().get('/api/hello', { noRetry: true });
      if (r.ok) return r.data || {};
      setState('reconnecting', r.message);
      await new Promise(res => setTimeout(res, RETRY_MS));
    }
  }
  /** Boot: hello, stream, snapshots and the shell's caches. Resolves when the sessions exist (zero is a valid number: the server
   *  may host no tab, and the page then shows an empty shell). */
  async function start() {
    if (L.started) return L.ready; L.started = true;
    L.ready = (async () => {
      const h = L.hello = await hello();
      L.addr = (h.server && h.server.addr) || L.addr;
      openStream(h.streamAfter || 0);
      const tabs = (h.tabs || []).slice().sort((a, b) => (a.order || 0) - (b.order || 0));
      tabs.forEach(t => { L.tabs[t.id] = t; });
      const D = SL.data;
      /* the catalogues fill in when they arrive (a slow /api/models never holds the page back) */
      L.catalogue = Promise.all([D.load('cli'), D.load('models'), D.load('providers'), D.load('projects'), D.load('recorded')]);
      await Promise.all([
        Promise.all(tabs.map(t => loadTab(t.id))),
        api().get('/api/recorded/watching', { noRetry: true }).then(r => Promise.all(((r.ok && r.data && r.data.tabs) || []).filter(t => !tabs.some(x => x.id === t.id)).map(t => { L.tabs[t.id] = t; return loadWatch(t); }))),
      ]);
      L.booted = true;
      const buffered = early; early = [];
      buffered.forEach(([type, data]) => onFrame(type, data));
      return h;
    })();
    return L.ready;
  }

  /* ---------------- recorded sessions opened read-only, or followed ---------------- */
  const PAGE = 5000, MAX_EVENTS = 400000, FOLLOW_MS = 2000;
  /** The agents of a recorded log, in the order they first act: the manager first, workers on legs by that order. */
  function rosterOf(events) {
    const ids = ['mgr'], seen = { mgr: true }, codeRole = {}; Object.keys(SL.D.roles).forEach(r => { codeRole[SL.D.roles[r].code] = r; });
    events.forEach(e => { const id = e.k === 'mail' ? null : e.k === 'ask' ? e.q && e.q.agent : e.id; if (!id || seen[id] || !/^[a-z]+-\d+$/.test(id)) return; seen[id] = true; ids.push(id); });
    return ids.map((id, i) => { const code = id === 'mgr' ? 'mgr' : id.split('-')[0], role = id === 'mgr' ? 'manager' : codeRole[code] || code, ro = !!(SL.D.roles[role] && SL.D.roles[role].ro); return { id, role, code, nth: id === 'mgr' ? 0 : +id.split('-')[1] || 1, k: i, leg: id === 'mgr' ? -1 : (i - 1) % 8, scope: id === 'mgr' ? '- (edits no file)' : ro ? '- (read-only)' : '', ro, model: '', spawn: 0 }; });
  }
  /** Fetch a recorded session's UI events from `from` (GET /api/recorded/{sid}/events, page by page) until the server says there are
   *  no more, or MAX_EVENTS (L.maxEvents when set) were read: then cut is true (the log has more than the page holds). Resolves {ok, events, next, cut}. */
  async function recordedEvents(sid, from) {
    let out = [], next = +from || 0, cut = false;
    for (;;) {
      const max = L.maxEvents || MAX_EVENTS; if (out.length >= max) { cut = true; break; }
      const r = await api().get('/api/recorded/' + api().seg(sid) + '/events?from=' + encodeURIComponent(String(next)) + '&limit=' + Math.min(PAGE, max - out.length));
      if (!r.ok) return { ok: false, r, events: out, next, cut };
      const d = r.data || {}, got = Array.isArray(d.events) ? d.events : [], nx = d.next === '' || d.next == null ? null : +d.next; for (const e of got) out.push(e);
      if (nx == null || isNaN(nx) || nx <= next || !got.length) { next = next + got.length; break; }
      next = nx;
    }
    return { ok: true, events: out, next, cut };
  }
  /**
   * Open a recorded session in a read-only tab of this page and play it in the Replay view. opt.follow keeps reading the log
   * while another process writes it: the page asks for the events after the last one every 2 s while the tab is open.
   * Resolves the API-shaped result {ok, ...}; failures toast.
   */
  async function openRecorded(sid, opt) {
    opt = opt || {};
    if (opt.follow) {
      const w = await api().post('/api/recorded/' + api().seg(sid) + '/watch');
      if (w.ok) { const id = (w.data && w.data.tab && w.data.tab.id) || 'w-' + sid; L.tabs[id] = Object.assign({ id, sid }, w.data && w.data.tab); await (ss().get(id) ? null : loadWatch(L.tabs[id])); SL.act.switchSession(id); return w; }
      if (w.status !== 501) { if (SL.ui && SL.ui.toast) SL.ui.toast(w.message, w.status === 409 ? 'warm' : 'err'); return w; }
      /* a build without following: the page reads the log every 2 s itself */
    }
    const id = 'rec-' + sid, have = ss().get(id);
    if (have) { SL.act.switchSession(id); return { ok: true, data: { tab: { id } } }; }
    const got = await recordedEvents(sid, 0);
    if (!got.ok) { if (SL.ui && SL.ui.toast) SL.ui.toast(got.r.message, 'err'); return got.r; }
    const rec = (ss().recorded || []).find(x => x.id === sid) || {}, events = got.events.filter(e => e && typeof e === 'object').map(inbound);
    events.sort((a, b) => a.t - b.t || (a.seq || 0) - (b.seq || 0));
    const S = ss().make({ id, name: rec.name || ('replay ' + sid.slice(9, 15)), sid, recorded: true, follow: !!opt.follow, order: 1e6, cwd: rec.cwd || '', meta: { model: rec.model || '', headless: !!opt.follow, startedAt: rec.lastWritten && rec.dur ? rec.lastWritten - rec.dur * 1000 : 0 }, roster: rosterOf(events) });
    const last = events.length ? events[events.length - 1] : null;
    S.reset(events, last && last.seq || 0, last ? last.t : 0); S.cursor = got.next; S.truncated = got.cut ? events.length : 0;
    SL.bus.emit('sessions-changed');
    SL.act.switchSession(id); SL.views.show('replay'); S.seek(0, !opt.follow); S.touch();
    if (opt.follow) follow(S);
    return { ok: true, data: { tab: { id } } };
  }
  /** Read the events a followed log gained since the cursor, every 2 s, until the tab closes. */
  function follow(S) {
    const tick = async () => {
      if (S.closed) return;
      const got = await recordedEvents(S.sid, S.cursor);
      if (got.ok && got.events.length && !S.truncated) { const known = new Set(S.roster.map(r => r.id)); got.events.forEach(e => applyEv(S, e)); S.cursor = got.next; const ros = rosterOf(S.log); if (ros.some(r => !known.has(r.id))) { S.roster = ros; ros.forEach(r => { SL.model.addAgent(S.wm, r); if (S.m) SL.model.addAgent(S.m, r); }); SL.bus.emit('roster-changed', S); } }
      if (!S.closed) S.followTimer = setTimeout(tick, FOLLOW_MS);
    };
    S.followTimer = setTimeout(tick, FOLLOW_MS);
  }

  /** The defaults of a new session (the server's flags and configuration), when the server sends them. */
  L.defaults = () => (L.hello && L.hello.defaults) || null;
  Object.assign(L, { start, loadTab, loadWatch, isWatch, applyEv, fromSnapshot, onFrame, setState, refetchAll, H, openRecorded, rosterOf, recordedEvents, MAX_EVENTS });
})(SL);
