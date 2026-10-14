// goal-sheet.test.mjs: /goal TEXT sets the goal and opens the Goal sheet at once, before the session has taken the goal up (a set
// runs as the tab's next item). The sheet shows the goal just set, starting; it used to say "No goal set" beside a header that
// already showed the goal.
import test from 'node:test';
import assert from 'node:assert/strict';
import { boot } from './pagedom.mjs';

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
