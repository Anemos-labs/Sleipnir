// board-cards.test.mjs: what a task card says about its task at a glance (82-ui-board.js ages, 83-ui-cockpit.js ui.boardMarks), on the
// shipped page booted on the fake DOM of pagedom.mjs: an unfinished card's time in its column, which moves with the clock and starts
// again when the task changes column or hands; a running card whose owner is working on it now is marked live; a card the stall
// detector raised something about is marked stalled and says why on the Board view; a failed card says why it failed, in words.
import test from 'node:test';
import assert from 'node:assert/strict';
import { boot } from './pagedom.mjs';

const card = (L, id) => L.$('.kan .tcard[data-task="' + id + '"]');
const age = (L, id) => { const a = card(L, id).querySelector('.amark'); return a ? a.textContent : null; };

test('an unfinished card shows its time in its column, which restarts when it moves and goes when it is merged', async () => {
  const L = await boot(), S = L.SL.sessions.active;
  await L.step(1); const t0 = S.vt;
  await L.event('task', { id: 'T1', title: 'catalogue', s: 'running', owner: 'be-1', t: t0 });
  await L.event('task', { id: 'T1', title: 'catalogue', s: 'running', owner: 'be-1', t: t0 + 0.5, cost: 0.02 });   /* a spend change is not a move */
  await L.step(1); L.SL.views.show('board'); await L.settle();
  const T = S.m.tasks.T1;
  assert.equal(T.since, t0, 'the time it entered running, not of its latest event');
  assert.equal(T.t, t0 + 0.5);
  await L.step(125);
  const a = card(L, 'T1').querySelector('.amark');
  assert.ok(a, 'a running card has an age');
  assert.equal(a.textContent, '2m', 'it follows the clock without an event');
  assert.match(a.title, /^running for \d+m \d\ds, held by be-1$/);
  const t1 = S.vt;
  await L.event('task', { id: 'T1', title: 'catalogue', s: 'verify', owner: 'be-1', t: t1 });
  await L.step(1);
  assert.equal(S.m.tasks.T1.since, t1, 'a move to another column starts the age again');
  assert.match(age(L, 'T1'), /^\d+s$/);
  await L.event('task', { id: 'T1', title: 'catalogue', s: 'merged', owner: 'be-1', t: t1 + 2 });
  await L.step(1);
  assert.equal(age(L, 'T1'), null, 'a merged card has no age');
});

test('a running card whose owner works on it now is live; one the stall detector names is stalled and says why', async () => {
  const L = await boot();
  await L.event('task', { id: 'T2', title: 'cart', s: 'running', owner: 'be-1' });
  await L.event('state', { id: 'be-1', s: 'edit', doing: 'Edit cart.go', task: 'T2' });
  await L.step(1); L.SL.views.show('board'); await L.settle();
  assert.ok(card(L, 'T2').classList.contains('live'), 'its owner is editing: live');
  assert.equal(card(L, 'T2').dataset.live, 'edit');
  await L.event('state', { id: 'be-1', s: 'wait', doing: 'waits for T1', task: 'T2' }); await L.step(1);
  assert.ok(!card(L, 'T2').classList.contains('live'), 'waiting is not working');
  await L.event('stall', { id: 'be-1', task: 'T2', kind: 'no progress', s: 'raise', text: 'no edit in 10 minutes' }); await L.step(1);
  const c = card(L, 'T2');
  assert.ok(c.classList.contains('stalled'));
  assert.ok(c.querySelector('.smark'), 'a ⚠ on its owner line, in the Cockpit too, whatever its owner\'s colour');
  assert.match(c.querySelector('.twhy').textContent, /⚠ no edit in 10 minutes/, 'the Board view says what stalls it on the card');
  await L.event('stall', { id: 'be-1', task: 'T2', kind: 'no progress', s: 'clear', text: '' }); await L.step(1);
  assert.ok(!card(L, 'T2').classList.contains('stalled'));
  assert.equal(card(L, 'T2').querySelector('.twhy'), null);
});

test('a failed card says why it failed, in words', async () => {
  const L = await boot();
  await L.event('task', { id: 'T3', title: 'pager', s: 'todo', failed: true, closure: 'verifier', attempts: 3 });
  await L.event('task', { id: 'T4', title: 'old pager', s: 'todo', failed: true, closure: 'superseded(T3)' });
  await L.event('task', { id: 'T5', title: 'flaky', s: 'todo', failed: true, closure: 'exhausted', attempts: 4 });
  await L.step(1); L.SL.views.show('board'); await L.settle();
  assert.ok(card(L, 'T3').classList.contains('failed'));
  assert.equal(card(L, 'T3').querySelector('.twhy').textContent, '✗ verification kept failing');
  assert.equal(card(L, 'T4').querySelector('.twhy').textContent, '✗ replaced by T3');
  assert.match(card(L, 'T5').querySelector('.twhy').textContent, /^✗ out of attempts \(4\)/);
  assert.equal(age(L, 'T3'), null, 'a failed card has no age');
  assert.equal(L.SL.ui.closureWords('blocked_on(T9)'), 'blocked on T9');
  assert.equal(L.SL.ui.closureWords('denied'), 'the manager refused the work');
  assert.equal(L.SL.ui.closureWords(''), 'failed');
});

test('the server\'s since wins over the event\'s own t: a page loaded from a keyframe keeps the true age', async () => {
  const L = await boot(), S = L.SL.sessions.active;
  await L.step(700); const t0 = S.vt;
  /* a keyframe re-dates the task at its own t0; since still says when the task started running */
  await L.event('task', { id: 'T8', title: 'old', s: 'running', owner: 'be-1', t: t0, since: t0 - 600 });
  await L.step(1); L.SL.views.show('board'); await L.settle();
  assert.equal(S.m.tasks.T8.since, t0 - 600);
  assert.match(age(L, 'T8'), /^1\dm$/);
});

test('on the Board view a task that moved has a history strip: a segment per stretch, and how often it was sent back', async () => {
  const L = await boot(), S = L.SL.sessions.active;
  await L.step(1); const t0 = S.vt;
  const hist = [{ t: t0, s: 'todo' }, { t: t0 + 1, s: 'running', owner: 'be-1' }, { t: t0 + 2, s: 'verify', owner: 'be-1' }, { t: t0 + 3, s: 'running', owner: 'fe-1' }];
  await L.event('task', { id: 'T6', title: 'pager', s: 'running', owner: 'fe-1', t: t0 + 3, hist });
  await L.event('task', { id: 'T7', title: 'fresh', s: 'todo', t: t0 + 3, hist: [{ t: t0 + 3, s: 'todo' }] });
  await L.step(2); L.SL.views.show('board'); await L.settle(); await L.step(1);
  const h = card(L, 'T6').querySelector('.thist');
  assert.ok(h, 'a task that moved has a strip');
  const segs = h.querySelectorAll('.thbar i');
  assert.equal(segs.length, 4, 'todo, running be-1, verify, running fe-1');
  assert.match(h.textContent, /↩ 1/, 'sent back once, from verify to running');
  assert.match(segs[1].title, /^running · be-1 · 1s$/);
  assert.match(segs[3].title, /^running · fe-1 · \d+s$/, 'the open stretch runs to the view clock');
  assert.equal(card(L, 'T7').querySelector('.thist'), null, 'a task that never moved has none');
  await L.event('task', { id: 'T6', title: 'pager', s: 'running', owner: 'fe-1', t: t0 + 9 }); await L.step(1);
  assert.deepEqual(S.m.tasks.T6.hist.length, 0, 'an event without hist clears it, as for every field of a task event');
  assert.equal(card(L, 'T6').querySelector('.thist'), null);
});

test('a strip segment says what its stretch cost and how it ended: passed or failed verification, and why it was sent back', async () => {
  const L = await boot(), S = L.SL.sessions.active;
  await L.step(1); const t0 = S.vt;
  const hist = [{ t: t0, s: 'todo' }, { t: t0 + 1, s: 'running', owner: 'be-1', cost: 0.12, reqs: 8 }, { t: t0 + 2, s: 'verify', owner: 'be-1', cost: 0.01, reqs: 1 },
    { t: t0 + 3, s: 'running', owner: 'be-1', why: 'tests failed in cart_test.go' }, { t: t0 + 4, s: 'verify', owner: 'be-1' }, { t: t0 + 5, s: 'merged', owner: 'be-1' }];
  await L.event('task', { id: 'T6', title: 'pager', s: 'merged', owner: 'be-1', t: t0 + 5, hist });
  await L.event('task', { id: 'T9', title: 'flaky', s: 'todo', failed: true, closure: 'exhausted', attempts: 2, t: t0 + 5,
    hist: [{ t: t0, s: 'running', owner: 'fe-1', cost: 0.5 }, { t: t0 + 2, s: 'failed', owner: 'fe-1', why: 'its workers kept stopping', cost: -3, reqs: 'x' }] });
  await L.step(2); L.SL.views.show('board'); await L.settle(); await L.step(1);
  const segs = card(L, 'T6').querySelectorAll('.thbar i');
  assert.match(segs[1].title, /^running · be-1 · 1s · \$0\.12 in 8 requests$/);
  assert.match(segs[2].title, /· failed verification, sent back: tests failed in cart_test\.go$/, 'a verify stretch followed by running was sent back, with the reason');
  assert.match(segs[4].title, /· passed verification$/);
  const f = card(L, 'T9').querySelectorAll('.thbar i');
  assert.match(f[0].title, /\$0\.50 · then failed: its workers kept stopping$/);
  assert.equal(S.m.tasks.T9.hist[1].cost, 0, 'a negative cost is dropped');
  assert.equal(S.m.tasks.T9.hist[1].reqs, 0);
});

test('a failed card on the Board view says what to do, and its buttons take the step', async () => {
  const L = await boot(), S = L.SL.sessions.active;
  await L.event('task', { id: 'T3', title: 'pager', s: 'todo', failed: true, closure: 'verifier', attempts: 3, owner: 'be-1' });
  await L.event('task', { id: 'T4', title: 'old pager', s: 'todo', failed: true, closure: 'superseded(T3)' });
  await L.event('task', { id: 'T5', title: 'flaky', s: 'todo', failed: true, closure: 'exhausted', attempts: 4, owner: 'fe-1' });
  await L.event('task', { id: 'T8', title: 'stopped', s: 'running', failed: true, closure: 'canceled' });
  await L.step(1); L.SL.views.show('board'); await L.settle();
  const d3 = card(L, 'T3').querySelector('.tdo');
  assert.ok(d3, 'a failed card has a What to do line');
  assert.match(d3.textContent, /What to do: Read why verification failed/);
  assert.deepEqual([...d3.querySelectorAll('[data-tdo]')].map(b => b.dataset.tdo + ':' + (b.dataset.arg || '')), ['agent:be-1', 'ask:'], 'a team without worktrees has no verify output: the worker that ran the tests');
  S.meta.isolation = 'worktree';
  await L.event('task', { id: 'T3', title: 'pager', s: 'todo', failed: true, closure: 'verifier', attempts: 3, owner: 'be-1' }); await L.step(1);
  assert.deepEqual([...d3.querySelectorAll('[data-tdo]')].map(b => b.dataset.tdo), ['verify', 'ask'], 'with worktrees, the merge queue\'s verify output');
  assert.deepEqual([...card(L, 'T4').querySelectorAll('[data-tdo]')].map(b => b.dataset.tdo + ':' + (b.dataset.arg || '')), ['go:T3'], 'a replaced task points at the one that replaced it');
  assert.deepEqual([...card(L, 'T5').querySelectorAll('[data-tdo]')].map(b => b.dataset.tdo + ':' + (b.dataset.arg || '')), ['agent:fe-1', 'ask:'], 'out of attempts: its last worker, then the manager');
  assert.equal(card(L, 'T8').querySelector('.tdo'), null, 'a task the person canceled needs nothing');
  /* the buttons: ask the manager puts the question in the message box; go flashes the card it names */
  let composed = ''; L.SL.ui.compose = text => { composed = text; };
  d3.querySelector('[data-tdo="ask"]').click();
  assert.equal(composed, 'About task T3 (it failed: verification kept failing): ');
  let verified = ''; const ws = L.SL.ui.ws.verify; L.SL.ui.ws.verify = id => { verified = id; };
  d3.querySelector('[data-tdo="verify"]').click(); L.SL.ui.ws.verify = ws;
  assert.equal(verified, 'T3');
  card(L, 'T4').querySelector('[data-tdo="go"]').click();
  assert.ok(card(L, 'T3').classList.contains('tflash'), 'go flashes the card of the task it names');
  /* a card back to work loses it */
  await L.event('task', { id: 'T3', title: 'pager', s: 'running', owner: 'be-1' }); await L.step(1);
  assert.equal(card(L, 'T3').querySelector('.tdo'), null);
  assert.equal(S.m.tasks.T3.failed, false);
});

test('the Board view has a legend of what the marks on its cards mean', async () => {
  const L = await boot();
  L.SL.views.show('board'); await L.settle();
  const lg = L.$('.blegend');
  assert.ok(lg, 'a legend under the board');
  for (const w of ['live', 'stalled', 'failed', '↩ N', 'time in its column', 'try 2']) assert.ok(lg.textContent.includes(w), 'the legend names ' + w);
});
