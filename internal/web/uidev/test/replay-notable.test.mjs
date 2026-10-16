import test from 'node:test';
import assert from 'node:assert/strict';
import { boot } from './pagedom.mjs';

test('the replay\'s notable events leave out the endpoint\'s quiet cache breaks, which the Cache view keeps', async () => {
  const L = await boot();
  await L.event('break', { id: 'mgr', kind: 'low_hit', read: 0, expected: 9000, why: 'the endpoint did not serve it' });
  for (let i = 0; i < 20; i++) await L.event('break', { id: 'mgr', kind: 'low_hit', read: 9000, expected: 12000, why: 'the endpoint did not serve it', quiet: true });
  await L.event('mail', { from: 'mgr', to: 'be-1', kind: 'request', text: 'start on T2' });
  L.SL.views.show('replay'); await L.settle();
  const rows = L.doc.querySelectorAll('.evlist .mrow').map(r => r.textContent);
  assert.equal(rows.filter(t => /cache break on mgr/.test(t)).length, 1, 'the break the terminal says, not the twenty it keeps quiet');
  assert.ok(rows.some(t => /start on T2/.test(t)), 'the mail is not pushed out of the list');
  assert.equal(L.SL.sessions.active.log.filter(e => e.k === 'break').length, 21, 'every break stays in the log the Cache view reads');
});
