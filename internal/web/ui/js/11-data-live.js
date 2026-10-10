/* 11-data-live.js: SL.D, SL.G and SL.data: the page's catalogues as live caches over the API.
 *
 * SL.D keeps the field names the views read; its values come from the server. Reads are synchronous: a cache that has not arrived yet
 * reads as its empty shape (an empty list, a zero), and when it arrives SL.G.ver is bumped and the loop re-renders through the views'
 * own update hooks. The tables that are the UI's own copy (the five modes, the keys list, the names and colours of the six prompt
 * layers, the built-in roles as a fallback) live here as constants; nothing here is invented data.
 *
 * SL.data.load(name, opts) fills one cache (names below); SL.data.forPage(page) loads what a Settings page or a Tools view reads. */
(function (SL) {
  'use strict';

  /* ---- the UI's own tables ---- */
  const MODES = [
    { id: 'default', desc: 'reads and read-only commands go through; everything else asks', danger: 0 },
    { id: 'accept-edits', desc: 'also edits inside the project and build and test commands; ask and deny rules still win', danger: 0 },
    { id: 'plan', desc: 'read-only: writes, network and commands that are not provably read-only are refused', danger: 0 },
    { id: 'bypass', desc: 'no questions, except about the very dangerous (sudo, deleting the workspace, a forced push, disk tools, shutdown); deny rules still apply', danger: 1 },
    { id: 'yolo', desc: 'asks nothing at all, the very dangerous included; deny rules and guarded paths still refuse. For sandboxes only', danger: 2 },
  ];
  /** The roles the harness builds in (internal/swarm/roles.go); the server's list replaces it. */
  const ROLES = {
    manager: { code: 'mgr', ro: false, desc: 'plans, spawns, merges; edits no file' },
    backend: { code: 'be', ro: false, desc: 'server code' },
    frontend: { code: 'fe', ro: false, desc: 'pages and client code' },
    fullstack: { code: 'fs', ro: false, desc: 'both sides of one feature' },
    tester: { code: 'ts', ro: false, desc: 'tests' },
    reviewer: { code: 'rv', ro: true, desc: 'read-only review' },
    scout: { code: 'sc', ro: true, desc: 'read-only survey' },
    docs: { code: 'dc', ro: false, desc: 'documentation' },
  };
  const ROLE_ORDER = ['scout', 'backend', 'frontend', 'tester', 'reviewer', 'docs', 'fullstack'];
  /** The six prompt layers G0..G5 as the page draws them (the hot tail is folded into G5). Token sizes come from the model. */
  const LAYERS = [
    { id: 'G0', name: 'constitution', note: 'the constitution and the universal tool list (every agent sends the same tools array, byte for byte)', col: 'var(--mgr)' },
    { id: 'G1', name: 'shared pin', note: 'the project map (recon) and the instruction files: AGENTS.md, the skills listing', col: 'var(--be)' },
    { id: 'G2', name: 'role pin', note: 'the role instructions (one per role: all backend workers share one)', col: 'var(--fe)' },
    { id: 'G3', name: 'notes', note: "the agent's private notes, rewritten at a compaction", col: 'var(--ts)' },
    { id: 'G4', name: 'spine', note: 'the summary spine: what compactions folded away', col: 'var(--warm)' },
    { id: 'G5', name: 'thread', note: 'the conversation and tool results, verbatim, append-only between rebases', col: 'var(--rv)' },
  ];
  /** The keys the Help sheet and Settings › Keys list, after the rows those two draw themselves. The first column holds keys and nothing
   *  else: `·` between alternatives, a space between keys that are pressed one after another, the key's name as people write it (Enter,
   *  Esc, ctrl+k, shift+Left); what a key does goes in the second column. help-keys.test.mjs fails when a key named here is one the page
   *  does nothing with (internal/parity/testdata/keys-web.json). */
  const SHORTCUTS = [
    ['Enter', 'send the message'],
    ['\\ at end of line · alt+enter · ctrl+j', 'a newline in the message'],
    ['Up / Down', 'in the message box: recall the lines sent before'],
    ['ctrl+r', 'search the lines sent before'],
    ['/ · ctrl+k', 'the commands of the session; the command palette'],
    ['@', 'complete a path from the project tree'],
    ['shift+tab', 'step the permission mode default → accept-edits → plan (never bypass or yolo)'],
    ['ctrl+t · alt+t', 'the stats page (the same as /stats); the browser keeps ctrl+t'],
    ['ctrl+g · alt+g', 'the team cockpit (the same as /agents)'],
    ['ctrl+o', 'expand or collapse the output of the tools in the conversation'],
    ['Esc', 'interrupt the running turn (and pause a goal); releases a pinned hold and closes overlays first'],
    ['ctrl+c', 'at the prompt: discard the line; twice within 1.5 s: close the session (it asks first); during a turn: cancel the turn; with text selected: copy'],
    ['ctrl+d', 'on an empty line: quit, which closes the session (it asks first)'],
    ['1 2 3 4', 'answer a question (2 only where it would remember something, 4 only for a build or test command); esc is no; letters never answer; keys are taken only after the keyboard has been quiet for 0.8 s'],
    ['o c m b r s , .', 'a view: Cockpit, Cache, Mail, Board, Replay, Sessions, Settings, Tools'],
    ['Up / Down', 'outside the message box: choose the agent the views are about'],
    ['space', 'over the conversation: pin the hold (Esc releases it); elsewhere: pause or resume the view'],
    ['Left Right · shift+Left shift+Right', 'replay: seek 10 s (shift: a minute)'],
    ['+ - · Home End', 'replay: speed (+ -), start (Home), live (End)'],
    ['?', 'list the keys and the commands'],
  ];
  const ZERO6 = [0, 0, 0, 0, 0, 0];

  /** The model the layer getters read: the active tab's view model, else its world model. */
  const curModel = () => { const S = SL.sessions && SL.sessions.active; return S ? (S.m || S.wm) : null; };
  const toks = (m, id) => { const A = m && m.ag[id]; return A && Array.isArray(A.layers) && A.layers.length === 6 ? A.layers : null; };

  const X = {
    permissions: null, trust: null, mcp: null, skills: [], skillsBudget: null, commands: [], hooks: null, providers: [], providerNote: '',
    config: null, schedule: null, projects: [], doctor: { endpoints: [] }, update: null,
    /** The next run time of a cron expression from the memo of GET /api/schedule/next; undefined while unknown (the request starts). */
    cronNext(expr) {
      const k = String(expr || '').trim(); if (!k) return undefined;
      if (Object.prototype.hasOwnProperty.call(cron, k)) return cron[k];
      cron[k] = undefined; SL.api.get('/api/schedule/next?cron=' + encodeURIComponent(k)).then(r => { cron[k] = r.ok ? r.data : { ok: false, error: r.message }; bump('cron'); });
      return undefined;
    },
  };
  const cron = {};

  const D = {
    live: true,
    get roles() { return D._roles || ROLES; }, set roles(v) { D._roles = v; },
    roleOrder: ROLE_ORDER.slice(),
    models: [], roleModels: {}, efforts: [],
    modes: MODES, shortcuts: SHORTCUTS,
    rules: [], testsPreset: [],
    skills: [],
    prices: { mgr: { in: 0, cached: 0, out: 0 }, worker: { in: 0, cached: 0, out: 0 }, sample: false },
    spec: { commands: [], chatSlash: [], exitCodes: [] },
    /** The slash commands of the active tab (GET .../slash), else the spec's built-ins. */
    get slash() { const S = SL.sessions && SL.sessions.active; const own = S && slashOf[S.id]; return own || D.spec.chatSlash || []; },
    /** The six layers with the active tab's manager's latest token sizes (G0..G2 are shared by every agent). */
    get layers() { const t = toks(curModel(), 'mgr') || ZERO6; return LAYERS.map((l, i) => ({ id: l.id, name: l.name, tok: t[i], note: l.note, col: l.col })); },
    /** G5 by agent: {id: tokens}. */
    get g5() { const m = curModel(), out = {}; if (m) m.order.forEach(id => { const t = toks(m, id); if (t) out[id] = t[5]; }); return out; },
    extra: X,
    /* fields a reader may still look up: always empty here, because the live caches (SL.G, SL.data) hold this data */
    recorded: [], schedule: [], outputs: {}, mcp: [], providers: [], config: [], trustFiles: [],
  };
  /** The token sizes of an agent's latest prompt by layer, else six zeros. */
  D.layerToks = id => (toks(curModel(), id) || ZERO6).slice();
  D.model = ref => D.models.find(m => m.ref === ref) || null;
  D.output = () => null;
  D.ctx = () => ({});
  D.packSync = () => {};
  D.LAYERS = LAYERS;
  const slashOf = {};

  /** State shared by every session. */
  const G = SL.G = {
    ver: 0, mcp: [], favs: new Set(), trust: [], trustDirs: [], providers: [], schedule: [], roleModels: {}, history: [], runs: [],
    sched: { jobs: [], daemon: {}, logs: [], n: 0 },
  };

  const bump = what => { G.ver++; if (SL.loop) SL.loop.dirty = true; if (SL.bus) SL.bus.emit('data', what); };
  const day = d => { const m = /^\d{4}-(\d\d)-(\d\d)/.exec(d || ''); return m ? ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'][+m[1] - 1] + ' ' + (+m[2]) : (d || ''); };
  const tabPath = id => '/api/sessions/' + encodeURIComponent(String(id));
  const activeId = () => { const S = SL.sessions && SL.sessions.active; return S && !S.placeholder && !S.recorded ? S.id : null; };

  /* ---- mappers from the server's views to the shapes the views read ---- */
  const map = {
    models(v) {
      D.models = (v.models || []).map(r => ({ ref: r.ref, provider: r.provider, ctx: r.ctx || 0, in: r.in == null ? null : r.in, out: r.out == null ? null : r.out, cached: r.cached == null ? null : r.cached, tools: !!r.tools, reasoning: !!r.reasoning, fav: !!r.fav, plan: !!r.plan, sample: false }));
      G.favs = new Set(v.favs || D.models.filter(m => m.fav).map(m => m.ref));
      D.models.forEach(m => { m.fav = G.favs.has(m.ref); });
      if (Array.isArray(v.roles) && v.roles.length) { const r = {}; v.roles.forEach(x => { r[x.name] = { code: x.code, ro: !!x.ro, desc: x.desc || '' }; }); if (!r.manager) r.manager = ROLES.manager; D.roles = r; }
      if (Array.isArray(v.roleOrder) && v.roleOrder.length) D.roleOrder = v.roleOrder.slice();
      D.roleModels = Object.assign({}, v.roleModels || {}); G.roleModels = Object.assign({}, D.roleModels);
      D.efforts = (v.efforts || []).slice(); D.modelErrors = (v.errors || []).slice(); D.modelsNote = v.configNote || '';
      SL.bus.emit('models-changed');
    },
    providers(v) {
      X.providers = (v.providers || []).slice(); X.providerNote = v.providerNote || '';
      G.providers = X.providers.map(p => Object.assign({}, p)); D.providers = G.providers;
    },
    projects(v) { X.projects = (v.projects || []).map(p => Object.assign({ root: p.dir }, p)); },
    recorded(v) {
      const reg = SL.sessions && SL.sessions.reg; if (!reg) return;
      reg.recorded = (v.recorded || []).map(r => Object.assign({}, r, { first: r.first || '', agents: r.agents || 0, mb: r.mb || 0 })).sort((a, b) => a.ageS - b.ageS);
      reg.recordedMb = typeof v.mb === 'number' ? v.mb : null;
      D.recorded = reg.recorded;
      SL.bus.emit('recorded-changed');
    },
    cli(v) { D.spec = { generatedFrom: v.generatedFrom || '', generator: v.generator || '', commands: v.commands || [], chatSlash: v.chatSlash || [], exitCodes: v.exitCodes || [] }; },
    permissions(v) {
      X.permissions = v; const r = v.rules || {};
      D.rules = [].concat(r.deny || [], r.ask || [], r.allow || []).map(x => Object.assign({ fixed: true }, x));
      D.testsPreset = (v.testsPreset && v.testsPreset.rules) || [];
    },
    trust(v) {
      X.trust = v;
      G.trust = (v.files || []).map(f => ({ file: f.path, hash: String(f.hash || '').slice(0, 8), state: 'trusted', kind: f.kind, bytes: f.bytes }));
      D.trustFiles = G.trust.map(t => [t.file, t.hash]);
      G.trustDirs = (v.ledger || []).map(l => ({ dir: l.dir, files: l.files, saved: l.saved, now: l.now, state: l.state === 'trusted' ? 'trusted (' + day(l.saved) + ')' : /^changed/.test(l.state) ? 'changed since your yes: ' + (l.now || l.state.replace(/^changed:\s*/, '')) : /^gone/.test(l.state) ? 'gone: ' + (l.now || 'directory is gone') : l.state }));
      SL.bus.emit('trust-changed');
    },
    mcp(v) {
      X.mcp = v;
      G.mcp = (v.servers || []).map(s => ({ name: s.name, origin: s.origin || s.from || '', transport: s.transport, state: s.state, tools: s.tools || [], raw: s }));
      D.mcp = G.mcp; SL.bus.emit('mcp-changed');
    },
    skills(v) {
      X.skills = (v.skills || []).slice(); X.skillsBudget = v.skillsBudget || null; X.commands = (v.commands || []).slice(); X.hooks = v.hooks || null;
      D.skills = X.skills.map(s => ({ name: s.name, desc: s.summary, from: s.source + (s.youOnly ? ' (you only)' : '') }));
    },
    config(v) { X.config = v; D.config = (v.effective || []).map(r => [r.key, typeof r.value === 'string' ? r.value : JSON.stringify(r.value), r.layer + (r.file && r.file !== 'built-in' ? ' · ' + r.file : '')]); },
    schedule(v) {
      X.schedule = v; G.sched = { jobs: (v.jobs || []).slice(), daemon: v.daemon || {}, logs: (v.logs || []).slice(), n: (v.jobs || []).length };
      G.schedule = G.sched.jobs; D.schedule = G.sched.jobs;
    },
    doctor(v) { X.doctor = { endpoints: (v.endpoints || []).slice() }; },
    runs(v) { G.runs = (v.runs || []).map(r => ({ id: r.id, cmd: r.cmd, exit: r.exit, ms: r.ms, path: r.path, flags: r.flags, running: !!r.running })); },
    update(v) { X.update = v; },
  };

  /* ---- what each cache reads ---- */
  const ROUTES = {
    models: () => '/api/models', providers: () => '/api/providers', projects: () => '/api/projects', recorded: () => '/api/recorded', cli: () => '/api/cli',
    schedule: () => '/api/schedule', doctor: () => '/api/doctor/endpoints', runs: () => '/api/runs', update: () => '/api/update',
    permissions: id => tabPath(id) + '/permissions', trust: id => tabPath(id) + '/trust', mcp: id => tabPath(id) + '/mcp', skills: id => tabPath(id) + '/skills', config: id => tabPath(id) + '/config',
  };
  const PER_TAB = new Set(['permissions', 'trust', 'mcp', 'skills', 'config']);
  const inflight = {}, loaded = {};

  /**
   * Fill one cache from the server. name: models providers projects recorded cli schedule doctor runs update (global) or permissions
   * trust mcp skills config (per tab: opts.tab, else the active tab) or slash (per tab). Resolves true when the cache was filled.
   * A failure leaves the previous value and is reported with an err toast only when opts.loud is set.
   */
  function load(name, opts) {
    opts = opts || {};
    if (name === 'slash') return loadSlash(opts.tab || activeId(), opts);
    const route = ROUTES[name]; if (!route) return Promise.resolve(false);
    const tab = PER_TAB.has(name) ? (opts.tab || activeId()) : null;
    if (PER_TAB.has(name) && !tab) return Promise.resolve(false);
    const key = name + '|' + (tab || '');
    if (inflight[key] && !opts.force) return inflight[key];
    const p = SL.api.get(route(tab)).then(r => {
      delete inflight[key];
      if (!r.ok) { if (opts.loud && r.status) SL.ui && SL.ui.toast && SL.ui.toast(r.message, 'err'); return false; }
      if (tab && tab !== activeId() && !opts.keep) return false;   // the person switched tabs meanwhile: the per-tab cache belongs to the active tab
      try { map[name](r.data || {}); } catch (e) { console.error('data ' + name, e); return false; }
      loaded[key] = Date.now(); bump(name); return true;
    });
    inflight[key] = p; return p;
  }
  function loadSlash(tab, opts) {
    if (!tab) return Promise.resolve(false);
    const key = 'slash|' + tab; if (inflight[key] && !(opts && opts.force)) return inflight[key];
    const p = SL.api.get(tabPath(tab) + '/slash').then(r => { delete inflight[key]; if (!r.ok) return false; slashOf[tab] = (r.data && r.data.slash) || []; bump('slash'); if (SL.palette && SL.palette.build) SL.palette.build(); return true; });
    inflight[key] = p; return p;
  }
  /** Forget what belongs to a tab that went away. */
  function forget(tab) { delete slashOf[tab]; }

  /** The caches a page reads (loaded when the page opens, then every 30 s while it is open). */
  const PAGES = {
    models: ['models'], roles: ['models'], budget: [], permissions: ['permissions'], trust: ['trust', 'projects'], run: ['models'], mcp: ['mcp'],
    skills: ['skills'], providers: ['providers'], config: ['config'], look: [],
    settings: ['models', 'providers', 'permissions', 'trust', 'mcp', 'skills', 'config'],
    tools: ['cli', 'schedule', 'doctor', 'runs'], runner: ['cli', 'runs'], doctor: ['doctor', 'models'], schedule: ['schedule', 'projects', 'models'], sessions: ['recorded'],
  };
  function forPage(page, opts) { return Promise.all((PAGES[page] || []).map(n => load(n, opts))); }
  /** Per-tab caches after a tab switch (only those already loaded once, so that a page that was never opened costs nothing). */
  function onSwitch() {
    const tab = activeId(); if (!tab) return;
    loadSlash(tab);
    PER_TAB.forEach(n => { if (Object.keys(loaded).some(k => k.indexOf(n + '|') === 0)) load(n, { tab }); });
  }

  SL.D = D;
  SL.data = { load, forPage, onSwitch, forget, PAGES, map, X, LAYERS, MODES, ROLES, loaded };
})(SL);
