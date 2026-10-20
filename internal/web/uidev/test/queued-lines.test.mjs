// queued-lines.test.mjs: the lines that wait for the turn (meta.queued) in the composer of the shipped page. A message has Send now
// (it goes to the running turn as a steer), Edit (it leaves the queue and comes back into the message box) and ×; a slash line has
// Edit and ×; an action of the session (a goal change) has no buttons. While a turn runs, the hint under the box says what Enter and
// Ctrl+Enter do, Ctrl+Enter steers instead of queueing, and Up in an empty box takes the last queued line back, as in the terminal.
import test from 'node:test';
import assert from 'node:assert/strict';
import { boot, FakeEvent } from './pagedom.mjs';

const QUEUED = [{ id: 'q2', text: 'second', kind: 'message' }, { id: 'q3', text: '/budget 3', kind: 'command' }, { id: 'q4', text: '/goal resume', kind: 'action' }];

test('editing a queued line while switching sessions saves it in its original draft', async () => {
  let resolve;
  const reply = new Promise(r => { resolve = r; });
  const L = await boot({ route: (m, p) => m === 'DELETE' && p === '/api/sessions/a/queue/q2' ? reply : undefined });
  await L.frame('meta', { tab: 'a', patch: { running: true, queued: QUEUED } });
  await L.step(0.1);
  const original = L.SL.sessions.active;
  await click(L, 'q2', 'edit');
  L.SL.sessions.activate(null);
  L.$('#input').value = 'other draft';
  original.ui.draft = 'saved draft';
  resolve(QUEUED[0]);
  await L.settle();
  assert.equal(original.ui.draft, 'second\nsaved draft');
  assert.equal(L.$('#input').value, 'other draft');
});
/** The page, with a turn running and three queued lines; the server answers a drop with the line it dropped. */
async function running() {
  const L = await boot({ route: (method, p) => {
    const m = /^\/api\/sessions\/a\/queue\/(q\d+)(\/now)?$/.exec(p);
    if (!m) return undefined;
    const q = QUEUED.find(x => x.id === m[1]);
    return m[2] ? { id: q.id, text: q.text, kind: q.kind, steered: true } : { id: q.id, text: q.text, kind: q.kind };
  } });
  await L.frame('meta', { tab: 'a', patch: { running: true, queued: QUEUED } }); await L.step(0.1);
  return L;
}
const rows = L => L.doc.querySelectorAll('#queuedLines .ql');
const buttons = row => Array.from(row.querySelectorAll('button')).map(b => b.dataset.qa);
const click = async (L, qid, qa) => { L.$('#queuedLines .ql[data-qid="' + qid + '"] [data-qa="' + qa + '"]').click(); await L.settle(); };

test('each queued line has the buttons its kind allows, and the hint says what Enter and Ctrl+Enter do while a turn runs', async () => {
  const L = await running();
  const r = rows(L);
  assert.equal(r.length, 3);
  assert.deepEqual(buttons(r[0]), ['now', 'edit', 'drop'], 'a message');
  assert.deepEqual(buttons(r[1]), ['edit', 'drop'], 'a slash line cannot steer');
  assert.deepEqual(buttons(r[2]), [], 'an action of the session stays');
  assert.equal(L.$('#cHint').textContent, 'Enter queues · Ctrl+Enter steers');
  assert.equal(L.$('#cHint').title, 'Enter queues for after this turn · Ctrl+Enter sends now (steer)');
  await L.frame('meta', { tab: 'a', patch: { running: false, queued: [] } }); await L.step(0.1);
  assert.equal(L.$('#cHint').textContent, '/ commands', 'the idle hint comes back');
  assert.equal(rows(L).length, 0);
});

test('Send now, Edit and × make their requests; Edit puts the whole line back in the message box', async () => {
  const L = await running();
  await click(L, 'q2', 'now');
  assert.deepEqual(L.calls, ['POST /api/sessions/a/queue/q2/now']);
  await click(L, 'q3', 'edit');
  assert.equal(L.calls[1], 'DELETE /api/sessions/a/queue/q3');
  assert.equal(L.$('#input').value, '/budget 3');
  await click(L, 'q2', 'drop');
  assert.equal(L.calls[2], 'DELETE /api/sessions/a/queue/q2');
  assert.equal(L.$('#input').value, '/budget 3', 'a dropped line does not come back');
});

test('Ctrl+Enter while a turn runs steers with what the box holds; Enter queues it', async () => {
  const L = await running();
  await L.type('use the other file');
  L.press('ctrl+enter'); await L.settle();
  assert.deepEqual(L.posts.map(p => p.method + ' ' + p.path), ['POST /api/sessions/a/steer']);
  assert.deepEqual(L.posts[0].body, { text: 'use the other file' });
  assert.equal(L.$('#input').value, '', 'the line went');
  await L.type('later');
  L.press('enter'); await L.settle();
  assert.equal(L.posts[1].path, '/api/sessions/a/messages', 'Enter sends it to the queue');
  // a slash line is not a steer
  await L.type('/cost');
  const e = new FakeEvent('keydown', { bubbles: true, key: 'Enter', ctrlKey: true });
  L.$('#input').dispatchEvent(e); await L.settle();
  assert.equal(L.posts.length, 2, 'a command is not sent as a steer');
});

test('Up in an empty message box takes the last queued line that can be edited back; with text typed it is the history', async () => {
  const L = await running();
  L.focus('#input');
  L.press('up'); await L.settle();
  assert.deepEqual(L.calls, ['DELETE /api/sessions/a/queue/q3'], 'the action is passed over');
  assert.equal(L.$('#input').value, '/budget 3');
  L.press('up'); await L.settle();
  assert.equal(L.calls.length, 1, 'with text in the box Up is the history');
});
