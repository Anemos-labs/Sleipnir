/* 11-data-adapter.js: SL.D, the one object the engine reads sample data from.
 * It starts from the built-in fixture (SL.FX) and lets window.SLDATA / window.SLCLISPEC (the data pack) override pieces of it,
 * each only when the shape matches what the engine needs. Nothing else in the app touches window.SLDATA.
 * To teach the core a new data-pack field: add one line to `adopt()` below, never a read elsewhere. */
(function (SL) {
  'use strict';
  const FX = SL.FX;
  const W = (typeof window !== 'undefined') ? window : {};
  const pack = W.SLDATA && typeof W.SLDATA === 'object' ? W.SLDATA : null;
  const isArr = (x, n) => Array.isArray(x) && x.length >= (n || 1);
  const isObj = x => x && typeof x === 'object' && !Array.isArray(x);

  const D = {
    pack: !!pack, raw: pack,
    roles: FX.ROLES, roleOrder: FX.ROLE_ORDER, prices: FX.PRICES, roster: FX.ROSTER, nreq: FX.NREQ, hitSeries: FX.HITSERIES,
    layers: FX.LAYERS, g5: FX.G5TOK, goal: FX.GOAL, plan: FX.PLAN, tasks: FX.TASKS, deps: FX.DEPS, ckpts: FX.CKPTS,
    code: FX.CODE, orderCode: FX.ORDERS_CODE, treeShop: FX.TREE_SHOP, treeOrders: FX.TREE_ORDERS, streamTest: FX.STREAM_TEST,
    live: FX.LIVE, recorded: FX.RECORDED, models: FX.MODELS, providers: FX.PROVIDERS, roleModels: FX.ROLE_MODELS,
    modes: FX.MODES, rules: FX.RULES, testsPreset: FX.TESTS_PRESET, trustFiles: FX.TRUST_FILES, mcp: FX.MCP, skills: FX.SKILLS,
    shortcuts: FX.SHORTCUTS, config: FX.CONFIG, schedule: FX.SCHEDULE, spec: FX.SPEC, outputs: {}, slash: null, extra: {},
  };
  D.slash = [].concat.apply([], FX.SLASH.map(g => g[1].map(c => ({ cmd: c[0], args: c[1], desc: c[2], group: g[0] }))));

  function adopt() {
    const spec = isObj(W.SLCLISPEC) ? W.SLCLISPEC : (pack && isObj(pack.cliSpec) ? pack.cliSpec : null);
    if (spec && isArr(spec.commands)) { const ch = spec.commands.find(c => c.path.join(' ') === 'chat'), sw = ch && ch.flags.find(f => f.name === 'swarm'); if (sw && sw.default == null) sw.default = 8;   /* on a terminal `sleipnir chat` starts a manager and eight workers */
      D.spec = spec; if (isArr(spec.chatSlash)) D.slash = spec.chatSlash; }
    if (!pack) return;
    if (isObj(pack.outputs)) D.outputs = pack.outputs;
    if (isObj(pack.prices) && isObj(pack.prices.manager) && isObj(pack.prices.worker)) { const p = (o, k) => ({ in: o.inPerM, cached: o.readPerM, out: o.outPerM }); D.prices = { mgr: p(pack.prices.manager), worker: p(pack.prices.worker), sample: true }; }
    if (isArr(pack.team, 9) && pack.team[0].prompt != null && pack.team[0].read != null) {   /* the one table: the pack's token counts and snapshot state replace the built-in ones when it carries the same ids; the built-in order (legs) stays */
      const by = {}; pack.team.forEach(t => { by[t.id] = t; });
      if (D.roster.every(r => by[r.id])) {
        D.roster = D.roster.map(r => { const t = by[r.id], o = Object.assign({}, r, { role: t.role, state: t.state, task: t.task, doing: t.doing, prompt: t.prompt, read: t.read, out: t.out }); if (isArr(t.scopeGlobs) || t.readOnly) o.scope = t.scope; return o; });
        D.g5 = Object.assign({}, D.g5); pack.team.forEach(t => { if (isObj(t.layerTokens) && t.layerTokens.G5 > 0) D.g5[t.id] = t.layerTokens.G5; });
      }
    }
    if (isObj(pack.hitSeries) && pack.hitSeries.mgr && pack.hitSeries['be-2']) D.hitSeries = pack.hitSeries;
    if (isArr(pack.layers, 6) && pack.layers[0].tokens != null) D.layers = D.layers.map((l, i) => { const p = pack.layers.find(x => x.id === l.id); return p ? Object.assign({}, l, { tok: p.tokens, note: p.holds || l.note }) : l; });
    if (isArr(pack.models, 8) && pack.models[0] && pack.models[0].ref) D.models = pack.models.map(m => ({ ref: m.ref, ctx: m.context, in: m.priceKnown === false ? null : m.inPerM, out: m.priceKnown === false ? null : m.outPerM, cached: m.priceKnown === false ? null : m.cachedPerM, tools: !!m.tools, reasoning: !!m.reasoning, fav: !!m.favourite, sample: m.sample !== false }));
    if (isArr(pack.providers, 3) && pack.providers[0] && pack.providers[0].name) D.providers = pack.providers.map(p => ({ id: p.name, name: p.name.charAt(0).toUpperCase() + p.name.slice(1), base: p.baseUrl, key: p.signedIn ? 'stored' : p.keyState === 'env' ? 'env' : p.keyState === 'stored' ? 'stored' : 'none', env: p.keyEnv, state: p.signedIn ? 'signed in' : (p.keyWhere || p.keyState), note: p.notes || '' }));
    if (isArr(pack.shortcuts, 5) && pack.shortcuts[0].action) D.shortcuts = pack.shortcuts.map(s => [s.web && s.web !== s.keys ? s.keys + ' · ' + s.web : s.keys, s.action]);
    if (isObj(pack.mcp) && isArr(pack.mcp.servers, 2)) D.mcp = pack.mcp.servers.map(s => ({ name: s.name, origin: s.origin || s.from, transport: s.transport, state: s.state === 'ready' ? 'running' : s.state, tools: s.toolNames || [] }));
    if (isArr(pack.skills, 2) && pack.skills[0].name) D.skills = pack.skills.map(s => ({ name: s.name, desc: s.summary, from: s.source + (s.youOnly ? ' (you only)' : '') }));
    if (isObj(pack.config) && isArr(pack.config.effective, 10)) D.config = pack.config.effective.filter(r => /^(models|permissions|swarm|budget|cache\.(warm|breakpoints|affinity)|mcp\.enabled)/.test(r.key)).slice(0, 20).map(r => [r.key, String(r.value), r.layer + (r.file && r.file !== 'built-in' ? ' · ' + r.file : '')]);
    if (isObj(pack.trust) && isArr(pack.trust.files, 2)) D.trustFiles = pack.trust.files.map(f => [f.path, f.hash.slice(0, 8)]);
    if (isObj(pack.permissions) && isObj(pack.permissions.testsPreset) && isArr(pack.permissions.testsPreset.rules, 3)) D.testsPreset = pack.permissions.testsPreset.rules;
    if (isObj(pack.sessions) && isArr(pack.sessions.recorded, 6) && pack.sessions.recorded[0].id && pack.sessions.recorded[0].prompt != null) {
      const now = Date.parse(isObj(pack.meta) && pack.meta.sampleNow || '2026-01-02T03:04:43') / 1000, map = r => ({ id: r.id, first: r.prompt, model: r.model, cost: r.cost, mb: (r.size || 0) / 1048576, ageS: Math.max(0, now - Date.parse(r.lastWritten) / 1000), agents: r.kind === 'single' ? 1 : (r.swarm || 0) + 1, resumable: !!r.resumable, dur: Math.round((r.requests || 10) * 6.5), interrupted: !!r.ended && r.ended !== 'exit' });
      D.recorded = pack.sessions.recorded.concat(isArr(pack.sessions.archive) ? pack.sessions.archive : []).map(map).sort((a, b) => a.ageS - b.ageS);
    }
    ['recon', 'friction', 'sim', 'inspect', 'replay', 'rl', 'doctor', 'update', 'files', 'config', 'trust', 'permissions', 'goal', 'question', 'conversation', 'channels', 'script', 'mail', 'mailFuture', 'orders', 'docsSweep', 'projects', 'real', 'fmt', 'costOf', 'totals', 'checkpoints', 'checkpointNote', 'fileAt', 'diffText', 'stepDiff', 'diffSince', 'unified', 'blame', 'treeAt', 'schedule', 'cronNext', 'doctorText', 'commands', 'hooks', 'skillsBudget', 'providerNote', 'providerKeyVars', 'efforts', 'roleDefs', 'serviceRoles', 'sessionRule', 'managerRefusal', 'mcp', 'skills', 'models', 'modelsFilter', 'providers', 'roles', 'roleModels', 'sessions', 'sessionTotals', 'teamSummary', 'legs', 'mailStats'].forEach(k => { if (pack[k] != null) D.extra[k] = pack[k]; });
  }
  try { adopt(); } catch (e) { console.error('data adapter', e); }
  /* The pack's command outputs keep their own mutable state (trust ledger, schedule, sessions ...). The runner passes it back on every call;
   * packSync() removes pruned sessions from it, so `sleipnir sessions` after a prune agrees with the live registry. */
  D.packState = pack && typeof pack.newState === 'function' ? (function () { try { return pack.newState(); } catch (e) { return null; } })() : null;
  D.ctx = () => (D.packState ? { state: D.packState } : {});
  D.packSync = ids => { if (D.packState && isArr(D.packState.sessions)) { const gone = {}; ids.forEach(i => { gone[i] = 1; }); D.packState.sessions = D.packState.sessions.filter(x => !gone[x.id]); } };

  /** Command output for `sleipnir <path>`: pack output when present, else null (runner falls back to its built-in sample). */
  D.output = path => { const f = D.outputs && D.outputs[Array.isArray(path) ? path.join(' ') : path]; return typeof f === 'function' ? f : null; };
  /** Find the model catalogue entry for a ref. */
  D.model = ref => D.models.find(m => m.ref === ref) || null;
  /** Layer tokens of an agent's prompt (G5 differs per agent). */
  D.layerToks = id => D.layers.map(l => l.id === 'G5' ? (D.g5[id] || 800) : l.tok);
  SL.D = D;
})(SL);
