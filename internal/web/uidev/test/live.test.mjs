// live.test.mjs: the page's connection (live.js) with the registry (50-sessions.js) and the actions (60-actions.js), against a fake
// fetch and a fake EventSource: boot from snapshots, buffered frames, seq dedupe, the clock rule, meta, roster, tabs, reset, gap,
// ping, the connection states, a new boot, and what the actions send.
import test from 'node:test';
import assert from 'node:assert/strict';
import { context, load, fakeFetch, FakeES, tick, plain } from './harness.mjs';

const MODS = ['00-namespace.js', '00-util.js', 'api.js', '11-data-live.js', '20-clock.js', '30-model.js', '50-sessions.js', '60-actions.js', 'live.js'];
const ev = (seq, t, k, f) => Object.assign({ seq, t, k }, f || {});
function snapshot(id, seq, events, extra) {
  return Object.assign({ tab: { id, sid: 's-' + id, name: id, cwd: '/p/' + id, gen: 1, order: id === 'a' ? 0 : 1 }, gen: 1, seq, now: 10, meta: { model: 'm', mode: 'default', startedAt: 1760000000000 },
    roster: [{ id: 'mgr', role: 'manager', code: 'mgr', nth: 0, k: 0, leg: -1, scope: '-', ro: false, model: 'm', spawn: 0 }, { id: 'be-1', role: 'backend', code: 'be', nth: 1, k: 1, leg: 0, scope: 'api/**', ro: false, model: 'm', spawn: 1 }],
    keyframe: [ev(0, 1, 'use', { id: 'be-1', rd: 10, un: 5, out: 1 })], events, hist: ['earlier line'], questions: [] }, extra || {});
}
function setup(o) {
  o = o || {};
  const state = { boot: 'b1', snaps: { a: snapshot('a', 3, [ev(1, 2, 'say', { who: 'you', text: 'hi' }), ev(2, 3, 'state', { id: 'be-1', s: 'edit', doing: 'Edit x', task: 'T1' }), ev(3, 4, 'ask', { q: { id: 'q_1', agent: 'be-1', cmd: 'go test', why: 'w', what: 'this command' } })]) }, snapCalls: {}, posts: [], down: false };
  const fetch = fakeFetch((method, p, init) => {
    if (state.down) return 'network';
    if (p === '/api/hello') return { status: 200, body: { boot: state.boot, streamAfter: 7, server: { addr: '127.0.0.1:7777' }, tabs: Object.keys(state.snaps).map(id => state.snaps[id].tab), active: 'a' } };
    const m = /^\/api\/sessions\/([^/]+)\/snapshot$/.exec(p); if (m) { state.snapCalls[m[1]] = (state.snapCalls[m[1]] || 0) + 1; return state.snaps[m[1]] ? { status: 200, body: state.snaps[m[1]] } : { status: 404, body: { error: 'no', code: 'not_found' } }; }
    if (p === '/api/cli') return { status: 200, body: { commands: [], chatSlash: [{ cmd: '/goal', args: 'TEXT', desc: 'd', group: 'g' }] } };
    if (p === '/api/models') return { status: 200, body: { models: [{ ref: 'm', ctx: 1, in: 1, out: 2, cached: 0.1 }], favs: [], roles: [], roleOrder: [], roleModels: {} } };
    if (p === '/api/providers') return { status: 200, body: { providers: [] } };
    if (p === '/api/projects') return { status: 200, body: { projects: [{ dir: '/p/a', trust: 'trusted', default: true }] } };
    if (p === '/api/recorded') return { status: 200, body: { recorded: [{ id: '20261001-090000-0a7d55', first: 'x', resumable: true, ageS: 99999, mb: 1 }], mb: 1 } };
    if (method !== 'GET') { state.posts.push([method, p, init.body ? JSON.parse(init.body) : undefined, init.headers]); return o.post ? o.post(method, p, init) : { status: 200, body: { ok: true } }; }
    return { status: 404, body: { error: 'not found', code: 'not_found' } };
  });
  FakeES.all.length = 0;
  const ctx = load(context({ fetch }), MODS);
  const SL = ctx.SL, toasts = [];
  SL.ui = { toast: (t, k) => toasts.push([t, k]), approvals: { shown: {} } };
  SL.api.cfg.toast = (t, k) => toasts.push([t, k]);
  SL.api.cfg.reopenMs = 5;
  return { ctx, SL, fetch, state, toasts, es: () => FakeES.all[FakeES.all.length - 1] };
}

test('boot: hello, stream at streamAfter, snapshots, caches; keyframe events get seq 0; questions come from the events', async () => {
  const { SL, es, state } = setup();
  await SL.live.start();
  assert.equal(es().url, '/api/stream?after=7');
  const S = SL.sessions.get('a');
  assert.ok(S); assert.equal(S.lastSeq, 3); assert.equal(S.log[0].seq, 0); assert.equal(S.log.length, 4);
  assert.equal(S.wt, 10); assert.equal(S.wm.ag['be-1'].state, 'edit'); assert.equal(S.wm.ag['be-1'].rd, 10);
  assert.equal(SL.calc.openQuestion(S.wm).id, 'q_1'); assert.equal(SL.sessions.needs().length, 1);
  assert.deepEqual(plain(S.hist), ['earlier line']);
  assert.equal(SL.live.addr, '127.0.0.1:7777'); assert.equal(state.snapCalls.a, 1);
  assert.equal(SL.D.models[0].ref, 'm'); assert.equal(SL.D.spec.chatSlash.length, 1); assert.equal(SL.sessions.recorded.length, 1);
  assert.ok(S.meta.t0 > 0 && /^\d\d:\d\d:\d\d$/.test(S.meta.started));
});

test('frames that arrive during boot are applied after the snapshot, the older ones dropped', async () => {
  const { SL, es } = setup();
  const p = SL.live.start();
  await tick(0);
  es().emit('ev', { tab: 'a', ev: ev(2, 3, 'say', { who: 'you', text: 'old' }) }, 8);
  es().emit('ev', { tab: 'a', ev: ev(4, 12, 'say', { who: 'you', text: 'new' }) }, 9);
  await p;
  const S = SL.sessions.get('a');
  assert.equal(S.lastSeq, 4); assert.equal(S.log.filter(e => e.k === 'say' && e.text === 'old').length, 0); assert.equal(S.log.filter(e => e.text === 'new').length, 1);
  assert.equal(S.wt, 12, 'an event moves the world clock up to its time');
});

test('ev: duplicates by seq are dropped; meta merges and carries the queue; roster adds agents; ping moves the clock', async () => {
  const { SL, es } = setup();
  await SL.live.start(); SL.sessions.init();
  const S = SL.sessions.get('a'); let roster = 0; SL.bus.on('roster-changed', () => roster++);
  es().emit('ev', { tab: 'a', ev: ev(5, 11, 'answer', { qid: 'q_1', choice: 1, by: 'you' }) }, 10);
  es().emit('ev', { tab: 'a', ev: ev(5, 11, 'answer', { qid: 'q_1', choice: 1, by: 'you' }) }, 11);
  assert.equal(S.log.filter(e => e.k === 'answer').length, 1); assert.equal(SL.calc.openQuestion(S.wm), null);
  es().emit('meta', { tab: 'a', patch: { mode: 'plan', queued: [{ id: 'l1', text: 'later' }], running: true } }, 12);
  assert.equal(S.meta.mode, 'plan'); assert.deepEqual(plain(S.ui.queued), [{ id: 'l1', text: 'later' }]); assert.equal(S.state(), 'run');
  es().emit('roster', { tab: 'a', roster: S.roster.concat([{ id: 'be-2', role: 'backend', code: 'be', nth: 2, k: 2, leg: 1, scope: 'web/**', ro: false, model: 'm', spawn: 11 }]) }, 13);
  assert.ok(S.wm.ag['be-2']); assert.ok(S.m.ag['be-2']); assert.equal(roster, 1);
  es().emit('ping', { now: { a: 40 } }, 14); assert.equal(S.wt, 40);
  es().emit('ping', { now: { a: 20 } }, 15); assert.equal(S.wt, 40, 'a ping never moves the clock back');
});

test('tab frames add, rename and remove tabs; the last removal leaves the placeholder (D-12)', async () => {
  const { SL, es, state } = setup();
  await SL.live.start(); SL.sessions.init();
  state.snaps.b = snapshot('b', 1, [ev(1, 1, 'say', { who: 'you', text: 'b' })]);
  es().emit('tab', { op: 'add', tab: state.snaps.b.tab }, 20); await tick(5);
  assert.deepEqual(plain(SL.sessions.list.map(S => S.id)), ['a', 'b']);
  es().emit('tab', { op: 'update', tab: Object.assign({}, state.snaps.b.tab, { name: 'renamed' }) }, 21); assert.equal(SL.sessions.get('b').name, 'renamed');
  es().emit('tab', { op: 'remove', tab: { id: 'a' } }, 22); assert.equal(SL.sessions.active.id, 'b');
  es().emit('tab', { op: 'remove', tab: { id: 'b' } }, 23); assert.equal(SL.sessions.list.length, 0); assert.equal(SL.sessions.active.placeholder, true);
  assert.equal(SL.act.send('hello').ok, false, 'the placeholder takes no message');
});

test('reset and gap refetch snapshots; a reset rebuilds the session in place, keeping its UI state', async () => {
  const { SL, es, state } = setup();
  await SL.live.start(); SL.sessions.init();
  const S = SL.sessions.get('a'); S.ui.draft = 'keep me';
  state.snaps.a = snapshot('a', 1, [ev(1, 0.5, 'say', { who: 'sys', text: 'fresh' })], { gen: 2 });
  es().emit('reset', { tab: 'a', gen: 2 }, 30); await tick(5);
  assert.equal(SL.sessions.get('a'), S); assert.equal(S.ui.draft, 'keep me'); assert.equal(S.lastSeq, 1); assert.equal(S.log.filter(e => e.text === 'fresh').length, 1);
  assert.equal(SL.calc.openQuestion(S.wm), null);
  es().emit('gap', { reason: 'aged out', dropped: 9, last: 30 }, 31); await tick(5);
  assert.equal(state.snapCalls.a, 3);
});

test('a tab whose snapshot is in flight buffers its frames and applies the newer ones after it', async () => {
  const { SL, es, state } = setup();
  await SL.live.start(); SL.sessions.init();
  state.snaps.a = snapshot('a', 2, [ev(1, 1, 'say', { who: 'you', text: 'one' }), ev(2, 2, 'say', { who: 'you', text: 'two' })]);
  es().emit('reset', { tab: 'a', gen: 2 }, 40);
  es().emit('ev', { tab: 'a', ev: ev(2, 2, 'say', { who: 'you', text: 'two' }) }, 41);
  es().emit('ev', { tab: 'a', ev: ev(3, 13, 'say', { who: 'you', text: 'three' }) }, 42);
  await tick(5);
  const S = SL.sessions.get('a');
  assert.deepEqual(plain(S.log.filter(e => e.k === 'say').map(e => e.text)), ['one', 'two', 'three']); assert.equal(S.lastSeq, 3);
});

test('connection states: reconnecting, down on bye, a new boot reloads the page', async () => {
  const { ctx, SL, es, state } = setup();
  await SL.live.start(); SL.sessions.init(); es().open();
  assert.equal(SL.live.state, 'open');
  es().fail(false); assert.equal(SL.live.state, 'reconnecting');
  es().open(); assert.equal(SL.live.state, 'open');
  state.boot = 'b2'; es().fail(true); await tick(30);
  assert.equal(ctx.reloads.n, 1, 'another server run: the page reloads (its cookie is no longer valid)');
  const s2 = setup(); await s2.SL.live.start(); s2.es().open();
  s2.es().emit('bye', { reason: 'shutting down' }, 50); assert.equal(s2.SL.live.state, 'down');
});

test('actions: synchronous refusals keep the mock\'s whys; requests carry the header, the body and the confirm scope', async () => {
  const { SL, state } = setup({ post: (m, p) => p === '/api/confirm' ? { status: 200, body: { id: 'cid-1' } } : { status: 200, body: { ok: true, queued: false } } });
  await SL.live.start(); SL.sessions.init();
  assert.deepEqual(plain(SL.act.setMode('yolo')), { ok: false, why: 'yolo is set only by typing its name to confirm' });
  assert.deepEqual(plain(SL.act.setBudget('abc')), { ok: false, why: 'a budget is a number of dollars, or off' });
  assert.deepEqual(plain(SL.act.allowRule('  ')), { ok: false, why: 'a rule needs text, e.g. tests or Bash(go test:*)' });
  assert.deepEqual(plain(SL.act.setGoal('')), { ok: false, why: '/goal needs text' });
  assert.equal(SL.act.closeSession('a').why, 'the last session cannot be closed: start another first');
  await SL.act.setMode('yolo', { confirm: 'yolo' }).done;
  const mode = state.posts.find(x => x[1] === '/api/sessions/a/mode');
  assert.deepEqual(mode[2], { mode: 'yolo' }); assert.equal(mode[3]['X-Confirm'], 'cid-1'); assert.equal(mode[3]['X-Sleipnir-Web'], '1');
  assert.deepEqual(state.posts.find(x => x[1] === '/api/confirm')[2], { scope: 'mode:yolo:a' });
  await SL.act.answerQuestion('q_1', 2).done;
  assert.deepEqual(state.posts.find(x => x[1] === '/api/questions/q_1/answer')[2], { choice: 2 });
  await SL.act.send('[pasted text #1 +5 lines]', undefined, { text: 'a\nb\nc\nd\ne' }).done;
  const msg = state.posts.find(x => x[1] === '/api/sessions/a/messages')[2]; assert.equal(msg.text, 'a\nb\nc\nd\ne'); assert.equal(msg.display, '[pasted text #1 +5 lines]'); assert.match(msg.clientId, /^c\d+$/);
  await SL.act.restartTeam({ swarm: 4 }).done;
  assert.deepEqual(state.posts.find(x => x[1] === '/api/sessions/a/restart')[2], { kind: 'swarm', swarm: 4, fresh: false }, '/swarm carries the conversation (D-06)');
  await SL.act.newChat().done;
  assert.deepEqual(state.posts.filter(x => x[1] === '/api/sessions/a/restart')[1][2], { kind: 'new', fresh: true });
});

test('a too-early answer re-arms the quiet period and says why; an answer another page gave first is silent', async () => {
  let code = 'too_soon';
  const { SL, toasts } = setup({ post: () => ({ status: 409, body: { error: 'too soon', code, detail: { retryAfterMs: 200 } } }) });
  await SL.live.start(); SL.sessions.init(); SL.time.T.wall = 1234;
  await SL.act.answerQuestion('q_1', 1).done;
  assert.equal(SL.ui.approvals.shown.q_1, 1234); assert.deepEqual(toasts.pop(), ['the buttons wake up when the keyboard has been quiet for a moment', 'warm']);
  code = 'answered'; const n = toasts.length; await SL.act.answerQuestion('q_1', 1).done; assert.equal(toasts.length, n);
});

test('a recorded session opens read-only from its events (D-10)', async () => {
  const { SL } = setup();
  await SL.live.start(); SL.sessions.init();
  SL.views = { show() {} };
  const evs = [ev(1, 0, 'say', { who: 'you', text: 'old', at: 1 }), ev(2, 1, 'state', { id: 'be-1', s: 'tool', doing: 'Read x' }), ev(3, 2, 'tool', { id: 'te-2', name: 'Bash', arg: 'go test', out: 'ok', ok: true })];
  const get = SL.api.get; SL.api.get = (p, o) => /\/api\/recorded\/[^/]+\/events/.test(p) ? Promise.resolve({ ok: true, status: 200, data: { events: evs, next: null } }) : get(p, o);
  const r = await SL.act.openRecorded('20261001-090000-0a7d55').done;
  SL.api.get = get;
  assert.equal(r.ok, true);
  const S = SL.sessions.get('rec-20261001-090000-0a7d55');
  assert.ok(S.recorded && S.readOnly); assert.equal(SL.sessions.active, S);
  assert.deepEqual(plain(S.roster.map(x => x.id)), ['mgr', 'be-1', 'te-2']); assert.equal(S.log.length, 3);
  assert.equal(SL.act.send('x', S.id).ok, false); assert.equal(SL.act.setMode('plan', null, S.id).ok, false);
  assert.equal(SL.act.closeSession(S.id).ok, true); assert.equal(SL.sessions.get(S.id), undefined);
});

test('a watch tab (w-<sid>) is built from the recorded events, never from a snapshot; its frames continue it; closing stops the watch', async () => {
  const { SL, es, fetch, state } = setup();
  await SL.live.start(); SL.sessions.init();
  const sid = '20261001-090000-0a7d55', pages = { 0: { events: [ev(1, 0, 'say', { who: 'you', text: 'one' }), ev(2, 1, 'state', { id: 'be-1', s: 'tool', doing: 'x' })], next: '2' }, 2: { events: [ev(3, 2, 'tool', { id: 'te-2', name: 'Bash', arg: 'a', out: 'b', ok: true })], next: '' } };
  const get = SL.api.get; SL.api.get = (p, o) => { const m = /\/api\/recorded\/[^/]+\/events\?from=(\d+)/.exec(p); return m ? Promise.resolve({ ok: true, status: 200, data: pages[m[1]] }) : get(p, o); };
  es().emit('tab', { op: 'add', tab: { id: 'w-' + sid, sid, name: 'watching ' + sid, headless: true, order: 1000 } }, 60); await tick(10);
  const S = SL.sessions.get('w-' + sid);
  assert.ok(S && S.recorded && S.follow); assert.equal(S.log.length, 3); assert.equal(S.lastSeq, 3);
  assert.equal(fetch.calls.filter(c => /w-.*\/snapshot/.test(c.path)).length, 0, 'no snapshot is asked for a watch tab');
  es().emit('ev', { tab: 'w-' + sid, ev: ev(4, 3, 'state', { id: 'rv-9', s: 'think', doing: 'reads' }) }, 61);
  assert.ok(S.wm.ag['rv-9'], 'an agent the frames name joins the roster'); assert.equal(S.wm.ag['rv-9'].state, 'think');
  SL.api.get = get;
  await SL.act.closeSession(S.id).done;
  assert.ok(state.posts.some(x => x[0] === 'DELETE' && x[1] === '/api/recorded/' + sid + '/watch'));
});
