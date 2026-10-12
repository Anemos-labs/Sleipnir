// cdp-watchdog.test.mjs: the DevTools clients of the browser tests (scripts/web-parity.mjs's Browser, and cdp.mjs's open for the
// *.browser.mjs scripts) do not wait for ever for a browser that stopped answering or went away, nor for one that does not start: a
// browser that hangs while it starts (fakechrome.mjs plays one) is killed with its profile and another is started.
import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { Browser } from '../../../../scripts/web-parity.mjs';
import { open } from './cdp.mjs';

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

const FAKE = fileURLToPath(new URL('./fakechrome.mjs', import.meta.url));
const sh = s => "'" + s.replace(/'/g, "'\\''") + "'";
/** an executable that runs fakechrome.mjs in `mode` (the path Browser.launch and open are given), and the pids and profiles of the browsers it ran */
function fake(t, mode) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'fakechrome-')), bin = path.join(dir, 'chrome'), pids = path.join(dir, 'pids');
  fs.writeFileSync(bin, '#!/bin/sh\nexec ' + [process.execPath, FAKE, mode, pids].map(sh).join(' ') + ' "$@"\n', { mode: 0o755 });
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  const runs = () => (fs.existsSync(pids) ? fs.readFileSync(pids, 'utf8').split('\n').filter(Boolean).map(l => l.split('\t')) : []);
  return { bin, pids: () => runs().map(r => Number(r[0])), profiles: () => runs().map(r => r[1]) };
}
const alive = pid => { try { process.kill(pid, 0); return true; } catch { return false; } };
/** what fn writes to stderr, and its result */
async function stderrOf(fn) {
  const write = process.stderr.write, notes = [];
  process.stderr.write = (s, ...a) => { notes.push(String(s)); return true; };
  try { return { value: await fn(), notes: notes.join('') }; } finally { process.stderr.write = write; }
}
const unix = { skip: process.platform === 'win32' && 'the stand-in browser is started by a /bin/sh script' };

for (const [mode, step] of [['silent', /the line "DevTools listening on" on its stderr/], ['nohandshake', /the WebSocket handshake of ws:\/\/127\.0\.0\.1:\d+\/devtools\/browser\/fake/], ['noanswer', /the answer to Browser\.getVersion/]]) {
  test(`a browser that hangs while it starts (${mode}): the start fails at its deadline, names the step it hung at, and the browser is killed`, unix, async t => {
    const f = fake(t, mode), exits = process.listenerCount('exit'), t0 = Date.now();
    const err = await Browser.launch(f.bin, { attempts: 1, deadline: 3000 }).then(() => null, e => e);
    assert.ok(err, 'the start fails'); const took = Date.now() - t0;
    assert.match(err.message, /^chrome did not start: nothing came in 3 s while waiting for /); assert.match(err.message, step); assert.match(err.message, /; it was killed\)/);
    if (mode !== 'silent') assert.match(err.message, /the end of its stderr:\nDevTools listening on ws:/);
    assert.ok(took >= 2900 && took < 15000, 'it waited for its deadline and not for ever: ' + took + ' ms');
    assert.equal(f.pids().length, 1); assert.deepEqual(f.pids().filter(alive), [], 'no browser is left running to keep the test file alive');
    assert.equal(process.listenerCount('exit'), exits, 'nothing of it is left for the exit of the process');
  });
}

test('a browser that exits while it starts: every attempt is noted, the last error says it exited and quotes its stderr, nothing is left running', unix, async t => {
  const f = fake(t, 'exit');
  const { value: err, notes } = await stderrOf(() => Browser.launch(f.bin, { attempts: 3, deadline: 3000 }).then(() => null, e => e));
  assert.ok(err, 'the start fails');
  assert.match(err.message, /^chrome did not start: it exited \(1\) while waiting for the line "DevTools listening on"/); assert.doesNotMatch(err.message, /killed/);
  assert.match(err.message, /the end of its stderr:\n\[fake\] no display to open$/);
  assert.equal(notes.match(/^chrome attempt \d of 3 failed, starting another: chrome did not start: it exited \(1\)/gm).length, 2, notes);
  assert.equal(f.pids().length, 3, 'three browsers were tried'); assert.deepEqual(f.pids().filter(alive), []);
});

test('a browser that hangs while it starts is replaced: the next one is used, and the hung one is killed', unix, async t => {
  const f = fake(t, 'stall-once');
  const { value: b, notes } = await stderrOf(() => Browser.launch(f.bin, { attempts: 3, deadline: 3000 }));
  try {
    assert.match(notes, /^chrome attempt 1 of 3 failed, starting another: chrome did not start: nothing came in 3 s while waiting for the line "DevTools listening on"/);
    assert.equal((await b.send('Browser.getVersion')).product, 'FakeChrome/1', 'the browser that started answers');
    const [hung, used] = f.pids(); assert.equal(f.pids().length, 2); assert.equal(alive(hung), false, 'the hung browser was killed'); assert.equal(alive(used), true);
  } finally { await b.close(); }
  assert.deepEqual(f.pids().filter(alive), [], 'close ends the browser');
});

/** the listeners on this process that a browser of cdp.mjs adds while it runs: the exit, and the interruptions */
const listeners = () => ['exit', 'SIGINT', 'SIGTERM'].map(e => e + ' ' + process.listenerCount(e)).join(', ');
/** the profiles of f's browsers that are still on disk */
const kept = f => f.profiles().filter(p => fs.existsSync(p));

for (const [mode, step] of [['silent', /the line "DevTools listening on" on its stderr/], ['nolist', /the list of its targets at http:\/\/127\.0\.0\.1:\d+\/json\/list/],
  ['nohandshake', /the WebSocket handshake of ws:\/\/127\.0\.0\.1:\d+\/devtools\/page\/fake/], ['noanswer', /the answer to Page\.enable/]]) {
  test(`cdp.mjs: a browser that hangs while it starts (${mode}): open fails at its deadline, names the step it hung at, and the browser is killed and its profile removed`, unix, async t => {
    const f = fake(t, mode), before = listeners(), t0 = Date.now();
    const err = await open('about:blank', { chrome: f.bin, attempts: 1, deadline: 3000 }).then(() => null, e => e);
    assert.ok(err, 'the start fails'); const took = Date.now() - t0;
    assert.match(err.message, /^chrome did not start: nothing came in 3 s while waiting for /); assert.match(err.message, step); assert.match(err.message, /; it was killed\)/);
    if (mode !== 'silent') assert.match(err.message, /the end of its stderr:\nDevTools listening on ws:/);
    assert.ok(took >= 2900 && took < 15000, 'it waited for its deadline and not for ever: ' + took + ' ms');
    assert.equal(f.pids().length, 1); assert.deepEqual(f.pids().filter(alive), [], 'no browser is left running to keep the script alive');
    assert.match(f.profiles()[0], /slcore-/); assert.deepEqual(kept(f), [], 'its profile was removed');
    assert.equal(listeners(), before, 'nothing of it is left for the exit or interruption of the process');
  });
}

test('cdp.mjs: a browser that exits while it starts: every attempt is noted, the last error says it exited and quotes its stderr, nothing is left', unix, async t => {
  const f = fake(t, 'exit'), before = listeners();
  const { value: err, notes } = await stderrOf(() => open('about:blank', { chrome: f.bin, attempts: 3, deadline: 3000 }).then(() => null, e => e));
  assert.ok(err, 'the start fails');
  assert.match(err.message, /^chrome did not start: it exited \(1\) while waiting for the line "DevTools listening on"/); assert.doesNotMatch(err.message, /killed/);
  assert.match(err.message, /the end of its stderr:\n\[fake\] no display to open$/);
  assert.equal(notes.match(/^chrome attempt \d of 3 failed, starting another: chrome did not start: it exited \(1\)/gm).length, 2, notes);
  assert.equal(f.pids().length, 3, 'three browsers were tried'); assert.deepEqual(f.pids().filter(alive), []);
  assert.deepEqual(kept(f), []); assert.equal(listeners(), before);
});

test('cdp.mjs: a browser that hangs while it starts is replaced: the page opens in the next one, the hung one is killed, and close ends the one used', unix, async t => {
  const f = fake(t, 'stall-once'), before = listeners();
  const { value: page, notes } = await stderrOf(() => open('about:blank', { chrome: f.bin, attempts: 3, deadline: 3000 }));
  try {
    assert.match(notes, /^chrome attempt 1 of 3 failed, starting another: chrome did not start: nothing came in 3 s while waiting for the line "DevTools listening on"/);
    assert.equal((await page.send('Browser.getVersion')).product, 'FakeChrome/1', 'the browser that started answers');
    const [hung, used] = f.pids(); assert.equal(f.pids().length, 2); assert.equal(alive(hung), false, 'the hung browser was killed'); assert.equal(alive(used), true);
    assert.deepEqual(kept(f), [f.profiles()[1]], "the hung browser's profile was removed");
  } finally { await page.close(); }
  assert.deepEqual(f.pids().filter(alive), [], 'close ends the browser, which does not heed SIGTERM');
  assert.deepEqual(kept(f), [], 'and removes its profile'); assert.equal(listeners(), before, 'and leaves nothing for the exit of the process');
});

test('cdp.mjs: a browser that stops answering after it started: the call fails after callTimeout and names it, and the browser is killed', unix, async t => {
  const f = fake(t, 'answer-once'), before = listeners(), t0 = Date.now();
  const err = await open('about:blank', { chrome: f.bin, attempts: 1, deadline: 20000, callTimeout: 1000 }).then(() => null, e => e);
  assert.ok(err, 'open fails'); assert.equal(err.message, 'no answer to Runtime.enable in 1 s: the browser is stuck');
  assert.ok(Date.now() - t0 < 15000, 'it failed at the timeout of the call');
  assert.deepEqual(f.pids().filter(alive), [], 'the browser was killed'); assert.deepEqual(kept(f), []); assert.equal(listeners(), before);
});

test('cdp.mjs: a browser that goes away after it started ends a call at once, with the reason, instead of at the timeout', unix, async t => {
  const f = fake(t, 'answer'), before = listeners();
  const page = await open('about:blank', { chrome: f.bin, attempts: 1, deadline: 20000, callTimeout: 60000 });
  try {
    const [pid] = f.pids(); process.kill(pid, 'SIGKILL');
    const t0 = Date.now();
    await assert.rejects(page.send('Runtime.evaluate', { expression: '1' }), /chrome exited \(SIGKILL\)|the DevTools connection closed/);
    assert.ok(Date.now() - t0 < 15000, 'the call did not wait for its timeout');
    await assert.rejects(page.send('Runtime.evaluate', { expression: '1' }), /chrome exited \(SIGKILL\)|the DevTools connection closed/, 'nor does a later one');
  } finally { await page.close(); }
  assert.deepEqual(kept(f), []); assert.equal(listeners(), before);
});
