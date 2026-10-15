// goal-sheet.test.mjs: /goal TEXT sets the goal and opens the Goal sheet at once, before the session has taken the goal up (a set
// runs as the tab's next item). The sheet shows the goal just set, starting; it used to say "No goal set" beside a header that
// already showed the goal.
import test from 'node:test';
import assert from 'node:assert/strict';
import { boot, FakeEvent } from './pagedom.mjs';

test('the Goal sheet opened by /goal TEXT shows the goal just set', async () => {
  const L = await boot();
  await L.type('/goal build the ledger with tests'); L.press('enter'); await L.settle();
  assert.equal(L.posts.filter(p => p.path.endsWith('/goal')).at(-1).body.text, 'build the ledger with tests');
  assert.equal(L.SL.ui.hasModal(), true, 'the sheet is open');
  const sheet = L.doc.querySelector('.sheet').textContent;
  assert.match(sheet, /build the ledger with tests/);
  assert.doesNotMatch(sheet, /No goal set/);
  assert.match(sheet, /State starting/);
});

// A goal the server refuses is not lost: the line comes back into the composer, as a refused message does, and no sheet claims
// that it is starting. (A 4,000-character brief was once refused as too long while the composer emptied and the sheet said
// "starting".)
test('a goal the server refuses stays in the composer and opens no sheet', async () => {
  const L = await boot({ route: (m, p) => (m === 'POST' && p.endsWith('/goal')) ? { _status: 400, error: 'the goal is longer than 32,000 characters', code: 'bad_goal' } : undefined });
  await L.type('/goal build the ledger with tests'); L.press('enter'); await L.settle();
  assert.equal(L.posts.filter(p => p.path.endsWith('/goal')).length, 1, 'the goal was sent');
  assert.equal(L.$('#input').value, '/goal build the ledger with tests', 'the refused goal is back in the composer');
  assert.equal(L.SL.ui.hasModal(), false, 'no sheet says the refused goal is starting');
});

test('a goal longer than the server takes is refused before it is sent, and kept', async () => {
  const L = await boot();
  const line = '/goal ' + 'x'.repeat(L.SL.goalMax() + 1), i = L.focus('#input');
  i.value = line; i.dispatchEvent(new FakeEvent('input', { bubbles: true })); await L.settle();
  L.press('enter'); await L.settle();
  assert.equal(L.posts.filter(p => p.path.endsWith('/goal')).length, 0, 'nothing was sent');
  assert.equal(L.$('#input').value, line, 'the text is kept');
  assert.equal(L.SL.ui.hasModal(), false);
});

// A brief is shown as it was written: its lines stay lines and a `code` span is code, so `raven simulate --hosts 5000` does not
// run into one paragraph with --hosts drawn as a dash by the proportional face.
test('the Goal sheet keeps the lines of a brief and shows its code spans as code', async () => {
  const L = await boot();
  const text = 'Build Ravenwatch.\n- `raven`, the agent.\n\nMock mode: `raven simulate --hosts 5000` <b>x</b>', i = L.focus('#input');
  i.value = '/goal ' + text; i.dispatchEvent(new FakeEvent('input', { bubbles: true })); await L.settle();
  L.press('enter'); await L.settle();
  assert.equal(L.posts.filter(p => p.path.endsWith('/goal')).at(-1).body.text, text);
  const big = L.doc.querySelector('.goal-big');
  assert.equal(big.textContent, text.replace(/`/g, ''), 'every line and character is there, the markup escaped');
  assert.deepEqual(Array.from(big.querySelectorAll('code')).map(c => c.textContent), ['raven', 'raven simulate --hosts 5000']);
  assert.equal(big.querySelectorAll('b').length, 0, 'text from the goal is never markup');
});
