// gov-note.test.mjs: the Cockpit's governor note (83-ui-cockpit.js) blames the endpoint's throttling only when there were 429s;
// retries without one are requests that failed (a dropped connection, a server error, a proxy that refused the host), and it says so.
import test from 'node:test';
import assert from 'node:assert/strict';
import { boot } from './pagedom.mjs';

test('the governor note names throttling only when the endpoint sent 429s', async () => {
  const L = await boot();
  const note = m => L.SL.ui.govNote(m);
  assert.equal(note({ r429: 0, retries: 0 }), 'no 429s, no retries: nothing is throttling the team');
  assert.equal(note({ r429: 2, retries: 3 }), '2 429s, 3 retries: the endpoint is throttling the team');
  assert.equal(note({ r429: 1, retries: 1 }), '1 429, 1 retry: the endpoint is throttling the team');
  const failing = note({ r429: 0, retries: 5 });
  assert.equal(failing, 'no 429s, 5 retries: requests are failing, not throttled (the activity log says why)');
  assert.doesNotMatch(failing, /throttling the team/);
});
