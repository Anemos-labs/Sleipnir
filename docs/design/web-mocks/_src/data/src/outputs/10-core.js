/* ---------------------------------------------------------------------------------------------------------------
 * outputs: S.outputs[cmdPath](flags, ctx) -> { lines:[{k,t}], exit, ms, card? }
 *   cmdPath  the command words joined by one space: 'init', 'sessions prune', 'rl taskgen git', ...
 *   flags    { flagName: value } (bool true, string, number; repeatable flags are arrays) and flags._ = [positional words]
 *   ctx      { state?, session? }  state: a mutable state from S.newState() (the module keeps its own default one);
 *            session: the live session to answer slash commands for (see 30-slash.js), default the shop snapshot
 *   k        out (stdout), err (stderr), dim (secondary), head (a title row), ok, warn, bad
 * Base cases are REAL captures of bin/sleipnir (S.real); variants are derived by code from the sample state.
 * ------------------------------------------------------------------------------------------------------------- */
var OUT = S.outputs = {};
var MS = function (n) { return n; };
var _state = null;
/** newState(): a fresh mutable state for the command runner (trust ledger, MCP approvals, schedule, sessions, favourites, auth). */
S.newState = function () {
  return {
    now: S.meta.sampleNow,
    sessions: S.sessions.all().map(function (x) { return S.clone(x); }),
    trust: S.clone(S.trust.ledger).map(function (e) { return e; }),
    mcpApproved: {},
    schedule: S.clone(S.schedule.jobs), scheduleSeq: 5,
    favourites: ['anthropic/claude-sonnet-5-5', 'anthropic/claude-haiku-5-5', 'heimdall/demo-model'],
    auth: { heimdall: 'stored', openrouter: 'none', openai: 'env', anthropic: 'env', chatgpt: 'signed-in' },
    projectInitialised: true, userConfigExists: true,
    rl: { tasksFile: true },
  };
};
S.resetState = function () { _state = S.newState(); return _state; };
function st(ctx) { if (ctx && ctx.state) return ctx.state; if (!_state) _state = S.newState(); return _state; }
function L(k, t) { return { k: k, t: t }; }
function lines(k, text) { return String(text).split('\n').map(function (t) { return L(k, t); }); }
function res(ls, exit, ms, card) { var r = { lines: ls, exit: exit || 0, ms: ms == null ? 40 : ms }; if (card) r.card = card; return r; }
function bad(msg, exit) { return res([L('bad', 'sleipnir: ' + msg)], exit == null ? 1 : exit, 12); }
function flag(fl, name, dflt) { if (!fl) return dflt; var v = fl[name]; if (v === undefined) v = fl['--' + name]; if (v === undefined || v === null || v === '') return dflt; return v; }
function pos(fl) { return (fl && fl._) || []; }
function ago(iso, now) { return Math.max(0, (Date.parse(now) - Date.parse(iso)) / 1000); }
function oneLine(s, n) { s = String(s).split(/\s+/).filter(Boolean).join(' '); var r = Array.from(s); return r.length > n ? r.slice(0, n).join('') + '…' : s; }
function realText(section, cmd, kind) { var b = S.realBlock(section, cmd); return b ? lines(kind || 'out', b.text) : null; }
/** card(title, rows) */
function card(title, rows) { return { title: title, rows: rows }; }
function elapsed(ms) { return ms < 1000 ? ms + 'ms' : (ms / 1000).toFixed(1) + 's'; }
/** run(path, flags, ctx): look up and call an output; unknown paths answer with the binary's own usage error. */
S.runOutput = function (path, flags, ctx) {
  var key = Array.isArray(path) ? path.join(' ') : String(path);
  var fn = OUT[key];
  if (!fn) return res([L('bad', 'sleipnir: unknown command "' + key.split(' ')[0] + '" (sleipnir -h lists the commands)')], 2, 5);
  return fn(flags || {}, ctx || {});
};

/* ---- help, version, usage errors ------------------------------------------------------------------------------------------ */
OUT['help'] = function () { return res(lines('out', S.realBlock('basic', '--help').text), 0, 6); };
OUT['version'] = function () { return res([L('out', S.update.version.string)], 0, 4, card('version', [['version', '0.3.2'], ['commit', '5b2fee0']])); };
OUT['init'] = function (fl, ctx) {
  var s = st(ctx), user = !!flag(fl, 'user'), model = flag(fl, 'model', ''), local = flag(fl, 'local-url', '');
  if (local && !user) return bad('init: --local-url goes with --user (providers are set in your own config, not in a project\'s)');
  var path = user ? '~/.sleipnir/config.json' : '~/projects/shop/.sleipnir/config.json';
  if (user ? s.userConfigExists : s.projectInitialised) return bad('init: ' + path + ' already exists; edit it, or use `sleipnir config` to inspect it');
  var o = [];
  if (user && !model && !local) o.push(L('err', 'no model is set: `sleipnir` asks your provider which models it has on its first run and keeps your choice; or pass --model provider/model here'));
  o.push(L('err', 'wrote ' + path));
  if (!user) { o.push(L('err', 'wrote ~/projects/shop/AGENTS.md')); o.push(L('err', 'providers and the permission mode live in your user config: `sleipnir init --user` (project files cannot set them unless trusted)')); }
  o.push(L('err', 'next: sleipnir doctor --model <model> --deep, then sleipnir chat'));
  if (user) s.userConfigExists = true; else s.projectInitialised = true;
  var body = user ? JSON.stringify({ models: model ? { default: model } : undefined, permissions: { mode: 'default' }, providers: local ? { local: { base_url: local, options: { capture_tokens: true } } } : undefined }, null, 2) : S.real.initFiles.projectConfig;
  return res(o, 0, 31, card('init', [['wrote', path], ['content', body]]));
};

/* ---- config ---------------------------------------------------------------------------------------------------------------- */
OUT['config'] = function (fl) {
  var json = !!flag(fl, 'json'), trusted = !!flag(fl, 'trust-project');
  if (json) {
    var key = trusted ? 'config --trust-project --json' : 'config --json';
    var b = S.realBlock('config', key);
    return res(lines('out', b.text), 0, 24, card('effective configuration', [['layers', 'defaults, user, project, local, env, flags'], ['keys', String(S.config.effective.length)]]));
  }
  var b2 = S.realBlock('config', trusted ? 'config --trust-project' : 'config');
  var o = lines('err', b2.text).map(function (l) { return /^configuration is valid/.test(l.t) ? L('ok', l.t) : /^security-sensitive|^warnings:/.test(l.t) ? L('warn', l.t) : l; });
  return res(o, 0, 22, card('config', [['project file', '~/projects/shop/.sleipnir/config.json'], ['user file', '~/.sleipnir/config.json'], ['project trusted for this run', trusted ? 'yes (--trust-project)' : 'no: its security-sensitive keys are ignored (permissions.allow)']]));
};

/* ---- sessions -------------------------------------------------------------------------------------------------------------- */
OUT['sessions'] = function (fl, ctx) {
  var s = st(ctx), n = Number(flag(fl, 'n', 20));
  if (flag(fl, 'dir')) return res([L('err', 'no sessions yet')], 0, 8);
  var rows = s.sessions.slice().sort(function (a, b) { return a.lastWritten < b.lastWritten ? 1 : -1; }).filter(function (r) { return r.prompt; }).slice(0, n);
  if (!rows.length) return res([L('err', 'no sessions yet')], 0, 8);
  var o = rows.map(function (r) {
    return L('out', (r.resumable ? '↺' : ' ') + ' ' + r.id + '  ' + F.padR(F.ago(ago(r.lastWritten, s.now)), 9) + ' ' + F.padR(r.model, 28) + ' $' + F.padR(r.cost.toFixed(4), 8) + ' ' + oneLine(r.prompt, 70));
  });
  o.push(L('dim', '↺ can be continued: sleipnir chat --resume <id>   (or --continue for this project\'s newest)'));
  return res(o, 0, 18, card('sessions', [['listed', String(rows.length) + ' of ' + s.sessions.length], ['resumable', String(rows.filter(function (r) { return r.resumable; }).length)]]));
};
/** parseAge(s) seconds, or null (REAL: cmd/sleipnir parseAge) */
function parseAge(s) {
  s = String(s).trim();
  var m = /^([0-9.]+)(d|w)$/.exec(s);
  if (m) return parseFloat(m[1]) * (m[2] === 'd' ? 86400 : 604800);
  m = /^([0-9.]+)(h|m|s)$/.exec(s);
  if (m) return parseFloat(m[1]) * { h: 3600, m: 60, s: 1 }[m[2]];
  if (s === '0') return 0;
  return null;
}
/** planPrune(sessions, now, olderThan seconds, keep): REAL rules (cmd/sleipnir/sessions_prune.go): the newest `keep` are kept whatever their age, anything written in the last ten minutes is left alone. */
S.planPrune = function (sessions, now, olderThan, keep) {
  var all = sessions.map(function (x) { return { id: x.id, mod: Date.parse(x.lastWritten), age: Math.max(0, (Date.parse(now) - Date.parse(x.lastWritten)) / 1000), bytes: x.size }; }).sort(function (a, b) { return b.mod - a.mod; });
  var p = { total: all.length, items: [], inUse: [], freed: 0, keptNewest: 0 };
  all.forEach(function (x, i) {
    if (i < keep) p.keptNewest++;
    else if (x.age < olderThan) { /* young enough */ }
    else if (x.age < 600) p.inUse.push(x.id);
    else { p.items.push(x); p.freed += x.bytes; }
  });
  p.items.reverse();   // oldest first
  return p;
};
OUT['sessions prune'] = function (fl, ctx) {
  var s = st(ctx), olderRaw = String(flag(fl, 'older-than', '30d')), keep = Number(flag(fl, 'keep', 20)), yes = !!flag(fl, 'yes');
  var age = parseAge(olderRaw);
  if (age === null) return bad('sessions prune: --older-than: "' + olderRaw + '" is not an age (try 30d, 36h or 2w)');
  if (keep < 0) return bad('sessions prune: --keep must not be negative');
  if (flag(fl, 'dir')) return res([L('err', 'no sessions yet')], 0, 6);
  var p = S.planPrune(s.sessions, s.now, age, keep);
  if (!p.items.length) return res([L('out', 'nothing to prune: ' + p.total + ' sessions, ' + p.keptNewest + ' of the newest kept, none older than ' + olderRaw + ' beyond those')], 0, 14, card('sessions prune', [['sessions', String(p.total)], ['would delete', '0']]));
  var o = p.items.map(function (it) { return L('out', '  ' + F.padR(it.id, 30) + ' ' + F.padL(F.ageText(it.age), 5) + ' old  ' + F.padL(F.sizeText(it.bytes), 8)); });
  o.push(L(yes ? 'warn' : 'out', (yes ? 'deleting ' : 'would delete ') + p.items.length + ' of ' + p.total + ' sessions (' + F.sizeText(p.freed) + '); the newest ' + p.keptNewest + ' are kept'));
  if (p.inUse.length) o.push(L('out', p.inUse.length + ' old enough but written to in the last ten minutes: left alone'));
  if (!yes) { o.push(L('dim', 'nothing deleted: run again with --yes')); return res(o, 0, 22, card('sessions prune (dry run)', [['would delete', p.items.length + ' of ' + p.total], ['freed', F.sizeText(p.freed)], ['kept', p.keptNewest + ' newest']])); }
  var gone = {}; p.items.forEach(function (it) { gone[it.id] = 1; });
  s.sessions = s.sessions.filter(function (x) { return !gone[x.id]; });
  o.push(L('ok', 'deleted ' + p.items.length + ' sessions, ' + F.sizeText(p.freed) + ' freed'));
  return res(o, 0, 210, card('sessions prune', [['deleted', String(p.items.length)], ['freed', F.sizeText(p.freed)], ['sessions left', String(s.sessions.length)]]));
};

/* ---- mcp ------------------------------------------------------------------------------------------------------------------- */
function projectTrusted(s) { return s.trust.some(function (e) { return e.dir === '~/projects/shop' && e.state === 'trusted'; }); }
function mcpEntries(s, trustProject, session) {
  var include = trustProject || (session && projectTrusted(s));   // REAL: `mcp list` reads project entries only with --trust-project; a session (mcp test, /mcp) also uses the trust ledger
  return S.mcp.servers.filter(function (v) { return v.trusted || include; }).sort(function (a, b) { return a.name < b.name ? -1 : 1; }).map(function (v) {
    var approved = v.trusted || !!s.mcpApproved[v.name];
    var stateTxt = v.trusted ? (v.disabled ? 'disabled' : 'trusted') : (approved ? 'approved' : 'needs approval');
    return { v: v, approved: approved, state: stateTxt, what: v.trusted ? 'stdio(command="' + v.command + '"' + (v.args ? ' args=' + v.args.length : '') + ' scope=user trusted)' : v.describe };
  });
}
OUT['mcp'] = function () { return res(lines('err', 'usage: sleipnir mcp <command> [flags]\n\nTool servers (the Model Context Protocol). Servers are configured under "mcp" in\nyour user configuration (trusted) or a project\'s (.sleipnir/config.json or\n.mcp.json; only read with --trust-project, and each entry needs your approval).\n\ncommands:\n  list [--trust-project]              the servers a session here would consider, where each came from and whether it may start\n  approve NAME [--yes]                remember a project entry for this project (shows what it would run and asks first)\n  revoke NAME                         forget an approval\n  test [NAME...] [--trust-project]    start the servers and list the tools they offer (project entries still need approval)'), 0, 5); };
OUT['mcp list'] = function (fl, ctx) {
  var s = st(ctx), es = mcpEntries(s, !!flag(fl, 'trust-project'), false);
  var rows = [['NAME', 'FROM', 'STATE', 'WHAT IT DOES']].concat(es.map(function (e) { return [e.v.name, e.v.trusted ? 'your config' : 'this project', e.state, e.what]; }));
  return res(F.tabwriter(rows, 2).map(function (t, i) { return L(i ? 'out' : 'head', t); }), 0, 30, card('mcp', [['servers', String(es.length)], ['need approval', String(es.filter(function (e) { return e.state === 'needs approval'; }).length)]]));
};
OUT['mcp approve'] = function (fl, ctx) {
  var s = st(ctx), name = pos(fl)[0];
  if (pos(fl).length !== 1) return bad('mcp approve: exactly one server name is required');
  var v = S.mcp.servers.filter(function (x) { return x.name === name; })[0];
  if (!v) return bad('no project MCP server "' + name + '" (see: sleipnir mcp list --trust-project)');
  if (v.trusted) return bad('"' + name + '" is your own entry: it needs no approval');
  var o = [L('err', name + ' from ~/projects/shop'), L('err', '  it ' + v.describe)];
  if (!flag(fl, 'yes')) { o.push(L('err', 'approve this exact entry for this project? [y/N] ')); o.push(L('bad', 'sleipnir: not approved')); return res(o, 1, 15); }
  s.mcpApproved[name] = true;
  o.push(L('ok', 'approved "' + name + '" for this project (the approval ends when its entry changes)'));
  return res(o, 0, 21, card('mcp approve', [['server', name], ['project', '~/projects/shop']]));
};
OUT['mcp revoke'] = function (fl, ctx) {
  var s = st(ctx), name = pos(fl)[0];
  if (pos(fl).length !== 1) return bad('mcp revoke: exactly one server name is required');
  var v = S.mcp.servers.filter(function (x) { return x.name === name; })[0];
  if (!v) return bad('no project MCP server "' + name + '" (see: sleipnir mcp list --trust-project)');
  if (v.trusted) return bad('"' + name + '" is your own entry: it needs no approval');
  delete s.mcpApproved[name];
  return res([L('ok', 'approval for "' + name + '" forgotten')], 0, 12);
};
OUT['mcp test'] = function (fl, ctx) {
  var s = st(ctx), want = pos(fl), es = mcpEntries(s, !!flag(fl, 'trust-project'), true), o = [], badN = 0, tools = [];
  var pre = [];
  es.forEach(function (e) {
    var v = e.v;
    if (!v.trusted && !e.approved) { pre.push(L('warn', 'warn: mcp: server "' + v.name + '" did not start: mcp: server not approved: it was not approved')); return; }
    if (v.state === 'failed') pre.push(L('warn', 'warn: mcp: server "' + v.name + '" did not start: ' + v.error));
  });
  o = o.concat(pre);
  es.forEach(function (e) {
    var v = e.v;
    if (want.length && want.indexOf(v.name) < 0) return;
    if (v.disabled) { o.push(L('out', v.name + ': disabled')); return; }
    if (!v.trusted && !e.approved) { o.push(L('bad', v.name + ': refused — mcp: server not approved: it was not approved')); badN++; return; }
    if (v.state === 'failed') { o.push(L('bad', v.name + ': failed — ' + v.error)); badN++; return; }
    o.push(L('out', v.name + ': ready (' + v.server + ')'));
    v.toolNames.forEach(function (t) { tools.push('mcp__' + v.name + '__' + t); });
  });
  tools.sort().forEach(function (t) { o.push(L('dim', '  ' + t)); });
  o.push(L('out', tools.length + ' tools in the frozen list'));
  if (badN) o.push(L('bad', 'sleipnir: ' + badN + ' server(s) not ready'));
  return res(o, badN ? 1 : 0, 780, card('mcp test', [['tools', String(tools.length)], ['not ready', String(badN)]]));
};

/* ---- trust ------------------------------------------------------------------------------------------------------------------ */
function shopEntry(s) { return s.trust.filter(function (e) { return e.dir === '~/projects/shop'; })[0]; }
function footprint() { return S.trust.files.map(function (f) { return L('out', '  ' + f.path + ': ' + f.kind + ', ' + f.bytes + ' B'); }); }
OUT['trust'] = function (fl, ctx) {
  var s = st(ctx), e = shopEntry(s), o = [L('head', '~/projects/shop')].concat(footprint());
  o.push(L('out', '  digest ' + S.trust.project.digest), L('out', ''));
  if (e && e.state === 'trusted') o.push(L('ok', 'trusted since ' + e.saved + ', for exactly these files: a session started here uses them without asking (`sleipnir trust forget` ends it)'));
  else o.push(L('warn', 'not trusted: a chat asks about these at its start, and a run leaves them out unless you pass --trust-project or run `sleipnir trust add`'));
  return res(o, 0, 14, card('trust', [['project', '~/projects/shop'], ['state', e && e.state === 'trusted' ? 'trusted (' + e.saved + ')' : 'not trusted'], ['digest', S.trust.project.digest]]));
};
OUT['trust add'] = function (fl, ctx) {
  var s = st(ctx), o = [L('head', '~/projects/shop')].concat(footprint());
  if (!flag(fl, 'yes')) { o.push(L('err', 'use exactly these files, in a session started here, until any of them changes? [y/N] ')); o.push(L('bad', 'sleipnir: not trusted')); return res(o, 1, 12); }
  var e = shopEntry(s);
  if (e) { e.state = 'trusted'; e.saved = s.now.slice(0, 10); e.now = 'unchanged'; e.files = S.trust.files.length; } else s.trust.push({ dir: '~/projects/shop', saved: s.now.slice(0, 10), files: S.trust.files.length, now: 'unchanged', state: 'trusted' });
  o.push(L('ok', 'trusted (digest ' + S.trust.project.digest + '): the answer ends when any of these files changes'));
  return res(o, 0, 18, card('trust add', [['project', '~/projects/shop'], ['files', String(S.trust.files.length)], ['digest', S.trust.project.digest]]));
};
OUT['trust forget'] = function (fl, ctx) {
  var s = st(ctx);
  if (flag(fl, 'all')) { var n = s.trust.length; s.trust = []; return res([L('out', 'forgot ' + n + ' ' + (n === 1 ? 'project' : 'projects'))], 0, 10); }
  var had = shopEntry(s);
  if (!had) return res([L('out', 'nothing was remembered for ~/projects/shop')], 0, 8);
  s.trust = s.trust.filter(function (e) { return e.dir !== '~/projects/shop'; });
  return res([L('ok', 'forgot ~/projects/shop: its files are not used until you say yes again')], 0, 11);
};
OUT['trust list'] = function (fl, ctx) {
  var s = st(ctx);
  if (!s.trust.length) return res([L('out', 'no project is trusted (`sleipnir trust add` in a project, or the question at the start of a chat)')], 0, 7);
  var rows = [['DIRECTORY', 'SAVED', 'FILES', 'NOW']].concat(s.trust.slice().sort(function (a, b) { return a.dir < b.dir ? -1 : 1; }).map(function (e) { return [e.dir, e.saved, String(e.files), e.now]; }));
  return res(F.tabwriter(rows, 2).map(function (t, i) { return L(i ? 'out' : 'head', t); }), 0, 9);
};

/* ---- schedule, daemon -------------------------------------------------------------------------------------------------------- */
function jobsTable(s) {
  if (!s.schedule.length) return lines('out', 'no scheduled jobs. Add one:\n  sleipnir schedule add --cron "0 9 * * 1-5" "summarize yesterday\'s commits"');
  var rows = [['ID', 'CRON', 'NEXT', 'LAST', 'GOAL']].concat(s.schedule.map(function (j) {
    var nx = S.cronNext(j.cron, s.now), next = nx ? nx.slice(5, 7) + '-' + nx.slice(8, 10) + ' ' + nx.slice(11, 16) : '-';
    var last = j.lastRun ? j.lastRun.slice(5, 7) + '-' + j.lastRun.slice(8, 10) + ' ' + j.lastRun.slice(11, 16) + ' ' + j.lastExit : 'never';
    var goal = oneLine(j.goal, 400); if (goal.length > 60) goal = goal.slice(0, 59) + '…';
    return [j.id, j.cron, next, last, goal];
  }));
  return F.tabwriter(rows, 2).map(function (t, i) { return L(i ? 'out' : 'head', t); });
}
OUT['schedule'] = function (fl, ctx) { return res(jobsTable(st(ctx)), 0, 11, card('schedule', [['jobs', String(st(ctx).schedule.length)], ['daemon', S.schedule.daemon.running ? 'running (pid ' + S.schedule.daemon.pid + ')' : 'not running']])); };
OUT['schedule add'] = function (fl, ctx) {
  var s = st(ctx), cron = flag(fl, 'cron', ''), goal = pos(fl).join(' ').trim();
  if (!cron) return bad('schedule add: --cron is required (five fields "minute hour day-of-month month day-of-week", or @hourly, @daily, @weekly)');
  if (!goal) return bad('schedule add: a goal is required');
  var nx = S.cronNext(cron, s.now);
  var f = String(cron).trim().split(/\s+/), names = ['minute', 'hour', 'day-of-month', 'month', 'day-of-week'], ranges = [[0, 59], [0, 23], [1, 31], [1, 12], [0, 7]];
  if (!/^@(hourly|daily|weekly)$/.test(cron)) {
    if (f.length !== 5) return bad('schedule add: cron "' + cron + '": want five fields (minute hour day-of-month month day-of-week), got ' + f.length);
    for (var i = 0; i < 5; i++) { var m = /^\d+$/.exec(f[i]); if (m && (+f[i] < ranges[i][0] || +f[i] > ranges[i][1])) return bad('schedule add: cron "' + cron + '": ' + names[i] + ': "' + f[i] + '" is outside ' + ranges[i][0] + '-' + ranges[i][1]); }
  }
  if (!nx) return bad('schedule add: cron "' + cron + '": no run time in the next year');
  var id = 'j' + (s.scheduleSeq++);
  s.schedule.push({ id: id, cron: cron, goal: goal, dir: flag(fl, 'cwd', '~/projects/shop'), model: flag(fl, 'model', ''), mode: flag(fl, 'mode', ''), budgetUsd: Number(flag(fl, 'budget-usd', 1)), created: s.now, lastRun: '', lastExit: '', next: nx });
  return res([L('ok', id + ': runs next at ' + nx.slice(0, 10) + ' ' + nx.slice(11, 16) + '; `sleipnir daemon` starts the jobs that are due')], 0, 17, card('schedule add', [['id', id], ['cron', cron], ['next run', nx.replace('T', ' ').slice(0, 16)], ['mode', flag(fl, 'mode', 'default (refuses what needs a yes)')], ['budget', '$' + Number(flag(fl, 'budget-usd', 1)).toFixed(2)]]));
};
OUT['schedule rm'] = function (fl, ctx) {
  var s = st(ctx), id = pos(fl)[0];
  if (!id) return bad('schedule rm: want a job id (see `sleipnir schedule`)');
  var n = s.schedule.length; s.schedule = s.schedule.filter(function (j) { return j.id !== id; });
  if (s.schedule.length === n) return bad('schedule rm: no job "' + id + '"');
  return res([], 0, 9);
};
OUT['daemon'] = function (fl, ctx) {
  var s = st(ctx);
  if (flag(fl, 'once')) {
    var due = s.schedule.filter(function (j) { var nx = j.next || S.cronNext(j.cron, j.lastRun || j.created); return nx && nx <= s.now.slice(0, 19); });
    if (!due.length) return res([], 0, 30);
    var o = []; due.forEach(function (j) { o.push(L('err', 'sleipnir daemon: ' + j.id + ' starts: ' + oneLine(j.goal, 200))); o.push(L('err', 'sleipnir daemon: ' + j.id + ' ended: ok')); });
    return res(o, 0, 4200);
  }
  return res([L('err', 'sleipnir daemon: looking for due jobs every ' + flag(fl, 'every', '30s') + ' (ctrl-c stops it); jobs: ~/.sleipnir/schedule.json')], 0, 10, card('daemon', [['looking every', String(flag(fl, 'every', '30s'))], ['jobs', String(s.schedule.length)], ['logs', '~/.sleipnir/schedule-logs/']]));
};

/* ---- login, logout, models, update, doctor ----------------------------------------------------------------------------------- */
var LOGIN_CHOICES = ['heimdall', 'openrouter', 'openai', 'anthropic', 'gemini', 'mistral', 'xai', 'deepseek', 'together', 'fireworks', 'groq', 'huggingface'];
OUT['login'] = function (fl, ctx) {
  var s = st(ctx), name = (pos(fl)[0] || '').toLowerCase();
  if (!name) {
    var o = [L('out', 'Which provider will you use?')];
    LOGIN_CHOICES.forEach(function (c, i) { o.push(L('out', F.padL(i + 1, 3) + '. ' + c + (c === 'heimdall' ? '  (recommended)' : ''))); });
    o.push(L('out', F.padL(LOGIN_CHOICES.length + 1, 3) + '. chatgpt  (your ChatGPT / OpenAI plan: sign in with the browser, no key)'));
    o.push(L('out', F.padL(LOGIN_CHOICES.length + 2, 3) + '. local servers answering on this machine: vllm  (running on this machine, no key)'));
    o.push(L('err', 'Number (q to quit): '));
    return res(o, 0, 30);
  }
  if (name === 'chatgpt') { s.auth.chatgpt = 'signed-in'; return res([L('out', 'Signed in as ada@example.com. Your ChatGPT plan pays for the requests (its usage limits apply); `sleipnir logout chatgpt` ends the sign-in.')], 0, 2400, card('login', [['provider', 'chatgpt'], ['who', 'ada@example.com'], ['tokens', '~/.sleipnir/chatgpt.json (0600)']])); }
  var env = (S.providerKeyVars[name]);
  if (!env) return bad('login: "' + name + '" is not a provider that takes a key (heimdall, openrouter, openai, anthropic, ...; chatgpt signs in with the browser)');
  s.auth[name] = 'stored';
  return res([L('err', 'Paste your ' + name + ' key (hidden; kept in ~/.sleipnir/auth.json, readable by you only): Saved. (' + env + ' in the environment still takes precedence over it.)'), L('out', 'Checking the key... ok.')], 0, 1300, card('login', [['provider', name], ['key', 'stored in ~/.sleipnir/auth.json (0600)'], ['variable', env + ' wins if set']]));
};
OUT['logout'] = function (fl, ctx) {
  var s = st(ctx), name = (pos(fl)[0] || '').toLowerCase();
  if (!name) return bad('usage: sleipnir logout <provider>');
  if (name === 'chatgpt') { s.auth.chatgpt = 'none'; return res([L('err', 'signed out of ChatGPT, and the issuer was told to revoke the token')], 0, 420); }
  if (!S.providerKeyVars[name]) return bad('logout: "' + name + '" is not a provider that takes a key');
  s.auth[name] = 'none';
  return res([L('err', 'removed the stored key for ' + name + ' (a ' + S.providerKeyVars[name] + ' in the environment is untouched)')], 0, 9);
};
OUT['models'] = function (fl, ctx) {
  var s = st(ctx), minc = flag(fl, 'min-context') ? S.parseTokens(flag(fl, 'min-context')) : 0;
  if (flag(fl, 'min-context') && isNaN(minc)) return bad('models: --min-context: want a token count such as 128k, got "' + flag(fl, 'min-context') + '"');
  var words = pos(fl).slice(); if (flag(fl, 'filter')) words.push(flag(fl, 'filter'));
  var prov = flag(fl, 'provider', '');
  var favSet = {}; s.favourites.forEach(function (r) { favSet[r] = 1; });
  var rows = S.modelsFilter({ words: words, tools: !!flag(fl, 'tools'), reasoning: !!flag(fl, 'reasoning'), maxPrice: Number(flag(fl, 'max-price', 0)), minContext: minc, fav: !!flag(fl, 'fav'), all: !!flag(fl, 'all'), provider: prov })
    .map(function (m) { var c = S.clone(m); c.favourite = !!favSet[m.ref]; return c; })
    .sort(function (a, b) { return a.favourite !== b.favourite ? (a.favourite ? -1 : 1) : a.ref < b.ref ? -1 : a.ref > b.ref ? 1 : 0; });
  var cells = [['MODEL', 'CONTEXT', '$/M IN', '$/M CACHED', '$/M OUT', 'TOOLS', 'REASONING']];
  rows.forEach(function (m) {
    // REAL: a catalogue that names no price prints 0.0000 (the UI says "price unknown"); a model without a cached-read price is read at its input price
    var p = m.plan ? ['plan', 'plan', 'plan'] : m.inPerM === null ? ['0.0000', '0.0000', '0.0000'] : [m.inPerM.toFixed(4), (m.cachedPerM === null ? m.inPerM : m.cachedPerM).toFixed(4), m.outPerM.toFixed(4)];
    cells.push([(m.favourite ? '* ' : '') + m.ref, S.humanTokens(m.context), p[0], p[1], p[2], String(m.tools), String(m.reasoning)]);
  });
  var o = F.tabwriter(cells, 2).map(function (t, i) { return L(i ? 'out' : 'head', t); });
  if (!rows.length) o.push(L('err', 'models: nothing matches (' + S.models.length + ' models listed by 6 provider(s); try fewer words or filters)'));
  return res(o, 0, 640, card('models', [['listed', String(rows.length)], ['favourites', String(s.favourites.length)], ['price unknown', String(rows.filter(function (m) { return !m.priceKnown; }).length) + ' (the CLI prints 0.0000 for them; the web UI says price unknown)']]));
};
OUT['models fav list'] = function (fl, ctx) { return res(st(ctx).favourites.map(function (r) { return L('out', r); }), 0, 10); };
function favEdit(op) {
  return function (fl, ctx) {
    var s = st(ctx), refs = pos(fl);
    if (!refs.length) return bad('models fav: want `list`, `add provider/model...` or `rm provider/model...`');
    for (var i = 0; i < refs.length; i++) if (!/^[^/\s]+\/.+/.test(refs[i])) return bad('models fav: "' + refs[i] + '" is not a provider/model reference (see `sleipnir models`)');
    refs.forEach(function (r) { var k = s.favourites.indexOf(r); if (op === 'add' && k < 0) s.favourites.push(r); if (op === 'rm' && k >= 0) s.favourites.splice(k, 1); });
    return res([L('out', s.favourites.length + ' favorite(s)')], 0, 14);
  };
}
OUT['models fav add'] = favEdit('add'); OUT['models fav rm'] = favEdit('rm');

OUT['update'] = function (fl) {
  var U = S.update;
  if (flag(fl, 'source')) return res([L('out', U.fromSource)], 0, 6);
  var o = [L('out', U.newer(U.current, U.latest, 0))];
  if (flag(fl, 'check')) { o.push(L('out', U.runHint)); return res(o, 0, 890, card('update', [['running', U.current], ['latest', U.latest], ['install', 'sleipnir update']])); }
  o.push(L('ok', U.done(U.exe, U.latest, U.releaseUrl)));
  return res(o, 0, 5200, card('update', [['from', U.current], ['to', U.latest], ['binary', U.exe], ['checksum', 'matched checksums.txt']]));
};

OUT['doctor'] = function (fl, ctx) {
  var model = String(flag(fl, 'model', '')), base = flag(fl, 'base-url', '');
  if (!model && (base || flag(fl, 'provider'))) return bad('doctor: --model is required with --base-url or --provider, as in `sleipnir doctor --base-url URL --model MODEL`', 2);
  var json = !!flag(fl, 'json'), deep = !!flag(fl, 'deep');
  var idx = 0;
  if (!model) model = 'anthropic/claude-sonnet-5-5';
  S.doctor.endpoints.forEach(function (e, i) { if (model === e.ref || model === e.report.model) idx = i; });
  if (/^anthropic\//.test(model)) idx = 0;
  if (base && /127\.0\.0\.1:8089|mock/.test(base + model)) {
    var m = S.doctor.render(-1);
    var out = [L('err', 'probing mock-1 at ' + base + ' (key from none)')].concat(m.log.map(function (t) { return L('dim', t); }), [L('out', '')], lines('out', m.text));
    return res(out, 0, 22, card('doctor', [['endpoint', base], ['cache works', 'yes (98%)'], ['granularity', '~16 tokens']]));
  }
  var e = S.doctor.endpoints[idx], r = S.doctor.render(idx);
  var where = e.where;
  var o = [L('err', 'probing ' + e.report.model + ' at ' + where)];
  if (!e.ok) {
    o.push(L('out', '')); o = o.concat(lines('out', r.text)); o.push(L('bad', e.failure));
    return res(o, 1, 130, card('doctor', [['endpoint', e.ref], ['result', 'failed: connection refused'], ['next', 'start the server, or check the address']]));
  }
  if (json) return res(lines('out', JSON.stringify(e.report, null, 2)), 0, 3900);
  var rep = e.report;
  if (!deep) {   // a probe without --deep stops after the reasoning check: no min-prefix, no warm-up, no granularity measurements
    rep = Object.assign({}, rep, { findings: Object.assign({}, rep.findings, { warmup_needed: null, min_cache_prefix: 0 }), stepsC: (rep.stepsC || []).filter(function (s) { return !/^(minp|warmup)/.test(s[0]); }) });
    r = { text: S.doctorText(rep), log: S.doctorSteps(rep) };
  }
  o = o.concat(r.log.map(function (t) { return L('dim', t); }), [L('out', '')], lines('out', r.text));
  var f = e.report.findings;
  return res(o, 0, 3900, card('doctor', [['endpoint', e.ref], ['streaming', 'yes (first byte ' + f.ttfb + ')'], ['tools', f.tools ? 'yes' : 'NO'], ['prefix cache', f.cache_works ? (f.cache_repeat_hits < f.cache_repeats ? 'partly' : 'yes') + ' (' + Math.round(f.cache_hit_ratio * 100) + '%)' : 'NO'], ['granularity', '~' + f.cache_granularity + ' tokens'], ['smallest cached size', '~' + f.min_cache_prefix + ' tokens'], ['warm-up needed', f.warmup_needed ? 'yes' : 'no']]));
};
