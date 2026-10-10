/* ---- rl: REAL captures of a small lab (12 fixture tasks, two scripted policies, G=4) ------------------------------------------------ */
function rlBlocks(text) {   // "$ sleipnir rl ... \n output \n[exit N]\n" blocks of the captured files
  var out = {};
  String(text).split(/^\$ sleipnir /m).slice(1).forEach(function (b) {
    var nl = b.indexOf('\n'), cmd = b.slice(0, nl), body = b.slice(nl + 1), m = /\n?\[exit (\d+)\]\s*$/.exec(body);
    out[cmd] = { text: (m ? body.slice(0, m.index) : body).replace(/\n+$/, ''), exit: m ? +m[1] : 0 };
  });
  return out;
}
var RLB = {};
['report', 'compare', 'show', 'evalTaskgen'].forEach(function (k) { var b = rlBlocks(S.real.rl[k]); Object.keys(b).forEach(function (c) { RLB[c] = b[c]; }); });
function rlBlock(prefix) { var ks = Object.keys(RLB); for (var i = 0; i < ks.length; i++) if (ks[i].indexOf(prefix) === 0) return RLB[ks[i]]; return null; }
function rlRun(name) { return S.rl.runs.filter(function (r) { return r.id === name.replace(/^.*\//, ''); })[0]; }
function needsArg(cmd, what) { return bad('rl ' + cmd + ': ' + what, 2); }

OUT['rl taskgen fixture'] = function (fl) {
  var dir = flag(fl, 'dir'), out = flag(fl, 'o', 'tasks.jsonl');
  if (!dir) return bad('rl taskgen fixture: --dir is required', 2);
  var ids = String(flag(fl, 'id', '')).split(',').filter(Boolean), all = S.rl.tasks.map(function (t) { return t.id; });
  var re = function (g) { return new RegExp('^' + g.replace(/[.+^${}()|[\]\\]/g, '\\$&').replace(/\*/g, '.*').replace(/\?/g, '.') + '$'); };
  var sel = ids.length ? all.filter(function (id) { return ids.some(function (g) { return re(g).test(id); }); }) : all;
  if (!sel.length) return bad('rl taskgen fixture: no fixture matches --id ' + ids.join(','));
  return res([L('out', 'wrote ' + sel.length + ' tasks to ' + out + ' (repositories in fixture-repos, hidden files and reference solutions in blobs)'), L('err', 'next: sleipnir rl tasks check ' + out + ', run from the directory that holds fixture-repos/ so the tasks\' relative repositories resolve')], 0, 1800, card('rl taskgen fixture', [['tasks', String(sel.length)], ['file', out], ['repositories', 'fixture-repos/<id> (one commit each)']]));
};
OUT['rl taskgen git'] = function (fl) {
  if (!flag(fl, 'repo')) return bad('rl taskgen git: --repo is required', 2);
  var out = flag(fl, 'o', 'tasks.jsonl');
  return res([L('out', 'wrote 0 tasks to ' + out + ' (hidden files and reference solutions in blobs)'), L('out', '  examined 1 commits, 0 candidates, 0 accepted, in 0s'), L('err', 'next: sleipnir rl tasks split ' + out + ', then sleipnir rl rollout --tasks <train file> --model <policy>')], 0, 90, card('rl taskgen git', [['examined', '1 commit'], ['accepted', '0'], ['why', 'a commit must change source and tests; this history has none that does']]));
};
OUT['rl taskgen mutate'] = function (fl) {
  if (!flag(fl, 'repo')) return bad('rl taskgen mutate: --repo is required', 2);
  var mx = Number(flag(fl, 'max', 3)), b = rlBlock('rl taskgen mutate'), out = flag(fl, 'o', 'tasks.jsonl');
  var o = S.rl.mutate.slice(0, Math.min(3, mx || 3)).map(function (t, i, a) { return L('out', 'accepted ' + t.id + ' (' + (i + 1) + '/' + (mx || 3) + ')'); });
  var n = o.length;
  o.push(L('out', 'wrote ' + n + ' tasks to ' + out + ' (hidden files and reference solutions in blobs)'), L('out', '  examined 8 commits, 12 candidates, ' + n + ' accepted, in 26s'), L('out', '  rejected: the mutation survives: the project\'s tests still pass=4'), L('err', 'next: sleipnir rl tasks split ' + out + ', then sleipnir rl rollout --tasks <train file> --model <policy>'));
  return res(o, 0, 26000, card('rl taskgen mutate', [['accepted', String(n)], ['kinds', 'boundary, return-bool'], ['verifier', 'the project\'s own go test, per package']]));
};
OUT['rl taskgen composite'] = function (fl) {
  var k = Number(flag(fl, 'k', 3)), file = pos(fl)[0];
  if (!file) return bad('rl taskgen composite: want a TASKS file', 2);
  return res([L('out', 'wrote 0 composite tasks (' + k + ' components each) from 12 tasks to ' + flag(fl, 'o', file.replace(/\.jsonl$/, '') + '.composite.jsonl')), L('err', 'composite verifiers reuse the components\' hidden files: keep the blobs directory next to the output (copy or link it)')], 0, 40, card('rl taskgen composite', [['written', '0'], ['why', 'every task of the sample set lives in its own repository; composites combine independent tasks of one repository']]));
};
OUT['rl taskgen recall'] = function (fl) {
  if (!flag(fl, 'repo')) return bad('rl taskgen recall: --repo is required', 2);
  return bad('rl taskgen recall: recall: only 1 suitable source files at HEAD; need more than --files=' + flag(fl, 'files', 12));
};
OUT['rl tasks validate'] = function (fl) { var f = pos(fl)[0]; if (!f) return bad('rl tasks validate: want a tasks FILE', 2); return res([L('ok', f + ': 12 tasks, all valid')], 0, 22); };
OUT['rl tasks stats'] = function (fl) { var f = pos(fl)[0]; if (!f) return bad('rl tasks stats: want a tasks FILE', 2); return res(lines('out', RLB_stats(f)), 0, 20); };
function RLB_stats(f) { return f + ': 12 tasks in 12 repositories\n  kinds: feature=6 fix=5 refactor=1\n  teams: single=12\n  tags:  easy=1 feature=6 fix=5 fixture=12 go=4 hard=3 js=3 medium=8 python=3 refactor=1 rust=2 sample=12'; }
OUT['rl tasks filter'] = function (fl) {
  var f = pos(fl)[0]; if (!f) return bad('rl tasks filter: want a tasks FILE', 2);
  var n = Number(flag(fl, 'n', 0)), tag = String(flag(fl, 'tag', '')), id = String(flag(fl, 'id', ''));
  var sel = S.rl.tasks.filter(function (t) {
    if (tag) { var ok = tag.split(',').every(function (x) { return x[0] === '!' ? t.tags.indexOf(x.slice(1)) < 0 : t.tags.indexOf(x) >= 0; }); if (!ok) return false; }
    if (id) { var gs = id.split(','); if (!gs.some(function (g) { return new RegExp('^' + g.replace(/\*/g, '.*') + '$').test(t.id); })) return false; }
    return true;
  });
  if (n > 0) sel = sel.slice(0, n);
  var o = []; if (!flag(fl, 'o') || flag(fl, 'o') === '/dev/stdout') sel.forEach(function (t) { o.push(L('dim', JSON.stringify({ id: t.id, kind: t.kind, repo: { path: t.repo, commit: t.commit }, tags: t.tags }).slice(0, 110) + '…')); });
  o.push(L('err', 'kept ' + sel.length + ' of 12 tasks'));
  return res(o, 0, 25);
};
OUT['rl tasks split'] = function (fl) {
  var f = pos(fl)[0]; if (!f) return bad('rl tasks split: want a tasks FILE', 2);
  var spec = String(flag(fl, 'spec', 'train:0.8,test:0.2')), parts = spec.split(',').map(function (p) { var q = p.split(':'); return [q[0], +q[1]]; });
  var prefix = flag(fl, 'out-prefix', f.replace(/\.jsonl$/, '')), ids = S.rl.tasks.map(function (t) { return t.id; }), o = [], i = 0;
  var tot = parts.reduce(function (a, p) { return a + p[1]; }, 0);
  parts.forEach(function (p, k) { var n = k === parts.length - 1 ? ids.length - i : Math.max(1, Math.round(ids.length * p[1] / tot)); i += n; o.push(L('out', prefix + '.' + p[0] + '.jsonl: ' + n + ' tasks from ' + n + ' repositories')); });
  return res(o, 0, 30);
};
OUT['rl tasks check'] = function (fl) {
  var f = pos(fl)[0]; if (!f) return bad('rl tasks check: want a tasks FILE', 2);
  var o = lines('out', S.real.rl.check).map(function (l) { return /: ok$/.test(l.t) ? L('ok', l.t) : l; });
  if (flag(fl, 'mutants')) o = o.map(function (l) { return /^(js-slugify|py-ini): ok$/.test(l.t) ? L('warn', l.t.replace(': ok', ': ok (WEAK: the verifier still passes with ' + (l.t[0] === 'j' ? 'src/slugify.js' : 'ini.py') + ' left at the start)')) : l; });
  return res(o, 0, 16400, card('rl tasks check', [['tasks', '12'], ['ok', '12'], ['failed', '0'], ['skipped', '0']]));
};
OUT['rl rollout'] = function (fl) {
  var model = String(flag(fl, 'model', 'sample-policy-v2')), v = /v1/.test(model) ? 1 : 2, run = rlRun('r00' + v);
  var out = flag(fl, 'out', 'runs/r00' + v), grp = Number(flag(fl, 'group', 4));
  if (!flag(fl, 'tasks')) return bad('rl rollout: --tasks is required', 2);
  var total = 12 * grp, o = [L('err', 'rolling out 12 tasks x ' + grp + ' samples with ' + model + ' (' + flag(fl, 'base-url', 'http://127.0.0.1:8000/v1') + ') into ' + out)];
  var rows = run.samples.filter(function (s) { return s[1] < grp; });
  rows.forEach(function (s, i) { o.push(L(s[2] ? 'ok' : 'dim', '[' + (i + 1) + '/' + total + '] ' + s[0] + '/' + s[1] + ' ok pass=' + (s[2] ? 'true' : 'false') + ' score=' + (s[2] ? '1.000' : '0.000'))); });
  o = o.concat(lines('out', S.real.rl['rolloutV' + v]));
  o.push(L('err', 'next: sleipnir rl export ' + out + ' --format steps -o data.jsonl'));
  return res(o, 0, run.durationMs, card('rl rollout', [['run', run.id], ['policy', model], ['rollouts', String(run.summary.rollouts)], ['pass rate', (run.summary.pass_rate * 100).toFixed(1) + '%'], ['spent', '$' + run.summary.spent_usd.toFixed(4)]]));
};
OUT['rl eval'] = function (fl) {
  if (!flag(fl, 'tasks')) return bad('rl eval: --tasks is required', 2);
  var b = rlBlock('rl eval');
  var o = lines('out', b.text.replace(/^\$ .*\n/, '')).map(function (l) { return /^\[\d\/6\]/.test(l.t) ? L('ok', l.t) : l; });
  return res(o, 0, 8100, card('rl eval', [['run', 'e001'], ['pass@1', '100.0%'], ['tasks', '3 held out (split test)'], ['contamination check', 'passed: none of them is in the training list']]));
};
OUT['rl serve'] = function (fl) { return res([L('err', 'rollout server on ' + flag(fl, 'addr', '127.0.0.1:8090') + ' (runs in ' + flag(fl, 'runs', 'runs') + ')')], 0, 25, card('rl serve', [['listening', flag(fl, 'addr', '127.0.0.1:8090')], ['endpoint', 'POST /v1/rollouts {task, policy:{base_url,model,sampling}, group, rewards}'], ['loopback', 'a token is required off loopback']])); };
OUT['rl reward'] = function (fl) {
  if (flag(fl, 'list-targets')) return res(lines('out', S.real.rl.listTargets), 0, 6);
  var runs = pos(fl); if (!runs.length) return bad('rl reward: want RUN_DIR...', 2);
  var b = rlBlock('rl reward'), t = b.text.split('\n'), keep = t.slice(0, 9).concat(['…'], t.slice(-1));
  var o = keep.map(function (l, i) { return L(i === 0 ? 'head' : /^\d+ episodes/.test(l) ? 'ok' : 'out', l); });
  if (!flag(fl, 'dry-run')) o[o.length - 1] = L('ok', '48 episodes rescored: mean reward 0.569 -> 0.569, 0 with hack flags (episode.json rewritten)');
  return res(o, 0, 640, card('rl reward', [['episodes', '48'], ['mean reward', '0.569 -> 0.569'], ['target price', flag(fl, 'target-price', 'anthropic-haiku')]]));
};
OUT['rl report'] = function (fl) {
  var runs = pos(fl).map(function (p) { return p.replace(/^.*\//, ''); }).filter(Boolean);
  if (!runs.length) return bad('rl report: want RUN_DIR|REPORT.json...', 2);
  var fmt = String(flag(fl, 'format', 'table')), md = rlBlock('rl report --format md').text.split('\n');
  var rowOf = function (r) { return md.filter(function (l) { return l.indexOf('| ' + r + ' |') === 0; })[0]; };
  for (var i = 0; i < runs.length; i++) if (!rowOf(runs[i])) return bad('rl report: ' + runs[i] + ': no such run directory (the sample set has r001 and r002)');
  if (fmt === 'json') return res(lines('out', JSON.stringify(S.real.rl.reportR002, null, 2)), 0, 120);
  if (fmt === 'md') return res(lines('out', [md[0], md[1]].concat(runs.map(rowOf)).join('\n')), 0, 90);
  var cells = function (l) { return l.replace(/^\| /, '').replace(/ \|$/, '').split(' | '); };
  var hdr = cells(md[0]).map(function (h, i, a) { return h; });
  var tbl = F.tabwriter([hdr.map(function (h) { return h; })].concat(runs.map(function (r) { return cells(rowOf(r)); })), 2);
  var o = tbl.map(function (t, i) { return L(i ? 'out' : 'head', t); });
  o.push(L('out', ''), L('dim', 'PASS: passed episodes / completed ones, with a 95% Wilson interval; SOLVED: tasks that at least one sample passed; HIT: cache-read tokens / input tokens.'), L('dim', 'Friction is per completed episode: tool calls that failed (TOOLERR), malformed or unknown ones (INVALID), requests the endpoint did not answer and were repeated (RETRY).'));
  if (flag(fl, 'tasks') || flag(fl, 'by-tag')) {
    var full = rlBlock('rl report --tasks --by-tag').text.split('\n'), at = full.indexOf('r002 by tag');
    o.push(L('out', ''));
    full.slice(at).forEach(function (l) { if (/ by tag$/.test(l) && !flag(fl, 'by-tag')) return; o.push(L('out', l)); });
  }
  return res(o, 0, 150, card('rl report', runs.map(function (r) { var ru = rlRun(r); return [r + ' (' + ru.model + ')', (ru.summary.pass_rate * 100).toFixed(1) + '% pass, $' + ru.summary.mean_cost_usd.toFixed(4) + '/episode']; })));
};
OUT['rl compare'] = function (fl) {
  var p = pos(fl).map(function (x) { return x.replace(/^.*\//, ''); });
  if (p.length !== 2) return bad('rl compare: want two runs or saved reports (A B)', 2);
  var ab = p.join(' '), blk = ab === 'r001 r002' ? rlBlock('rl compare runs/r001') : ab === 'r002 r001' ? rlBlock('rl compare --gate') : null;
  if (!blk) return bad('rl compare: ' + p.join(', ') + ': the sample set has runs r001 and r002');
  if (String(flag(fl, 'format', 'table')) === 'json') return res(lines('out', JSON.stringify(S.real.rl.compareJson, null, 2)), 0, 150);
  var t = blk.text.split('\n'), cut = t.indexOf('gates:'); if (cut >= 0) t = t.slice(0, cut - 1);
  var o = t.map(function (l, i) { return L(/^(A|B) /.test(l) ? 'head' : /better$/.test(l) ? 'ok' : /worse$/.test(l) ? 'warn' : 'out', l); });
  var gates = [].concat(flag(fl, 'gate', [])); var failed = 0;
  if (gates.length) {
    o.push(L('out', ''), L('head', 'gates:'));
    String(gates.join(',')).split(',').forEach(function (g) {
      var m = /^pass_at_1:([0-9.]+)$/.exec(g);
      if (ab === 'r002 r001' && m && 0.125 > +m[1]) { failed++; o.push(L('bad', '  FAIL  ' + g + '     pass_at_1 got worse by 0.125 (A 0.7292, B 0.6042), more than the tolerance of ' + m[1] + ', and the interval [-0.2292, -0.04167] excludes zero')); }
      else o.push(L('ok', '  ok    ' + g + '     did not get worse beyond the tolerance (or the interval includes zero)'));
    });
    if (failed) o.push(L('bad', 'sleipnir: rl compare: ' + failed + ' of ' + gates.join(',').split(',').length + ' gates failed'));
  }
  return res(o, failed ? 1 : 0, 210, card('rl compare', [['A', p[0]], ['B', p[1]], ['pass@1', ab === 'r001 r002' ? '0.604 -> 0.729 (+0.125, better)' : '0.729 -> 0.604 (-0.125, worse)'], ['paired tasks', '12'], ['resamples', '2000']]));
};
OUT['rl export'] = function (fl) {
  var runs = pos(fl); if (!runs.length) return bad('rl export: want RUN_DIR...', 2);
  var fmt = String(flag(fl, 'format', 'steps')), out = flag(fl, 'o', '(stdout)'), mx = Number(flag(fl, 'max-samples', 0));
  var o;
  if (fmt === 'sft') o = lines('out', 'exported ' + (mx || 3) + ' sft records from 1 of 48 episodes to ' + out + '\n  tokens: 9013 prompt, 1331 response, 1331 trained\n  by role: worker=' + (mx || 3) + '\n  dropped: episode:not_verified=13');
  else if (fmt === 'steps') o = lines('out', 'exported ' + (mx || 2) + ' steps records from 1 of 48 episodes to ' + out + '\n  tokens: 4338 prompt, 34 response, 34 trained\n  by role: worker=' + (mx || 2) + '\n  dropped: episode:flat_group=16');
  else o = lines('out', 'exported 48 ' + fmt + ' records from 35 of 48 episodes to ' + out + '\n  by role: worker=48\n  dropped: episode:flat_group=13  (sample: only steps and sft were captured from the binary)');
  return res(o, 0, 380, card('rl export', [['format', fmt], ['records', String(mx || 2)], ['redaction', flag(fl, 'no-redact') ? 'off' : 'on (secrets and personal data)'], ['advantage', flag(fl, 'advantage', 'grpo')]]));
};
OUT['rl expand'] = function (fl) { if (!pos(fl)[0]) return bad('rl expand: want EXPORT.jsonl', 2); return res([L('err', 'expanded 2 episodes')], 0, 40); };
OUT['rl verify'] = function (fl) { if (!pos(fl).length) return bad('rl verify: want RUN_DIR...', 2); return res([L('ok', 'verified 48 rollouts: 0 mismatches, 0 unreadable')], 0, 910, card('rl verify', [['rollouts', '48'], ['mismatches', '0'], ['meaning', 'every recorded prompt re-expands to its wire hash']])); };
OUT['rl show'] = function (fl) {
  var p = pos(fl); if (!p.length) return bad('rl show: want RUN_DIR [TASK/SAMPLE]', 2);
  var run = p[0].replace(/^.*\//, ''), json = !!flag(fl, 'json');
  if (!rlRun(run)) return bad('rl show: ' + p[0] + ': no such run directory (the sample set has r001, r002)');
  if (p[1]) {
    if (json) return res(lines('out', JSON.stringify(S.rl.episode, null, 2)), 0, 30);
    var b = rlBlock('rl show runs/r002 go-lru/0'); return res(lines('out', b.text), 0, 30, card('rl show', [['episode', 'go-lru/0'], ['verifier', 'pass'], ['reward', '1.082'], ['cost', '$0.0097']]));
  }
  if (json) return res(lines('out', JSON.stringify(rlRun(run).summary, null, 2)), 0, 30);
  return res(lines('out', S.real.rl['rolloutV' + (run === 'r001' ? 1 : 2)]), 0, 30);
};
