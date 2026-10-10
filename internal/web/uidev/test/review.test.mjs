// review.test.mjs: the page's data layer against what a long or unlucky connection does. A reset or a gap while a snapshot is in flight
// refetches it; a failed snapshot is retried while its frames wait; boot does not wait for the catalogues; a recorded session is read to
// its end (or says it was cut); an event that omits an omitempty field clears it (live and snapshot agree); a long stream keeps every
// list bounded; the New session dialog's command line is shell-safe; the `@` completion forgets what it learned.
import test from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { context, load, fakeFetch, FakeES, tick, plain } from './harness.mjs';

const MODS = ['00-namespace.js', '00-util.js', 'api.js', '11-data-live.js', '20-clock.js', '30-model.js', '50-sessions.js', '60-actions.js', 'live.js'];
const ev = (seq, t, k, f) => Object.assign({ seq, t, k }, f || {});
const ROSTER = [{ id: 'mgr', role: 'manager', code: 'mgr' }, { id: 'be-1', role: 'backend', code: 'be' }];
const snap = (id, gen, seq, events) => ({ tab: { id, sid: 's-' + id, name: id, cwd: '/p/' + id, gen, order: 0 }, gen, seq, now: 50, meta: { model: 'm', mode: 'default', startedAt: 1760000000000 }, roster: ROSTER, keyframe: [], events: events || [], hist: [], questions: [] });

/** A page against a fake server. routes(method, path) may answer first; snapshots come from state.snaps (a function of the call count). */
function page(o) {
  o = o || {};
  const state = { snapCalls: {}, posts: [], tabs: o.tabs || ['a'] };
  const fetch = fakeFetch(async (method, p, init) => {
    if (o.route) { const r = await o.route(method, p, init, state); if (r) return r; }
    if (p === '/api/hello') return { status: 200, body: { boot: 'b1', streamAfter: 1, server: { addr: '127.0.0.1:1' }, tabs: state.tabs.map((id, i) => ({ id, name: id, sid: 's-' + id, order: i })), active: state.tabs[0] } };
    const m = /^\/api\/sessions\/([^/]+)\/snapshot$/.exec(p);
    if (m) { const n = state.snapCalls[m[1]] = (state.snapCalls[m[1]] || 0) + 1; return o.snapshot ? o.snapshot(m[1], n, state) : { status: 200, body: snap(m[1], 1, 0) }; }
    if (method !== 'GET') { state.posts.push([method, p]); return { status: 200, body: {} }; }
    return { status: 200, body: {} };
  });
  FakeES.all.length = 0;
  const ctx = load(context({ fetch }), MODS), SL = ctx.SL, toasts = [];
  SL.ui = { toast: (t, k) => toasts.push([t, k]), approvals: { shown: {} } };
  SL.api.cfg.toast = (t, k) => toasts.push([t, k]); SL.api.cfg.reopenMs = 5;
  return { ctx, SL, state, toasts, es: () => FakeES.all[FakeES.all.length - 1] };
}
const until = async (f, ms = 3000) => { const t0 = Date.now(); while (!f()) { if (Date.now() - t0 > ms) throw new Error('timed out'); await tick(5); } };

test('a reset that arrives while the tab\'s snapshot is in flight refetches it once it lands: the tab ends on the new generation', async () => {
  let release; const gate = new Promise(r => { release = r; });
  const { SL, state, es } = page({ snapshot: async (id, n) => { if (n === 2) await gate; return { status: 200, body: n <= 2 ? snap(id, 1, 54, [ev(54, 5, 'say', { who: 'you', text: 'old' })]) : snap(id, 2, 3, [ev(3, 1, 'say', { who: 'you', text: 'new gen' })]) }; } });
  await SL.live.start(); assert.equal(SL.sessions.get('a').gen, 1);
  SL.live.loadTab('a');                      /* a refetch in flight (held at the gate) */
  await tick(5); es().emit('reset', { tab: 'a', gen: 2 }, 9); release(); await until(() => state.snapCalls.a >= 3);
  await until(() => SL.sessions.get('a').gen === 2);
  const S = SL.sessions.get('a'); assert.equal(S.lastSeq, 3);
  es().emit('ev', { tab: 'a', ev: ev(4, 2, 'say', { who: 'you', text: 'after reset' }) }, 10);
  assert.ok(S.log.some(e => e.text === 'after reset'), 'an event of the new generation (seq below the old lastSeq) is kept');
});

test('a snapshot that fails is retried while the tab says so and its frames wait; boot does not wait for the catalogues', async () => {
  const { SL, state, toasts, es } = page({ tabs: ['a', 'b'],
    route: (method, p) => p === '/api/models' ? 'hang' : null,
    snapshot: (id, n) => id === 'b' && n <= 2 ? { status: 500, body: { error: 'the server failed', code: 'internal' } } : { status: 200, body: snap(id, 1, 2, [ev(1, 1, 'say', { who: 'you', text: 'one' }), ev(2, 2, 'say', { who: 'you', text: 'two' })]) } });
  SL.live.RETRY.steps = [20, 20]; SL.live.RETRY.later = 20;
  const booted = await Promise.race([SL.live.start().then(() => true), new Promise(r => setTimeout(() => r(false), 1500))]);
  assert.equal(booted, true, 'boot ended although /api/models never answered');
  const B = SL.sessions.get('b'); assert.ok(B, 'the tab is there'); assert.ok(B.loading && B.loading.why === 'the server failed', 'and says it is not loaded');
  assert.ok(toasts.some(t => /could not load b: retrying/.test(t[0])), JSON.stringify(toasts));
  es().emit('ev', { tab: 'b', ev: ev(3, 3, 'say', { who: 'you', text: 'three' }) }, 9);
  assert.ok(!B.log.some(e => e.text === 'three'), 'a frame is never applied over a missing base');
  await until(() => state.snapCalls.b >= 3 && !SL.sessions.get('b').loading);
  const S = SL.sessions.get('b'); assert.deepEqual(plain(S.log.map(e => e.text)), ['one', 'two', 'three'], 'the snapshot, then the frame that waited');
});

test('a recorded session is read to its end; past the cap the tab says it shows the first N events', async () => {
  const N = 30000, page5 = (from, limit) => { const end = Math.min(N, from + limit), events = []; for (let i = from; i < end; i++) events.push({ t: i / 100, k: 'say', who: 'sys', text: 'e' + i, seq: i + 1 }); return { events, next: end < N ? String(end) : '' }; };
  const { SL } = page({ route: (method, p) => { const m = /^\/api\/recorded\/S1\/events\?from=(\d+)&limit=(\d+)$/.exec(p); return m ? { status: 200, body: page5(+m[1], +m[2]) } : null; } });
  await SL.live.start(); SL.views = { show() {} }; SL.act.switchSession = () => {};
  SL.live.maxEvents = 25000; await SL.live.openRecorded('S1');
  let S = SL.sessions.get('rec-S1'); assert.equal(S.log.length, 25000); assert.equal(S.truncated, 25000, 'the banner says it was cut');
  SL.sessions.drop('rec-S1'); SL.live.maxEvents = 0; await SL.live.openRecorded('S1');
  S = SL.sessions.get('rec-S1'); assert.equal(S.log.length, N, 'every event, more than the old 50,000-page limit would have allowed per 10 pages'); assert.equal(S.truncated, 0);
});

test('an event that omits an omitempty field clears it: the live model and the snapshot\'s agree (task, verdict, goal, use, gov, queue)', () => {
  const { SL } = page(), M = SL.model;
  const journal = [
    ev(1, 1, 'task', { id: 'T1', title: 'pages', owner: 'be-1', deps: ['T0'], scope: 'api/**', s: 'todo', closure: 'exhausted', failed: true, attempts: 2, blocked: true }),
    ev(2, 2, 'task', { id: 'T1', title: 'pages', scope: 'api/**', s: 'running', attempts: 3 }),
    ev(3, 3, 'verdict', { text: 'not yet', kind: 'blocked', left: ['tests'] }), ev(4, 4, 'verdict', { text: 'checking' }),
    ev(5, 5, 'goal', { s: 'active', objective: 'ship', turns: 2, max: 5, reason: 'r' }), ev(6, 6, 'goal', { s: 'paused', objective: 'ship', paused: 'you interrupted it' }),
    ev(7, 7, 'use', { id: 'be-1', rd: 5, un: 1, out: 1, wr: 0, cost: 0.1, saved: 0.01, savedPartial: true, unpriced: 40 }), ev(8, 8, 'use', { id: 'be-1', rd: 6, un: 1, out: 1, wr: 0, cost: 0.2, saved: 0.02 }),
    ev(9, 9, 'gov', { rpm: 4, r429: 1, retries: 0, inflight: 3, queued: 2 }), ev(10, 10, 'gov', { rpm: 4, r429: 1, retries: 0 }),
    ev(11, 11, 'queue', { head: 'T1', cmd: 'go test', step: 'verifying', conflicts: 1, bounced: 0 }), ev(12, 12, 'queue', { head: null, conflicts: 1, bounced: 0 }),
    ev(13, 13, 'warm', { ttl: 300 }), ev(14, 14, 'warm', {})
  ];
  /* the snapshot's keyframe holds the last event of each entity, as the server's mirror does */
  const last = {}; journal.forEach(e => { last[e.k + '|' + (e.id || '')] = e; });
  const S = { id: 'x', meta: { goalText: '' }, roster: ROSTER }, live = M.newModel(S, { noChat: true }), keyed = M.newModel(S, { noChat: true });
  journal.forEach(e => M.reduce(live, M.clean(JSON.parse(JSON.stringify(e)))));
  Object.values(last).sort((a, b) => a.seq - b.seq).forEach(e => M.reduce(keyed, M.clean(JSON.parse(JSON.stringify(e)))));
  const view = m => ({ task: m.tasks.T1, verdict: [m.verdict, m.verdictKind, m.left], goal: m.goal, use: [m.ag['be-1'].savedPartial, m.ag['be-1'].unpriced], gov: [m.inflight, m.queued], queue: [m.qHead, m.conflicts], ttl: m.ttl });
  assert.deepEqual(plain(view(live)), plain(view(keyed)));
  const T = live.tasks.T1; assert.equal(T.failed, false); assert.equal(T.closure, ''); assert.equal(T.owner, null); assert.deepEqual(plain(T.deps), []);
  assert.deepEqual(plain(live.left), []); assert.equal(live.verdictKind, ''); assert.equal(live.goal.turns, 0); assert.equal(live.goal.reason, ''); assert.equal(live.ag['be-1'].savedPartial, false); assert.equal(live.inflight, 0);
});

test('a long stream keeps every list bounded: 150,000 events', async () => {
  const { SL, state, es } = page();
  await SL.live.start(); const S0 = SL.sessions.get('a'); let seq = 0, t = 1;
  const send = e => { seq++; t += 0.01; SL.live.H.ev({ tab: 'a', ev: Object.assign({ seq, t }, e) }); };
  for (let i = 0; i < 25000; i++) {
    if (i % 500 === 0) await tick(0);
    send({ k: 'req', id: 'be-1', ratio: 0.9, p: 1000, o: 10 });
    send({ k: 'say', who: 'mgr', text: 'x', mid: 'm' + i }); send({ k: 'more', mid: 'm' + i, text: 'y', end: i % 2 === 0 });
    send({ k: 'ask', q: { id: 'q' + i, agent: 'be-1', cmd: 'ls', why: 'w', what: 'x' } }); send({ k: 'answer', qid: 'q' + i, choice: 1, by: 'you' });
    send(i % 2 ? { k: 'break', id: 'be-1', kind: 'k', read: 0, expected: 1, why: 'w' } : { k: 'refuse', id: 'be-1', name: 'Bash', arg: 'x', reason: 'r' });
  }
  await until(() => !SL.live.pending.a, 10000); const S = SL.sessions.get('a'), m = S.wm, A = m.ag['be-1']; assert.equal(S, S0);
  assert.ok(A.nreq > 0); assert.ok(A.ratios.length <= 400, 'ratios ' + A.ratios.length);
  assert.ok(Object.keys(m.mids).length <= 1000, 'mids ' + Object.keys(m.mids).length); assert.ok(m.qs.length <= 300, 'qs ' + m.qs.length);
  assert.ok(m.marks.length <= 800, 'marks ' + m.marks.length); assert.ok(m.anomalies.length <= 500, 'anomalies ' + m.anomalies.length);
  assert.ok(S.log.length <= SL.live.LOG_CAP + 1, 'log ' + S.log.length); assert.ok(state.snapCalls.a >= 2, 'the long log was replaced by a fresh snapshot');
  void es;
});

test('the New session dialog\'s command line is shell-safe: sh -c reads back every argument as typed', () => {
  const { ctx, SL } = page();
  SL.ui = SL.ui || {}; load(ctx, ['87-ui-sheets.js']);
  const q = SL.ui.shellQuote;
  for (const a of ['plain', 'go test ./...', 'Bash(go test:*)', 'go test -run "TestA|TestB" ./...', "it's", '/home/me/my project', '$HOME`id`;rm -rf /', 'a\nb', '', '--flag=x', 'tab\there']) {
    const out = execFileSync('sh', ['-c', 'printf "%s\\0" ' + q(a)], { encoding: 'utf8' });
    assert.equal(out, a + '\0', 'round trip of ' + JSON.stringify(a));
  }
  const line = SL.ui.dialogs.cliLine({ cwd: '/home/me/my project', model: 'ollama/m', mode: 'default', swarm: 2, isolation: 'worktree', verify: 'go test -run "TestA|TestB" ./...', rules: ['Bash(go test:*)'], roleModels: { tester: 'm x' }, budget: '5', trustProject: true, commit: false, mailman: false, mailmanDefault: false, noMcp: false, resume: '' });
  const argv = execFileSync('sh', ['-c', 'set -- ' + line.replace(/^sleipnir chat/, '') + '; for a; do printf "%s\\0" "$a"; done'], { encoding: 'utf8' }).split('\0').slice(0, -1);
  assert.deepEqual(argv, ['--cwd', '/home/me/my project', '--model', 'ollama/m', '--mode', 'default', '--swarm', '2', '--isolation', 'worktree', '--verify', 'go test -run "TestA|TestB" ./...', '--budget-usd', '5', '--role-model', 'tester=m x', '--allow', 'Bash(go test:*)', '--trust-project']);
});

test('the `@` completion asks again after 5 s and after the workspace changed', async () => {
  let n = 0; const { ctx, SL } = page({ route: (method, p) => /\/complete\?prefix=/.test(p) ? { status: 200, body: { paths: ['a' + (++n) + '.go'] } } : null });
  await SL.live.start(); SL.act.switchSession('a'); SL.views = { V: { reg: {}, order: [] }, register() {}, show() {} }; load(ctx, ['93-palette.js']);
  const now = { t: 1000 }; SL.palette.clock = () => now.t;
  assert.deepEqual(plain(SL.palette.files('a')), []); await tick(5);
  assert.deepEqual(plain(SL.palette.files('a')), ['a1.go']); assert.deepEqual(plain(SL.palette.files('a')), ['a1.go']); assert.equal(n, 1, 'within 5 s: the answer it has');
  now.t += 6000; assert.deepEqual(plain(SL.palette.files('a')), ['a1.go'], 'the old answer while it asks again'); await tick(5);
  assert.equal(n, 2); assert.deepEqual(plain(SL.palette.files('a')), ['a2.go'], 'after 5 s: the new answer');
  SL.bus.emit('ws-changed', SL.sessions.get('a')); SL.palette.files('a'); await tick(5); assert.equal(n, 3, 'after a change of the workspace: asked again');
  assert.deepEqual(plain(SL.palette.files('a')), ['a3.go']);
});

test('meta and roster frames carry a version: a snapshot older than a frame already applied keeps the frame; an older frame is dropped', async () => {
  const gates = []; let metaV = 4, rosterV = 1, mode = 'default';
  const { SL, es } = page({ tabs: ['a', 'b'], snapshot: async (id, n) => {
    if (id === 'b' && n === 1) await new Promise(r => gates.push(r));
    if (id === 'a' && n === 2) await new Promise(r => gates.push(r));
    return { status: 200, body: Object.assign(snap(id, 1, 0), { metaV, rosterV, meta: { model: 'm', mode, startedAt: 1760000000000 } }) };
  } });
  const booting = SL.live.start(); await until(() => gates.length === 1);
  /* b's snapshot is in flight at boot: a roster frame for it waits, then counts by its version */
  es().emit('roster', { tab: 'b', roster: ROSTER.concat([{ id: 'fe-1', role: 'frontend', code: 'fe' }]), v: 2 }, 9);
  gates.shift()(); await booting; await until(() => SL.sessions.get('b'));
  assert.deepEqual(plain(SL.sessions.get('b').roster.map(r => r.id)), ['mgr', 'be-1', 'fe-1'], 'the frame (v2) is newer than the snapshot (rosterV 1)');
  const A = SL.sessions.get('a'); assert.equal(A.meta.mode, 'default');
  SL.live.loadTab('a'); await until(() => gates.length === 1);              /* a's refetch in flight */
  es().emit('meta', { tab: 'a', patch: { mode: 'yolo' }, v: 5 }, 10); assert.equal(A.meta.mode, 'yolo');
  gates.shift()(); await until(() => !SL.live.pending.a);                   /* it lands with metaV 4 and the old mode */
  assert.equal(SL.sessions.get('a').meta.mode, 'yolo', 'a snapshot older than the frame does not take the frame back');
  es().emit('meta', { tab: 'a', patch: { mode: 'plan' }, v: 3 }, 11); assert.equal(A.meta.mode, 'yolo', 'an older frame is dropped');
  metaV = 7; mode = 'accept-edits'; await SL.live.loadTab('a'); assert.equal(A.meta.mode, 'accept-edits', 'a newer snapshot counts');
  es().emit('meta', { tab: 'a', patch: { mode: 'default' }, v: 7 }, 12); assert.equal(A.meta.mode, 'accept-edits', 'a frame already in the snapshot is dropped');
  es().emit('bye', { reason: '' }, 13); assert.equal(SL.live.state, 'down'); assert.equal(SL.live.stopped, true); assert.equal(SL.live.why, 'the server stopped');
});
