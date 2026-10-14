// live-view.test.mjs: live, the view follows the world clock on the shipped page. An event is drawn in the frame after it arrives,
// whatever the pointer and the focus do (nothing holds the view while the pointer rests on the conversation); a tab that comes back
// after being hidden draws every row it missed, the manager's last message included, with no summary row in their place; a replay still
// pauses and plays at its speed; and the frame loop goes on after a frame that throws.
import test from 'node:test';
import assert from 'node:assert/strict';
import { boot, FakeEvent } from './pagedom.mjs';

/** Frames of `s` seconds each, as the display would run them. */
async function frames(L, n, s) { for (let i = 0; i < n; i++) await L.step(s || 1 / 60); }

test('an event that arrives while the pointer and the focus are on the conversation is drawn at once', async () => {
  const L = await boot(), S = L.SL.sessions.active, talk = L.$('#talk');
  talk.dispatchEvent(new FakeEvent('pointerover', { bubbles: true })); talk.focus();
  await frames(L, 60);                                 // the pointer rests there for a second
  await L.event('say', { who: 'mgr', text: 'the plan is ready', stream: false });
  await frames(L, 2);
  assert.ok(Math.abs(S.vt - S.wt) < 1e-9, 'the view is at the live edge: ' + S.vt + ' of ' + S.wt);
  assert.match(talk.textContent, /the plan is ready/);
  await L.event('tool', { id: 'be-1', name: 'Bash', arg: 'go test ./...', out: 'ok', ok: true });
  await frames(L, 2);
  assert.match(L.$('#feed').textContent, /go test \.\/\.\.\./, 'the Team activity feed is drawn too');
  assert.equal(L.$('#holdChip'), null, 'the page has no hold chip');
  assert.equal(L.SL.time.rate, 1);
});

test('a tab that was hidden draws every row it missed when it comes back, the manager\'s final message included', async () => {
  const L = await boot(), S = L.SL.sessions.active, talk = L.$('#talk');
  await frames(L, 30);
  /* hidden: the browser runs no frame; the stream goes on and moves the world clock to the server's time */
  L.doc.hidden = true;
  const at = (t, k, f) => L.frame('ev', { tab: 'a', ev: Object.assign({ seq: 1000 + Math.round(t * 10), t, k }, f) });
  await at(100, 'say', { who: 'you', text: 'add the tests' });
  for (let i = 0; i < 40; i++) await at(110 + i * 10, 'tool', { id: 'be-1', name: 'Edit', arg: 'api/cart_test.go', out: '', ok: true });
  await at(520, 'say', { who: 'mgr', text: 'Done: the tests pass and the work is merged.', stream: false });
  await at(521, 'final', {});
  for (let i = 0; i < 20; i++) await at(530 + i * 10, 'note', { id: 'be-1', g: 'done', text: 'tidied ' + i });
  L.doc.hidden = false;
  await frames(L, 1);
  assert.ok(Math.abs(S.vt - S.wt) < 1e-9, 'the first frame back is at the live edge');
  assert.match(talk.textContent, /add the tests/);
  assert.match(talk.textContent, /Done: the tests pass and the work is merged\./, 'the manager\'s final message is drawn');
  assert.doesNotMatch(talk.textContent + L.$('#feed').textContent, /while held|catching up/);
  assert.equal(S.m.chan.mgr.filter(e => e.k === 'feed' && /api\/cart_test\.go/.test(e.text)).length, 40, 'every missed row is in the log');
  assert.match(L.$('#feed').textContent, /tidied 19/, 'the feed reaches the last row');
  assert.ok(L.$('#liveHint').hidden, 'live, the elapsed clock has no lag to show');
});

test('a replay still pauses, plays at its speed and ends at the live edge', async () => {
  const L = await boot(), S = L.SL.sessions.active;
  await frames(L, 30);
  S.seek(2, false); S.touch(); await frames(L, 60);   /* the rate eases down to 0 in a fraction of a second */
  const stood = S.vt; await frames(L, 30);
  assert.ok(stood < 2.3 && S.vt === stood, 'paused, the view stands: ' + stood + ', ' + S.vt);
  assert.equal(L.SL.time.rate, 0, 'paused, the rate is 0');
  assert.match(L.$('#liveHint').textContent, /live \+\d+s/, 'the elapsed clock says how far behind live the replay is');
  S.replay.playing = true; S.replay.speed = 4; S.touch();
  await frames(L, 60);
  assert.ok(S.vt > 4 && S.vt < 6.1, 'four seconds of the session in a second (with the easing): ' + S.vt);
  await frames(L, 240);
  assert.equal(S.replay, null, 'the replay ended at the live edge');
  assert.ok(Math.abs(S.vt - S.wt) < 1e-9);
});

test('a frame that throws is logged and the loop goes on', async () => {
  const queue = [];
  const L = await boot({ globals: { requestAnimationFrame: fn => { queue.push(fn); return queue.length; } } });
  let now = 0; const run = () => { now += 16; queue.splice(0).forEach(fn => fn(now)); };
  run(); run();
  const loop = L.SL.loop, pump = L.SL.actions.pump; let thrown = 0;
  L.SL.actions.pump = () => { if (!thrown++) throw new Error('a frame failed'); return pump(); };
  const before = loop.frameNo;
  run();
  assert.equal(loop.errors, 1, 'the error is counted');
  assert.ok(L.errors.some(e => /a frame failed/.test(e)), 'and logged');
  assert.equal(queue.length, 1, 'the next frame is asked for');
  run(); run();
  assert.ok(loop.frameNo >= before + 3, 'the loop goes on: ' + (loop.frameNo - before));
  assert.equal(loop.errors, 1);
});
