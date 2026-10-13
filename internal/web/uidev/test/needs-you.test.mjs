// needs-you.test.mjs: a question that waits is seen and answered, on the shipped page booted on the fake DOM of pagedom.mjs.
// Needs you is on every view (the session strip and the left rail) with how long the oldest question has waited, and a click on it
// brings that question forward in its session; a browser notification (off by default) comes once per question that waits too long
// while the page is out of sight; a replay never disables answering without saying so over the card, with a Go live; a click on the
// gantt's chart never starts a replay; the manager's answers to routine questions are one collapsed line, never a card for the person;
// the free slots of a team are named by role; a new tab is shown as soon as the server made it, and what is sent to it while it starts
// waits for it instead of failing.
import test from 'node:test';
import assert from 'node:assert/strict';
import { boot } from './pagedom.mjs';

const q = (id, agent, cmd) => ({ id, agent: agent || 'be-1', task: 'T1', cmd: cmd || 'go test ./...', why: 'run the tests', what: 'this command', cwd: '.', kind: 'command' });
const txt = el => (el ? el.textContent : '').replace(/\s+/g, ' ').trim();

test('Needs you is on every view with the count and the wait of the oldest question, and opens it in its session', async () => {
  const L = await boot({ two: true });
  assert.equal(L.$('#sInbox').classList.contains('has'), false, 'nothing waits: the quiet button');
  assert.equal(L.$('#nav .nvneed'), null);
  /* a question in the other tab (b), while tab a is active and a full-page view is shown */
  await L.frame('ev', { tab: 'b', ev: { seq: 50, t: 12, k: 'ask', q: q('q_b', 'fe-1', 'npm test') } });
  L.SL.views.show('settings'); await L.step(0.1);
  await L.step(1); L.advance(1000); await L.settle();
  const badge = L.$('#sInbox');
  assert.ok(badge.classList.contains('has'), 'the badge is loud');
  assert.match(txt(badge), /Needs you 1/);
  assert.match(L.$('#sInbox [data-needage]').textContent, /^\d+s$/, 'how long it has waited');
  const nav = L.$('#nav .nvneed');
  assert.ok(nav, 'the left rail has it too, on the Settings view');
  assert.match(txt(nav), /Needs you/);
  assert.equal(L.SL.sessions.active.id, 'a');
  badge.click(); await L.step(0.1); L.advance(100); await L.settle();
  assert.equal(L.SL.sessions.active.id, 'b', 'the question\'s session is active');
  assert.equal(L.$('#app').dataset.rail, 'open', 'the rail is open');
  const strip = L.$('#qSlot .qstrip');
  assert.ok(strip, 'the question is on screen');
  assert.equal(L.doc.activeElement, strip, 'and has the focus');
  assert.match(txt(strip), /npm test/);
});

test('the list of every open question stays one click away, beside the badge', async () => {
  const L = await boot();
  await L.event('ask', { q: q('q_1') }); await L.step(0.2);
  L.$('#sInboxAll').click(); await L.settle();
  assert.ok(L.$('.ibxpop'), 'the inbox opens');
  assert.equal(L.$('#sInboxAll').getAttribute('aria-expanded'), 'true');
});

test('a replay says over the question that answers are paused, and Go live answers again', async () => {
  const L = await boot();
  await L.event('ask', { q: q('q_1') }); await L.step(0.2);
  const S = L.SL.sessions.active; S.seek(S.vt, false); S.touch(); await L.step(0.1);
  const ban = L.$('#qSlot .qreplay');
  assert.ok(ban && !ban.hidden, 'the banner is shown');
  assert.match(txt(ban), /Replay: ?answers are paused ?Go live/);
  await L.step(2);
  assert.equal(L.SL.ui.approvals.tryKey('1'), false, 'a key does not answer in a replay');
  L.$('#qSlot [data-golive]').click(); await L.step(0.1);
  assert.equal(S.replay, null, 'live again');
  assert.equal(L.$('#qSlot .qreplay').hidden, true);
  await L.step(1);
  assert.equal(L.SL.ui.approvals.tryKey('1'), true, 'the key answers');
});

test('a click on the gantt\'s chart opens nothing in replay: only its controls do', async () => {
  const L = await boot();
  L.SL.views.show('cockpit'); await L.step(0.1);
  const g = L.$('.gsvg'); assert.ok(g, 'the gantt is on the Cockpit');
  g.click(); await L.step(0.1);
  assert.equal(L.SL.sessions.active.replay, null);
  L.$('.gplay').click(); await L.step(0.1);
  assert.ok(L.SL.sessions.active.replay, 'the pause button starts a replay');
});

test('the manager\'s answers are one collapsed line in the rail and in the chat, never an open card', async () => {
  const L = await boot();
  await L.event('ask', { q: q('q_1', 'be-1', 'go test ./api/...') }); await L.step(0.1);
  await L.event('answer', { qid: 'q_1', choice: 1, by: 'manager' }); await L.step(0.1);
  assert.equal(L.$('#qSlot .qstrip'), null, 'no card for the person');
  assert.equal(L.SL.sessions.needs().length, 0);
  await L.event('ask', { q: q('q_2', 'fe-1', 'npm run build') }); await L.event('answer', { qid: 'q_2', choice: 3, by: 'manager' }); await L.step(0.1);
  const line = L.$('#qSlot .qmgr');
  assert.ok(line, 'one line in the rail');
  assert.match(txt(line), /the manager answered 2 questions: 1 approved, 1 denied/);
  assert.equal(L.$('#qSlot .qmgr-l').hidden, true, 'collapsed');
  L.$('#qSlot [data-mgrtog]').click(); await L.step(0.1);
  assert.equal(L.$('#qSlot .qmgr-l').hidden, false, 'it opens');
  assert.match(txt(L.$('#qSlot .qmgr-l')), /be-1 T1 go test \.\/api\/\.\.\. approved ?fe-1 T1 npm run build denied/);
  await L.step(1);   // the chat follows the view's clock
  const rows = L.doc.querySelectorAll('#talk .mgrdec');
  assert.equal(rows.length, 1, 'one row in the chat for both');
  assert.match(txt(rows[0]), /the manager answered 2 questions/);
  assert.equal(L.doc.querySelectorAll('#talk .askrow').length, 0);
});

test('the manager approving alone says "approved N actions"', async () => {
  const L = await boot();
  for (const id of ['q_1', 'q_2', 'q_3']) { await L.event('ask', { q: q(id) }); await L.event('answer', { qid: id, choice: 1, by: 'manager' }); }
  await L.step(1);
  assert.match(txt(L.$('#qSlot .qmgr')), /the manager approved 3 actions/);
  assert.match(txt(L.$('#talk .mgrdec')), /the manager approved 3 actions/);
});

/** A browser's Notification, recorded: the permission it has and asks for, and every notification made. */
function fakeNotification(perm, answer) {
  const made = [];
  class N { constructor(title, o) { this.title = title; this.o = o; this.closed = false; made.push(this); } close() { this.closed = true; } }
  N.permission = perm; N.asked = 0; N.requestPermission = () => { N.asked++; N.permission = answer; return Promise.resolve(answer); };
  return { N, made };
}

test('the notification of a waiting question is off by default; on, it asks the browser once and is kept in this browser', async () => {
  const { N } = fakeNotification('default', 'granted');
  const L = await boot({ globals: { Notification: N } });
  assert.equal(L.SL.settings.notify, 'off');
  L.SL.views.show('settings'); await L.step(0.1);
  L.$('.snb[data-page="look"]').click(); await L.settle();
  const on = L.$('[data-seg="notify"][data-v="on"]'); assert.ok(on, 'Appearance has the setting');
  assert.equal(L.$('[data-seg="notify"][data-v="off"]').getAttribute('aria-pressed'), 'true');
  on.click(); await L.settle();
  assert.equal(N.asked, 1, 'the browser is asked from the click');
  assert.equal(L.SL.settings.notify, 'on');
  assert.match(L.store.get('sleipnir.web.settings'), /"notify":"on"/);
  const D = fakeNotification('denied', 'denied'), M = await boot({ globals: { Notification: D.N } });
  await M.SL.ui.approvals.setNotify(true);
  assert.equal(M.SL.settings.notify, 'off', 'a browser that blocks them leaves it off');
  assert.match(M.$('#toasts').textContent, /blocks notifications/);
});

test('one notification per question that waits the set seconds while the page is hidden; a click on it opens the question', async () => {
  const { N, made } = fakeNotification('granted', 'granted');
  const L = await boot({ globals: { Notification: N }, storage: { 'sleipnir.web.settings': JSON.stringify({ notify: 'on', notifyAfter: '10' }) } });
  L.doc.hidden = true;
  L.SL.views.show('cache'); await L.step(0.1);
  await L.event('ask', { q: q('q_1') }); await L.step(5);
  assert.equal(made.length, 0, 'not before 10 s');
  await L.step(6);
  assert.equal(made.length, 1, 'after 10 s');
  assert.match(made[0].title, /needs you/); assert.match(made[0].o.body, /be-1 wants to run a command: go test/); assert.equal(made[0].o.tag, 'sleipnir-q_1');
  await L.step(20);
  assert.equal(made.length, 1, 'once per question');
  L.doc.hidden = false; made[0].onclick(); await L.step(0.1); L.advance(100); await L.settle();
  assert.equal(L.doc.activeElement, L.$('#qSlot .qstrip'), 'the question has the focus');
  await L.event('answer', { qid: 'q_1', choice: 1, by: 'you' }); await L.step(0.1);
  assert.equal(made[0].closed, true, 'an answered question\'s notification goes');
  const V = fakeNotification('granted', 'granted'), P = await boot({ globals: { Notification: V.N }, storage: { 'sleipnir.web.settings': JSON.stringify({ notify: 'on', notifyAfter: '10' }) } });
  await P.event('ask', { q: q('q_1') }); await P.step(30);
  assert.equal(V.made.length, 0, 'a page in sight is not notified');
});

test('the free slots of a team are named by their role, never by a leg number', async () => {
  const L = await boot();
  const S = L.SL.sessions.active; S.setMeta({ swarm: 8, team: [{ role: 'backend' }, { role: 'frontend' }, { role: 'tester' }, { role: 'docs' }] });
  L.SL.views.show('cockpit'); await L.step(0.1);
  const ghosts = Array.from(L.doc.querySelectorAll('.stall.ghost'));
  assert.ok(ghosts.length > 0);
  for (const g of ghosts) assert.doesNotMatch(txt(g) + ' ' + g.getAttribute('aria-label'), /\bleg\b/i);
  assert.match(txt(ghosts[0]), /^free ?tester slot/);
});

test('Start shows the new tab at once, and a line sent while it starts waits for it instead of failing', async () => {
  let starting = 2, snapOk = false;
  const L = await boot({ route: (m, p) => {
    if (m === 'POST' && p === '/api/sessions') return { _status: 201, tab: { id: 'c', sid: 's-c', name: 'shop', cwd: '/p/c', order: 2 } };
    if (p === '/api/sessions/c/snapshot') return snapOk ? undefined : { _status: 503, error: 'not yet', code: 'busy' };
    if (m === 'POST' && p === '/api/sessions/c/messages') return starting-- > 0 ? { _status: 409, error: 'the session is starting: try again in a moment', code: 'busy' } : { ok: true };
    if (p === '/api/sessions/c/ws/index') return { _status: 409, error: 'the session is starting: try again in a moment', code: 'busy' };
    return undefined;
  } });
  const r = L.SL.act.newSession({ cwd: '/p/c', swarm: 8 }); await r.done; await L.settle();
  assert.equal(L.SL.sessions.active.id, 'c', 'the new tab is active as soon as the server made it');
  const sent = L.SL.act.send('add pagination'); await L.settle();
  for (let i = 0; i < 4; i++) { L.advance(1100); await L.settle(); }
  const res = await sent.done;
  assert.equal(res.ok, true, 'the line got through once the tab had started');
  assert.equal(L.posts.filter(x => x.path === '/api/sessions/c/messages').length, 3, 'sent again while the tab was starting');
  assert.doesNotMatch(L.$('#toasts').textContent, /is starting/, 'nothing says "try again in a moment"');
});
