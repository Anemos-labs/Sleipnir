// approval-done.test.mjs: the card of an answered question in the rail (#qSlot .qdone) says how it was answered for 8 seconds and then
// goes, with nothing else happening on the page that would draw the slot again. The page is the shipped one on the fake DOM.
import test from 'node:test';
import assert from 'node:assert/strict';
import { boot } from './pagedom.mjs';

const q1 = { id: 'q_1', agent: 'be-1', task: 'T1', cmd: 'go test ./...', why: 'run the tests', what: 'this command', cwd: '/p/a', t0: 12, rule: 'Bash(go test:*)' };

test('the card of an answered question goes after 8 s', async () => {
  const L = await boot();
  await L.event('ask', { q: q1 }); await L.step(0.2);
  assert.ok(L.$('#qSlot .qstrip'), 'the question is in the rail');
  await L.event('answer', { qid: 'q_1', choice: 3, by: 'canceled' }); await L.step(0.1);
  const card = L.$('#qSlot .qdone');
  assert.ok(card, 'the answered card is shown');
  assert.match(card.textContent, /refused: canceled/);
  await L.step(4);
  assert.ok(L.$('#qSlot .qdone'), 'still there at 4 s');
  L.advance(5000); await L.settle();
  assert.equal(L.$('#qSlot .qdone'), null, 'gone after 8 s');
  assert.equal(L.$('#qSlot').innerHTML, '');
});
