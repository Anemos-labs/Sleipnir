// settings.test.mjs: the eight Settings pages (98-ui-settings.js): the pages of an earlier version and where they went, the cards each page
// draws, the stored layout of a merged page carried over, the switches that are plugins, the empty states, the filters, the Team & run
// line, the rules in force, the ledger and the key tags, and the markup of every page with hostile text in every field the server fills.
import test from 'node:test';
import assert from 'node:assert/strict';
import { page, EVIL, hasMarkup, scriptAttrs } from './pages-harness.mjs';

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
  const { SL } = setup(), S = SL.toolkit.settings, favs = new Set(['b/fav']);
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
  const { SL } = setup(), c = SL.toolkit.settings.modelsCli(st({ q: 'claude', tools: true, favs: true, all: true, maxo: '5', maxp: '3', minc: '128000' }));
  assert.equal(c.label, 'models claude --tools --fav --all --max-price 5 --min-context 128000');
  assert.deepEqual(plain(c.flags), { tools: true, fav: true, all: true, 'max-price': 5, 'min-context': '128000' });
  assert.deepEqual(plain(c.pos), { WORDS: 'claude' });
  assert.equal(SL.toolkit.settings.modelsCli(st()).label, 'models');
});

test('the rules in force: the server\'s then the session\'s, a rule both carry once, the session one removable', () => {
  const { SL } = setup(), S = session({ meta: Object.assign(session().meta, { rules: [
    { effect: 'allow', rule: 'Bash(go test:*)', origin: '--allow flag' },
    { effect: 'deny', rule: 'Read(./.env)', origin: 'project config', file: '.sleipnir/config.json' },
    { effect: 'allow', rule: 'Bash(make:*)', origin: 'this session' }] }) });
  SL.D.extra.permissions = { rules: { allow: [{ rule: 'Bash(go test:*)', origin: '--allow flag' }], deny: [{ rule: 'Read(./.env)', origin: 'project config', file: '.sleipnir/config.json' }, { rule: 'Read(~/.ssh/**)', origin: 'built-in protection', fixed: true }], ask: [] } };
  const out = SL.toolkit.settings.rulesIn(S).map(r => r.effect + ' ' + r.rule + ' ' + (r.fixed ? 'locked' : 'removable'));
  assert.deepEqual(plain(out), ['deny Read(./.env) locked', 'deny Read(~/.ssh/**) locked', 'allow Bash(go test:*) removable', 'allow Bash(make:*) removable']);
});

test('how a provider\'s key is told, and what button its row gets', () => {
  const { SL } = setup(), k = SL.toolkit.settings.keyInfo;
  assert.deepEqual(plain(k({ key: 'none', state: 'no key' })), { txt: 'no key', cls: '', btn: 'in' });
  assert.deepEqual(plain(k({ key: 'none', state: 'no key needed' })), { txt: 'no key needed', cls: '', btn: '' });
  assert.deepEqual(plain(k({ key: 'env', env: 'X_KEY' })), { txt: '✓ env X_KEY', cls: 'ok', btn: 'out', env: true });
  assert.equal(k({ key: 'stored' }).txt, '✓ stored'); assert.equal(k({ key: 'signed in' }).txt, '✓ signed in');
});

test('the Run settings: the command line says what a restart would start, the stepper is clamped to 0 or 8 and up, and to the ceiling', () => {
  const { SL } = setup(), S = SL.toolkit.settings, M = session().meta;
  M.swarm = 8; M.verify = 'go test {dirs}'; M.budget = 5; M.commit = true; M.trustProject = false;
  assert.equal(S.runLine(M, {}), 'sleipnir chat --model a/m --mode default --swarm 8 --isolation worktree --verify "go test {dirs}" --budget-usd 5 --commit');
  assert.ok(S.runLine(Object.assign({}, M, { commit: false, mailman: false }), { mailman: true, commit: false }).endsWith('--mailman=false'), 'off against a default of on');
  assert.deepEqual([S.clampWorkers('99', 10), S.clampWorkers('-3', 10), S.clampWorkers('x', 10), S.clampWorkers('9', 10), S.clampWorkers('99', 0)], [10, 0, 0, 9, 99]);
  assert.deepEqual([S.clampWorkers('1', 0, 1), S.clampWorkers('7', 0, -1), S.clampWorkers('5', 0)], [8, 0, 8], 'a team has at least 8 workers: counting up from 0 gives 8, down from 8 gives 0');
  assert.equal(S.teamWord(0), 'single agent'); assert.equal(S.teamWord(1), 'manager + 1 worker'); assert.equal(S.teamWord(10), 'manager + 10 workers'); assert.equal(S.teamWord(32), 'manager + 32 workers');
});

test('the trust ledger gets the project of the tab when nobody said yes to it; a warning splits at its position', () => {
  const { SL } = setup(), S = SL.toolkit.settings;
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
  const ST = SL.toolkit.settings.newState({ page: 'models' }); ST.models.q = EVIL; ST.cfg.q = EVIL; ST.perm.arg = EVIL; ST.perm.res = { d: EVIL, why: EVIL, cls: EVIL }; ST.perm.danger = EVIL;
  const seen = [], T = SL.toolkit.settings;
  for (const [what, fns] of [['page', T.pages], ['part', T.parts]]) Object.keys(fns).forEach(name => {
    const html = fns[name](S, ST);
    assert.ok(!hasMarkup(html), 'the ' + name + ' ' + what + ' holds an element made of hostile text');
    assert.deepEqual(plain(scriptAttrs(html)), [], 'the ' + name + ' ' + what + ' has an attribute that runs script');
    if (html.includes('&lt;img')) seen.push(what + ':' + name);
  });
  ['models', 'run', 'permissions', 'providers', 'skills', 'config'].forEach(n => assert.ok(seen.includes('page:' + n), 'the hostile text reaches the ' + n + ' page, as text'));
  ['roles', 'trust', 'mcp'].forEach(n => assert.ok(seen.includes('part:' + n), 'the hostile text reaches the ' + n + ' part, as text'));
});

test('the Permissions tester keeps its previous result while a check is on its way, and shows what the server said as text', () => {
  const { SL } = setup(), S = session(), ST = SL.toolkit.settings.newState({ page: 'permissions' });
  SL.D.extra.permissions = { modes: [], order: ['deny'], managerWrites: { text: 't', note: 'n' }, testsPreset: { summary: '', rules: [] }, rules: { allow: [], deny: [], ask: [] } };
  ST.perm.res = { d: 'ask', why: 'no rule matches', cls: 'warm' }; ST.perm.busy = true;
  const html = SL.toolkit.settings.pages.permissions(S, ST);
  assert.ok(html.includes('no rule matches') && html.includes('data-do="try" disabled'));
});

test('the projects nobody trusted, or that could not be read, are rows of the ledger with the server\'s word', () => {
  const { SL } = setup(), S = SL.toolkit.settings;
  const projects = [{ dir: '/a', trust: 'trusted', files: 3 }, { dir: '/b', trust: 'untrusted', files: 2 }, { dir: '/c', trust: 'partial', files: 9 }, { dir: '/d', trust: 'unreadable', files: 1 }, { dir: '/e', trust: 'changed', files: 4 }, { dir: '/f', trust: 'none' }, { dir: '/g', trust: 'partial' }, { dir: '/led', trust: 'partial' }];
  const rows = S.ledgerRows({ project: { dir: '/a', state: 'trusted' }, files: [] }, [{ dir: '/led', files: 2, state: 'trusted (Oct 8)' }], projects);
  assert.deepEqual(plain(rows.map(r => r.dir + ':' + r.state)), ['/led:trusted (Oct 8)', '/b:not trusted', '/c:partial', '/d:unreadable', '/e:changed since your yes', '/g:partial'], 'trusted and nothing-to-trust projects are not listed; a project in the ledger once');
  SL.D.extra.projects = projects; SL.D.extra.trust = { project: { dir: '/a', state: 'trusted' }, files: [], covers: '', ledger: [], question: { options: [] } }; SL.G.trustDirs = [];
  const page = S.parts.trust(session(), S.newState({ page: 'trust' }));
  const row = d => page.split('<tr><td class="bright mono">').find(p => p.startsWith(d + '</td>'));
  for (const [dir, word] of [['/c', 'partial'], ['/d', 'unreadable']]) {
    assert.match(row(dir), new RegExp('<span class="tag warm" title="[^"]*session only[^"]*">' + word + '</span>'), dir + ' shows the word in the tag style, with what it means');
    assert.match(row(dir), new RegExp('data-trust="' + dir + '" data-on="1">Trust these files'), dir + ' offers the trust step');
  }
  assert.match(row('/b'), /<span class="tag warm" title="">not trusted<\/span>|<span class="tag warm">not trusted<\/span>/);
});

test('trusting goes through the trust step: it gets the whole challenge, and only the id it resolves is sent', async () => {
  const challenge = { dir: '/p', files: [{ path: 'AGENTS.md', kind: 'instructions', bytes: 10, hash: 'ab' }, { path: '/p/' + EVIL, kind: 'unread' }], partial: true, confirm: 'cf_the_challenge', digest: 'd' };
  const seen = [];
  const mk = trustStep => { const t = page({ modules: MODULES, routes: call => call.path.startsWith('/api/trust/challenge') ? { status: 200, body: challenge } : call.path === '/api/trust' ? { status: 200, body: { ok: true } } : { status: 404, body: { error: 'x', code: 'not_found' } } }); if (trustStep) t.SL.ui.trustStep = trustStep; return t; };
  const posts = t => t.fetch.calls.filter(c => c.method === 'POST' && c.path === '/api/trust');
  // no step: nothing is trusted, the person is told
  let t = mk(null); assert.equal(await t.SL.toolkit.settings.trustFlow('/p'), false); assert.equal(posts(t).length, 0); assert.match(t.toasts[0][0], /nothing was trusted/);
  // declined
  t = mk(async () => null); assert.equal(await t.SL.toolkit.settings.trustFlow('/p'), false); assert.equal(posts(t).length, 0);
  // yes: the step got the challenge as the server sent it (the unread path included), and its id is what is sent
  t = mk(async (ch, o) => { seen.push([ch, o]); return ch.confirm; });
  assert.equal(await t.SL.toolkit.settings.trustFlow('/p'), true);
  assert.deepEqual(plain(seen[0][0]), challenge); assert.deepEqual(plain(seen[0][1]), { dir: '/p' });
  assert.equal(posts(t).length, 1); assert.equal(posts(t)[0].headers['X-Confirm'], 'cf_the_challenge'); assert.deepEqual(plain(posts(t)[0].body), { dir: '/p', on: true });
  assert.deepEqual(plain(t.fetch.calls.map(c => c.path)), ['/api/trust/challenge?dir=%2Fp', '/api/trust'], 'no confirmation of the page\'s own is asked for');
  // the id the step resolves is the one sent, even when it is not the challenge's
  t = mk(async () => 'cf_other'); await t.SL.toolkit.settings.trustFlow('/p'); assert.equal(posts(t)[0].headers['X-Confirm'], 'cf_other');
  // the server refuses the challenge: no step, no trust
  const bad = page({ modules: MODULES, routes: () => ({ status: 403, body: { error: 'start a session in one of the listed projects', code: 'not_a_project' } }) });
  let stepped = false; bad.SL.ui.trustStep = async () => { stepped = true; return 'x'; };
  assert.equal(await bad.SL.toolkit.settings.trustFlow('/nope'), false); assert.equal(stepped, false); assert.deepEqual(plain(bad.toasts), [['start a session in one of the listed projects', 'warm']]);
  const f = t.SL.toolkit.settings.forgetSpec([{ dir: '/a' }, { dir: EVIL }]); assert.match(f.text, /<b>2<\/b> directories/); assert.ok(!hasMarkup(f.detail)); assert.equal(f.danger, true);
});

test('a hostile project path and a hostile unread entry of the ledger are text on the Trust page', () => {
  const { SL } = setup(), S = SL.toolkit.settings;
  SL.D.extra.projects = [{ dir: '/p/' + EVIL, trust: 'partial', files: EVIL }, { dir: '/q/' + EVIL, trust: 'unreadable' }];
  SL.D.extra.trust = { project: { dir: '/p/' + EVIL, state: 'partial' }, files: [{ path: EVIL, kind: 'unread', bytes: EVIL, hash: EVIL }], covers: EVIL, ledger: [], question: { options: [EVIL] } };
  const html = S.parts.trust(session(), S.newState({ page: 'trust' }));
  assert.ok(!hasMarkup(html)); assert.deepEqual(plain(scriptAttrs(html)), []); assert.ok(html.includes('&lt;img'));
});

test('model changes wait to be applied together: only the ones that differ from what runs now, in the order of the roles', () => {
  const { SL } = setup(), S = session(), pend = SL.toolkit.settings.pendingModels;
  assert.deepEqual(plain(pend(S, { backend: 'a/big', manager: 'a/m' })), [['backend', 'a/big']], 'the manager already runs on a/m');
  assert.deepEqual(plain(pend(S, { backend: 'a/big', manager: 'a/cheap' })), [['manager', 'a/cheap'], ['backend', 'a/big']]);
  assert.deepEqual(plain(pend(S, { backend: 'a/w' })), [], 'the worker model a role runs on without an entry of its own');
  assert.deepEqual(plain(pend(S, null)), []);
});

test('eight pages with plain names; every id of the eleven pages before still opens the page that has its cards', () => {
  const { SL } = page({ modules: ['83b-ui-dash.js', '91-views-b.js', '98-ui-settings.js'] }), T = SL.toolkit.settings;
  assert.deepEqual(plain(T.PAGES.map(p => p[1])), ['Models & roles', 'Team & run', 'Budget', 'Permissions & trust', 'Connections', 'Skills & extensions', 'Configuration', 'Appearance']);
  const opens = { models: 'models', roles: 'models', run: 'run', budget: 'budget', permissions: 'permissions', trust: 'permissions', providers: 'providers', mcp: 'providers', skills: 'skills', config: 'config', look: 'look' };
  for (const [old, now] of Object.entries(opens)) assert.equal(T.pageOf(old), now, 'ui.settingsPage(\'' + old + '\') opens ' + now);
  for (const p of T.PAGES) {
    assert.ok(p[3].length > 20 && /\.$/.test(p[3]), p[0] + ': one sentence of what the page is for');
    assert.ok(p[5].length <= 28, p[0] + ': the few words under its name in the list fit');
    assert.ok(SL.dash.PAGES['settings.' + p[0]], p[0] + ' is a page of cards');
  }
  assert.equal(SL.dash.BY_ID['settings.trust.dirs'].page, 'settings.permissions', 'a card keeps its id on the page it joined');
  /* each page draws exactly the cards its page of cards has */
  const S = session(), ST = T.newState({ page: 'models' });
  SL.D.extra.permissions = { modes: [], order: ['deny'], managerWrites: { text: 't', note: 'n' }, testsPreset: { summary: '', rules: [] }, rules: { allow: [], deny: [], ask: [] } };
  for (const p of T.PAGES) {
    const html = T.pages[p[0]](Object.assign(S, { wm: { order: [], ag: {} } }), ST), slots = [...html.matchAll(/<section class="panel[^"]*" data-slot="([^"]+)"/g)].map(m => m[1]);
    assert.deepEqual(plain(slots), plain(SL.dash.PAGES['settings.' + p[0]].cards), p[0] + ': one section for each of its cards, in order');
  }
});

test('the stored layout of a page that was merged is carried over under the page it joined, and the old page is dropped', () => {
  const { SL } = page({ modules: ['83b-ui-dash.js'] }), D = SL.dash;
  const doc = { version: D.VERSION, pages: {
    'settings.roles': [{ id: 'settings.roles.effort', x: 0, y: 0, w: 6, h: 4 }, { id: 'settings.roles.roles', x: 6, y: 0, w: 6, h: 8, hidden: true }, { id: 'gov', x: 0, y: 4, w: 4, h: 3 }],
    'settings.mcp': [{ id: 'settings.mcp.servers', x: 0, y: 0, w: 12, h: 8 }],
    'settings.providers': [{ id: 'settings.providers.providers', x: 0, y: 0, w: 8, h: 5 }],
  } };
  const d = D.normalizeDoc(JSON.parse(JSON.stringify(doc)));
  assert.equal(d.pages['settings.roles'], undefined); assert.equal(d.pages['settings.mcp'], undefined);
  const m = d.pages['settings.models'], at = (L, id) => L.find(c => c.id === id);
  assert.deepEqual(plain(at(m, 'settings.models.catalogue')), { id: 'settings.models.catalogue', x: 0, y: 0, w: 12, h: 14 }, 'the page it joined keeps its default when nothing was stored for it');
  assert.deepEqual(plain(at(m, 'settings.roles.effort')), { id: 'settings.roles.effort', x: 0, y: 14, w: 6, h: 4 }, 'under it, where it was');
  assert.equal(at(m, 'settings.roles.roles').hidden, true, 'a hidden card stays hidden');
  assert.ok(at(m, 'gov'), 'a card of another page added to the old page comes along');
  const c = d.pages['settings.providers'];
  assert.deepEqual(plain(at(c, 'settings.providers.providers')), { id: 'settings.providers.providers', x: 0, y: 0, w: 8, h: 5 }, 'what was stored for the page it joined stays');
  assert.deepEqual(plain(at(c, 'settings.mcp.servers')), { id: 'settings.mcp.servers', x: 0, y: 5, w: 12, h: 8 });
  const s = new Map([[D.KEY, JSON.stringify(doc)]]), store = { getItem: k => (s.has(k) ? s.get(k) : null), setItem: (k, v) => s.set(k, v), removeItem: k => s.delete(k) };
  assert.ok(D.load(store, 'settings.permissions').some(x => x.id === 'settings.trust.dirs'), 'the merged page shows the cards of both');
  assert.deepEqual(plain(D.load(store, 'settings.models').filter(x => x.id.startsWith('settings.roles')).map(x => x.y)), [14, 14], 'read from the browser\'s copy, it is migrated');
});

test('a switch of Team & run that is also a plugin says so and leads to the Plugins page; every flag is secondary text under a plain label', () => {
  const { SL } = setup(), T = SL.toolkit.settings, S = session({ meta: Object.assign(session().meta, { mailman: true, managerApproves: false }) });
  SL.plugins = { find: id => ({ mailman: { id, title: 'Mailman', enabled: false }, 'auto-docs': { id, title: 'Docs on autopilot', enabled: true } })[id] || null };
  const html = T.parts.run(S, T.newState({ page: 'run' }));
  for (const fl of ['--commit', '--mailman', '--auto-docs', '--manager-approves', '--no-mcp', '--trust-project', '--isolation', '--verify', '--swarm N']) assert.ok(html.includes('<span class="setflag mono">' + fl + '</span>'), fl + ' is secondary text');
  for (const l of ['Commit each merged task', 'Mailman', 'Docs on autopilot', 'Manager approves', 'Start without tool servers', 'Use this project’s own files', 'Separate checkouts', 'Check before merging']) assert.ok(html.includes('>' + l + '</label>'), l + ' is a label');
  for (const id of ['mailman', 'auto-docs', 'manager-approves', 'push-progress']) assert.match(html, new RegExp('data-do="open-plugins" data-plg="' + id + '"'), id + ' leads to the Plugins page');
  assert.match(html, /the <b>Mailman<\/b> plugin, off by default/); assert.match(html, /the <b>Docs on autopilot<\/b> plugin, on by default/);
  assert.ok(!/data-flag="pushProgress"/.test(html), 'push progress has no second switch here: it is the plugin\'s');
  assert.match(html, /data-flag="mailman" checked/); assert.ok(!/data-flag="managerApproves" checked/.test(html));
  assert.match(T.parts.skills(S, T.newState({ page: 'skills' })), /data-plg="builtin-skills"/, 'the built-in skills say they are a plugin');
});

test('an empty list says what will appear there and what puts it there', () => {
  const { SL } = setup(), T = SL.toolkit.settings, S = session(), ST = T.newState({ page: 'models' });
  SL.G.mcp = []; SL.G.providers = []; SL.D.extra.skills = []; SL.D.extra.commands = []; SL.D.extra.hooks = { events: [], configured: [] }; SL.D.extra.config = null;
  SL.D.extra.trust = { project: { dir: '/p', state: 'trusted' }, files: [], covers: '', ledger: [], question: { options: [] } }; SL.G.trustDirs = []; SL.D.extra.projects = [];
  const text = h => h.replace(/<[^>]*>/g, ' ').replace(/\s+/g, ' ');
  assert.match(text(T.parts.mcp(S, ST)), /No tool server is configured\. A server you add under "mcp" in your configuration file/);
  assert.match(text(T.parts.providers(S, ST)), /No provider yet\. The providers of your configuration appear here/);
  const sk = text(T.parts.skills(S, ST));
  assert.match(sk, /No skill yet\. A folder with a SKILL\.md/); assert.match(sk, /No command of your own yet\. A markdown file/); assert.match(sk, /No hook is configured\. A hook you add/);
  assert.match(text(T.parts.config(S, ST)), /The configuration has not arrived yet\./);
  const tr = text(T.parts.trust(S, ST));
  assert.match(tr, /No directory yet\. A project appears here when you trust its files/); assert.match(tr, /no file of its own that the harness reads/);
  SL.D.models = []; assert.match(text(T.parts.models(S, ST)), /The catalogue is empty: the models of every provider you are signed in to appear here/);
});
