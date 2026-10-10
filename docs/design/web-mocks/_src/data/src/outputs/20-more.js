/* ---- chat, run, swarm ---------------------------------------------------------------------------------------------------------- */
OUT['chat'] = function (fl) {
  var swarm = flag(fl, 'swarm', null), n = swarm === null ? 8 : Number(swarm), model = flag(fl, 'model', 'anthropic/claude-sonnet-5-5'), mode = flag(fl, 'mode', 'default');
  var o = [
    L('head', 'sleipnir chat is an interactive session: in the web UI it is the Chat view.'),
    L('out', 'sleipnir 0.3.2 · ' + model + ' · ' + (n > 0 ? 'manager + ' + n + ' workers' : 'single agent') + ' · ' + mode),
    L('dim', 'Sessions → New session opens the same options as a form: cwd, model, mode, workers, isolation, verify, commit, mailman, role models, budget, allow rules, trust, no-mcp.'),
    L('dim', 'ctrl+t is the stats page, ctrl+g the team cockpit (alt+t and alt+g in the browser), / the command palette, Esc interrupts.'),
  ];
  return res(o, 0, 8, card('chat', [['web view', 'Chat'], ['team', n > 0 ? 'manager + ' + n + ' workers' : 'single agent'], ['mode', mode], ['model', model], ['budget', flag(fl, 'budget-usd') ? '$' + Number(flag(fl, 'budget-usd')).toFixed(2) : 'none'], ['isolation', flag(fl, 'isolation', 'config swarm.isolation')]]));
};
function runScript(goal) {
  if (/pagin|orders|failing|fix/i.test(goal)) return {
    say: ['I\'ll start with the code the test exercises.', 'Now the tests, to see how they fail.', 'Two pagination tests fail. Let me read what they expect.', 'The offset is the bug: pages count from 1, so page 1 starts at row 0, not at row size. It should be (page - 1) * size.', 'Running the tests again.', 'They pass. One more look, for callers that worked around the old offset.'],
    tools: [['read orders/list.go', '2ms', 0], ['bash go test ./orders/...', '481ms', 1], ['read orders/list_test.go', '2ms', 0], ['edit orders/list.go', '3ms', 0], ['bash go test ./orders/...', '1.236s', 0], ['grep \\.List\\(', '21ms', 0]],
    final: '## Fixed\n\n`List` took its offset from the page number, but pages count from 1, so page 1 skipped the first `size` rows.\n\n- the offset is `(page - 1) * size` now\n- `go test ./orders/...` passes, and the HTTP handler already sends one-based pages',
    steps: 7, cost: 0.0464, hit: 70, took: '23s', changed: 'orders/list.go' };
  return { say: ['I\'ll look at the project map first, then the files the goal names.', 'Done reading; the change is small.'], tools: [['read AGENTS.md', '1ms', 0], ['grep ' + oneLine(goal, 24), '14ms', 0], ['edit docs/install.md', '3ms', 0]], final: 'Done: ' + oneLine(goal, 90) + '. I changed the places the search found and ran the project\'s checks.', steps: 5, cost: 0.0318, hit: 84, took: '17s', changed: 'docs/install.md' };
}
function runOut(fl, team) {
  var goal = pos(fl).join(' ').trim() || (flag(fl, 'resume') ? '(resumed)' : '');
  if (!goal) return bad('run: a goal is required (words, or - to read stdin)', 2);
  var mode = flag(fl, 'mode', 'default'), quiet = !!flag(fl, 'quiet'), json = !!flag(fl, 'json'), n = team ? Number(team) : Number(flag(fl, 'swarm', 0));
  var sc = runScript(goal), id = '20260102-030512-a41c0d', o = [];
  if (json) {
    o.push(L('out', JSON.stringify({ type: 'text', agent: 'main', text: sc.say[0] })));
    sc.tools.forEach(function (t, i) { o.push(L('out', JSON.stringify({ type: 'tool_start', id: 'call_' + (i + 1), name: t[0].split(' ')[0] }))); o.push(L('out', JSON.stringify({ type: 'tool_end', id: 'call_' + (i + 1), failed: !!t[2], ...(t[2] ? { exit_code: 1 } : {}) }))); });
    o.push(L('out', JSON.stringify({ type: 'result', text: sc.final, steps: sc.steps, cost_usd: sc.cost, hit_ratio: sc.hit / 100, compactions: 0, stop: 'end_turn', session: id, dir: '~/.sleipnir/sessions/' + id, elapsed_ms: 23000 })));
    return res(o, 0, 23000);
  }
  var noTerm = mode === 'default' ? 'no terminal to ask on: edits and commands that need an answer are refused. --mode accept-edits lets the edits and the usual build and test commands through.' : null;
  if (!quiet) {
    o.push(L('err', 'sleipnir 0.3.2 · ' + flag(fl, 'model', 'anthropic/claude-sonnet-5-5') + ' · session ' + id + (n > 0 ? ' · manager + ' + n + ' workers' : '')));
    if (noTerm) o.push(L('err', noTerm));
    var ti = 0;
    sc.say.forEach(function (t, i) { o.push(L('out', t)); if (sc.tools[i]) { var tl = sc.tools[i]; o.push(L(tl[2] ? 'warn' : 'dim', (tl[2] ? '✗ ' : '✓ ') + tl[0] + ' (' + tl[1] + (tl[2] ? ', exit 1' : '') + ')')); } });
    o.push(L('out', ''), L('out', sc.final));
  } else o.push(L('out', sc.final));
  var sumLine = '── ' + sc.took + ' · ' + sc.steps + ' steps · $' + sc.cost.toFixed(4) + ' · cache hit ' + sc.hit + '% · 0 compactions · ~/.sleipnir/sessions/' + id;
  o.push(L('err', sumLine));
  if (n > 0 && flag(fl, 'verify')) o.push(L('err', '   verify `' + flag(fl, 'verify') + '`: the gate ran 4 times and passed every time'));
  o.push(L('err', mode === 'plan' ? '   no file was changed' : '   changed: ' + sc.changed));
  return res(o, 0, 23000, card('run', [['session', id], ['steps', String(sc.steps)], ['cost', '$' + sc.cost.toFixed(4)], ['cache hit', sc.hit + '%'], ['changed', mode === 'plan' ? 'nothing (plan mode)' : sc.changed]]));
}
OUT['run'] = function (fl) { return runOut(fl, 0); };
OUT['swarm'] = function (fl) {
  var n = Number(pos(fl)[0]);
  if (!(n >= 1)) return bad('swarm: want the number of workers first, 1 or more, as in `sleipnir swarm 8 "fix the failing test"`', 2);
  var f2 = Object.assign({}, fl); f2._ = pos(fl).slice(1);
  if (S.config && n > 12) return bad('swarm: ' + n + ' workers requested (and the manager) but swarm.max_workers caps a session at 12 workers');
  return runOut(f2, n);
};

/* ---- recon, inspect, watch, replay, demo, mock, sim, friction ------------------------------------------------------------------- */
OUT['recon'] = function (fl) {
  var b = Number(flag(fl, 'budget', 5000)), keys = Object.keys(S.recon.budgets).map(Number).sort(function (a, c) { return a - c; });
  var pick = keys[0]; keys.forEach(function (k) { if (k <= b) pick = k; });
  var r = S.recon.budgets[pick];
  var o = lines('out', r.text); o.push(L('err', r.footer));
  var cd = [['budget', String(b)], ['tokens', String(r.tokens)], ['files considered', String(r.files)]];
  if (pick !== b) cd.push(['note', 'shown as captured at --budget ' + pick + ' (the survey is deterministic; other budgets are not precomputed)']);
  return res(o, 0, 120, card('recon', cd));
};
OUT['inspect'] = function (fl) {
  if (flag(fl, 'json')) return res(lines('out', JSON.stringify(S.inspect, null, 2)), 0, 80);
  var addr = flag(fl, 'addr', '127.0.0.1:8787');
  return res([L('out', 'http://' + addr + '/'), L('dim', 'serving a read-only dashboard over ' + (pos(fl)[0] || 'the newest session') + ' (GET only, loopback, loads nothing from the network); in the web UI this is the Cache view')], 0, 30, card('inspect', [['dashboard', 'http://' + addr + '/'], ['session', pos(fl)[0] || '20260102-030405-5eed01'], ['web view', 'Cache']]));
};
OUT['watch'] = function (fl) { return res([L('out', 'sleipnir watch is a terminal program: the swarm cockpit, the cache, the mail and the board drawn from a session\'s event log.'), L('dim', 'In the web UI it is the Cockpit view (views: ' + flag(fl, 'view', 'cockpit') + '; keys o c m b).')], 0, 6, card('watch', [['session', pos(fl)[0] || 'latest'], ['view', flag(fl, 'view', 'cockpit')], ['web view', 'Cockpit']])); };
OUT['replay'] = function (fl) {
  if (flag(fl, 'final')) {
    var view = flag(fl, 'view', 'cockpit'), sc = S.replay.finalScreens[view] || S.replay.finalScreens.cockpit;
    return res(lines('out', sc.replace(/\n$/, '')), 0, 240, card('replay --final', [['session', pos(fl)[0] || '20260101-171204-7c1e3a'], ['view', S.replay.finalScreens[view] ? view : 'cockpit (the other views are drawn by the web UI)']]));
  }
  if (flag(fl, 'record')) return res([L('out', 'wrote ' + flag(fl, 'record') + ' (an animated SVG of the replay: CSS only, no script)')], 0, 900);
  return res([L('out', 'sleipnir replay plays a recorded session back on the same screens, on a clock of its own.'), L('dim', 'In the web UI it is the Replay view: space pauses, the arrows seek 10 s (shift: a minute), + and - change the speed, Home and End jump.')], 0, 6, card('replay', [['session', pos(fl)[0] || 'latest'], ['events', String(S.replay.shop.events.length) + ' distilled (of 661)'], ['duration', '20.7 s']]));
};
OUT['demo'] = function (fl) {
  var sc = flag(fl, 'scenario', 'shop');
  var t = sc === 'handbook' ? S.real.demo.handbook : S.real.demo.shop;
  var o = lines('out', t);
  return res(o, 0, sc === 'handbook' ? 900 : 20700, card('demo', [['scenario', sc], ['session', sc === 'handbook' ? '20260101-171310-d2f6b8' : '20260101-171204-7c1e3a'], ['see it again', 'sleipnir replay ' + (sc === 'handbook' ? '20260101-171310-d2f6b8' : '20260101-171204-7c1e3a')]]));
};
OUT['mock'] = function (fl) {
  var addr = flag(fl, 'addr', '127.0.0.1:8089'), eng = flag(fl, 'engines', 2);
  return res([L('out', 'mock provider on http://' + addr + ' (' + eng + ' engines); use --provider custom --base-url http://' + addr)], 0, 12);
};
function simKey(fl) {
  var G = S.sim.grid, mode = flag(fl, 'mode', 'compare'), prov = flag(fl, 'provider', 'anthropic'), ag = Number(flag(fl, 'agents', 20)), seed = Number(flag(fl, 'seed', 1));
  var near = function (arr, v) { var best = arr[0]; arr.forEach(function (x) { if (Math.abs(x - v) < Math.abs(best - v)) best = x; }); return best; };
  var a2 = near(G.agents, ag);
  var key = mode === 'agents' ? 'agents|' + prov + '|20|1' : mode === 'compare' ? 'compare|' + prov + '|' + a2 + '|' + (a2 === 20 && G.seeds.indexOf(seed) >= 0 ? seed : 1) : mode + '|' + prov + '|' + a2 + '|1';
  return { key: key, exact: a2 === ag && (mode !== 'compare' || a2 !== 20 || G.seeds.indexOf(seed) >= 0), used: { agents: a2 } };
}
OUT['sim'] = function (fl) {
  var mode = flag(fl, 'mode', 'compare'), prov = flag(fl, 'provider', 'anthropic');
  if (['compare', 'scenarios', 'pins', 'agents'].indexOf(mode) < 0) return bad('sim: unknown --mode "' + mode + '"');
  if (['anthropic', 'marketplace'].indexOf(prov) < 0) return bad('sim: unknown --provider "' + prov + '"');
  var k = simKey(fl);
  if (flag(fl, 'json')) {
    var jk = mode + '|' + prov, j = S.sim.json[jk] || S.sim.json[mode + '|anthropic'];
    return res(lines('out', JSON.stringify(j, null, 2)), 0, 70);
  }
  var text = S.sim.pool[S.sim.idx[k.key]];
  var o = lines('out', text.replace(/\n$/, ''));
  if (!k.exact) o.push(L('dim', '(sample: the model is run for --agents 20/50 and seeds 1-3; this is the nearest precomputed case, ' + k.used.agents + ' workers)'));
  var cd = [['mode', mode], ['provider', prov], ['assumptions', 'printed above: a model, not a benchmark']];
  return res(o, 0, 70 + Number(flag(fl, 'agents', 20)) * 4, card('sim', cd));
};
OUT['friction'] = function (fl) {
  var paths = pos(fl);
  if (!paths.length) return res([L('bad', 'sleipnir: friction: pass a session, a directory of sessions or a run directory'), L('dim', 'usage: sleipnir friction [flags] PATH...')], 2, 4);
  var rep = S.clone(S.friction.report), cat = flag(fl, 'category', ''), top = Number(flag(fl, 'top', 15)), minc = Number(flag(fl, 'min-count', 1)), ex = Number(flag(fl, 'examples', 3));
  rep.findings = rep.findings.filter(function (f) { return f.count >= minc; });
  if (cat) rep.findings = rep.findings.filter(function (f) { return f.category.indexOf(cat) === 0; });
  if (top > 0) rep.findings = rep.findings.slice(0, top);
  rep.findings.forEach(function (f) { f.examples = (f.examples || []).slice(0, ex); });
  if (flag(fl, 'json')) return res(lines('out', JSON.stringify(rep, null, 2)), 0, 40);
  return res(lines('out', S.frictionText(rep)), 0, 40, card('friction', [['sessions', String(rep.sessions)], ['requests', String(rep.requests)], ['findings', String(rep.findings.length)]]));
};

/* ---- index commands ------------------------------------------------------------------------------------------------------------ */
OUT['rl'] = function () { return res(lines('out', 'sleipnir rl: the RL environment (docs/TRAINING-DATA.md): generate tasks, roll a policy out on them, score, export.\n\n  sleipnir rl taskgen <generator>    make tasks from history, authored fixtures, mutations or recall questions, or compose swarm tasks\n  sleipnir rl tasks <command> FILE   validate, filter, split and check task files\n  sleipnir rl rollout                run G samples per task with a policy and write a run directory\n  sleipnir rl eval                   run held-out tasks and report pass@k, cost and protocol quality\n  sleipnir rl serve                  HTTP rollout server for a trainer\n  sleipnir rl reward                 re-score a run directory with different reward weights\n  sleipnir rl report                 what runs measured: pass rate with its interval, cost, cache hits, friction\n  sleipnir rl compare                two runs or saved reports over the tasks both ran, with paired intervals and gates\n  sleipnir rl export                 write trainer-ready data\n  sleipnir rl expand                 turn a deduplicated canonical export back into inline form\n  sleipnir rl verify                 replay every recorded prompt and check it against its wire hash\n  sleipnir rl show                   summarise a run directory, or one episode of it'), 0, 4); };
OUT['rl taskgen'] = function () { return res(lines('out', 'usage: sleipnir rl taskgen <generator> [flags]\n\n  git        mine a repository\'s history: a commit that changes source and tests becomes a task\n  mutate     inject bugs the project\'s own tests catch; the reverse patch is the reference solution\n  composite  combine independent tasks of one repository into swarm tasks\n  recall     memory tasks: read a fact early, read many other files, then state the fact exactly\n  fixture    hand-written tasks: a directory of fixtures, one subdirectory each'), 0, 4); };
OUT['rl tasks'] = function () { return res(lines('out', 'usage: sleipnir rl tasks <validate|stats|filter|split|check> [flags] FILE'), 0, 4); };
