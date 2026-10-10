/* live.js: SL.live, the page's connection to the server (UI-WIRING.md 3 and 5): the boot sequence, the one stream, the snapshots,
 * and the frames that become sessions, events, meta and rosters.
 *
 * Boot: GET /api/hello; open the stream at hello.streamAfter and buffer its frames; fetch the snapshot of every tab and the shell's
 * caches in parallel; build the sessions; apply the buffered frames whose seq is newer than the snapshot; then the page mounts.
 * An event of a tab is dropped when its seq is not newer than the tab's lastSeq; otherwise the world clock moves up to its time (an
 * event is never held back by a slow page clock), it is inserted into the log and the world model steps (questions, needs-you and the
 * toasts react at once, whatever the view does). A tab whose snapshot is in flight buffers its frames (at most BUFFER per tab; beyond
 * that its snapshot is fetched again). A `gap` refetches every snapshot; a `reset` rebuilds its tab from a new snapshot.
 *
 * Connection states (D-13): `open` (the stream is up), `reconnecting` (the browser reconnects with Last-Event-ID, or the page reopens
 * the stream after asking /api/hello every 2 s), `down` (the server said bye, or the topic closed). A new boot id means a new server
 * run: the page reloads (the server answers with its sign-in page). */
(function (SL) {
  'use strict';
  const BUFFER = 10000, RETRY_MS = 2000;
  const L = SL.live = { hello: null, state: 'connecting', addr: '127.0.0.1:6969', stream: null, booted: false, pending: {}, gen: {}, started: false, tabs: {} };
  const api = () => SL.api;
  const ss = () => SL.sessions;

  /** The derived clock fields of a meta patch: t0 (seconds since local midnight of startedAt) and started (hh:mm:ss). */
  function metaOf(patch) { const m = Object.assign({}, patch || {}); return m; }
  const keyframeOf = list => (list || []).map(e => Object.assign({}, e, { seq: 0 }));

  /** Build or rebuild a session from a TabSnapshot. The session object (and its UI state) is kept when it exists. */
  function fromSnapshot(snap) {
    const t = snap.tab || {}, id = t.id; let S = ss().get(id);
    const meta = metaOf(snap.meta);
    if (!S) S = ss().make({ id, name: t.name || id, sid: t.sid, gen: snap.gen || t.gen, cwd: t.cwd, headless: t.headless, order: t.order, meta, roster: snap.roster || [], recorded: t.kind === 'recorded', follow: !!t.follow });
    else { S.name = t.name || S.name; S.sid = t.sid || S.sid; S.gen = snap.gen || t.gen || S.gen; S.order = t.order || S.order; Object.assign(S.meta, meta); S.roster = snap.roster || S.roster; if (t.headless) S.meta.headless = true; }
    ss().setStart(S);
    if (meta.queued) S.ui.queued = meta.queued.map(q => ({ id: q.id, text: q.text }));
    const log = keyframeOf(snap.keyframe).concat((snap.events || []).map(e => Object.assign({}, e)));
    log.sort((a, b) => a.t - b.t || (a.seq || 0) - (b.seq || 0));
    S.reset(log, snap.seq || 0, snap.now || 0);
    if (Array.isArray(snap.hist)) S.hist = snap.hist.slice(-200);
    L.gen[id] = snap.gen || t.gen || 0;
    return S;
  }

  /** A tab the server made to follow a session another process writes (POST /api/recorded/{sid}/watch, PARITY A7): it has no
   *  snapshot; its history is the recorded session's events, and its frames continue them. */
  const isWatch = id => /^w-/.test(String(id || ''));
  /** Fetch a tab's snapshot; frames of the tab that arrive meanwhile are buffered and applied after it (the newer ones only). */
  function loadTab(id) {
    if (isWatch(id)) return loadWatch(L.tabs[id] || { id, sid: String(id).slice(2), name: 'watching ' + String(id).slice(2), headless: true });
    if (L.pending[id] && L.pending[id].loading) return L.pending[id].loading;
    const rec = L.pending[id] = { frames: [], loading: null, overflow: false };
    rec.loading = api().get(api().tab(id) + '/snapshot').then(r => {
      if (L.pending[id] !== rec) return null;
      if (!r.ok) { delete L.pending[id]; if (r.status === 404) { if (ss().get(id)) ss().drop(id); return null; } return null; }
      const S = fromSnapshot(r.data || {});
      delete L.pending[id];
      if (rec.overflow) return loadTab(id);
      rec.frames.forEach(f => applyEv(S, f));
      S.stepWorld(S.wt); SL.bus.emit('roster-changed', S); SL.bus.emit('sessions-changed'); SL.bus.emit('needs-changed', S);
      if (SL.loop) SL.loop.dirty = true;
      return S;
    });
    return rec.loading;
  }

  /** Build (or rebuild) a watch tab: the recorded events so far, then the frames that arrived meanwhile. */
  function loadWatch(t) {
    const id = t.id; if (L.pending[id] && L.pending[id].loading) return L.pending[id].loading;
    const rec = L.pending[id] = { frames: [], loading: null, overflow: false };
    rec.loading = recordedEvents(t.sid, 0).then(got => {
      if (L.pending[id] !== rec) return null;
      const events = (got.events || []).map(e => Object.assign({}, e)).sort((a, b) => a.t - b.t || (a.seq || 0) - (b.seq || 0));
      let S = ss().get(id);
      if (!S) S = ss().make({ id, name: t.name || id, sid: t.sid, cwd: t.cwd, headless: t.headless, order: t.order != null ? t.order : 1000, recorded: true, follow: true, meta: { headless: !!t.headless }, roster: rosterOf(events) });
      else S.roster = rosterOf(events);
      const last = events.length ? events[events.length - 1] : null;
      S.reset(events, events.reduce((n, e) => Math.max(n, e.seq || 0), 0), last ? last.t : 0);
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
    if (ev.seq && ev.seq <= S.lastSeq) return;
    if (S.follow) ensureAgents(S, ev);
    S.catchUp(ev.t || 0);
    S.add([ev]);
    if (ev.seq) S.lastSeq = ev.seq;
    S.stepWorld(S.wt);
    if (SL.loop) SL.loop.dirty = true;
  }

  /* ---------------- frames ---------------- */
  const H = {
    ev(d) { const id = d.tab; if (L.pending[id]) { const p = L.pending[id]; if (p.frames.length >= BUFFER) p.overflow = true; else p.frames.push(d.ev); return; } const S = ss().get(id); if (S) applyEv(S, d.ev); },
    meta(d) {
      const S = ss().get(d.tab); if (!S) return; const p = d.patch || {};
      if (Array.isArray(p.queued)) S.ui.queued = p.queued.map(q => ({ id: q.id, text: q.text }));
      S.setMeta(p);
      if (p.rules && SL.data && SL.data.loaded && Object.keys(SL.data.loaded).some(k => k.indexOf('permissions|') === 0)) SL.data.load('permissions', { tab: S.id });
    },
    roster(d) {
      const S = ss().get(d.tab); if (!S) return; S.roster = d.roster || [];
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
    reset(d) { const S = ss().get(d.tab); if (!S) return; L.gen[d.tab] = d.gen; loadTab(d.tab); },
    recorded() { if (SL.data) SL.data.load('recorded', { force: true }); },
    run(d) { SL.bus.emit('run', d); },
    toast(d) { if (SL.ui && SL.ui.toast && d && d.text) SL.ui.toast(d.text, d.kind || ''); },
    ping(d) { const now = (d && d.now) || {}; Object.keys(now).forEach(id => { const S = ss().get(id); if (S && typeof now[id] === 'number') { S.catchUp(now[id]); S.stepWorld(S.wt); } }); },
    gap() { refetchAll(); },
    lagged() { /* the browser reconnects with Last-Event-ID */ },
    bye(d) { setState('down', (d && d.reason) || 'the server is shutting down'); },
    closed() { setState('down', 'the server closed the stream'); },
  };
  function refetchAll() { ss().list.slice().forEach(S => { if (!S.recorded || isWatch(S.id)) loadTab(S.id); }); }

  /** Frames that arrive before the sessions exist (during boot) wait in a list. */
  let early = [];
  function onFrame(type, data) {
    if (!L.booted) { early.push([type, data]); return; }
    const h = H[type]; if (h) { try { h(data || {}); } catch (e) { console.error('frame ' + type, e); } }
  }

  /* ---------------- connection state (D-13) ---------------- */
  function setState(s, why) {
    if (L.state === s) return; const was = L.state; L.state = s; L.why = why || '';
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
  /** Boot: hello, stream, snapshots and the shell's caches. Resolves when the sessions exist (zero is a valid number: D-12). */
  async function start() {
    if (L.started) return L.ready; L.started = true;
    L.ready = (async () => {
      const h = L.hello = await hello();
      L.addr = (h.server && h.server.addr) || L.addr;
      openStream(h.streamAfter || 0);
      const tabs = (h.tabs || []).slice().sort((a, b) => (a.order || 0) - (b.order || 0));
      tabs.forEach(t => { L.tabs[t.id] = t; });
      const D = SL.data;
      await Promise.all([
        Promise.all(tabs.map(t => isWatch(t.id) ? loadWatch(t) : api().get(api().tab(t.id) + '/snapshot').then(r => { if (r.ok) fromSnapshot(r.data || {}); }))),
        api().get('/api/recorded/watching', { noRetry: true }).then(r => Promise.all(((r.ok && r.data && r.data.tabs) || []).filter(t => !tabs.some(x => x.id === t.id)).map(t => { L.tabs[t.id] = t; return loadWatch(t); }))),
        D.load('cli'), D.load('models'), D.load('providers'), D.load('projects'), D.load('recorded'),
      ]);
      L.booted = true;
      const buffered = early; early = [];
      buffered.forEach(([type, data]) => onFrame(type, data));
      return h;
    })();
    return L.ready;
  }

  /* ---------------- recorded sessions opened read-only (D-10), or followed (PARITY A7) ---------------- */
  const PAGE = 5000, MAX_EVENTS = 50000, FOLLOW_MS = 2000;
  /** The agents of a recorded log, in the order they first act: the manager first, workers on legs by that order. */
  function rosterOf(events) {
    const ids = ['mgr'], seen = { mgr: true }, codeRole = {}; Object.keys(SL.D.roles).forEach(r => { codeRole[SL.D.roles[r].code] = r; });
    events.forEach(e => { const id = e.k === 'mail' ? null : e.k === 'ask' ? e.q && e.q.agent : e.id; if (!id || seen[id] || !/^[a-z]+-\d+$/.test(id)) return; seen[id] = true; ids.push(id); });
    return ids.map((id, i) => { const code = id === 'mgr' ? 'mgr' : id.split('-')[0], role = id === 'mgr' ? 'manager' : codeRole[code] || code, ro = !!(SL.D.roles[role] && SL.D.roles[role].ro); return { id, role, code, nth: id === 'mgr' ? 0 : +id.split('-')[1] || 1, k: i, leg: id === 'mgr' ? -1 : (i - 1) % 8, scope: id === 'mgr' ? '- (edits no file)' : ro ? '- (read-only)' : '', ro, model: '', spawn: 0 }; });
  }
  /** Fetch a recorded session's UI events from `from` (GET /api/recorded/{sid}/events, page by page). */
  async function recordedEvents(sid, from) {
    let out = [], next = +from || 0;
    for (let i = 0; i < MAX_EVENTS / PAGE; i++) {
      const r = await api().get('/api/recorded/' + api().seg(sid) + '/events?from=' + encodeURIComponent(String(next)) + '&limit=' + PAGE);
      if (!r.ok) return { ok: false, r, events: out, next };
      const d = r.data || {}, got = d.events || [], nx = d.next === '' || d.next == null ? null : +d.next; out = out.concat(got);
      if (nx == null || isNaN(nx) || nx <= next || !got.length) { next = next + got.length; break; }
      next = nx;
    }
    return { ok: true, events: out, next };
  }
  /**
   * Open a recorded session in a read-only tab of this page and play it in the Replay view (D-10). opt.follow keeps reading the log
   * while another process writes it (PARITY A7): the page asks for the events after the last one every 2 s while the tab is open.
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
    const rec = (ss().recorded || []).find(x => x.id === sid) || {}, events = got.events.map(e => Object.assign({}, e));
    events.sort((a, b) => a.t - b.t || (a.seq || 0) - (b.seq || 0));
    const S = ss().make({ id, name: rec.name || ('replay ' + sid.slice(9, 15)), sid, recorded: true, follow: !!opt.follow, order: 1e6, cwd: rec.cwd || '', meta: { model: rec.model || '', headless: !!opt.follow, startedAt: rec.lastWritten && rec.dur ? rec.lastWritten - rec.dur * 1000 : 0 }, roster: rosterOf(events) });
    const last = events.length ? events[events.length - 1] : null;
    S.reset(events, last && last.seq || 0, last ? last.t : 0); S.cursor = got.next;
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
      if (got.ok && got.events.length) { const known = new Set(S.roster.map(r => r.id)); got.events.forEach(e => applyEv(S, Object.assign({}, e))); S.cursor = got.next; const ros = rosterOf(S.log); if (ros.some(r => !known.has(r.id))) { S.roster = ros; ros.forEach(r => { SL.model.addAgent(S.wm, r); if (S.m) SL.model.addAgent(S.m, r); }); SL.bus.emit('roster-changed', S); } }
      if (!S.closed) S.followTimer = setTimeout(tick, FOLLOW_MS);
    };
    S.followTimer = setTimeout(tick, FOLLOW_MS);
  }

  /** The defaults of a new session (the server's flags and configuration, PARITY A4), when the server sends them. */
  L.defaults = () => (L.hello && L.hello.defaults) || null;
  Object.assign(L, { start, loadTab, loadWatch, isWatch, applyEv, fromSnapshot, onFrame, setState, refetchAll, H, openRecorded, rosterOf });
})(SL);
