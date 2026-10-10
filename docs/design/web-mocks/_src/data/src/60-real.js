/* ---------------------------------------------------------------------------------------------------------------
 * REAL captures (bin/sleipnir on the sample projects), exposed as S.real (raw text blocks keyed by command line),
 * S.sim (the sim grid), S.replay (a distilled event timeline) and S.rl (a run you can read), plus the structured views
 * built from them: recon, friction, inspect, doctor, update, config.
 * ------------------------------------------------------------------------------------------------------------- */
var REAL = /*REAL:build/real.json*/;
S.real = REAL;
S.sim = /*REAL:build/sim.json*/;
S.replay = /*REAL:build/replay.json*/;
S.rl = /*REAL:build/rl.json*/;

/** realBlock(section, cmd): the captured text of `sleipnir <cmd>` (REAL), or null. section: basic recon config trust mcp schedule models init */
S.realBlock = function (section, cmd) {
  var list = REAL[section] || [];
  for (var i = 0; i < list.length; i++) if (list[i].cmd === cmd) return list[i];
  return null;
};

/* ---- recon: the real project survey of the shop (at the start of the session) ------------------------------------------- */
S.recon = (function () {
  var b = S.realBlock('recon', 'recon');
  var m = /\n?(\[\d+ tokens, \d+ files considered\])$/.exec(b.text);
  var text = m ? b.text.slice(0, m.index) : b.text;
  var secs = [], cur = null;
  text.split('\n').forEach(function (l) { var h = /^## (.+)$/.exec(l); if (h) { cur = { key: h[1], lines: [] }; secs.push(cur); } else if (cur) cur.lines.push(l); });
  secs.forEach(function (s) { s.text = s.lines.join('\n').replace(/\n+$/, ''); delete s.lines; });
  var budgets = {};
  ['recon', 'recon --budget 1000', 'recon --budget 2000', 'recon --budget 10000'].forEach(function (c) {
    var bl = S.realBlock('recon', c); if (!bl) return;
    var mm = /\n?(\[(\d+) tokens, (\d+) files considered\])$/.exec(bl.text);
    budgets[c === 'recon' ? 5000 : +c.replace(/\D+/g, '')] = { text: mm ? bl.text.slice(0, mm.index) : bl.text, footer: mm ? mm[1] : '', tokens: mm ? +mm[2] : 0, files: mm ? +mm[3] : 0 };
  });
  return { footer: m ? m[1] : '', tokens: m ? +/\[(\d+)/.exec(m[1])[1] : 0, files: m ? +/(\d+) files/.exec(m[1])[1] : 0, sectionKeys: secs.map(function (s) { return s.key; }), budgets: budgets, project: '~/projects/shop (at the base commit, before the team started)', layer: 'G1 (shared pin)', note: 'REAL output of `sleipnir recon` on the sample shop project; the survey is pinned in the shared layer (G1) with the instruction files' };
})();

/* ---- friction (of the recorded demo session) --------------------------------------------------------------------------- */
S.friction = { session: '20260101-171204-7c1e3a', report: REAL.friction.json, note: 'REAL: `sleipnir friction` on the recorded session of `sleipnir demo --scenario shop` (a team of 9 agents), shown as the sample user\'s session 20260101-171204-7c1e3a' };
/** frictionText(report): the text view, same format as cmd/sleipnir printFriction. */
S.frictionText = function (rep) {
  var o = ['friction in ' + rep.sessions + ' sessions (' + rep.events + ' events, ' + rep.requests + ' model requests)', ''];
  if (!rep.findings.length) { o.push('nothing that cost anything: no refusals, failed tool calls, stuck or cancelled runs, retries or repeated reads.'); return o.join('\n'); }
  var rows = [['#', 'SCORE', 'CATEGORY', 'COUNT', 'SESSIONS', 'WASTED', 'WHAT']];
  rep.findings.forEach(function (f, i) { rows.push([String(i + 1), String(Math.round(f.score)), f.category, String(f.count), String(f.sessions), String(f.wasted_requests), f.key]); });
  o = o.concat(F.tabwriter(rows, 2));
  o.push('', 'score = count x severity squared (1 a cost, 2 waste, 3 a stop) x (1 + requests wasted per occurrence); WASTED is model requests.', '');
  rep.findings.slice(0, 5).forEach(function (f, i) {
    o.push((i + 1) + '. ' + f.category + ': ' + f.key);
    if (f.hint) o.push('   ' + f.hint);
    (f.examples || []).forEach(function (e) { o.push('   ' + e.session + ' seq ' + e.seq + ': ' + e.detail); });
  });
  return o.join('\n');
};

/* ---- inspect (of the same recorded session): what a UI needs from `inspect --json` ------------------------------------------- */
S.inspect = REAL.inspect;
S.inspect.note = 'REAL: `sleipnir inspect --json` on the recorded demo session 20260101-171204-7c1e3a (55 requests); series.points is [i, t_ms, agents, prompt, in, read, out, hit, usd, anomaly, commits]; the full summary also lists the event types (38) and the RL fields';

/* ---- doctor ------------------------------------------------------------------------------------------------------------ */
(function () {
  var steps = REAL.doctor.mockDeep.steps;
  // sample step lists are stored compact ([name, ms, in, cached, out, cost]) and expanded when a report is asked for
  var tpl = function (scale, o) {
    o = o || {};
    var miss = o.miss || [];
    return steps.map(function (s, i) {
      var cached = s.usage.cache_read_tokens;
      if (miss.indexOf(s.name) >= 0 || (o.coldMiss && /^warmup-cold/.test(s.name))) cached = 0;   // a prefix the endpoint did not serve from its cache
      return [s.name, Math.max(1, Math.round(s.took_ns / 1e6 * scale + (o.base || 0) + (i % 5) * 7)), s.usage.input_tokens + s.usage.cache_read_tokens, cached, s.usage.output_tokens, s.cost_usd];
    });
  };
  var expand = function (rep) {
    if (!rep.stepsC) return rep;
    var r = Object.assign({}, rep); r.steps = rep.stepsC.map(function (s) { return { name: s[0], ok: true, ms: s[1], in: s[2], cached: s[3], out: s[4], cost: s[5] }; }); delete r.stepsC; return r;
  };
  /** doctorText(report): `rep.Text()` of internal/provider/probe, ported 1:1. */
  S.doctorText = function (rep) {
    rep = expand(rep); var f = rep.findings, yn = function (b) { return b ? 'yes' : 'NO'; };
    var o = ['Endpoint probe for ' + rep.model, '  streaming            ' + yn(f.streaming) + ' (first byte ' + f.ttfb + ')', '  usage reported       ' + yn(f.usage_reported), '  exact cost reported  ' + yn(f.cost_reported), '  tool calling         ' + yn(f.tools) + ' (round trip ' + yn(f.tool_round_trip) + ')', '  cached tokens shown  ' + yn(f.cached_tokens_reported)];
    var verdict = yn(f.cache_works);
    if (f.cache_repeat_hits > 0 && f.cache_repeat_hits < f.cache_repeats) verdict = 'partly';
    o.push('  prefix cache works   ' + verdict + ' (' + f.cache_repeat_hits + ' of ' + f.cache_repeats + ' repeat requests hit; ' + Math.round(f.cache_hit_ratio * 100) + '% of their prompt tokens)');
    if (f.cache_granularity > 0) o.push('  cache granularity    ~' + f.cache_granularity + ' tokens');
    if (f.min_cache_prefix > 0) o.push('  smallest cached size ~' + f.min_cache_prefix + ' tokens');
    if (f.warmup_needed !== null && f.warmup_needed !== undefined) o.push('  warm-up before burst ' + yn(f.warmup_needed));
    o.push('  reasoning exposed    ' + yn(f.reasoning_seen) + ' (structured details ' + yn(f.reasoning_details) + ')');
    if (f.token_ids || f.token_logprobs || f.token_prefix_stable) o.push('  token ids            ' + yn(f.token_ids) + ' (logprobs ' + yn(f.token_logprobs) + ', prefix-stable for packing ' + yn(f.token_prefix_stable) + ')');
    if (f.rate_limit) o.push('  rate limit           ' + f.rate_limit);
    o.push('  bytes per token      ' + f.bytes_per_token.toFixed(2));
    var n = rep.steps ? rep.steps.length : rep.stepsC.length;
    if (rep.cost_complete) o.push('  probe cost           $' + rep.total_usd.toFixed(6) + ' over ' + n + ' requests');
    else if (f.cost_reported) o.push('  probe cost           $' + rep.total_usd.toFixed(6) + ' reported; total unavailable (' + n + ' requests)');
    else o.push('  probe cost           total unavailable (' + n + ' requests; endpoint supplied no costs)');
    (f.notes || []).slice().sort().forEach(function (x) { o.push('  note: ' + x); });
    return o.join('\n');
  };
  /** doctorSteps(report): the live log lines the probe writes to stderr while it runs. */
  S.doctorSteps = function (rep) {
    rep = expand(rep); var out = [], last = '';
    var grp = function (n) { return n === 'basic' ? 'basic' : n === 'tools' ? 'tools' : /^cache/.test(n) ? 'cache' : n === 'reasoning' ? 'reasoning' : /^minp/.test(n) ? 'min-prefix' : /^warmup/.test(n) ? 'warm-up' : n; };
    rep.steps.forEach(function (s) {
      if (s.ms === undefined) s = { name: s.name, ok: s.ok, ms: Math.round(s.took_ns / 1e6), in: s.usage.input_tokens + s.usage.cache_read_tokens, cached: s.usage.cache_read_tokens, out: s.usage.output_tokens, cost: s.cost_usd, note: s.note, detail: s.detail };
      var g = grp(s.name); if (g !== last) { out.push(g); last = g; }
      if (!s.ok) { out.push('  ✗ ' + F.padR(s.name, 14) + ' ' + (s.detail || 'failed')); return; }
      if (s.name === 'basic') out.push('  ✓ basic          ' + F.padL(s.ms, 5) + 'ms  in=' + s.in + ' out=' + s.out + ' cost=' + (s.cost != null));
      else out.push('  ✓ ' + F.padR(s.name, 14) + ' ' + F.padL(s.ms, 5) + 'ms  in=' + s.in + ' cached=' + s.cached + ' out=' + s.out);
      if (s.name === 'tools' && !rep.findings.tools) out.push('    ! it answered, but did not call the offered tool');
    });
    var cold = rep.steps.filter(function (s) { return /^warmup-cold/.test(s.name); }), warm = rep.steps.filter(function (s) { return /^warmup-warm/.test(s.name); });
    var sum = function (a, k) { return a.reduce(function (t, s) { return t + (k === 'in' ? (s.in !== undefined ? s.in : s.usage.input_tokens + s.usage.cache_read_tokens) : (s.cached !== undefined ? s.cached : s.usage.cache_read_tokens)); }, 0); };
    if (cold.length) out.push('  cold burst hit ' + sum(cold, 'c') + '/' + sum(cold, 'in') + ', warm burst hit ' + sum(warm, 'c') + '/' + sum(warm, 'in'));
    return out;
  };
  var base = REAL.doctor.mockDeep;
  var mk = function (model, findings, scale, extra) {
    return { model: model, at: '2026-01-01T17:30:00Z', stepsC: tpl(scale, extra), findings: findings, total_usd: extra && extra.total, cost_complete: !(extra && extra.incomplete) };
  };
  var okF = { streaming: true, tools: true, tool_round_trip: true, usage_reported: true, cost_reported: true, cached_tokens_reported: true, cache_works: true, cache_hit_ratio: 0.97, cache_repeats: 9, cache_repeat_hits: 9, cache_granularity: 128, min_cache_prefix: 1024, warmup_needed: false, reasoning_seen: false, reasoning_details: false, bytes_per_token: 3.97, ttfb: '412ms', notes: [] };
  var partF = { streaming: true, tools: true, tool_round_trip: true, usage_reported: true, cost_reported: false, cached_tokens_reported: true, cache_works: true, cache_hit_ratio: 0.71, cache_repeats: 9, cache_repeat_hits: 6, cache_granularity: 64, min_cache_prefix: 512, warmup_needed: true, reasoning_seen: true, reasoning_details: true, bytes_per_token: 4.02, ttfb: '1.3s', rate_limit: '60 requests per minute, 150000 tokens per minute (x-ratelimit headers)', notes: ['a burst after a pause missed the cache until one request had warmed it: send one request first'] };
  var failF = { streaming: false, tools: false, tool_round_trip: false, usage_reported: false, cost_reported: false, cached_tokens_reported: false, cache_works: false, cache_hit_ratio: 0, cache_repeats: 0, cache_repeat_hits: 0, cache_granularity: 0, min_cache_prefix: 0, warmup_needed: null, reasoning_seen: false, reasoning_details: false, bytes_per_token: 0, ttfb: '', notes: [] };
  S.doctor = {
    note: 'The report format and step log are REAL (internal/provider/probe); the mock report is a real probe of `sleipnir mock`; the three endpoint reports are SAMPLE values rendered with the real formatter; the failure text is the binary\'s own.',
    mock: { command: 'sleipnir doctor --base-url http://127.0.0.1:8089/v1 --model mock-1 --deep', report: base },
    endpoints: [
      { ref: 'heimdall/demo-model', where: 'heimdall (https://api-staging.impossiblecarrot.cc/api/v1, key from $HEIMDALL_API_KEY)', deep: true, ok: true, report: mk('demo-model', okF, 38, { total: 0.031648 }) },
      { ref: 'openrouter/sample/mid-reasoner-70b', where: 'openrouter (https://openrouter.ai/api/v1, key from $OPENROUTER_API_KEY)', deep: true, ok: true, report: mk('sample/mid-reasoner-70b', partF, 160, { incomplete: true, total: 0.018204, base: 220, miss: ['cache-grow-130', 'cache-grow-310', 'cache-grow-155'], coldMiss: true }) },
      { ref: 'local/qwen-sample-32b', where: 'local (http://127.0.0.1:8000/v1, key from none)', deep: false, ok: false, report: { model: 'qwen-sample-32b', at: '2026-01-01T17:31:00Z', stepsC: [], findings: failF, total_usd: 0, cost_complete: false },
        failure: 'sleipnir: doctor: the endpoint did not answer a basic request: provider: network: cannot connect to 127.0.0.1:8000: connection refused (is the server running, and is the address right?)', failureNote: 'REAL wording (captured against a closed port); exit 1' },
    ],
  };
  /** render(i): {text, log} of sample endpoint i (the stored data is the report; text and log are derived). render(-1) is the real mock probe. */
  S.doctor.render = function (i) { var r = expand(i < 0 ? S.doctor.mock.report : S.doctor.endpoints[i].report); return { text: S.doctorText(r), log: S.doctorSteps(r) }; };
})();

/* ---- update (REAL strings: cmd/sleipnir/update.go) ----------------------------------------------------------------------------- */
S.update = {
  current: '0.3.2', latest: 'v0.4.0', releaseUrl: 'https://github.com/anemos-labs/sleipnir/releases/tag/v0.4.0', exe: '/usr/local/bin/sleipnir', commitsAhead: 0,
  fromSource: 'This build is from source, not from a release: update it with `go install github.com/anemos-labs/sleipnir/cmd/sleipnir@latest`, or `git pull` and `make build`.',
  upToDate: function (v, tag) { return 'sleipnir ' + v + ' is up to date (the latest release is ' + tag + ').'; },
  newer: function (v, tag, ahead) { return 'sleipnir ' + v + ' -> ' + tag + (ahead ? ', ' + ahead + ' commits ahead' : ''); },
  runHint: 'Run `sleipnir update` to install it.',
  done: function (exe, tag, url) { return 'Updated ' + exe + ' to ' + tag + '. A chat that is open keeps the old version until it is restarted. Release notes: ' + url; },
  failed: 'sleipnir: update: could not look for the latest release: <reason>',
  chatBanner: 'update available: sleipnir 0.3.2 -> v0.4.0 (run `sleipnir update`)',
  checkEnv: 'SLEIPNIR_NO_UPDATE_CHECK=1 turns the background look off (once a day); the only request is for the latest release to api.github.com',
  version: { string: 'sleipnir 0.3.2 (5b2fee0)', note: 'the sample user runs a release build; a build from source prints "sleipnir dev (none)" (REAL)' },
};

/* ---- config: layers and the effective value of every key with the layer that supplied it --------------------------------------- */
S.config = (function () {
  var L = REAL.configLayers;
  var flat = function (o, p, out) { out = out || {}; Object.keys(o).forEach(function (k) { var v = o[k], key = p ? p + '.' + k : k; if (v && typeof v === 'object' && !Array.isArray(v) && Object.keys(v).length) flat(v, key, out); else out[key] = v; }); return out; };
  var d = flat(L.defaults), u = flat(L.user), p = flat(L.project);
  var eq = function (a, b) { return JSON.stringify(a) === JSON.stringify(b); };
  var keys = {}; [d, u, p].forEach(function (o) { Object.keys(o).forEach(function (k) { keys[k] = 1; }); });
  var rows = Object.keys(keys).sort().map(function (k) {
    var layer = 'defaults', file = 'built-in', v = d[k];
    if (k in u && !eq(u[k], d[k])) { layer = 'user'; file = '~/.sleipnir/config.json'; v = u[k]; }
    if (k in p && !eq(p[k], u[k] === undefined ? d[k] : u[k])) { layer = 'project'; file = '.sleipnir/config.json'; v = p[k]; }
    return { key: k, value: v, layer: layer, file: file };
  });
  // flags of this session override the files (docs/CONFIGURATION.md section 2)
  var flags = { 'swarm.budget_usd': 5, 'swarm.max_workers': 8, 'swarm.isolation': 'worktree', 'models.default': 'anthropic/claude-sonnet-5-5' };
  rows.forEach(function (r) {
    if (r.key === 'swarm.budget_usd') { r.value = 5; r.layer = 'flag'; r.file = '--budget-usd 5'; r.below = { user: 25 }; }
    if (r.key === 'swarm.max_workers') { r.below = { user: 16, project: 12 }; r.note = 'the project\'s 12 is the ceiling; --swarm 8 asks for 8 workers (the manager comes on top)'; }
  });
  return {
    layers: [
      { kind: 'defaults', source: 'defaults', state: 'active' },
      { kind: 'user', source: '~/.sleipnir/config.json', state: 'found', trusted: true },
      { kind: 'project', source: '~/projects/shop/.sleipnir/config.json', state: 'found', trusted: 'trusted by you (Jan 1, hash unchanged): security-sensitive keys apply' },
      { kind: 'local', source: '~/projects/shop/.sleipnir/config.local.json', state: 'not found' },
      { kind: 'env', source: 'SLEIPNIR_* variables', state: 'unused' },
      { kind: 'flags', source: 'sleipnir chat --swarm 8 --budget-usd 5 --isolation worktree --verify "go test {dirs}"', state: 'active' },
    ],
    precedence: 'lowest first: defaults, user file, project file, local file, environment, flags',
    keySources: [   // what `sleipnir config` prints under "keys:" (REAL: top-level key -> file that supplied it last)
      ['cache', '~/.sleipnir/config.json'], ['hooks', '~/.sleipnir/config.json'], ['mcp', '~/.sleipnir/config.json'], ['models', '~/.sleipnir/config.json'],
      ['permissions', '~/projects/shop/.sleipnir/config.json'], ['providers', '~/.sleipnir/config.json'], ['swarm', '~/projects/shop/.sleipnir/config.json'],
    ],
    effective: rows,
    files: { user: REAL.configFiles.user, project: REAL.configFiles.project, projectMcp: REAL.configFiles.projectMcp, agents: REAL.configFiles.agents },
    sensitive: ['providers.<name>.base_url', 'providers.<name>.api_key_env', 'providers.<name>.headers', 'providers.<name>.options', 'permissions.mode', 'permissions.allow', 'permissions.roles.<role>.mode', 'permissions.roles.<role>.allow', 'hooks', 'mcp', 'tools.web_allow_private', 'tools.web_allow_hosts', 'swarm.budget_usd'],
    sensitiveNote: 'ignored from a project file unless the project is trusted (REAL: docs/CONFIGURATION.md section 4); a project can add to permissions.deny and permissions.ask, never remove from them',
    env: ['SLEIPNIR_MODEL', 'SLEIPNIR_MODEL_<ROLE>', 'SLEIPNIR_PERMISSION_MODE', 'SLEIPNIR_SWARM_MAX_WORKERS', 'SLEIPNIR_SWARM_ISOLATION', 'SLEIPNIR_SWARM_MAILMAN', 'SLEIPNIR_SWARM_BUDGET_USD', 'SLEIPNIR_CACHE_SHARED_TTL', 'SLEIPNIR_HOME'],
    swarmSettings: { isolation: 'swarm.isolation: worktree (~/projects/shop/.sleipnir/config.json): every writer gets its own git worktree; finished work is merged and verified, and applied to the checkout at the end', mailman: 'swarm.mailman: off (default): worker mail is delivered at once' },
  };
})();

/* ---- trust: the footprint as `sleipnir trust` read it (REAL sizes and digest) ---------------------------------------------------- */
(function () {
  var blk = S.realBlock('trust', 'trust add --yes');
  var files = [];
  blk.text.split('\n').forEach(function (l) { var m = /^ {2}(\S+): (.+), (\d+) B$/.exec(l); if (m) files.push({ path: m[1], kind: m[2], bytes: +m[3] }); });
  var led = REAL.trustLedger;
  files.forEach(function (f) { f.hash = led.files[f.path] || ''; });
  S.trust.files = files; S.trust.project.digest = led.digest.replace(/^sha256:/, '').slice(0, 12); S.trust.project.digestFull = led.digest;
  S.trust.ledger.forEach(function (e) { if (e.dir === '~/projects/shop') e.files = files.length; });
})();

/** friction.text: the binary's text view, derived from the JSON (non-enumerable, so it is not serialised twice) */
Object.defineProperty(S.friction, 'text', { enumerable: false, get: function () { return S.frictionText(S.friction.report); } });
/** simText(mode, provider, agents, seed): the text of `sleipnir sim` for a precomputed case (nearest case when the flags are outside the grid) */
S.simText = function (mode, provider, agents, seed) {
  var G = S.sim.grid, near = function (arr, v) { var b = arr[0]; arr.forEach(function (x) { if (Math.abs(x - v) < Math.abs(b - v)) b = x; }); return b; };
  var a = near(G.agents, agents || 20), key = mode === 'agents' ? 'agents|' + provider + '|20|1' : mode === 'compare' ? 'compare|' + provider + '|' + a + '|' + (a === 20 && G.seeds.indexOf(seed) >= 0 ? seed : 1) : mode + '|' + provider + '|' + a + '|1';
  return S.sim.pool[S.sim.idx[key]];
};
