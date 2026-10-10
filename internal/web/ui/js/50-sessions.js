/* 50-sessions.js: SL.sessions, the registry of the tabs this page shows and the two clocks of every session.
 *
 * A Session owns: its meta (chat flags, mode, model, budget, rules), a roster, an EVENT LOG (sorted by t, the only source of truth),
 * a WORLD model `wm` (events up to the world clock `wt`, no chat rows; needs-you reads it) and, for the active session only, a VIEW
 * model `m` (events up to the view clock `vt`, with chat rows; what the screen shows).
 *
 *   world  wt  advances 1 s per wall second for EVERY session, and an arriving event or a ping moves it forward to the server's time
 *   view   vt  advances by dtView (the governor's rate) for the active session; it is never ahead of wt; gap = wt - vt
 *
 * The events come from the server (live.js builds each session from a snapshot and adds the stream's events); the page itself adds
 * only `local` cards. While the view is held, events pile up between vt and wt: they are in the log, not applied. On release the view
 * first collapses everything older than TIME_CONST.WINDOW seconds (applied instantly, one digest row in the chat), then replays the
 * rest fast (see 20-clock.js). Switching sessions rebuilds the view model of the new active session from its log. */
(function (SL) {
  'use strict';
  const U = SL.u, { upperBound } = U;
  const R = () => SL.model, TC = SL.TIME_CONST;

  const reg = { list: [], byId: {}, active: null, counter: 0, recorded: [], recordedMb: null, pruned: [], wasCatching: false, placeholder: null };

  /** Seconds since local midnight now (the clock of a new session's start). */
  const nowTod = () => { const d = new Date(); return d.getHours() * 3600 + d.getMinutes() * 60 + d.getSeconds() + d.getMilliseconds() / 1000; };
  /** Seconds since local midnight and hh:mm:ss of an epoch-milliseconds instant. */
  const todOf = ms => { const d = new Date(ms); return d.getHours() * 3600 + d.getMinutes() * 60 + d.getSeconds() + d.getMilliseconds() / 1000; };

  const DEFAULT_META = { mode: 'default', effort: 'default', budget: 0, swarm: 0, isolation: 'none', verify: '', commit: false, mailman: false, trustProject: true, noMcp: false, roleModels: {}, rules: [], goalText: '', cwd: '', model: '', launch: '', headless: false, running: false, t0: 0, started: '' };

  class Session {
    /** spec: {id, name, sid, gen, cwd, headless, meta, roster, recorded (a read-only recorded session), placeholder (no tab)}. */
    constructor(spec) {
      this.id = spec.id; this.name = spec.name || spec.id; this.sid = spec.sid || ''; this.gen = spec.gen || 0; this.order = spec.order || 0; this.kind = spec.recorded ? 'recorded' : 'live';
      this.recorded = !!spec.recorded; this.follow = !!spec.follow; this.placeholder = !!spec.placeholder; this.readOnly = this.recorded || this.placeholder;
      this.meta = Object.assign({}, DEFAULT_META, { cwd: spec.cwd || '' }, spec.meta || {}); this.meta.rules = (this.meta.rules || []).slice(); this.meta.roleModels = Object.assign({}, this.meta.roleModels || {});
      if (spec.headless) this.meta.headless = true;
      this.ui = { chan: 'mgr', view: 'cockpit', scroll: {}, draft: '', planOpen: false, selAg: null, drawer: null, drawerTab: 'log', queued: [] };
      this.hold = { pinned: false, vtSaved: null };
      this.replay = null; this.hist = []; this.metaVer = 0; this.closed = false; this.runs = 0; this.actionLog = [];
      this.roster = spec.roster || [{ id: 'mgr', role: 'manager', code: 'mgr', nth: 0, k: 0, leg: -1, scope: '-', ro: false, model: this.meta.model, spawn: 0 }];
      this.reset([], 0, 0);
    }
    /** The goal plan's step texts (the plan tool of the agent the person talks to): the view model's when there is one. */
    get plan() { const mm = this.m || this.wm; return (mm && mm.planText) || []; }
    /** Replace the log and the clocks: used when the session is built or rebuilt from a snapshot. log is sorted by t; now is the
     *  server's session time. The view model, when this session is active, is rebuilt at the live edge. */
    reset(log, seq, now) {
      this.log = log; this.seq = seq || 0; this.lastSeq = seq || 0; this.runs++;
      const last = log.length ? log[log.length - 1].t : 0;
      /* the server's clock; an event stamped later waits for the clock to reach it */
      this.wt = now > 0 ? now : last; this.vt = this.wt; this.widx = 0; this.idx = 0; this.visW = 0; this.visV = 0;
      this.wm = R().newModel(this, { noChat: true }); this.m = null;
      this.stepWorld(this.wt, true);
      this.ui.drawer = null; this.replay = null;
      if (reg.active === this) this.rebuild(this.wt);
    }
    /** Insert events at the world time (t is clamped to >= wt); an event keeps the server's seq. Returns the inserted events. */
    add(list) {
      list = Array.isArray(list) ? list : [list];
      list.forEach(e => { if (e.t == null || e.t < this.wt) e.t = this.wt; if (e.seq == null) e.seq = 0; const p = Math.max(upperBound(this.log, e.t), this.widx); this.log.splice(p, 0, e); });
      return list;
    }
    /** World step: apply every event up to wt to the world model; needs-you and the toasts react here. */
    stepWorld(untilT, silent) {
      const log = this.log, R_ = R();
      while (this.widx < log.length && log[this.widx].t <= untilT + 1e-9) {
        const ev = log[this.widx++]; R_.reduce(this.wm, ev); if (R_.isVisible(ev)) this.visW++;
        if (!silent) this.afterWorld(ev);
        else if (ev.k === 'ask') SL.bus.emit('needs-changed', this);
      }
    }
    afterWorld(ev) {
      if (ev.k === 'ask') SL.bus.emit('ask-arrived', { S: this, q: ev.q });
      if (ev.k === 'ask' || ev.k === 'answer') SL.bus.emit('needs-changed', this);
      if (ev.k === 'final') SL.bus.emit('session-done', this);
      if (ev.k === 'goal' && ev.s === 'met') SL.bus.emit('goal-met', this);
    }
    advanceWorld(dt) { this.wt += dt; this.stepWorld(this.wt); }
    /** The server's clock: the world clock never runs behind an event or a ping. */
    catchUp(t) { if (t > this.wt) this.wt = t; }
    /** Number of events of visible kinds the view has not shown yet. */
    unseen() { return Math.max(0, this.visW - this.visV); }
    gap() { return this.wt - this.vt; }

    /* ---- view side (active session only) ---- */
    /** Apply events up to view time t to the view model. animate=true emits them on the 'ev' bus for effects. */
    stepView(untilT, animate) {
      const log = this.log, R_ = R(), m = this.m; if (!m) return 0; let n = 0;
      while (this.idx < log.length && log[this.idx].t <= untilT + 1e-9 && this.idx < this.widx) {
        const ev = log[this.idx++]; R_.reduce(m, ev); if (R_.isVisible(ev)) this.visV++; n++;
        if (animate) SL.bus.emit('ev', { S: this, ev });
      }
      return n;
    }
    advanceView(dtView) {
      if (!this.m) return;
      const target = Math.min(this.wt, this.vt + dtView);
      this.vt = target; this.stepView(target, true);
    }
    /** Rebuild the view model from the log, silently, at time t. */
    rebuild(t) {
      t = Math.min(t, this.wt); this.m = R().newModel(this); this.idx = 0; this.visV = 0; this.vt = t;
      const log = this.log, R_ = R(), m = this.m;
      while (this.idx < log.length && log[this.idx].t <= t + 1e-9 && this.idx < this.widx) { const ev = log[this.idx++]; R_.reduce(m, ev); if (R_.isVisible(ev)) this.visV++; }
      m.ver++; SL.bus.emit('rebuilt', this);
    }
    /** Collapse: apply everything older than wt - WINDOW instantly and summarise it as one digest row; the rest will replay. */
    collapse() {
      if (!this.m) return false;
      const boundary = this.wt - TC.WINDOW; if (this.vt >= boundary - 1e-9) return false;
      const dg = { tools: 0, merged: [], submitted: [], breaks: 0, mails: 0, asks: 0, compacts: 0, rows: [], t0: this.vt, t1: boundary }, log = this.log, R_ = R(), m = this.m; let n = 0;
      while (this.idx < log.length && log[this.idx].t <= boundary && this.idx < this.widx) { const ev = log[this.idx++]; R_.reduce(m, ev, { digest: dg }); if (R_.isVisible(ev)) this.visV++; n++; }
      this.vt = boundary; dg.n = n;
      if (n) { m.nid++; m.chan.mgr.push({ k: 'digest', id: m.nid, t: boundary, t0: dg.t0, t1: dg.t1, dg }); m.ver++; }
      SL.bus.emit('collapsed', { S: this, dg });
      return true;
    }
    /** Jump the view to the live edge without animating (leaving replay, switching back). */
    goLive() { this.replay = null; this.rebuild(this.wt); }
    /** Replay: seek the view to t (silent rebuild); the loop plays on from there at the replay speed. */
    seek(t, playing) { t = Math.max(0, Math.min(this.wt, t)); this.replay = { playing: !!playing, speed: (this.replay && this.replay.speed) || 1 }; this.rebuild(t); }
    isLive() { return !this.replay; }
    /* ---- derived ---- */
    openQuestion() { return SL.calc.openQuestion(this.wm); }
    /** run, ask, idle, done or paused: what the tab glyph shows. A turn the server says is running is `run`. */
    state() {
      const w = this.wm, running = !!w.turn || this.meta.running === true;
      if ((w.final && !running) || w.goal.state === 'met') return 'done';
      if (SL.calc.openQuestion(w)) return 'ask';
      if (w.goal.state === 'paused') return 'paused';
      return running || SL.calc.turnRunning(w) ? 'run' : 'idle';
    }
    touch() { this.metaVer++; SL.bus.emit('meta', this); }
    /** Merge a meta patch from the server (or the page's own derived fields) and re-render. */
    setMeta(patch) { Object.assign(this.meta, patch); if (patch && patch.startedAt) setStart(this); this.touch(); }
  }
  /** t0 (seconds since local midnight) and started (hh:mm:ss) from meta.startedAt. */
  function setStart(S) { if (!S.meta.startedAt) return; S.meta.t0 = todOf(S.meta.startedAt); S.meta.started = U.tod(0, S.meta.t0); }

  /* ---------------- registry ---------------- */
  /** Create a session and add it to the tabs (spec as Session). A placeholder is never listed. */
  function make(spec) {
    const S = new Session(spec); setStart(S);
    if (S.placeholder) return S;
    if (reg.byId[S.id]) drop(S.id, true);
    reg.list.push(S); reg.byId[S.id] = S; reg.list.sort((a, b) => (a.order || 0) - (b.order || 0)); return S;
  }
  /** The session shown when the server hosts no tab (D-12): empty values, no composer action. */
  function placeholder() { return reg.placeholder || (reg.placeholder = new Session({ id: '', name: 'no session', placeholder: true })); }
  function init() { if (!reg.list.length) { activate(null); return; } activate(reg.list[0].id); }
  /** Make a session active: the previous one drops its view model; the new one rebuilds from its log. null: the placeholder. */
  function activate(id) {
    const S = id == null || id === '' ? placeholder() : reg.byId[id]; if (!S) return null;
    const prev = reg.active;
    if (prev === S) return S;
    if (prev) { if (prev.hold.pinned && prev.m) prev.hold.vtSaved = prev.vt; prev.m = null; }
    reg.active = S;
    S.rebuild(S.hold.pinned && S.hold.vtSaved != null ? S.hold.vtSaved : S.wt);
    reg.wasCatching = false;
    SL.time.pin(S.hold.pinned);
    SL.bus.emit('activated', S);
    return S;
  }
  /** Remove a tab from the page (the server closed it, or it was never there). The neighbour becomes active; with no tab left the
   *  placeholder does (D-12). quiet: no sessions-changed event (a replacement follows). */
  function drop(id, quiet) {
    const S = reg.byId[id]; if (!S) return false;
    const i = reg.list.indexOf(S); reg.list.splice(i, 1); delete reg.byId[id]; S.closed = true; S.m = null;
    if (reg.active === S) { reg.active = null; const nx = reg.list[Math.min(i, reg.list.length - 1)]; activate(nx ? nx.id : null); }
    if (!quiet) SL.bus.emit('sessions-changed');
    return true;
  }
  /** The page's local half of closing a tab: refuses the last one (the server refuses it too). */
  function close(id) { if (reg.list.length <= 1) return false; return drop(id); }
  function rename(id, name) { const S = reg.byId[id]; if (!S) return false; S.name = String(name).trim() || S.name; S.touch(); SL.bus.emit('sessions-changed'); return true; }
  /** Open questions across all sessions (world view), oldest first: the cross-session inbox. */
  function needs() {
    const out = []; reg.list.forEach(S => { const q = SL.calc.openQuestion(S.wm); if (q) out.push({ S, q, waiting: SL.calc.waiting(S.wm) }); });
    return out.sort((a, b) => a.q.t0 + a.S.meta.t0 - b.q.t0 - b.S.meta.t0);
  }

  /* ---------------- recorded sessions: list / prune arithmetic (the server applies the same rule) ---------------- */
  /** What `sessions prune --older-than X --keep N` would delete: older than X, but never the newest N sessions on disk (the live sessions are the newest, so
   * they use up slots), never one written in the last 10 minutes. */
  function pruneCandidates(olderS, keep) {
    const sorted = reg.recorded.slice().sort((a, b) => a.ageS - b.ageS), slots = Math.max(0, keep - reg.list.length);
    const keepSet = new Set(sorted.slice(0, slots).map(r => r.id));
    return sorted.filter(r => r.ageS > olderS && r.ageS > 600 && !keepSet.has(r.id) && !r.locked);
  }
  function parseAge(s) {
    s = String(s == null ? '30d' : s).trim(); if (/^0+$/.test(s)) return 0; const m = /^(\d+(?:\.\d+)?)\s*([smhdw])?$/.exec(s); if (!m) return NaN;
    return parseFloat(m[1]) * ({ s: 1, m: 60, h: 3600, d: 86400, w: 604800 }[m[2] || 'd']);
  }
  /** The client preview of a prune; with apply, SL.act.pruneSessions sends it to the server. */
  function prune(olderStr, keep, apply) {
    const age = parseAge(olderStr); if (isNaN(age)) return { error: 'bad --older-than: ' + olderStr };
    const list = pruneCandidates(age, keep), mb = list.reduce((s, r) => s + r.mb, 0);
    if (apply && SL.act && SL.act.pruneSessions) SL.act.pruneSessions(olderStr, keep, true);
    return { list, mb, applied: false };
  }
  const recordedMb = () => reg.recordedMb != null ? reg.recordedMb : reg.recorded.reduce((s, r) => s + r.mb, 0);

  SL.sessions = { reg, Session, init, activate, make, drop, close, rename, needs, nowTod, todOf, setStart, placeholder, prune, pruneCandidates, parseAge, recordedMb,
    get list() { return reg.list; }, get active() { return reg.active; }, get(id) { return reg.byId[id]; },
    get recorded() { return reg.recorded; } };
})(SL);
