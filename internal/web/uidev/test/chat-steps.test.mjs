// chat-steps.test.mjs: the conversation with the manager stays a conversation on the shipped page (84-ui-chat.js). The steps of the
// work between two messages (the manager's tool calls, notes, the harness's lines about the work) are one collapsed line that says how
// many there are and which tools ran, and opens on a click; a single step keeps its own row; /verbose opens every group. The chat is
// also a card (Manager chat, 83b-ui-dash.js): while a page holds it, the rail's conversation and composer are drawn in the card and the
// rail closes; taken off the page, they are back in the rail. The questions for you have a card of their own too (Questions for you).
import test from 'node:test';
import assert from 'node:assert/strict';
import { boot } from './pagedom.mjs';

const txt = el => (el ? el.textContent : '').replace(/\s+/g, ' ').trim();

test('the steps between two messages are one collapsed line that opens, and a single step keeps its own row', async () => {
  const L = await boot(), S = L.SL.sessions.active;
  await L.event('say', { who: 'you', text: 'add pagination' });
  await L.event('say', { who: 'mgr', text: 'Looking at the handler first.', stream: false });
  await L.event('tool', { id: 'mgr', name: 'Read', arg: 'api/catalog.go' });
  await L.event('tool', { id: 'mgr', name: 'Read', arg: 'api/catalog_test.go' });
  await L.event('tool', { id: 'mgr', name: 'Bash', arg: 'go test ./api/...' });
  await L.event('say', { who: 'mgr', text: 'The handler pages now.', stream: false });
  await L.event('tool', { id: 'mgr', name: 'Edit', arg: 'docs/api.md' });
  await L.event('say', { who: 'mgr', text: 'Done.', stream: false });
  await L.step(5);
  const groups = L.doc.querySelectorAll('#talk .agrp');
  assert.ok(groups.length >= 2, 'the steps are grouped');
  const many = groups.find(g => /Bash/.test(txt(g.querySelector('.agrp-h'))) && /Read/.test(txt(g.querySelector('.agrp-h'))));
  assert.ok(many, 'one group holds the three steps');
  assert.equal(many.classList.contains('one'), false);
  assert.match(txt(many.querySelector('.agrp-h')), /^▸ ?3 steps ?Read 2 · Bash/, 'it says how many steps and which tools');
  assert.equal(many.classList.contains('open'), false, 'closed');
  assert.equal(many.querySelectorAll('.agrp-b .msg.tool').length, 3, 'its rows are kept inside it');
  many.querySelector('.agrp-h').click(); await L.step(0.1);
  assert.equal(many.classList.contains('open'), true, 'a click opens it');
  assert.equal(many.querySelector('.agrp-h').getAttribute('aria-expanded'), 'true');
  const one = groups.find(g => /docs\/api\.md/.test(txt(g)));
  assert.ok(one && one.classList.contains('one'), 'a single step is its own row');
  /* the manager's messages are rows of the conversation, never inside a group */
  for (const m of L.doc.querySelectorAll('#talk .msg.mgr')) assert.equal(m.closest('.agrp'), null);
  /* /verbose opens every group */
  many.querySelector('.agrp-h').click(); await L.step(0.1);
  S.ui.verbose = true; S.touch(); await L.step(0.1);
  for (const g of L.doc.querySelectorAll('#talk .agrp')) if (/Read/.test(txt(g.querySelector('.agrp-h')))) assert.equal(g.classList.contains('open'), false, 'a group the person closed stays closed');
  assert.deepEqual(L.errors, []);
});

test('the Manager chat card holds the conversation and the composer while a page shows it, and gives them back to the rail', async () => {
  const L = await boot(), SL = L.SL, box = L.$('#chatBox');
  assert.equal(box.parentNode, L.$('#rail'), 'the chat starts in the rail');
  SL.ui.dash.add('chat'); await L.settle(); await L.step(0.1);
  const card = L.$('#views .dcard[data-card="chat"]');
  assert.ok(card, 'the card is on the page');
  assert.ok(box.parentNode && box.parentNode.closest('.dcard') === card, 'the chat is in the card');
  assert.ok(card.querySelector('#composer') && card.querySelector('#talk'), 'with its conversation and composer');
  assert.equal(L.$('#app').dataset.chat, 'card', 'the rail closes');
  assert.equal(SL.ui.chatDock.docked, true);
  card.querySelector('.dc-rail').click(); await L.settle(); await L.step(0.1);
  assert.equal(L.$('#views .dcard[data-card="chat"]'), null, 'the card left the page');
  assert.equal(box.parentNode, L.$('#rail'), 'the chat is back in the rail');
  assert.equal(L.$('#app').dataset.chat, undefined);
  assert.deepEqual(L.errors, []);
});

test('the Questions for you card holds the question box while a page shows it, and gives it back to the rail', async () => {
  const L = await boot(), SL = L.SL, q = L.$('#qSlot');
  SL.ui.dash.add('questions'); await L.settle(); await L.step(0.1);
  const card = L.$('#views .dcard[data-card="questions"]');
  assert.ok(card && q.parentNode.closest('.dcard') === card, 'the question box is in the card');
  assert.equal(SL.ui.chatDock.questionsDocked, true);
  assert.ok(L.$('#rail #talk'), 'the conversation stays in the rail');
  card.querySelector('.dc-rail').click(); await L.settle(); await L.step(0.1);
  assert.equal(q.parentNode, L.$('#chatBox'), 'back in the rail');
  assert.equal(q.nextSibling, L.$('#planBox'), 'in its place, above the plan');
  assert.deepEqual(L.errors, []);
});
