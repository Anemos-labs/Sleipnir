// start-defaults.test.mjs: what the New session dialog chooses by itself and how its Start confirmation reads, on the shipped page
// booted on the fake DOM of pagedom.mjs. A team in the root of a repository with a commit, or in a folder git init makes one, is
// isolated in worktrees unless the person or the server's flags and configuration named the isolation; the confirmation lists every
// reason in view, with the rules of the tests preset as one line whose list opens on a click.
import test from 'node:test';
import assert from 'node:assert/strict';
import { boot, FakeEvent } from './pagedom.mjs';

const DEFAULTS = { swarm: 8, maxWorkers: 24, isolation: 'none', testsPreset: 'go, cargo, npm, pnpm and make: test, build, check, lint and vet' };
const listing = state => ({ dir: '/p', prefix: 'a', entries: [], home: '/home/u', sep: '/', state: Object.assign({ path: '/p/a', exists: true, dir: true, readable: true, parentOk: true, known: true, empty: false, git: 'repo', verify: [], repo: { hasCommits: true, branch: 'main', clean: true } }, state) });
async function openAt(state, seed, defaults) {
  const L = await boot({ route: (m, p) => p === '/api/hello' ? { boot: 'b1', streamAfter: 7, server: { addr: '127.0.0.1:7777' }, tabs: [], active: '', ui: {}, defaults: Object.assign({}, DEFAULTS, defaults) } : p.startsWith('/api/fs/dirs') ? listing(state) : undefined });
  L.SL.ui.dialogs.newSession(Object.assign({ cwd: '/p/a' }, seed)); await L.settle(); L.advance(200); await L.settle();
  return L;
}
const pressed = L => { const b = L.doc.querySelectorAll('[data-seg="isolation"]'); for (const x of b) if (x.getAttribute('aria-pressed') === 'true') return x.dataset.v; return ''; };

test('a team in a repository root with a commit is isolated in worktrees unless someone chose otherwise', async () => {
  assert.equal(pressed(await openAt({})), 'worktree', 'the server\'s isolation none is only its built-in default');
  assert.equal(pressed(await openAt({}, {}, { isolationSet: true })), 'none', '--isolation none or swarm.isolation none is kept');
  assert.equal(pressed(await openAt({}, { swarm: 0 })), 'none', 'a single agent has no isolation');
  assert.equal(pressed(await openAt({ repo: { hasCommits: false, branch: 'main', clean: true } })), 'none', 'a repository with no commit cannot');
  assert.equal(pressed(await openAt({ git: 'none', repo: undefined })), 'none', 'a folder that is no repository cannot');
  const L = await openAt({});
  L.$('[data-seg="isolation"][data-v="none"]').click(); await L.settle();
  assert.equal(pressed(L), 'none', 'what the person picks stays');
  assert.match(L.$('#nsCli').textContent, /--isolation none/);
});

test('git init ticked in an empty folder makes worktree the choice; unticked, none', async () => {
  const L = await openAt({ git: 'none', empty: true, repo: undefined }, { gitInit: true });
  assert.equal(pressed(L), 'worktree');
  const g = L.$('#nsGit'); g.checked = false; g.dispatchEvent(new FakeEvent('change', { bubbles: true })); await L.settle();
  assert.equal(pressed(L), 'none');
});

test('the manager answers routine questions unless the person says no: only an off is sent', async () => {
  const L = await openAt({});
  const box = L.$('[data-k="managerApproves"]'); assert.ok(box, 'the team option is there');
  assert.equal(box.checked, true, 'on by default');
  assert.doesNotMatch(L.$('#nsCli').textContent, /manager-approves/);
  box.checked = false; box.dispatchEvent(new FakeEvent('change', { bubbles: true })); await L.settle();
  assert.match(L.$('#nsCli').textContent, /--manager-approves=false/);
  L.$('#nsStart').click(); await L.settle(); L.advance(200); await L.settle();
  const sent = L.posts.filter(x => x.path === '/api/sessions').pop();
  assert.equal(sent.body.managerApproves, false);
  const M = await openAt({}); M.$('#nsStart').click(); await M.settle(); M.advance(200); await M.settle();
  assert.equal('managerApproves' in M.posts.filter(x => x.path === '/api/sessions').pop().body, false, 'on is the server\'s own default');
});

const PRESET = ['Bash(go test:*)', 'Bash(go build:*)', 'Bash(go vet:*)', 'Bash(cargo test:*)', 'Bash(cargo build:*)', 'Bash(npm test:*)', 'Bash(npm run build:*)', 'Bash(pnpm test:*)', 'Bash(make test:*)', 'Bash(make build:*)'];

test('the Start confirmation shows the tests preset as one line, every other reason before it, and the rules on a click', async () => {
  const L = await openAt({});
  L.SL.D.testsPreset = PRESET.slice();
  const reasons = ['make the folder /p/new and work in it', 'make /p/new a git repository, with an empty first commit', 'allow ' + PRESET.concat(['Bash(ls:*)']).join(', '), 'run the verify command go test {dirs}'];
  const p = L.SL.ui.askConfirm({ scope: 'session:x', reasons, method: 'POST', path: '/api/sessions' }); await L.settle();
  const items = Array.from(L.doc.querySelectorAll('.cf-d .plist > li')).map(li => li.textContent.replace(/\s+/g, ' ').trim());
  assert.equal(items.length, 5, 'four reasons and the preset: ' + JSON.stringify(items));
  assert.match(items[0], /make the folder/); assert.match(items[2], /^⚠ ?allow Bash\(ls:\*\)$/);
  assert.match(items[4], /allow the tests preset: build and test commands of go, cargo, npm… \(10 rules\) ▸ show/, 'the preset is the last line');
  const list = L.$('#cfRules0'); assert.equal(list.hidden, true, 'its rules are folded');
  L.$('[data-rules="0"]').click(); await L.settle();
  assert.equal(list.hidden, false); assert.equal(list.querySelectorAll('li').length, 10);
  L.$('[data-no]').click(); assert.equal(await p, false);
});

test('a long list of rules that is not the preset is one line too, with its count', async () => {
  const L = await openAt({});
  L.SL.D.testsPreset = [];
  const rules = Array.from({ length: 9 }, (_, i) => 'Bash(tool' + i + ':*)');
  L.SL.ui.askConfirm({ scope: 's', reasons: ['allow ' + rules.join(', '), 'permission mode plan'], method: 'POST', path: '/api/sessions' }); await L.settle();
  const items = Array.from(L.doc.querySelectorAll('.cf-d .plist > li')).map(li => li.textContent.replace(/\s+/g, ' ').trim());
  assert.match(items[0], /permission mode plan/);
  assert.match(items[1], /allow 9 rules: Bash\(tool0:\*\), Bash\(tool1:\*\)… ▸ show/);
});
