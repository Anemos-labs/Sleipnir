/* 50-sessions.js: SL.sessions, the multi-session registry and the two clocks of every session.
 *
 * A Session owns: its meta (chat flags, mode, model, budget, rules), a roster, an EVENT LOG (sorted by t, the only source of truth),
 * a WORLD model `wm` (events up to the world clock `wt`, no chat rows; the director and needs-you read it) and, for the active session
 * only, a VIEW model `m` (events up to the view clock `vt`, with chat rows; what the screen shows).
 *
 *   world  wt  advances 1 s per wall second for EVERY session, always (background sessions keep running and can raise questions)
 *   view   vt  advances by dtView (the governor's rate) for the active session; it is never ahead of wt; gap = wt - vt
 *
 * While the view is held, events pile up between vt and wt: they are queued in the log, not applied. On release the view first collapses
 * everything older than TIME_CONST.WINDOW seconds (applied instantly, one digest row in the chat), then replays the rest fast (see 20-clock.js).
 * Switching sessions rebuilds the view model of the new active session from its log (never from retained DOM). */
(function (SL) {
  'use strict';
  const U = SL.u, D = SL.D, { upperBound, deepCopy } = U;
  const R = () => SL.model, TC = SL.TIME_CONST;

  const reg = { list: [], byId: {}, active: null, counter: 0, recorded: deepCopy(D.recorded), pruned: [], wasCatching: false };

  /** Seconds since midnight "now" in the sample world (the shop's clock is the reference: 03:04:43 at load). */
  const nowTod = () => { const shop = reg.byId.shop; return shop ? shop.meta.t0 + shop.wt : 3 * 3600 + 4 * 60 + 43 + SL.time.wall / 1000; };
  const PLANS = {
    shop: D.plan,
    docs: ['List the docs and their links', 'Fix stale command names', 'Fix broken relative links', 'Check every link of every file', 'Merge each file after its check', 'Write the sweep report'],
    generic: ['Survey the project (recon)', 'Split the work', 'Implement', 'Verify each part', 'Review the diff', 'Verify the merged result'],
  };

  class Session {
    constructor(spec) {
      this.id = spec.id; this.name = spec.name; this.kind = spec.kind; this.sid = spec.sid;
      this.meta = Object.assign({ mode: 'default', effort: 'default', budget: 5, swarm: 8, isolation: 'none', verify: '', commit: false, mailman: false, trustProject: true, noMcp: false, roleModels: {}, rules: [], goalText: '' }, spec.meta || spec);
      delete this.meta.meta; this.meta.rules = (this.meta.rules || []).slice();
      this.plan = spec.plan || PLANS[this.kind === 'docs' ? 'docs' : this.kind === 'shop' ? 'shop' : 'generic'];
      this.ui = { chan: 'mgr', view: 'cockpit', scroll: {}, draft: '', planOpen: false, selAg: null, drawer: null, drawerTab: 'log', queued: [] };
      this.hold = { pinned: false, vtSaved: null };
      this.replay = null; this.hist = []; this.metaVer = 0; this.closed = false; this.runs = 0; this.actionLog = [];
      this.initRun();
    }
    /** (Re)start the run: roster, script, models. Used at creation and by /swarm N, /restart, /new. */
    initRun() {
      this.roster = SL.scripts.buildRoster(this);
      const sc = this.kind === 'shop' ? SL.scripts.shopScript(this) : this.kind === 'orders' ? SL.scripts.ordersScript(this) : this.kind === 'docs' ? SL.scripts.docsScript(this) : this.kind === 'resumed' ? SL.scripts.resumedScript(this) : SL.scripts.newRunScript(this);
      this.log = []; this.seq = 0; this.vqFree = sc.vqFree || 0; this.endgamed = false; this.runs++;
      sc.events.forEach(e => { e.seq = ++this.seq; this.log.push(e); });
      this.log.sort((a, b) => a.t - b.t || a.seq - b.seq);
      this.wt = sc.wt0 || 0; this.vt = this.wt; this.widx = 0; this.idx = 0; this.visW = 0; this.visV = 0;
      this.wm = R().newModel(this, { noChat: true }); this.m = null;
      this.stepWorld(this.wt, true);                               // the snapshot: everything up to wt0 applies at once
      this.ui.drawer = null;
      this.hold.pinned = false; this.hold.vtSaved = null; this.replay = null;
      if (reg.active === this) this.rebuild(this.wt);
      if (this.runs > 1) SL.bus.emit('roster-changed', this);
    }
    /** Insert events at the world time (t is clamped to >= wt). Returns the inserted events. */
    add(list) {
      list = Array.isArray(list) ? list : [list];
      list.forEach(e => { if (e.t == null || e.t < this.wt) e.t = this.wt; e.seq = ++this.seq; const p = Math.max(upperBound(this.log, e.t), this.widx); this.log.splice(p, 0, e); });
      return list;
    }
    /** World step: apply every event up to wt to the world model; the director and needs-you react here. */
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
      if ((ev.k === 'merge' || ev.k === 'task') && !this.endgamed && this.meta.goalText && this.wm.goal.state === 'active' && !this.wm.final && SL.calc.allMerged(this.wm)) {
        this.endgamed = true; this.add(SL.scripts.endgame(this, this.wt + 0.5));
      }
      if (ev.k === 'final') SL.bus.emit('session-done', this);
      this.metaVer += 0;      // reserved
    }
    advanceWorld(dt) { this.wt += dt; this.stepWorld(this.wt); }
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
      // events scheduled at exactly vt but not yet in the world pointer cannot be applied (idx < widx guard)
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
    state() { const w = this.wm; if (w.final || w.goal.state === 'met') return 'done'; if (SL.calc.openQuestion(w)) return 'ask'; if (this.wm.goal.state === 'paused') return 'paused'; return SL.calc.turnRunning(w) ? 'run' : 'idle'; }
    touch() { this.metaVer++; SL.bus.emit('meta', this); }
    setMeta(patch) { Object.assign(this.meta, patch); this.touch(); }
  }

  /* ---------------- registry ---------------- */
  function make(spec) { const S = new Session(spec); reg.list.push(S); reg.byId[S.id] = S; return S; }
  function init() {
    D.live.forEach(l => make({ id: l.id, name: l.name, kind: l.kind, sid: l.sid, meta: Object.assign({}, l, { goalText: l.kind === 'shop' ? D.goal : l.kind === 'docs' ? 'Sweep docs/: fix stale command names and broken relative links' : '' }) }));
    activate('shop');
  }
  /** Make a session active: the previous one drops its view model; the new one rebuilds from its log. */
  function activate(id) {
    const S = reg.byId[id]; if (!S) return null;
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
  function slug(s) { return String(s || 'session').toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '') || 'session'; }
  function newSid() {
    const t = nowTod(), s = Math.floor(t), hh = String(Math.floor(s / 3600) % 24).padStart(2, '0'), mm = String(Math.floor(s / 60) % 60).padStart(2, '0'), ss = String(s % 60).padStart(2, '0');
    return '20260102-' + hh + mm + ss + '-' + (U.hash('s' + reg.counter).toString(16) + '000000').slice(0, 6);
  }
  /** Create (and optionally activate) a session from the New dialog / resume / restart: spec = chat flags + name. */
  function create(spec, activateIt) {
    reg.counter++;
    let id = slug(spec.name || (spec.cwd || '').split('/').pop() || 'session'); if (reg.byId[id]) id += '-' + (reg.counter + 1);
    const swarm = spec.swarm == null ? 8 : spec.swarm;   /* the CLI's own default on a terminal: a manager and eight workers */
    const tod = nowTod();
    const meta = { cwd: spec.cwd || '~/projects/shop', model: spec.model || 'anthropic/claude-sonnet-5-5', mode: spec.mode || 'default', effort: spec.effort || 'default', budget: spec.budget == null ? 5 : spec.budget, swarm, isolation: spec.isolation || (swarm ? 'worktree' : 'none'), verify: spec.verify || '', commit: !!spec.commit, mailman: !!spec.mailman, trustProject: spec.trustProject !== false, noMcp: !!spec.noMcp, roleModels: Object.assign({}, spec.roleModels || {}), rules: (spec.rules || []).map(r => typeof r === 'string' ? { effect: 'allow', rule: r, origin: '--allow flag' } : r), goalText: spec.goalText || '', headless: false, t0: Math.floor(tod), started: U.tod(0, tod), launch: spec.launch || '', resumedFrom: spec.resumedFrom || null };
    const S = make({ id, name: spec.name || id, kind: spec.resumedFrom ? 'resumed' : 'new', sid: spec.sid || newSid(), meta });
    SL.bus.emit('sessions-changed');
    if (activateIt) activate(id);
    return S;
  }
  function close(id) {
    const S = reg.byId[id]; if (!S || reg.list.length <= 1) return false;
    const i = reg.list.indexOf(S); reg.list.splice(i, 1); delete reg.byId[id]; S.closed = true; S.m = null;
    if (reg.active === S) { reg.active = null; const nx = reg.list[Math.min(i, reg.list.length - 1)]; if (nx) activate(nx.id); }
    SL.bus.emit('sessions-changed'); return true;
  }
  function rename(id, name) { const S = reg.byId[id]; if (!S) return false; S.name = String(name).trim() || S.name; S.touch(); SL.bus.emit('sessions-changed'); return true; }
  /** Open questions across all sessions (world view), oldest first: the cross-session inbox. */
  function needs() {
    const out = []; reg.list.forEach(S => { const q = SL.calc.openQuestion(S.wm); if (q) out.push({ S, q, waiting: SL.calc.waiting(S.wm) }); });
    return out.sort((a, b) => a.q.t0 + a.S.meta.t0 - b.q.t0 - b.S.meta.t0);
  }

  /* ---------------- recorded sessions: list / prune arithmetic ---------------- */
  /** What `sessions prune --older-than X --keep N` would delete: older than X, but never the newest N sessions on disk (the live sessions are the newest, so
   * they use up slots), never one written in the last 10 minutes. */
  function pruneCandidates(olderS, keep) {
    const sorted = reg.recorded.slice().sort((a, b) => a.ageS - b.ageS), slots = Math.max(0, keep - reg.list.length);
    const keepSet = new Set(sorted.slice(0, slots).map(r => r.id));
    return sorted.filter(r => r.ageS > olderS && r.ageS > 600 && !keepSet.has(r.id));
  }
  function parseAge(s) {
    s = String(s == null ? '30d' : s).trim(); if (/^0+$/.test(s)) return 0; const m = /^(\d+(?:\.\d+)?)\s*([smhdw])?$/.exec(s); if (!m) return NaN;
    return parseFloat(m[1]) * ({ s: 1, m: 60, h: 3600, d: 86400, w: 604800 }[m[2] || 'd']);
  }
  function prune(olderStr, keep, apply) {
    const age = parseAge(olderStr); if (isNaN(age)) return { error: 'bad --older-than: ' + olderStr };
    const list = pruneCandidates(age, keep), mb = list.reduce((s, r) => s + r.mb, 0);
    if (apply) { const ids = new Set(list.map(r => r.id)); reg.recorded = reg.recorded.filter(r => !ids.has(r.id)); reg.pruned = reg.pruned.concat(list); SL.bus.emit('recorded-changed'); }
    return { list, mb, applied: !!apply };
  }
  const recordedMb = () => reg.recorded.reduce((s, r) => s + r.mb, 0);

  SL.sessions = { reg, Session, init, activate, create, close, rename, needs, nowTod, prune, pruneCandidates, parseAge, recordedMb, PLANS, make,
    get list() { return reg.list; }, get active() { return reg.active; }, get(id) { return reg.byId[id]; },
    get recorded() { return reg.recorded; } };
})(SL);
