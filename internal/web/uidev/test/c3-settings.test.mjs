// c3-settings.test.mjs: the eleven Settings pages (98-ui-settings.js): the filters, the Run settings line, the rules in force, the ledger and
// the key tags, and the markup of every page with hostile text in every field the server fills.
import test from 'node:test';
import assert from 'node:assert/strict';
import { page, EVIL, hasMarkup, scriptAttrs } from './c3-harness.mjs';

const MODULES = ['91-views-b.js', '98-ui-settings.js'];
const plain = x => JSON.parse(JSON.stringify(x));
const setup = () => page({ modules: MODULES });
const session = o => Object.assign({ id: 'shop', name: 'shop', roster: [{ id: 'mgr', role: 'manager', model: 'a/m' }, { id: 'be-1', role: 'backend', model: 'a/w' }], wm: {}, m: {}, meta: { model: 'a/m', mode: 'default', effort: 'default', budget: 0, roleModels: {}, rules: [], swarm: 1, isolation: 'worktree', verify: '', cwd: '/p', trustProject: true } }, o || {});
const MODELS = [
  { ref: 'a/cheap', ctx: 8000, in: 0.1, out: 0.4, tools: true, reasoning: false },
  { ref: 'a/big', ctx: 400000, in: 5, out: 25, tools: true, reasoning: true },
  { ref: 'b/unknown', ctx: 64000, in: null, out: null, tools: false, reasoning: false },
  { ref: 'b/fav', ctx: 128000, in: 1, out: 8, tools: true, reasoning: true },
];
const st = o => Object.assign({ q: '', tools: false, reasoning: false, favs: false, maxp: '', maxo: '', minc: '' }, o || {});

test('the model filters: words, tools, reasoning, favourites, input and output price, context; favourites first', () => {
  const { SL } = setup(), S = SL.c3.settings, favs = new Set(['b/fav']);
  const refs = f => S.modelRows(MODELS, st(f), favs).map(m => m.ref);
  assert.deepEqual(plain(refs()), ['b/fav', 'a/cheap', 'a/big', 'b/unknown']);
  assert.deepEqual(plain(refs({ q: 'A/ BIG' })), ['a/big'], 'every word, any case');
  assert.deepEqual(plain(refs({ tools: true })), ['b/fav', 'a/cheap', 'a/big']);
  assert.deepEqual(plain(refs({ reasoning: true })), ['b/fav', 'a/big']);
  assert.deepEqual(plain(refs({ favs: true })), ['b/fav']);
  assert.deepEqual(plain(refs({ maxp: '1' })), ['b/fav', 'a/cheap'], 'a model with no price is not under any price');
  assert.deepEqual(plain(refs({ maxo: '10' })), ['b/fav', 'a/cheap']);
  assert.deepEqual(plain(refs({ minc: '128000' })), ['b/fav', 'a/big']);
});

test('the "Run as CLI" line of Models names the output price flag, not the input price', () => {
  const { SL } = setup(), c = SL.c3.settings.modelsCli(st({ q: 'claude', tools: true, favs: true, all: true, maxo: '5', maxp: '3', minc: '128000' }));
  assert.equal(c.label, 'models claude --tools --fav --all --max-price 5 --min-context 128000');
  assert.deepEqual(plain(c.flags), { tools: true, fav: true, all: true, 'max-price': 5, 'min-context': '128000' });
  assert.deepEqual(plain(c.pos), { WORDS: 'claude' });
  assert.equal(SL.c3.settings.modelsCli(st()).label, 'models');
});

test('the rules in force: the server\'s then the session\'s, a rule both carry once, the session one removable', () => {
  const { SL } = setup(), S = session({ meta: Object.assign(session().meta, { rules: [
    { effect: 'allow', rule: 'Bash(go test:*)', origin: '--allow flag' },
    { effect: 'deny', rule: 'Read(./.env)', origin: 'project config', file: '.sleipnir/config.json' },
    { effect: 'allow', rule: 'Bash(make:*)', origin: 'this session' }] }) });
  SL.D.extra.permissions = { rules: { allow: [{ rule: 'Bash(go test:*)', origin: '--allow flag' }], deny: [{ rule: 'Read(./.env)', origin: 'project config', file: '.sleipnir/config.json' }, { rule: 'Read(~/.ssh/**)', origin: 'built-in protection', fixed: true }], ask: [] } };
  const out = SL.c3.settings.rulesIn(S).map(r => r.effect + ' ' + r.rule + ' ' + (r.fixed ? 'locked' : 'removable'));
  assert.deepEqual(plain(out), ['deny Read(./.env) locked', 'deny Read(~/.ssh/**) locked', 'allow Bash(go test:*) removable', 'allow Bash(make:*) removable']);
});

test('how a provider\'s key is told, and what button its row gets', () => {
  const { SL } = setup(), k = SL.c3.settings.keyInfo;
  assert.deepEqual(plain(k({ key: 'none', state: 'no key' })), { txt: 'no key', cls: '', btn: 'in' });
  assert.deepEqual(plain(k({ key: 'none', state: 'no key needed' })), { txt: 'no key needed', cls: '', btn: '' });
  assert.deepEqual(plain(k({ key: 'env', env: 'X_KEY' })), { txt: '✓ env X_KEY', cls: 'ok', btn: 'out', env: true });
  assert.equal(k({ key: 'stored' }).txt, '✓ stored'); assert.equal(k({ key: 'signed in' }).txt, '✓ signed in');
});

test('the Run settings: the command line says what a restart would start, the stepper is clamped to the ceiling', () => {
  const { SL } = setup(), S = SL.c3.settings, M = session().meta;
  M.swarm = 4; M.verify = 'go test {dirs}'; M.budget = 5; M.commit = true; M.trustProject = false;
  assert.equal(S.runLine(M, {}), 'sleipnir chat --model a/m --mode default --swarm 4 --isolation worktree --verify "go test {dirs}" --budget-usd 5 --commit');
  assert.ok(S.runLine(Object.assign({}, M, { commit: false, mailman: false }), { mailman: true, commit: false }).endsWith('--mailman=false'), 'off against a default of on');
  assert.deepEqual([S.clampWorkers('99', 10), S.clampWorkers('-3', 10), S.clampWorkers('x', 10), S.clampWorkers('7', 10)], [10, 0, 0, 7]);
  assert.equal(S.teamWord(0), 'single agent'); assert.equal(S.teamWord(1), 'manager + 1 worker'); assert.equal(S.teamWord(10), 'manager + 10 workers (2 share legs)');
});

test('the trust ledger gets the project of the tab when nobody said yes to it; a warning splits at its position', () => {
  const { SL } = setup(), S = SL.c3.settings;
  const T = { project: { dir: '/p', state: 'not trusted' }, files: [{ path: 'a' }, { path: 'b' }] };
  assert.deepEqual(plain(S.ledgerRows(T, [{ dir: '/q', files: 1, state: 'trusted (Oct 8)' }])).map(r => r.dir + ':' + r.state), ['/q:trusted (Oct 8)', '/p:not trusted']);
  assert.equal(S.ledgerRows({ project: { dir: '/q', state: 'trusted' }, files: [] }, [{ dir: '/q', state: 'trusted (x)' }]).length, 1);
  assert.deepEqual(plain(S.warningRow('.sleipnir/config.json:12: unknown key')), { where: '.sleipnir/config.json:12', text: 'unknown key' });
  assert.deepEqual(plain(S.warningRow('no position here')), { where: '', text: 'no position here' });
  assert.deepEqual(plain(S.promptCmds({ prompts: ['sum', 'mcp__fs__explain', { command: '/mcp__fs__x', description: 'does x' }, { name: 'y' }] }, 'fs')),
    [{ cmd: 'mcp__fs__sum', desc: '' }, { cmd: 'mcp__fs__explain', desc: '' }, { cmd: 'mcp__fs__x', desc: 'does x' }, { cmd: 'mcp__fs__y', desc: '' }]);
  assert.deepEqual(plain(S.configWarnings({ issues: [{ file: '.sleipnir/config.json', line: 12, col: 3, path: 'swarm.max_worker', message: 'unknown key', severity: 'warning' }, { message: 'no position' }] })),
    [{ where: '.sleipnir/config.json:12:3', text: 'swarm.max_worker: unknown key', sev: 'warning' }, { where: '', text: 'no position', sev: '' }]);
  assert.deepEqual(plain(S.configWarnings({ warnings: ['a.json:3: bad'] })), [{ where: 'a.json:3', text: 'bad' }], 'a server that sends only lines');
  assert.equal(S.dayTxt('2026-10-08'), 'Oct 8');
});

test('every page shows hostile text of the server as text', () => {
  const { SL } = setup(), X = SL.D.extra, G = SL.G, D = SL.D, S = session({ name: EVIL });
  S.meta.model = EVIL; S.meta.verify = EVIL; S.meta.rules = [{ effect: 'allow', rule: EVIL, origin: EVIL, file: EVIL, note: EVIL }]; S.meta.roleModels = { backend: EVIL }; S.roster[1].model = EVIL; S.roster[0].id = EVIL; S.meta.cwd = EVIL;
  D.modelsNote = EVIL; D.models = [{ ref: EVIL, ctx: 1000, in: 1, out: 2, tools: true, reasoning: true }]; D.modelErrors = [EVIL]; D.roleOrder = ['backend', EVIL]; D.roles[EVIL] = { code: 'x' + EVIL }; D.efforts = ['default', EVIL];
  X.permissions = { modes: [{ id: EVIL, danger: false, text: EVIL }, { id: 'yolo' + EVIL, danger: true, text: EVIL }], order: [EVIL], managerWrites: { text: EVIL, note: EVIL }, testsPreset: { summary: EVIL, rules: [EVIL] }, rules: { allow: [{ rule: EVIL, origin: EVIL, file: EVIL, note: EVIL }], deny: [], ask: [] } };
  X.trust = { project: { dir: EVIL, state: 'changed: ' + EVIL, savedDay: EVIL, digest: EVIL, unlocks: EVIL }, files: [{ path: EVIL, kind: EVIL, bytes: EVIL, hash: EVIL }], covers: EVIL, ledger: [], question: { options: [EVIL] } };
  G.trustDirs = [{ dir: EVIL, files: EVIL, state: 'changed since your yes: ' + EVIL }];
  G.mcp = [{ name: EVIL, origin: EVIL, transport: EVIL, state: 'running', tools: [EVIL] }, { name: 'f', origin: 'o', transport: 't', state: 'failed: ' + EVIL, tools: [] }];
  X.mcp = { servers: [{ name: EVIL, command: EVIL, args: [EVIL], server: EVIL, describe: EVIL, error: EVIL, note: EVIL, prompts: [{ command: EVIL, description: EVIL }], envRefs: [EVIL] }], sessionNote: EVIL };
  G.mcpOut = { [EVIL]: { t: 'approved ' + EVIL, cls: EVIL } };
  X.skills = [{ name: EVIL, summary: EVIL, source: EVIL, scope: EVIL, youOnly: true, note: EVIL, argumentHint: EVIL }]; X.skillsBudget = { used: EVIL, tokens: EVIL, note: EVIL };
  X.commands = [{ name: EVIL, description: EVIL, argumentHint: EVIL, source: EVIL }]; X.hooks = { events: [EVIL], configured: [{ event: EVIL, matcher: EVIL, command: EVIL, timeout: EVIL, origin: EVIL, purpose: EVIL }] };
  G.providers = [{ id: EVIL, name: EVIL, base: EVIL, key: 'env', env: EVIL, state: EVIL, keyWhere: EVIL, dialect: EVIL, usedBy: EVIL, who: EVIL, recommended: true }, { id: EVIL + '2', name: 'n', base: 'b', key: 'none', state: 'no key' }]; X.providerNote = EVIL;
  X.config = { layers: [{ kind: EVIL, source: EVIL, state: EVIL, trusted: EVIL }], precedence: EVIL, effective: [{ key: EVIL, value: EVIL, layer: EVIL, file: EVIL, note: EVIL, below: { [EVIL]: EVIL } }], issues: [{ file: EVIL, line: 3, path: EVIL, message: EVIL, severity: EVIL }], risks: [{ file: EVIL, message: EVIL }], valid: EVIL, ok: false };
  SL.live.hello = { server: { addr: EVIL }, defaults: { maxWorkers: 10, swarm: EVIL }, ui: {} }; SL.live.addr = EVIL;
  SL.settings.title = EVIL;
  const ST = SL.c3.settings.newState({ page: 'models' }); ST.models.q = EVIL; ST.cfg.q = EVIL; ST.perm.arg = EVIL; ST.perm.res = { d: EVIL, why: EVIL, cls: EVIL }; ST.perm.danger = EVIL;
  const seen = [];
  Object.keys(SL.c3.settings.pages).forEach(name => {
    const html = SL.c3.settings.pages[name](S, ST);
    assert.ok(!hasMarkup(html), 'the ' + name + ' page holds an element made of hostile text');
    assert.deepEqual(plain(scriptAttrs(html)), [], 'the ' + name + ' page has an attribute that runs script');
    if (html.includes('&lt;img')) seen.push(name);
  });
  ['models', 'roles', 'permissions', 'trust', 'run', 'mcp', 'skills', 'providers', 'config'].forEach(n => assert.ok(seen.includes(n), 'the hostile text reaches the ' + n + ' page, as text'));
});

test('the Permissions tester keeps its previous result while a check is on its way, and shows what the server said as text', () => {
  const { SL } = setup(), S = session(), ST = SL.c3.settings.newState({ page: 'permissions' });
  SL.D.extra.permissions = { modes: [], order: ['deny'], managerWrites: { text: 't', note: 'n' }, testsPreset: { summary: '', rules: [] }, rules: { allow: [], deny: [], ask: [] } };
  ST.perm.res = { d: 'ask', why: 'no rule matches', cls: 'warm' }; ST.perm.busy = true;
  const html = SL.c3.settings.pages.permissions(S, ST);
  assert.ok(html.includes('no rule matches') && html.includes('data-do="try" disabled'));
});

test('trusting: the confirm lists what the server scanned, the yes carries the id it issued, and nothing else', async () => {
  const t = page({ modules: MODULES, routes: call => call.path === '/api/trust' ? { status: 200, body: { ok: true } } : { status: 404, body: { error: 'x', code: 'not_found' } } });
  const S = t.SL.c3.settings, ch = { files: [{ path: 'AGENTS.md', kind: 'instructions', hash: 'a1b2c3d4e5f6a7b8c9d0' }, { path: EVIL, kind: EVIL, hash: EVIL }], changed: EVIL, partial: true, confirm: 'cf_issued_with_the_challenge' };
  const sp = S.trustSpec('/p/' + EVIL, ch);
  assert.equal(sp.title, 'Trust these files'); assert.equal(sp.ok, 'Trust these files'); assert.match(sp.text, /<b>2<\/b> files/); assert.match(sp.detail, /AGENTS\.md  instructions  a1b2c3d4e5f6a7b8/);
  assert.ok(!hasMarkup(sp.text) && !hasMarkup(sp.detail) && !hasMarkup(sp.title));
  const r = await S.trustPost('/p', ch); assert.equal(r.ok, true);
  assert.deepEqual(plain(t.fetch.calls.map(c => c.method + ' ' + c.path)), ['POST /api/trust'], 'no second confirmation is asked for');
  assert.equal(t.fetch.calls[0].headers['X-Confirm'], 'cf_issued_with_the_challenge'); assert.deepEqual(plain(t.fetch.calls[0].body), { dir: '/p', on: true });
  const f = S.forgetSpec([{ dir: '/a' }, { dir: EVIL }]); assert.match(f.text, /<b>2<\/b> directories/); assert.ok(!hasMarkup(f.detail)); assert.equal(f.danger, true);
});
