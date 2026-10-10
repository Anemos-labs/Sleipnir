// cdp-watchdog.test.mjs: the DevTools client of the browser tests does not wait for ever for a browser that stopped answering or went away.
import test from 'node:test';
import assert from 'node:assert/strict';
import { Browser } from '../../../../scripts/web-parity.mjs';

/** a Browser whose socket takes what is sent and never answers */
function silent(timeout) {
  const b = new Browser({ kill() {}, exitCode: null, once() {} }, '/nonexistent-profile');
  b.ws = { send() {}, close() {} };
  b.timeout = timeout;
  return b;
}

test('a call that is never answered fails after the timeout and names the call', async () => {
  const b = silent(30);
  await assert.rejects(b.send('Page.enable'), /no answer to Page\.enable in .* the browser is stuck/);
  assert.equal(b.pending.size, 0, 'nothing is left waiting');
});

test('an answer that comes in time resolves the call and clears its timer', async () => {
  const b = silent(5000);
  const p = b.send('Runtime.evaluate', { expression: '1' });
  b.dispatch({ id: 1, result: { value: 1 } });
  assert.deepEqual(await p, { value: 1 });
  assert.equal(b.pending.size, 0);
});

test('a browser that goes away ends every call that waits, and every later call, with the reason', async () => {
  const b = silent(60000);
  const a = b.send('Page.enable'), c = b.send('Page.navigate');
  b.fail('the DevTools connection closed');
  await assert.rejects(a, /connection closed/);
  await assert.rejects(c, /connection closed/);
  await assert.rejects(b.send('Page.reload'), /connection closed/);
  assert.equal(b.pending.size, 0);
});

test('a browser that was closed on purpose refuses later calls and leaves nobody waiting', async () => {
  const b = silent(60000);
  await b.close();
  await assert.rejects(b.send('Page.enable'), /closed/);
});
