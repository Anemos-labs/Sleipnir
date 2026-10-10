// compact-note.test.mjs: what the page says about the cache when it shows a compaction, in its rows and in the Cache view's fold.
// A compaction that a plan preceded has a moment (warm: a declared, priced rebase; cold: the rewrite cost nothing extra). One that no
// plan preceded (an emergency compaction, or one a person asked for) has none: the log does not tell its price, so the page claims no
// rebase and no saving for it and says how it was made, as the terminal's feed does.
import test from 'node:test';
import assert from 'node:assert/strict';
import { boot, PATCH } from './pagedom.mjs';

/** Words of a claim about the price or the state of the cache. */
const CLAIMS = /rebase|while cold|was cold|was warm|nothing extra|free/;

/** The page after be-1's compactions (`compact` events as the server sends them), with the Cache view on be-1. */
async function pageAfter(...compactions) {
  const L = await boot();
  for (const c of compactions) await L.event('compact', Object.assign({ id: 'be-1', from: 9000, to: 1000, pct: -89 }, c));
  await L.step(5); // the page's clock reaches the events
  L.SL.link.select('be-1');
  L.SL.views.show('cache'); await L.settle();
  return L;
}
/** The note under the fold of the Cache view ('' when there is none), and the compaction rows of the page (a worker's is in the team
 *  activity feed, the manager's in the conversation). */
const foldNote = L => { assert.ok(L.doc.querySelector('.fold-note'), 'the Cache view draws the fold'); const em = L.doc.querySelector('.fold-note em'); return em ? em.textContent : ''; };
const rows = L => L.doc.querySelectorAll('.msg.compact').map(r => r.textContent);

/** What a compaction nobody planned must show: its sizes, its agent, how it was made, and no claim about the cache. */
function assertNoClaim(L, mode) {
  const note = foldNote(L), row = rows(L).at(-1);
  assert.equal(note, mode ? mode + ' compaction' : '', 'the fold says how the thread was folded, and nothing else');
  assert.ok(row.endsWith(mode ? 'be-1 · ' + mode + ' compaction' : 'be-1'), 'the row ends with its agent and how it was made: ' + row);
  assert.doesNotMatch(note, CLAIMS); assert.doesNotMatch(row, CLAIMS);
  assert.match(row, /◆ compacted9k → 1k/, 'the committed compaction is shown with its sizes');
}

test('a compaction with a moment says it in its row and in the fold, in the terminal\'s words', async () => {
  const cold = await pageAfter({ moment: 'cold', mode: 'fork' });
  assert.equal(foldNote(cold), 'the cache was cold, so the rewrite cost nothing extra');
  assert.ok(rows(cold).at(-1).endsWith('be-1 · cache rewritten while cold: free'), rows(cold).at(-1));
  const warm = await pageAfter({ moment: 'warm', mode: 'emergency' });
  assert.equal(foldNote(warm), 'the cache was warm: a declared, priced rebase');
  assert.ok(rows(warm).at(-1).endsWith('be-1 · a declared, priced rebase'), rows(warm).at(-1));
});

test('a compaction nobody planned claims no rebase and no saving, in its row and in the fold: it says how it was made', async () => {
  for (const mode of ['emergency', 'mask', 'fork']) assertNoClaim(await pageAfter({ mode }), mode);
});

test('a compaction whose server says neither moment nor mode shows its sizes and its agent and nothing about the cache; hostile values are no words', async () => {
  assertNoClaim(await pageAfter({}), '');
  const L = await pageAfter({ moment: '<img src=x onerror=alert(1)>', mode: '<b>x</b>' });
  assertNoClaim(L, '');
  assert.equal(L.doc.querySelectorAll('.msg.compact img, .fold-note img, .fold-note b b').length, 0);
});

test('the guard fails when the page goes back to claiming a rebase for a compaction nobody planned', async () => {
  const fallback = "return c && (c.mode === 'fork' || c.mode === 'mask' || c.mode === 'emergency') ? c.mode + ' compaction' : '';";
  PATCH['30-model.js'] = src => { assert.ok(src.includes(fallback), 'the line this test puts the old default back into has moved'); return src.replace(fallback, "return long ? 'the cache was warm: a declared, priced rebase' : 'a declared, priced rebase';"); };
  try {
    const L = await pageAfter({ mode: 'emergency' });
    assert.throws(() => assertNoClaim(L, 'emergency'), /fold says how the thread was folded/);
    assert.match(foldNote(L), /priced rebase/);
  } finally { delete PATCH['30-model.js']; }
});
