// schedule-folder.test.mjs: the folder of a scheduled job (Schedule view, 98b-ui-tools.js) is the same field as the one of New session:
// any path, completed as it is typed from the server's answers, with ~ resolved by the server. A job is not run in a folder that is
// not there, so a folder that does not exist is refused here instead of offered to be made; what is sent is the path in full.
import test from 'node:test';
import assert from 'node:assert/strict';
import { boot, FakeEvent } from './pagedom.mjs';
import { answer, BASE } from './fsfake.mjs';

async function open() {
  const L = await boot({ route: (method, p) => {
    const m = method === 'GET' && /^\/api\/fs\/dirs\?path=(.*)$/.exec(p); if (m) return answer(decodeURIComponent(m[1]));
    if (method === 'GET' && p === '/api/schedule') return { jobs: [], daemon: { running: false, owner: 'none', every: '30s', timeout: '', line: '' }, logs: [] };
    if (method === 'GET' && /^\/api\/schedule\/next/.test(p)) return { ok: true, next: '2026-10-12T09:00:00Z' };
    if (method === 'POST' && p === '/api/confirm') return { id: 'cid-1' };
    if (method === 'POST' && p === '/api/schedule/jobs') return { id: 'j1', next: '2026-10-12T09:00:00Z' };
    return undefined;
  } });
  L.SL.views.show('schedule'); await L.settle(); L.advance(300); await L.settle();
  return L;
}
const write = async (L, text) => { const i = L.focus('#sDir'); i.value = text; i.dispatchEvent(new FakeEvent('input', { bubbles: true })); L.advance(200); await L.settle(); };
const job = L => { const p = L.posts.filter(x => x.path === '/api/schedule/jobs'); return p.length ? p[p.length - 1].body : null; };
const add = async L => { L.$('#sGoal').value = 'tidy the notes'; L.$('#sAdd').click(); await L.settle(); L.advance(500); await L.settle(); };

test('the folder of a job is a field that completes folder names, and starts in the first project', async () => {
  const L = await open();
  assert.equal(L.$('#sDir').value, BASE, 'a new job starts in the first project of the server');
  await write(L, '~/co');
  assert.deepEqual(L.doc.querySelectorAll('#sDirList li.dir').map(li => li.querySelector('.dn').textContent), ['code']);
  L.press('tab'); await L.settle(); L.advance(200); await L.settle();
  assert.equal(L.$('#sDir').value, '~/code/');
  assert.match(L.$('#sDirState').textContent, /New to this server/, 'it says what the folder is, as New session does');
  assert.equal(L.doc.querySelectorAll('select#sDir').length, 0, 'it is no list of projects any more');
});

test('Add sends the folder in full (~ resolved by the server), and refuses one that is not there instead of making it', async () => {
  const L = await open();
  await write(L, '~/code/shop'); await add(L);
  assert.equal(job(L).dir, '/home/me/code/shop');
  const M = await open();
  await write(M, '/home/me/code/fresh'); await add(M);
  assert.equal(job(M), null, 'a job in a folder that does not exist is not sent');
  assert.match(M.$('#toasts').textContent, /This folder does not exist\./);
  assert.match(M.$('#sDirState').textContent, /This folder does not exist\./, 'and the field says so, with no offer to make it');
  const N = await open();
  await write(N, '/home/me/code/readme.txt'); await add(N);
  assert.equal(job(N), null); assert.match(N.$('#toasts').textContent, /a file/);
});

test('the permissions of a job say what an unattended run may do: default mode refuses builds and tests too', async () => {
  const L = await open();
  assert.match(L.$('#sModeHint').textContent, /^refuses edits, builds, tests/);
  const m = L.$('#sMode'); m.value = 'accept-edits'; m.dispatchEvent(new FakeEvent('change', { bubbles: true })); await L.settle();
  assert.match(L.$('#sModeHint').textContent, /^edits inside the project, builds and tests/);
  assert.match(L.$('#sModeHint').textContent, /bypass and yolo cannot be scheduled from here$/);
});
