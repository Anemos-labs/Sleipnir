/* ---- slash commands that print (REAL formats: cmd/sleipnir/chat.go). ctx.session is a mutable session state (S.newSessionState()). ------------ */
/** newSessionState(): the shop session at the snapshot, as the slash commands read and change it. */
S.newSessionState = function () {
  return { id: S.session.id, dir: '~/.sleipnir/sessions/' + S.session.id, cwd: '~/projects/shop', model: S.session.model, mode: 'default', effort: 'default', budget: 5.00, verbose: false, anim: true,
    team: S.clone(S.team), swarm: true, rules: [], goal: { text: S.goal.text, state: 'active', paused: '', turns: 0, max: 20, reason: S.goal.judge.reason, plan: S.goal.plan.map(function (p) { return { step: p.step, status: p.status }; }) },
    checkpoints: [{ id: 'c07', time: '03:04:38', files: 3, label: 'T4 catalogue handler' }, { id: 'c06', time: '03:04:29', files: 1, label: 'T5 cart total in cents' }, { id: 'c05', time: '03:04:21', files: 2, label: 'seed items + loader' }, { id: 'c04', time: '03:04:12', files: 0, label: 'turn 1' }],
    roleModels: S.clone(S.roleModels.defaults), steered: [] };
};
var _sess = null;
function ss(ctx) { if (ctx && ctx.session) return ctx.session; if (!_sess) _sess = S.newSessionState(); return _sess; }
S.resetSessionState = function () { _sess = S.newSessionState(); return _sess; };
function agentTotals(sx) { return S.totals(sx.team); }
function costLine(sx) {
  if (!sx.swarm) { var a = sx.team[0]; return 'input ' + a.uncached + ' (uncached) + ' + a.read + ' cached-read + 0 cache-write · output ' + a.out + ' · hit ' + Math.round(a.read / a.prompt * 100) + '% · ' + F.usd4(S.costOf(a)); }
  var m = sx.team.filter(function (a) { return a.rider; })[0], t = agentTotals(sx);
  return 'input ' + m.uncached + ' (uncached) + ' + m.read + ' cached-read + 0 cache-write · output ' + m.out + ' · hit ' + Math.round(m.read / m.prompt * 100) + '% · ' + t.costText4;
}
OUT['/cost'] = function (fl, ctx) { return res([L('err', costLine(ss(ctx)))], 0, 3); };
OUT['/context'] = function (fl, ctx) {
  var m = ss(ctx).team.filter(function (a) { return a.rider || a.id === 'main'; })[0] || ss(ctx).team[0], lt = m.layerTokens;
  var row = function (n, t) { return L('err', '  ' + F.padR(n, 16) + ' ' + F.padL(t, 7) + ' tokens'); };
  return res([row('constitution', lt.G0), row('shared pin', lt.G1), row('role pin', lt.G2), row('notes', lt.G3), row('spine', lt.G4), row('thread (verbatim)', lt.G5)], 0, 4);
};
OUT['/status'] = function (fl, ctx) {
  var sx = ss(ctx), o = [L('err', 'model    ' + sx.model + ' (' + (sx.swarm ? 'swarm' : 'single agent') + ')'), L('err', 'mode     ' + sx.mode), L('err', 'session  ' + sx.id + ' (' + sx.dir + ')')];
  if (sx.budget > 0) o.push(L('err', 'budget   $' + sx.budget.toFixed(2)));
  o.push(L('err', costLine(sx)));
  return res(o, 0, 4, card('status', [['model', sx.model], ['mode', sx.mode], ['session', sx.id], ['budget', sx.budget > 0 ? '$' + sx.budget.toFixed(2) : 'none'], ['cost', agentTotals(sx).costText4], ['hit (all agents)', agentTotals(sx).hitPct + '%']]));
};
OUT['/permissions'] = function (fl, ctx) {
  var sx = ss(ctx), P = S.permissions.rules, o = [L('err', 'mode: ' + sx.mode)];
  var allow = P.allow.map(function (r) { return r.rule; }).concat(sx.rules.map(function (r) { return r.rule; }));
  var deny = P.deny.map(function (r) { return r.rule; });
  var ask = P.ask.map(function (r) { return r.rule; });
  [['allow', allow, 40], ['deny', deny, 6], ['ask', ask, 6]].forEach(function (g) {
    if (!g[1].length) return;
    o.push(L('err', g[0] + ' (' + g[1].length + '):'));
    g[1].forEach(function (r, i) { if (i === g[2]) { o.push(L('dim', '  ... ' + (g[1].length - i) + ' more')); return; } if (i > g[2]) return; o.push(L('err', '  ' + r)); });
  });
  return res(o, 0, 5, card('permissions', [['mode', sx.mode], ['allow', String(allow.length)], ['deny', String(deny.length)], ['ask', String(ask.length)]]));
};
OUT['/trust'] = function (fl, ctx) { return OUT['trust'](fl, ctx); };
OUT['/mcp'] = function (fl, ctx) {
  var w = pos(fl), s = st(ctx);
  if (w[0] === 'reconnect' && w[1]) return res([L('err', 'reconnecting ' + w[1])], 0, 5);
  var es = mcpEntries(s, false, true), o = [];
  es.forEach(function (e) {
    var v = e.v, ready = (v.trusted || e.approved) && v.state !== 'failed' && !v.disabled;
    var state = v.disabled ? 'disabled' : !v.trusted && !e.approved ? 'refused' : v.state === 'failed' ? 'failed' : 'ready';
    o.push(L(state === 'ready' ? 'err' : 'warn', '  ' + F.padR(v.name, 16) + ' ' + F.padR(state, 10) + ' ' + (ready ? v.tools : 0) + ' tools in this session' + (state === 'failed' ? ' — ' + v.error : state === 'refused' ? ' — mcp: server not approved: it was not approved' : '')));
  });
  o.push(L('err', 'prompts you can run:'), L('err', '  ' + F.padR(S.mcp.prompts[0].command, 32) + ' ' + S.mcp.prompts[0].description), L('err', 'the tool list is fixed for the session; /mcp reconnect NAME restarts a server that gave up'));
  return res(o, 0, 5);
};
OUT['/skills'] = function () { return res(S.skills.map(function (k) { return L('err', '  ' + F.padR(k.name, 20) + ' ' + oneLine(k.summary, 90) + (k.youOnly ? ' (you only)' : '')); }), 0, 3); };
OUT['/sessions'] = function (fl, ctx) { return OUT['sessions']({ n: 10 }, ctx); };
OUT['/cwd'] = function (fl, ctx) { return res([L('err', 'cwd: ' + ss(ctx).cwd + ' (a session works in one directory: start sleipnir there, or /restart --cwd DIR)')], 0, 2); };
OUT['/rewind'] = function (fl, ctx) {
  var sx = ss(ctx), id = pos(fl)[0];
  if (!id) {
    var list = sx.checkpoints.filter(function (c) { return c.files > 0; });
    if (!list.length) return res([L('err', 'no checkpoints yet (one is kept for each turn that changes a file)')], 0, 3);
    return res(list.map(function (c) { return L('err', '  ' + c.id + '  ' + c.time + '  ' + c.files + ' files  ' + c.label); }).concat([L('dim', '/rewind ID puts the files back as they were; /diff ID shows what changed')]), 0, 4);
  }
  var cp = sx.checkpoints.filter(function (c) { return c.id === id; })[0];
  if (!cp) return res([L('bad', 'rewind: checkpoint: unknown checkpoint')], 0, 3);
  var n = S.diffSince('shop', id, 'now').length;
  return res([L('ok', 'checkpoint ' + id + ': restored ' + n)], 0, 38, card('rewind', [['checkpoint', id], ['files put back', String(n)], ['the agents are told', 'to read them again']]));
};
OUT['/diff'] = function (fl, ctx) {
  var sx = ss(ctx), id = pos(fl)[0], o = [];
  if (!id) { var c = sx.checkpoints.filter(function (x) { return x.files > 0; })[0]; if (!c) return res([L('err', 'usage: /diff <checkpoint id>   (no checkpoint has changed a file yet)')], 0, 3); id = c.id; o.push(L('err', 'checkpoint ' + id + ', the newest that changed a file')); }
  if (!sx.checkpoints.some(function (c) { return c.id === id; })) return res([L('bad', 'diff: checkpoint: unknown checkpoint')], 0, 3);
  S.diffSince('shop', id, 'now').forEach(function (f) {
    o.push(L('head', f.path + ' (' + f.status + ')'));
    S.unified(f).split('\n').forEach(function (t) { o.push(L(t[0] === '+' && t.slice(0, 3) !== '+++' ? 'ok' : t[0] === '-' && t.slice(0, 3) !== '---' ? 'bad' : t[0] === '@' ? 'dim' : 'out', t)); });
  });
  return res(o, 0, 12);
};
OUT['/recon'] = function () {
  var r = S.recon.budgets[5000], lsk = S.skills.filter(function (k) { return !k.youOnly; }).map(function (k) { return k.name + ': ' + k.summary; });
  var t = '<shared-context>\n## runtime\nOperating system: linux.\nCommand shell: bash.\nThe bash tool runs commands directly in this shell; send commands without an extra shell wrapper. Use this shell\'s syntax.\n\n## project\n' + r.text.replace(/^## project\n/, '').replace(/\n+$/, '') + '\n\n## instructions\n### AGENTS.md (project)\n' + S.config.files.agents.replace(/\n+$/, '') + '\n\n## skills\n<skills>\nLoad one with the skill tool when its description matches the task.\n' + lsk.join('\n') + '\n</skills>\n</shared-context>';
  return res(lines('err', t), 0, 6, card('recon', [['layer', 'G1 shared pin (' + F.tok(S.layers[1].tokens) + ' in the sample roster)'], ['survey', r.tokens + ' tokens (REAL, this sample project)'], ['files considered', String(r.files)]]));
};
OUT['/help'] = function () {
  var o = S.chatSlashText().split('\n').map(function (t) { return L(/^\//.test(t) || !t ? 'err' : 'head', t); });
  o.push(L('err', ''), L('head', 'custom commands:'));
  S.commands.forEach(function (c) { o.push(L('err', '/' + F.padR(c.name, 17) + ' ' + oneLine(c.description, 90))); });
  o.push(L('err', ''), L('head', 'skills (also loadable by the model; /skills lists them):'));
  return res(o, 0, 3);
};
/** chatSlashText(): the text of /help (REAL: chatHelp), from the generated spec when it is loaded, else the built-in copy. */
S.chatSlashText = function () {
  var spec = S.cliSpec && S.cliSpec.chatSlash, out = [], group = '';
  if (!spec) return S.helpFallback;
  spec.forEach(function (c) { if (c.group !== group) { if (group) out.push(''); out.push(c.group); group = c.group; } out.push(F.padR(c.cmd + (c.args ? ' ' + c.args : ''), 18) + c.desc); });
  return out.join('\n');
};
S.helpFallback = 'conversation\n/goal TEXT         work until it is met, judged on evidence (/goal: status)\n/new               start again, empty (same model and mode)\n/clear             the same as /new\n/resume [id]       pick an earlier session from a menu, and continue it\n/sessions          the newest sessions\n/compact [focus]   fold the older thread now; focus says what to keep in view\n/rewind [id]       list checkpoints, or restore files to before a turn\n/diff [id]         what changed in the newest checkpoint, or in <id>\n/exit              quit (Ctrl-D, or Ctrl-C twice at the prompt)\n\nmodel and cost\n/model [ref]       pick a model from a menu; a team starts again on it\n/effort [level]    show or change reasoning effort (closest supported level)\n/fav [ref]         star a model, or unstar it; starred ones lead in /model\n/login [provider]  add a key, or sign in with ChatGPT (the chat comes back)\n/budget [usd|off]  the dollar budget for the turns from now on\n/cost              tokens, cost and cache hit ratio so far\n/stats             the stats page (ctrl+t): cost, cache, savings, layers\n/context           what each layer of the prompt weighs\n/status            model, mode, session, budget and cost at a glance\n\npermissions\n/mode <m>          default | accept-edits | plan | bypass | yolo\n/plan [prompt]     switch to read-only mode; with a prompt, start planning it\n/allow <rule>      allow, this session, what would ask: tests, Bash(go test:*)\n/permissions       the mode and the rules in force\n/trust             this project\'s own instructions and settings, and your yes\n\na team, and the program\n/roles [role=m]    which model each role runs on; change one (restarts)\n/swarm <n> [flags] start again as a manager and n workers\n/restart [flags]   start again with other flags: --no-mcp, --cwd DIR, ...\n/agents            the team\'s agents and tasks (ctrl+g: cockpit)\n/steer TEXT        tell the running turn something, without stopping it\n/verbose [on|off]  notices and tool errors\n/anim [on|off]     motion\n/cwd               the directory this session works in\n\nwhat the model knows\n/recon             the project map in the shared layer\n/skills            the skills the model can load\n/mcp               tool servers: state and tools (/mcp reconnect NAME)\n/help              this text, and your custom commands and skills';
OUT['/agents'] = function (fl, ctx) {
  var sx = ss(ctx);
  if (!sx.swarm) return res([L('err', 'single agent session (start with --swarm N for a team)')], 0, 3);
  var o = sx.team.slice().sort(function (a, b) { return a.id < b.id ? -1 : 1; }).map(function (a) { return L('err', '  ' + F.padR(a.id, 8) + ' ' + F.padR(a.role, 10) + ' ' + F.padR(a.state, 8) + ' ' + F.padR(a.task || '', 4) + ' ' + oneLine(a.doing, 80)); });
  S.tasks.forEach(function (t) { o.push(L('err', '  ' + F.padR(t.id, 4) + ' ' + F.padR(t.status, 8) + ' ' + F.padR(t.owner || '', 8) + ' ' + oneLine(t.title, 80))); });
  return res(o, 0, 4, card('agents', [['team', S.teamSummary().banner], ['active', S.teamSummary().activeText], ['board', S.board.text]]));
};
OUT['/goal'] = function (fl, ctx) {
  var sx = ss(ctx), w = pos(fl), arg = w.join(' ').trim(), g = sx.goal;
  if (!arg) {
    if (!g) return res([L('err', 'no goal. /goal TEXT sets one: the harness keeps the agent going until a judge finds evidence that it is met')], 0, 3);
    var o = [L('err', 'goal (' + (g.paused ? 'paused: ' + g.paused : 'active') + '): ' + g.text), L('err', 'continuations ' + g.turns + ' of ' + g.max + (g.reason ? '; the judge\'s last word: ' + g.reason : ''))];
    g.plan.forEach(function (p) { o.push(L('err', '  [' + p.status + '] ' + p.step)); });
    return res(o, 0, 4);
  }
  var a = arg.toLowerCase();
  if (a === 'clear') { sx.goal = null; return res([L('err', 'goal cleared')], 0, 2); }
  if (a === 'pause') { if (g) { g.paused = 'paused by you'; return res([L('err', 'goal paused; /goal resume goes on')], 0, 2); } return res([], 0, 2); }
  if (a === 'resume') { if (!g) return res([L('err', 'no goal to resume')], 0, 2); g.paused = ''; g.max = g.turns + 20; g.turns++; return res([L('dim', '(sends the continuation: "[standing goal, continuation ' + g.turns + ' of at most ' + g.max + ']")')], 0, 2); }
  sx.goal = { text: arg, state: 'active', paused: '', turns: 0, max: 20, reason: '', plan: [] };
  return res([L('err', 'goal set: checked after each turn. Esc pauses it; /goal shows where it stands')], 0, 3);
};
OUT['/budget'] = function (fl, ctx) {
  var sx = ss(ctx), v = pos(fl)[0], spent = agentTotals(sx).cost;
  if (v === undefined) return res([L('err', sx.budget > 0 ? 'budget: $' + sx.budget.toFixed(2) + ', spent $' + spent.toFixed(4) + ' (change it with /budget <dollars>, /budget off removes it)' : 'budget: none, spent $' + spent.toFixed(4) + ' (set one with /budget <dollars>)')], 0, 2);
  if (v === 'off') { sx.budget = 0; return res([L('err', 'budget: removed')], 0, 2); }
  var usd = parseFloat(v);
  if (!(usd > 0)) return res([L('err', 'budget: want a dollar amount above zero (or off), got "' + v + '"')], 0, 2);
  sx.budget = usd;
  return res([L('err', 'budget: $' + usd.toFixed(2) + ' for the turns from now on (spent so far $' + spent.toFixed(4) + ')')], 0, 2);
};
OUT['/mode'] = function (fl, ctx) {
  var sx = ss(ctx), m = pos(fl)[0];
  if (!m) return res([L('err', 'mode: ' + sx.mode + ' (change it: /mode default | accept-edits | plan | bypass | yolo)')], 0, 2);
  if (['default', 'accept-edits', 'plan', 'bypass', 'yolo'].indexOf(m) < 0) return res([L('err', 'unknown mode; use default, accept-edits, plan, bypass or yolo')], 0, 2);
  sx.mode = m;
  return res([L(m === 'bypass' || m === 'yolo' ? 'warn' : 'err', 'mode: ' + m)], 0, 2);
};
OUT['/plan'] = function (fl, ctx) { ss(ctx).mode = 'plan'; return res([L('err', 'plan mode: read-only')], 0, 2); };
OUT['/effort'] = function (fl, ctx) {
  var sx = ss(ctx), v = pos(fl).join(' ');
  if (v) sx.effort = v;
  var o = [L('err', 'effort: ' + sx.effort + ' (this model: ' + (sx.effort === 'default' ? 'provider default' : sx.effort) + ')')];
  o.push(L('err', v ? 'Applies to subsequent requests; team roles use their closest supported level.' : 'Choose: /effort default|none|minimal|low|medium|high|xhigh|max'));
  return res(o, 0, 2);
};
OUT['/model'] = function (fl, ctx) {
  var sx = ss(ctx), r = pos(fl)[0];
  if (!r) return res([L('err', 'model: ' + sx.model + ' (change it with /model provider/model; list them with `sleipnir models`)')], 0, 2);
  if (!S.models.some(function (m) { return m.ref === r; })) return res([L('err', 'model: unknown model "' + r + '" (see `sleipnir models`): nothing was changed')], 0, 3);
  sx.model = r;
  return res([L('err', 'model: ' + r + (sx.swarm ? ' (the team starts again on it, with the manager\'s conversation; the prompt cache starts over)' : ' (the conversation carries over; the prompt cache starts over)'))], 0, 14);
};
OUT['/roles'] = function (fl, ctx) {
  var sx = ss(ctx), w = pos(fl);
  if (w.length) {
    var kv = w[0].split('='); if (kv.length !== 2 || !kv[0] || !kv[1]) return res([L('err', 'usage: /roles [role=provider/model ...]: show the table, or change roles (e.g. /roles manager=anthropic/claude-opus-5-5 compactor=local/qwen-sample-32b)')], 0, 2);
    sx.roleModels[kv[0]] = kv[1];
    return res([L('err', 'restarting: sleipnir chat --role-model ' + kv[0] + '=' + kv[1])], 0, 20);
  }
  var o = [L('err', 'who runs on which model (change one with /roles role=provider/model; the chat restarts with the choice):')];
  S.roleModels.rows.forEach(function (r) {
    var m = r.role === 'default' || r.role === 'manager' ? sx.model : (sx.roleModels[r.role] && r.role !== 'compactor' ? sx.roleModels[r.role] : r.model);
    var from = (sx.roleModels[r.role] && r.role !== 'default' && r.role !== 'manager' && r.role !== 'compactor' && m !== r.model) ? '--role-model' : r.from;
    o.push(L('err', '  ' + F.padR(r.role, 10) + ' ' + F.padR(m, 44) + ' ' + from));
  });
  return res(o, 0, 3);
};
OUT['/allow'] = function (fl, ctx) {
  var sx = ss(ctx), w = pos(fl);
  if (!w.length) return res([L('err', 'usage: /allow tests | /allow \'Bash(go test:*)\' | /allow \'Edit(src/**)\' ...: allow for the rest of this session what would otherwise ask; tests is the build and test commands of most projects (' + S.permissions.testsPreset.summary + ')')], 0, 2);
  var rules = []; w.forEach(function (x) { if (x === 'tests') rules = rules.concat(S.permissions.testsPreset.rules); else rules.push(x); });
  rules.forEach(function (r) { if (!sx.rules.some(function (q) { return q.rule === r; })) sx.rules.push({ rule: r, origin: w.indexOf('tests') >= 0 ? 'this session (tests preset)' : 'this session', list: 'allow' }); });
  var done = rules, msg;
  if (w.indexOf('tests') >= 0 && done.length >= S.permissions.testsPreset.rules.length) msg = 'allowed for this session: the build and test commands (' + S.permissions.testsPreset.summary + '; ' + done.length + ' rules)';
  else if (done.length > 4) msg = 'allowed for this session: ' + done.length + ' rules, among them ' + done[0] + ', ' + done[1];
  else msg = 'allowed for this session: ' + done.join(', ');
  return res([L('err', msg)], 0, 3);
};
OUT['/fav'] = function (fl, ctx) { var s = st(ctx), ref = pos(fl)[0] || ss(ctx).model, i = s.favourites.indexOf(ref); if (i >= 0) { s.favourites.splice(i, 1); return res([L('err', ref + ' is no longer a favorite')], 0, 3); } s.favourites.push(ref); return res([L('err', ref + ' is now a favorite (starred models come first in /model and `sleipnir models`)')], 0, 3); };
OUT['/verbose'] = function (fl, ctx) { var v = pos(fl)[0] || (ss(ctx).verbose ? 'off' : 'on'); ss(ctx).verbose = v === 'on'; return res([L('err', 'verbose: ' + v + ' (notices and tool errors ' + (v === 'on' ? 'are shown' : 'show only warnings') + ')')], 0, 2); };
OUT['/anim'] = function (fl, ctx) { var v = pos(fl)[0] || (ss(ctx).anim ? 'off' : 'on'); ss(ctx).anim = v === 'on'; return res([L('err', 'animation: ' + v)], 0, 2); };
OUT['/steer'] = function (fl, ctx) {
  var text = pos(fl).join(' ').trim();
  if (!text) return res([L('err', 'usage: /steer TEXT: tell the running turn something without stopping it (use the other file, skip the tests); it is read with the agent\'s next step')], 0, 2);
  ss(ctx).steered.push(text);
  return res([L('err', 'steering sent: the agent reads it with its next step')], 0, 2);
};
OUT['/compact'] = function (fl, ctx) {
  var focus = pos(fl).join(' ').trim();
  return res([L('err', 'compacted (fork): 4 turns folded, 2612 → 1104 tokens; the next request writes the cached prefix again, once'), ...(focus ? [L('dim', 'focus kept in view: ' + focus)] : [])], 0, 1800, card('compact', [['folded', '4 turns'], ['thread', '2.6k → 1.1k  -57%'], ['rebase', 'a declared, priced event: the next request writes the cached prefix again, once']]));
};
OUT['/swarm'] = function (fl) {
  var n = Number(pos(fl)[0]);
  if (!(n >= 0)) return res([L('err', 'usage: /swarm <n> [flags]: start again as a manager and n workers, e.g. /swarm 8 --verify "go test {dirs}" --isolation worktree')], 0, 2);
  if (n > 12) return res([L('bad', 'swarm: ' + n + ' workers requested (and the manager) but swarm.max_workers caps a session at 12 workers')], 0, 3);
  return res([L('err', 'restarting: sleipnir chat --swarm ' + n + ' ' + pos(fl).slice(1).join(' '))], 0, 20, card('swarm', [['workers', String(n)], ['legs', n > 8 ? '8 (worker 9 shares leg 1: +1)' : String(n)], ['the conversation', 'the manager\'s comes along']]));
};
OUT['/restart'] = function (fl) { return res([L('err', 'restarting: sleipnir chat ' + Object.keys(fl).filter(function (k) { return k !== '_'; }).map(function (k) { return '--' + k + (fl[k] === true ? '' : ' ' + fl[k]); }).join(' '))], 0, 20); };
OUT['/new'] = function () { return res([L('err', 'new conversation (same model and mode)')], 0, 4); };
OUT['/exit'] = function () { return res([L('err', 'bye')], 0, 2); };
OUT['/stats'] = function (fl, ctx) {
  var a = OUT['/cost'](fl, ctx), b = OUT['/context'](fl, ctx), t = agentTotals(ss(ctx));
  var extra = [L('err', 'saved by the cache at list price: ~' + t.savedText3 + ' (est.)'), L('err', 'all agents: ' + F.tok(t.prompt) + ' prompt tokens, ' + F.tok(t.read) + ' read from the cache, hit ' + t.hitPct + '%, cost ' + t.costText4)];
  return res(a.lines.concat(b.lines, extra), 0, 6, card('stats', [['cost', t.costText4], ['hit', t.hitPct + '%'], ['saved (est.)', t.savedText3], ['compactions', '1'], ['cache breaks', '1']]));
};
OUT['/login'] = function (fl, ctx) { return OUT['login']({ _: pos(fl) }, ctx); };
OUT['/resume'] = function (fl, ctx) { var id = pos(fl)[0]; if (!id) return OUT['sessions']({ n: 10 }, ctx); var r = st(ctx).sessions.filter(function (x) { return x.id.indexOf(id) === 0; })[0]; if (!r) return res([L('bad', 'resume: no session starts with "' + id + '"')], 1, 3); return res([L('err', 'resuming ' + r.id + (r.kind === 'team' ? ' (a team: its manager and its board)' : ''))], 0, 40); };
OUT['/clear'] = OUT['/new'];
