// quiet-breaks.test.mjs: a cache break the terminal's chat would not say without /verbose (wire.Break.Quiet: the endpoint's own miss
// that cost less than a cent, or one past the first three) stays out of the feed, the cards' mark and the count of the rail on the
// shipped page; the Cache view keeps it, and the feed shows it with /verbose on. A break that is said marks the card for 8 s, with a
// title that says what kind of break it was.
import test from 'node:test';
import assert from 'node:assert/strict';
import { boot } from './pagedom.mjs';

test('held quiet breaks stay in channels but not digest rows or counts', async () => {
  const L = await boot(), S = L.SL.sessions.active, m = L.SL.model.newModel(S);
  const digest = { rows: [], breaks: 0 };
  L.SL.model.reduce(m, { k: 'break', t: 1, id: 'be-1', kind: 'low_hit', quiet: true }, { digest });
  assert.equal(digest.rows.length, 0);
  assert.equal(digest.breaks, 0);
  assert.ok(m.chan.mgr.some(r => r.k === 'break' && r.quiet));
  L.SL.model.reduce(m, { k: 'break', t: 2, id: 'be-1', kind: 'drift' }, { digest });
  assert.ok(digest.rows.length > 0);
  assert.equal(digest.breaks, 1);
});

test('a quiet break is kept for the Cache view and /verbose, and leaves the cards and the count alone', async () => {
  const L = await boot();
  const S = L.SL.sessions.active, m = () => S.wm;
  await L.event('break', { id: 'be-1', kind: 'low_hit', read: 0, expected: 1000, why: 'the prompt prefix did not change', missUsd: 0.0027, quiet: true });
  await L.step(0.1);
  assert.equal(m().anomalies.length, 1, 'the Cache view has it');
  assert.equal(m().anomalies[0].quiet, true);
  assert.ok(m().lastBreakT < -900, 'no card is marked');
  assert.equal(L.SL.ui.nav.badges(S).cache, undefined, 'the rail counts no anomaly');
  await L.step(3);   /* the view reaches the time of the event */
  const row = S.m.chan.mgr.filter(e => e.k === 'break').slice(-1)[0];
  assert.ok(row && row.quiet, 'the feed row is there for /verbose');
  assert.match(L.SL.chat.rowHtml(row, S, S.m), /class="msg break[^"]*qbrk/, 'drawn as a quiet break: hidden unless /verbose is on');
  assert.equal(L.$('#feed').classList.contains('verbose'), false);
  S.ui.verbose = true; S.touch(); await L.step(0.1);
  assert.equal(L.$('#feed').classList.contains('verbose'), true, '/verbose on shows quiet breaks in the feed');

  await L.event('break', { id: 'be-1', kind: 'drift', read: 0, expected: 5000, why: 'the prefix changed', layer: 'role' });
  await L.step(3);
  assert.ok(m().lastBreakT > -900, 'a break of the prompt marks the card');
  assert.equal(m().flash.kind, 'drift');
  assert.match(L.SL.ui.breakTitle(m().flash), /prompt prefix changed \(layer role\) without a declared rebase/);
  assert.match(L.SL.ui.breakTitle({ kind: 'low_hit' }), /did not change, the endpoint did not serve it/);
  assert.equal(S.m.anomalies.length, 2, 'the view has both');
  assert.match(L.SL.ui.nav.badges(S).cache[2], /^1 cache anomaly$/, 'the rail counts the one that is said');
  assert.equal(m().anomalies.length, 2);
});
