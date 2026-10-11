// folder-field.test.mjs: the folder field of the New session dialog (87b-ui-folder.js in 87-ui-sheets.js), on the shipped page booted on the
// fake DOM of pagedom.mjs. The field takes any path and completes folder names as it is typed, from the server's answers (GET
// /api/fs/dirs, here a small tree of folders): Tab completes what the names share, the arrow keys and Enter choose one, Escape closes the
// list and not the dialog, and an answer that arrives late for an older text is dropped. What the path is (a project, a folder to make,
// a file, a repository or not) is said under the field and decides what Start sends: the full path, createDir for a folder that does
// not exist, gitInit for a new or empty folder unless it is unchecked; a path nothing can start in sends nothing. The checks the folder's
// files suggest fill --verify, and an allow rule that cannot be one is said so before the request.
import test from 'node:test';
import assert from 'node:assert/strict';
import { boot, FakeEvent } from './pagedom.mjs';

import { HOME, BASE, answer } from './fsfake.mjs';

/** A page with the field's server: asked = the paths asked of it; hold = a map of paths whose answer waits for release(path). */
async function open(seed, o) {
  o = o || {};
  const asked = [], held = new Map();
  const L = await boot({ route: (method, p) => {
    const m = method === 'GET' && /^\/api\/fs\/dirs\?path=(.*)$/.exec(p); if (!m) return undefined;
    const text = decodeURIComponent(m[1]); asked.push(text);
    if (o.hold && o.hold.includes(text)) return new Promise(res => held.set(text, () => res(answer(text))));
    return answer(text);
  } });
  L.asked = asked; L.release = text => { held.get(text)(); held.delete(text); };
  L.SL.ui.dialogs.newSession(seed); await L.settle(); L.advance(200); await L.settle();
  return L;
}
/** Put text in the folder box as a paste does, and let the debounce and the answer pass. */
async function write(L, text) {
  const i = L.focus('#nsCwd'); i.value = text; i.dispatchEvent(new FakeEvent('input', { bubbles: true })); L.advance(200); await L.settle();
}
const names = L => L.doc.querySelectorAll('#nsDirList li.dir').map(li => li.querySelector('.dn').textContent);
const value = L => L.$('#nsCwd').value;
const stateText = L => L.$('#nsDirState').textContent.replace(/\s+/g, ' ').trim();
const key = async (L, k) => { const e = L.press(k); L.advance(200); await L.settle(); return e; };
const spec = L => { const p = L.posts.filter(x => x.path === '/api/sessions'); return p.length ? p[p.length - 1].body : null; };
const start = async L => { L.$('#nsStart').click(); await L.settle(); L.advance(200); await L.settle(); };

test('typing asks the server, lists the folders that start with it, and Tab completes the way a shell does', async () => {
  const L = await open({ cwd: '' });
  await write(L, '~/co');
  assert.deepEqual(names(L), ['code'], 'the folders of the home folder that start with co');
  assert.equal(L.$('#nsDirList').hidden, false, 'the list is open while the box has the focus');
  await key(L, 'tab');
  assert.equal(value(L), '~/code/', 'one name: Tab takes it and goes into it');
  assert.deepEqual(names(L), ['api-server', 'notes', 'Sandbox', 'shop'], 'its folders, by name without regard to case, files left out');
  assert.equal(L.$('#nsDirList li.dir .tag').textContent, 'git', 'a repository is marked');
  await write(L, '~/code/s');
  assert.deepEqual(names(L), ['shop', 'Sandbox'], 'the case typed first');
  await key(L, 'tab'); assert.equal(value(L), '~/code/s', 'two names share only what is typed: Tab changes nothing');
  await write(L, '~/code/sh'); await key(L, 'tab');
  assert.equal(value(L), '~/code/shop/');
  assert.equal(L.SL.ui.hasModal(), true, 'the dialog stays open');
  assert.ok(L.asked.includes('~/code/'), 'the folder taken is asked for again, to list what is in it');
});

test('the arrow keys choose a name, Enter takes it, and Escape closes the list before the dialog', async () => {
  const L = await open({ cwd: '' });
  await write(L, '~/code/');
  await key(L, 'down'); await key(L, 'down');
  assert.equal(L.$('#nsCwd').getAttribute('aria-activedescendant'), 'nsDir1');
  assert.equal(L.$('#nsDirList li.dir.on .dn').textContent, 'notes');
  await key(L, 'enter');
  assert.equal(value(L), '~/code/notes/');
  await write(L, '~/code/');
  assert.equal(L.$('#nsDirList').hidden, false);
  const e = await key(L, 'esc');
  assert.equal(e.defaultPrevented, true);
  assert.equal(L.$('#nsDirList').hidden, true, 'the list closed');
  assert.equal(L.SL.ui.hasModal(), true, 'and the dialog did not');
  await key(L, 'esc');
  assert.equal(L.SL.ui.hasModal(), false, 'the next Escape closes the dialog');
});

test('what the path is shows under the field: a project, a folder to make, a file, a repository or not', async () => {
  const L = await open({ cwd: BASE });
  assert.match(stateText(L), /A project of this server\./);
  assert.equal(L.$('#nsGitRow').hidden, true, 'no repository is offered in a folder that has files... (nothing is known of them here)');
  await write(L, '/home/me/code/fresh');
  assert.match(stateText(L), /^New folder: made when the session starts\./);
  assert.equal(L.$('#nsGitRow').hidden, false, 'a repository is offered in a folder to make');
  await write(L, '/home/me/code/Sandbox'); assert.match(stateText(L), /New to this server: you confirm it/); assert.equal(L.$('#nsGitRow').hidden, false, 'and in an empty one');
  await write(L, '/home/me/code/notes'); assert.match(stateText(L), /Not a git repository\./); assert.match(L.$('#nsDirState .qh').getAttribute('data-help'), /Checkpoints and rewind still work/);
  await write(L, '/home/me/code/api-server'); assert.match(stateText(L), /A git repository\./);
  await write(L, '/home/me/code/readme.txt'); assert.match(stateText(L), /That is a file, not a folder\./);
  await write(L, '/home/me/nothing/deeper'); assert.match(stateText(L), /does not exist, and neither does the one it would be made in/);
  await write(L, '~/code'); assert.match(stateText(L), /\/home\/me\/code/, 'a path written with ~ is also shown in full');
  await write(L, '/home/me/code'); assert.doesNotMatch(L.$('#nsDirState').innerHTML, /dirabs/, 'a path written in full is not repeated');
});

test('Start sends the full path, createDir for a folder to make, and gitInit unless it is unchecked', async () => {
  const L = await open({ cwd: '' });
  await write(L, '~/code/fresh'); await start(L);
  assert.deepEqual([spec(L).cwd, spec(L).createDir, spec(L).gitInit], ['/home/me/code/fresh', true, true]);

  const M = await open({ cwd: '' });
  await write(M, '~/code/fresh');
  const g = M.$('#nsGit'); g.checked = false; g.dispatchEvent(new FakeEvent('change', { bubbles: true })); await M.settle();
  assert.match(M.$('#nsCli').textContent, /^# first, as the page does: make the folder\n(SLEIPNIR_SWARM_BUDGET_USD=0 )?sleipnir chat --cwd \/home\/me\/code\/fresh /, 'the command line says what the page does first, with the path in full');
  await start(M);
  assert.deepEqual([spec(M).cwd, spec(M).createDir, spec(M).gitInit], ['/home/me/code/fresh', true, undefined]);

  const E = await open({ cwd: '' });
  await write(E, '/home/me/code/Sandbox'); await start(E);
  assert.deepEqual([spec(E).createDir, spec(E).gitInit], [undefined, true], 'an empty folder that exists becomes a repository too, and is not made');
  const N = await open({ cwd: '' });
  await write(N, '/home/me/code/notes'); await start(N);
  assert.deepEqual([spec(N).createDir, spec(N).gitInit], [undefined, undefined], 'a folder with files is never made a repository');
  const P = await open({ cwd: BASE }); await start(P);
  assert.deepEqual([spec(P).cwd, spec(P).createDir, spec(P).gitInit], [BASE, undefined, undefined], 'a project of the server needs nothing');
});

test('a path nothing can start in sends nothing and says why; a path the server could not check must be written in full', async () => {
  for (const [text, why] of [['/home/me/code/readme.txt', /a file/], ['/home/me/nothing/deeper', /does not exist/]]) {
    const L = await open({ cwd: '' });
    await write(L, text); await start(L);
    assert.equal(spec(L), null, text + ' started something');
    assert.match(L.$('#toasts').textContent, why);
    assert.equal(L.SL.ui.hasModal(), true, 'the dialog stays for another try');
  }
  const L = await boot({ route: (method, p) => (/^\/api\/fs\/dirs/.test(p) ? { _status: 504, error: 'slow', code: 'slow' } : undefined) });
  L.SL.ui.dialogs.newSession({ cwd: '' }); await L.settle();
  const i = L.focus('#nsCwd'); i.value = '~/x'; i.dispatchEvent(new FakeEvent('input', { bubbles: true })); L.advance(200); await L.settle();
  await start(L); assert.equal(spec(L), null); assert.match(L.$('#toasts').textContent, /write its full path/);
  i.value = '/srv/x'; i.dispatchEvent(new FakeEvent('input', { bubbles: true })); await start(L);
  assert.equal(spec(L).cwd, '/srv/x', 'a path written in full is sent as it is; the server decides');
});

test('an answer that arrives late for an older text is dropped', async () => {
  const L = await open({ cwd: '' }, { hold: ['/home/me/code/s'] });
  await write(L, '/home/me/code/s');            // its answer is held
  await write(L, '/home/me/code/sh');           // answered at once
  assert.deepEqual(names(L), ['shop']);
  L.release('/home/me/code/s'); await L.settle();
  assert.deepEqual(names(L), ['shop'], 'the late answer for s did not replace the list of sh');
  assert.match(stateText(L), /does not exist|New folder/);
});

test('with the box empty the projects of the server are offered, and the Projects button offers them whatever is typed', async () => {
  const L = await open({ cwd: '' });
  L.focus('#nsCwd'); L.$('#nsCwd').dispatchEvent(new FakeEvent('focus')); await L.settle();
  assert.equal(L.$('#nsDirList').hidden, false);
  assert.equal(L.doc.querySelectorAll('#nsDirList li.dir').length, 1);
  await write(L, '~/code/zz');
  assert.equal(L.$('#nsDirList').hidden, true, 'nothing starts with zz');
  L.$('#nsProjects').click(); await L.settle();
  assert.equal(L.$('#nsDirList').hidden, false); assert.equal(L.$('#nsDirList li.dir .dn').textContent, BASE);
  L.$('#nsDirList li.dir').click(); await L.settle();
  assert.equal(value(L), BASE + '/');
});

test('the checks a folder\'s files suggest fill --verify, and a rule that cannot be one is said so before anything is sent', async () => {
  const L = await open({ cwd: '', verify: '' });
  await write(L, '/home/me/code/shop');
  const chip = L.$('#nsVerifyHints [data-vh]');
  assert.equal(chip.dataset.vh, 'go test {dirs}'); assert.match(chip.getAttribute('data-help'), /Suggested by go\.mod/);
  chip.click(); await L.settle();
  assert.equal(L.$('#nsVerify').value, 'go test {dirs}');
  assert.match(L.$('#nsCli').textContent, /--verify 'go test \{dirs\}'/);
  await write(L, '/home/me/code/notes');
  assert.match(L.$('#nsVerifyHints').textContent, /no build files recognised here/);

  const r = L.focus('#nsRule'); r.value = 'Bash(go test'; await L.$('#nsAddRule').click(); await L.settle();
  assert.equal(L.$('#nsRuleErr').hidden, false); assert.match(L.$('#nsRuleErr').textContent, /is not a rule/);
  assert.deepEqual(L.SL.ui.dialogs.ruleProblem('Bash(go test:*)'), '');
  await start(L); assert.equal(spec(L), null, 'a rule typed and not added that cannot be one stops the start');
  r.value = 'Bash(go test:*)'; r.dispatchEvent(new FakeEvent('input', { bubbles: true })); assert.equal(L.$('#nsRuleErr').hidden, true, 'typing on clears the message');
  L.$('#nsAddRule').click(); await L.settle();
  L.$('#nsRuleEx [data-ex="tests"]').click(); await L.settle();
  assert.deepEqual(Array.from(L.doc.querySelectorAll('#nsRules .tag')).map(t => t.textContent.replace('×', '').trim()), ['Bash(go test:*)', 'tests']);
  await start(L); assert.deepEqual(spec(L).rules, ['Bash(go test:*)', 'tests']);
});

test('the words that describe a path, and what a taken name and Tab make of the text, need no page', async () => {
  const { folder } = (await boot()).SL.ui, j = x => JSON.parse(JSON.stringify(x));   // what the page's realm makes, as plain values
  assert.deepEqual(j(folder.split('~/code/sh')), { head: '~/code/', tail: 'sh' });
  assert.deepEqual(j(folder.split('C:\\work\\sh')), { head: 'C:\\work\\', tail: 'sh' });
  assert.equal(folder.accepted('/home/me/code/shop', '~/co', { home: HOME, sep: '/' }), '~/code/shop/');
  assert.equal(folder.accepted('/home/me/code/shop', '/home/me/co', { home: HOME, sep: '/' }), '/home/me/code/shop/');
  assert.equal(folder.accepted('/srv/x', '~/co', { home: HOME, sep: '/' }), '/srv/x/', 'outside the home folder a path is written in full');
  assert.equal(folder.common([{ name: 'shop' }, { name: 'shop2' }], 'sh'), 'shop');
  assert.equal(folder.common([{ name: 'shop' }, { name: 'Sandbox' }], 's'), '');
  assert.equal(folder.common([{ name: 'Shop' }], 'sh'), 'Shop', 'the case of the name is taken');
  assert.equal(folder.common([], 'x'), '');
  const D = folder.describe;
  assert.deepEqual([D(null).level, D({ exists: true, dir: false }).level, D({ exists: false, parentOk: false }).level, D({ exists: false, parentOk: true, git: 'none' }).level], ['', 'bad', 'bad', 'new']);
  assert.equal(D({ exists: true, dir: true, readable: false }).level, 'bad');
  assert.equal(D({ exists: true, dir: true, readable: true, empty: true, git: 'none' }).git, 'offer');
  assert.equal(D({ exists: true, dir: true, readable: true, git: 'none' }).git, 'files');
  assert.equal(D({ exists: true, dir: true, readable: true, git: 'inside', gitRoot: '/r' }).git, 'inside');
  assert.equal(D({ exists: false, parentOk: true, git: 'inside', gitRoot: '/r' }).git, 'inside', 'a folder to make inside a repository is not offered one');
  assert.match(folder.gitNote({ git: 'inside', root: '/r' }), /^Inside the git repository at \/r\.$/);
  assert.match(folder.gitHelp({ git: 'files' }), /Checkpoints and rewind/); assert.equal(folder.gitHelp({ git: 'repo' }), '');
  assert.equal(folder.gitNote({ git: 'offer' }), '');
  assert.deepEqual(j(folder.verifyHints({ verify: [{ cmd: 'make test', why: 'Makefile' }, null, { why: 'x' }] })), [{ cmd: 'make test', why: 'Makefile' }]);
  assert.deepEqual(j(folder.verifyHints(null)), []);
});
