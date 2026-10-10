// c3-runner.test.mjs: the command runner (92-runner.js): the form's command line and request, validation, the state machine of a run (a
// started run whose frames came first, a privileged command and its confirmation, refusals, cancel, leaving the view), the confirmation
// protocol through the real api.js, and hostile text in the spec and in the output.
import test from 'node:test';
import assert from 'node:assert/strict';
import { page, EVIL, hasMarkup, tick } from './c3-harness.mjs';

const MODULES = ['91-views-b.js', '92-runner.js'];
const SPEC = { commands: [
  { path: ['sessions', 'prune'], usage: 'sleipnir sessions prune [flags]', summary: 'delete old sessions', positional: [], mode: 'run', when: [{ flag: 'yes', mode: 'priv' }],
    flags: [{ name: 'older-than', arg: 'string', default: '30d', desc: 'age' }, { name: 'keep', arg: 'int', default: 20, desc: 'newest to keep' }, { name: 'yes', arg: 'bool', default: null, desc: 'delete' }] },
  { path: ['models'], usage: 'sleipnir models [words...]', summary: 'list models', mode: 'net', positional: [{ name: 'WORDS', required: false, variadic: true, desc: 'narrow it' }],
    flags: [{ name: 'tools', arg: 'bool', default: null, desc: 'tools' }, { name: 'max-price', arg: 'float', default: null, desc: 'output price' }, { name: 'role', arg: 'string', repeatable: true, desc: 'roles' }, { name: 'view', arg: 'string', choices: ['a', 'b'], desc: 'view' }] },
  { path: ['schedule', 'add'], usage: 'u', summary: 's', mode: 'priv', positional: [{ name: 'GOAL', required: true, variadic: true }], flags: [{ name: 'cron', arg: 'string', required: true, desc: 'when' }, { name: 'budget-usd', arg: 'float', default: 1, desc: 'b' }, { name: 'n', arg: 'uint', default: null, desc: 'n' }] },
  { path: ['rl', 'reward'], usage: 'u', summary: 's', mode: 'net', positional: [], flags: [{ name: 'probes', arg: 'bool', default: true, desc: 'probes' }] },
  { path: ['chat'], usage: 'u', summary: 's', mode: 'tty_only', why: '+ New session starts the same session here', positional: [], flags: [] },
] };
const cmd = (SL, p) => SPEC.commands.find(c => c.path.join(' ') === p);
const setup = routes => { const t = page({ routes, modules: MODULES }); t.SL.D.spec = SPEC; return t; };

test('the command line shows what is set, and a default that was typed is left out', () => {
  const { SL } = setup();
  const R = SL.runner;
  assert.equal(R.cmdline(cmd(SL, 'sessions prune'), { flags: { 'older-than': '7d', keep: '20', yes: true } }), 'sleipnir sessions prune --older-than 7d --yes');
  assert.equal(R.cmdline(cmd(SL, 'models'), { pos: { WORDS: 'claude opus' }, flags: { tools: true, role: 'a, b c', 'max-price': '5' } }), 'sleipnir models "claude opus" --tools --max-price 5 --role a --role "b c"');
  assert.equal(R.cmdline(cmd(SL, 'rl reward'), { flags: {} }), 'sleipnir rl reward', 'a flag that is on by default shows nothing while it is on');
  assert.equal(R.cmdline(cmd(SL, 'rl reward'), { flags: { probes: false } }), 'sleipnir rl reward --probes=false', 'and --flag=false when it is turned off');
});

test('the request is what the command line says, typed', () => {
  const { SL } = setup(), R = SL.runner;
  assert.deepEqual(JSON.parse(JSON.stringify(R.buildRequest(cmd(SL, 'sessions prune'), { flags: { 'older-than': '7d', keep: '5', yes: true } }, 'shop'))),
    { path: ['sessions', 'prune'], flags: { 'older-than': '7d', keep: 5, yes: true }, tab: 'shop' });
  assert.deepEqual(JSON.parse(JSON.stringify(R.buildRequest(cmd(SL, 'models'), { pos: { WORDS: 'a b' }, flags: { 'max-price': '2.5', role: 'x, y', view: 'b' } }, '', true))),
    { path: ['models'], pos: { WORDS: 'a b' }, flags: { 'max-price': 2.5, role: ['x', 'y'], view: 'b' }, keep: true });
  assert.deepEqual(JSON.parse(JSON.stringify(R.buildRequest(cmd(SL, 'rl reward'), { flags: { probes: false } }))), { path: ['rl', 'reward'], flags: { probes: false } });
  assert.deepEqual(JSON.parse(JSON.stringify(R.buildRequest(cmd(SL, 'rl reward'), { flags: { probes: true } }))), { path: ['rl', 'reward'] });
});

test('validation names what is missing or wrong before anything is sent', () => {
  const { SL } = setup(), R = SL.runner, c = cmd(SL, 'schedule add');
  assert.deepEqual(JSON.parse(JSON.stringify(R.validate(c, { pos: {}, flags: {} }).map(b => b.text))), ['GOAL is required', '--cron is required']);
  assert.deepEqual(JSON.parse(JSON.stringify(R.validate(c, { pos: { GOAL: 'x' }, flags: { cron: '* * * * *', n: '-1' } }).map(b => b.field + ':' + b.text))), ['rf-n:--n cannot be negative']);
  assert.deepEqual(JSON.parse(JSON.stringify(R.validate(c, { pos: { GOAL: 'x' }, flags: { cron: '* * * * *', 'budget-usd': 'abc', n: '2.5' } }).map(b => b.text))), ['--budget-usd needs a number', '--n needs a whole number']);
  assert.equal(R.validate(c, { pos: { GOAL: 'x' }, flags: { cron: '* * * * *' } }).length, 0);
});

test('the mode a flag changes a command to', () => {
  const { SL } = setup(), R = SL.runner;
  assert.equal(R.modeOf(cmd(SL, 'sessions prune'), { flags: {} }), 'run');
  assert.equal(R.modeOf(cmd(SL, 'sessions prune'), { flags: { yes: true } }), 'priv');
  assert.equal(R.modeOf(cmd(SL, 'chat'), { flags: {} }), 'tty_only');
});

test('hostile text of the spec and of the filter is escaped in the form and in the list', () => {
  const { SL } = setup(), R = SL.runner;
  const c = { path: ['x' + EVIL], usage: EVIL, summary: EVIL, mode: 'tty_only', why: EVIL, positional: [{ name: 'P' + EVIL, required: true, desc: EVIL }], flags: [{ name: 'f' + EVIL, arg: 'string', default: EVIL, desc: EVIL, choices: [EVIL] }, { name: 'g', arg: 'bool', desc: EVIL, defaultNote: EVIL }] };
  const html = R.formHtml(c, { pos: { ['P' + EVIL]: EVIL }, flags: { ['f' + EVIL]: EVIL } });
  assert.ok(!hasMarkup(html), 'the form holds no element made of the spec');
  assert.ok(html.includes('&lt;img'), 'the text is there, escaped');
  SL.D.spec = { commands: [c] };
  assert.ok(!hasMarkup(R.listHtml({ q: '', c })));
});

/** deps for a run: a post that answers by a script, a cancel that records */
function deps(answers) {
  const posts = [], cancels = [], lines = [], results = [];
  return { posts, cancels, lines, results,
    post: async (req, opts) => { posts.push([req, opts]); return answers.shift(); }, cancel: async id => { cancels.push(id); return { ok: true }; },
    onLine: l => lines.push(l), onResult: r => results.push(r) };
}
const started = (id, cmdline) => ({ ok: true, status: 202, data: { id, cmdline } });

test('a run whose frames arrive before the answer keeps every line, in order, and only its own', async () => {
  const { SL } = setup(), d = deps([started('r_a', 'sleipnir models')]);
  const run = SL.runner.createRun(d);
  const p = run.start({ path: ['models'] });
  run.frame({ id: 'r_a', lines: [{ k: 'out', t: 'one' }] });
  run.frame({ id: 'r_other', lines: [{ k: 'out', t: 'not mine' }] });
  run.frame({ id: 'r_a', lines: [{ k: 'err', t: 'two' }] });
  const r = await p;
  assert.deepEqual(JSON.parse(JSON.stringify(r)), { state: 'started', id: 'r_a', cmdline: 'sleipnir models' });
  run.frame({ id: 'r_a', lines: [{ k: 'out', t: 'three' }] });
  run.frame({ id: 'r_other', lines: [{ k: 'out', t: 'again not mine' }] });
  assert.deepEqual(d.lines.map(l => l.t), ['one', 'two', 'three']);
  assert.equal(run.phase, 'running');
  run.frame({ id: 'r_a', result: { exit: 0, ms: 12 } });
  run.frame({ id: 'r_a', lines: [{ k: 'out', t: 'after the end' }] });
  assert.equal(run.phase, 'done');
  assert.deepEqual(d.results.map(x => x.exit), [0]);
  assert.deepEqual(d.lines.map(l => l.t), ['one', 'two', 'three'], 'nothing is added after the result');
});

test('a second run cannot start while one is going', async () => {
  const { SL } = setup(), d = deps([started('r_a', 'x')]), run = SL.runner.createRun(d);
  await run.start({ path: ['models'] });
  assert.deepEqual(JSON.parse(JSON.stringify(await run.start({ path: ['models'] }))), { state: 'busy' });
  assert.equal(d.posts.length, 1);
});

test('a privileged command: the first request asks for the scope, the second carries it', async () => {
  const { SL } = setup(), d = deps([
    { ok: false, status: 428, code: 'confirm_required', message: 'confirm', scope: 'run:abc123', detail: { scope: 'run:abc123', cmdline: 'sleipnir sessions prune --yes' } },
    started('r_p', 'sleipnir sessions prune --yes')]);
  const run = SL.runner.createRun(d), req = { path: ['sessions', 'prune'], flags: { yes: true } };
  const r1 = await run.start(req);
  assert.deepEqual(JSON.parse(JSON.stringify(r1)), { state: 'confirm', scope: 'run:abc123', detail: { scope: 'run:abc123', cmdline: 'sleipnir sessions prune --yes' } });
  assert.equal(run.phase, 'confirming');
  assert.equal(d.posts[0][1], undefined, 'the probe carries no confirmation');
  const r2 = await run.confirmStart('run:abc123');
  assert.equal(r2.state, 'started');
  assert.deepEqual(d.posts[1][0], req);
  assert.deepEqual(JSON.parse(JSON.stringify(d.posts[1][1])), { confirm: 'run:abc123' }, 'the scope the server named, and no other');
});

test('a declined confirmation starts nothing and the form is free again', async () => {
  const { SL } = setup(), d = deps([{ ok: false, status: 428, code: 'confirm_required', message: 'c', scope: 'run:z', detail: {} }]), run = SL.runner.createRun(d);
  await run.start({ path: ['init'] }); run.declined();
  assert.equal(run.phase, 'idle'); assert.equal(d.posts.length, 1);
  assert.deepEqual(JSON.parse(JSON.stringify(await run.confirmStart('run:z'))), { state: 'busy' }, 'a confirmation after the decline starts nothing');
});

test('refusals are said as they are: a terminal-only command, bad flags, four runs already', async () => {
  const { SL } = setup();
  for (const [status, code, message] of [[403, 'tty_only', '+ New session starts the same session here'], [400, 'bad_flags', 'sleipnir models has no flag --x'], [409, 'busy', 'four runs are already going']]) {
    const d = deps([{ ok: false, status, code, message }]), run = SL.runner.createRun(d);
    const r = await run.start({ path: ['chat'] });
    assert.deepEqual(JSON.parse(JSON.stringify(r)), { state: 'refused', code, status, message });
    assert.equal(run.phase, 'idle');
  }
  const d = deps([{ ok: false, status: 0, code: 'network', message: 'the server is not reachable' }]), run = SL.runner.createRun(d);
  assert.equal((await run.start({ path: ['chat'] })).state, 'error');
});

test('cancel asks the server to stop it, and the result frame closes the run; a run that already ended closes at once', async () => {
  const { SL } = setup(), d = deps([started('r_c', 'x')]), run = SL.runner.createRun(d);
  await run.start({ path: ['models'] });
  assert.equal(await run.cancel(), true); assert.equal(run.phase, 'stopping'); assert.deepEqual(d.cancels, ['r_c']);
  run.frame({ id: 'r_c', result: { exit: 130, ms: 5, canceled: true } });
  assert.equal(run.phase, 'done'); assert.equal(d.results[0].canceled, true);
  const e = deps([started('r_e', 'x')]); e.cancel = async () => ({ ok: false, status: 404 });
  const run2 = SL.runner.createRun(e); await run2.start({ path: ['models'] }); await run2.cancel();
  assert.equal(run2.phase, 'done'); assert.equal(e.results[0].canceled, true);
});

test('leaving the view stops a run, unless it was kept; a request still on its way is stopped when it lands (D-05)', async () => {
  const { SL } = setup();
  let d = deps([started('r_l', 'x')]), run = SL.runner.createRun(d);
  await run.start({ path: ['models'] }); run.leave(); assert.deepEqual(d.cancels, ['r_l']);
  d = deps([started('r_k', 'x')]); run = SL.runner.createRun(d);
  await run.start({ path: ['rl', 'eval'], keep: true }); run.leave(); assert.deepEqual(d.cancels, [], 'a kept run goes on');
  let release; const slow = new Promise(r => { release = r; });
  d = deps([]); d.post = async () => { await slow; return started('r_s', 'x'); }; run = SL.runner.createRun(d);
  const p = run.start({ path: ['models'] }); run.leave(); release(); const r = await p;
  assert.equal(r.state, 'stale'); assert.deepEqual(d.cancels, ['r_s'], 'it started after the view was gone: it is stopped');
  d = deps([]); let rel2; const s2 = new Promise(r => { rel2 = r; }); d.post = async () => { await s2; return started('r_t', 'x'); }; run = SL.runner.createRun(d);
  const p2 = run.start({ path: ['models'] }); run.reset(); rel2(); await p2;
  assert.deepEqual(d.cancels, ['r_t'], 'Reset while it starts stops it too');
});

test('the confirmation protocol through api.js: the probe is silent, the repeat carries the id issued for the scope', async () => {
  let n = 0;
  const t = setup(call => {
    if (call.path === '/api/confirm') return { status: 200, body: { id: 'cf_1', scope: call.body.scope, expires_in: 60 } };
    if (call.path === '/api/runs' && !call.headers['X-Confirm']) return { status: 428, headers: { 'X-Confirm-Scope': 'run:feedbeef' }, body: { error: 'this action needs a confirmation', code: 'confirm_required', detail: { scope: 'run:feedbeef', argv: ['trust', 'add'], cmdline: 'sleipnir trust add --yes' } } };
    if (call.path === '/api/runs') { n++; return { status: 202, body: { id: 'r_ok', cmdline: 'sleipnir trust add --yes' } }; }
    return { status: 404, body: { error: 'x', code: 'not_found' } };
  });
  const { SL, fetch, toasts } = t;
  const run = SL.runner.createRun({ post: (req, opts) => SL.c3.net.post('/api/runs', req, opts), cancel: id => SL.c3.cancelRun(id), onLine() {}, onResult() {} });
  const r1 = await run.start({ path: ['trust', 'add'] });
  assert.equal(r1.state, 'confirm'); assert.equal(r1.scope, 'run:feedbeef');
  assert.deepEqual(toasts, [], 'the 428 of the probe is the protocol, not a page bug: nothing is toasted');
  assert.notEqual(SL.api.cfg.toast, undefined, 'the toast hook is put back');
  const r2 = await run.confirmStart(r1.scope);
  assert.equal(r2.state, 'started'); assert.equal(n, 1);
  const paths = fetch.calls.map(c => c.method + ' ' + c.path + (c.headers['X-Confirm'] ? ' [' + c.headers['X-Confirm'] + ']' : ''));
  assert.deepEqual(paths, ['POST /api/runs', 'POST /api/confirm', 'POST /api/runs [cf_1]']);
  assert.deepEqual(fetch.calls[1].body, { scope: 'run:feedbeef' });
  t.SL.api.cfg.toast('after', 'err'); assert.equal(toasts.length, 1, 'a later 428 would be toasted again');
});

test('the tracker keeps the frames of every run until its own id is known, and drops the other runs', () => {
  const { SL } = setup(), got = [];
  const tk = SL.c3.tracker({ line: l => got.push(l.t), result: r => got.push('end ' + r.exit) });
  tk.frame({ id: 'r_x', lines: [{ k: 'out', t: 'ignored: not armed' }] });
  tk.arm();
  tk.frame({ id: 'r_1', lines: [{ k: 'out', t: 'a' }] }); tk.frame({ id: 'r_2', lines: [{ k: 'out', t: 'b' }] }); tk.frame({ id: 'r_1', result: { exit: 3 } });
  tk.begin('r_1');
  tk.frame({ id: 'r_1', lines: [{ k: 'out', t: 'late' }] });
  assert.deepEqual(got, ['a', 'end 3']);
  const g2 = []; const t2 = SL.c3.tracker({ line: l => g2.push(l.t), result: r => g2.push('end') });
  t2.arm(); t2.frame({ id: 'r_9', lines: [{ k: 'out', t: 'x' }] }); t2.frame({ id: 'r_9', result: { exit: 0 } }); t2.begin('r_9', { onlyResult: true });
  assert.deepEqual(g2, ['end'], 'a run whose output was fetched separately only takes its end from the buffer');
});
