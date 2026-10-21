// teams.test.mjs: the team library (87c-ui-teams.js) and the saved teams of the New session dialog (87-ui-sheets.js), on the shipped
// page booted on the fake DOM of pagedom.mjs. A team turns into the file --team reads and back; the dialog opens the library, a saved
// team used there becomes the dialog's team and its command line says --team NAME (and the request teamName) until the team is
// changed; the dialog's own team is what Save sends.
import test from 'node:test';
import assert from 'node:assert/strict';
import { boot, FakeEvent } from './pagedom.mjs';

const CREW = { description: 'two backends', slots: ['architect=p/a', 'scout', 'backend=p/b', 'backend=p/b', 'frontend=p/f', 'devops', 'tester', 'docs', 'reviewer=p/r'], models: { manager: 'p/m', judge: 'p/j' } };
const LIST = { teams: [{ name: 'crew', scope: 'project', path: '/p/a/.sleipnir/teams/crew.json', team: CREW }, { name: 'crew', scope: 'user', path: '/h/.sleipnir/teams/crew.json', shadowed: true, team: CREW }, { name: 'bad', scope: 'user', path: '/h/.sleipnir/teams/bad.json', error: 'it has no slots' }], project: '/p/a/.sleipnir/teams', user: '/h/.sleipnir/teams' };

async function open() {
  const L = await boot({ route: (m, p) => {
    if (p === '/api/hello') return { boot: 'b1', streamAfter: 7, server: { addr: '127.0.0.1:7777' }, tabs: [], active: '', ui: {}, defaults: { swarm: 8, maxWorkers: 24, model: 'p/x' } };
    if (m === 'GET' && p.startsWith('/api/teams')) return LIST;
    if (m === 'PUT' && p.startsWith('/api/teams/')) return { entry: {} };
    return undefined;
  } });
  L.SL.ui.dialogs.newSession({ cwd: '/p/a' }); await L.settle(); L.advance(200); await L.settle();
  return L;
}
const cli = L => L.$('#nsCli').textContent;

test('a team becomes the file --team reads, and the file the team', async () => {
  const L = await boot();
  const T = L.SL.ui.teams;
  const f = T.fileOf([{ role: 'backend', model: 'p/b' }, { role: 'backend', model: '' }, { role: 'tester', model: 'p/t' }], { manager: 'p/m', backend: 'p/x', judge: '' }, ' d ');
  assert.deepEqual(JSON.parse(JSON.stringify(f)), { description: 'd', slots: ['backend=p/b', 'backend', 'tester=p/t'], models: { manager: 'p/m' } }, 'a worker role\'s model goes in its slots, an empty one nowhere');
  assert.deepEqual(JSON.parse(JSON.stringify(T.teamOf(f))), { slots: [{ role: 'backend', model: 'p/b' }, { role: 'backend', model: '' }, { role: 'tester', model: 'p/t' }], models: { manager: 'p/m' } });
  assert.equal(T.summary(f), '3 workers: 2 backend, tester');
  assert.deepEqual(JSON.parse(JSON.stringify(T.models(CREW))), ['p/a', 'p/b', 'p/f', 'p/r', 'p/m', 'p/j']);
  assert.equal(T.nameOf('My Crew (v2).json'), 'My-Crew-v2');
  assert.ok(T.validName('web-crew.v2') && !T.validName('.x') && !T.validName('a b') && !T.validName('x.json') && !T.validName(''));
  assert.match(T.text(f), /^\{\n {2}"description": "d",\n {2}"slots": \[/);
});

test('the dialog opens the library, uses a saved team, and says --team NAME until the team is changed', async () => {
  const L = await open();
  L.$('#nsTeams').click(); await L.settle();
  assert.match(L.$('#tlList').textContent, /crew/, 'the saved teams are listed');
  assert.match(L.$('#tlList').textContent, /hidden by the project’s team of the same name/);
  assert.match(L.$('#tlList').textContent, /it has no slots/, 'a file that is no team is listed with why, to be removed');
  assert.equal(L.SL.u.$$('[data-tl="load"]').length, 1, 'only a team --team NAME finds can be used: not the shadowed one, not the broken one');
  L.$('[data-tl="load"]').click(); await L.settle(); L.advance(200); await L.settle();
  assert.ok(L.$('#nsCli'), 'the dialog is back');
  assert.match(cli(L), / --model p\/m /, 'the manager\'s model is the dialog\'s model');
  assert.match(cli(L), / --swarm 9 --team crew /);
  assert.doesNotMatch(cli(L), /--role-model/, 'the saved team brings its slots and its judge');
  assert.match(L.$('#nsSw').textContent, /manager \+ 9 workers/);
  L.$('#nsStart').click(); await L.settle(); L.advance(200); await L.settle();
  const spec = L.posts.filter(x => x.path === '/api/sessions').pop().body;
  assert.equal(spec.teamName, 'crew');
  assert.equal(spec.team.length, 9);
  assert.deepEqual(JSON.parse(JSON.stringify(spec.team[2])), { role: 'backend', model: 'p/b' });

  const M = await open();
  M.$('#nsTeams').click(); await M.settle(); M.$('[data-tl="load"]').click(); await M.settle(); M.advance(200); await M.settle();
  M.$('[data-trole="tester"] [data-tstep="1"]').click(); await M.settle();
  assert.doesNotMatch(cli(M), /--team/, 'a team changed after it was loaded is no longer the saved one');
  assert.match(cli(M), /--swarm 10 .*--role-model judge=p\/j .*--role-model backend=p\/b/);
});

test('Save sends the dialog\'s team, every slot on the model it would run on', async () => {
  const L = await open();
  L.$('#nsTeams').click(); await L.settle();
  const name = L.$('#tlName'); name.value = 'eight'; name.dispatchEvent(new FakeEvent('input', { bubbles: true }));
  L.$('[data-tl="save"]').click(); await L.settle();
  const put = L.posts.filter(x => x.method === 'PUT' && x.path === '/api/teams/eight').pop();
  assert.ok(put, 'the team was saved');
  assert.equal(put.body.scope, 'user');
  assert.equal(put.body.team.slots.length, 8);
  assert.ok(put.body.team.slots.every(s => /^[a-z]+=p\/x$/.test(s)), 'a slot on the worker model names the dialog\'s model: ' + put.body.team.slots);
  assert.equal(put.body.team.models.manager, 'p/x');
});
