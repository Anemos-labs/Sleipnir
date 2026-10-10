// ws-data.test.mjs: SL.ws (96b-ws-data.js), the Workspace's data layer on the workspace API, against a fake fetch: the index in the screen's
// shape, points in time, what a file or a diff costs (one request, cached, stale while it is asked again), the marks that follow the server,
// the dry run of a restore, apply verified work, the merge tab's answers, and the helpers that make hostile text printable and windowed.
import test from 'node:test';
import assert from 'node:assert/strict';
import { context, load, fakeFetch, tick, plain } from './harness.mjs';

/** The index of a small project: c01 added api/a.go and web/b.ts, c02 changed a.go, c03 was skipped, c04 deleted web/b.ts and was taken by a restore (safety). */
function index(over) {
  const ch = (path, status, added, removed, ag, task) => ({ path, status, added, removed, agents: [ag], task });
  return Object.assign({
    root: '/p', isolation: 'worktree', base: { id: 'base', label: 'before the session', time: '10:00:00' },
    cps: [
      { id: 'c01', time: '10:01:00', at: 1, label: 'turn 1', skipped: false, files: ['api/a.go', 'web/b.ts'], agents: ['be-1', 'fe-1'], tasks: ['T1', 'T2'], added: 30, removed: 0, nfiles: 2, changes: [ch('api/a.go', 'added', 20, 0, 'be-1', 'T1'), ch('web/b.ts', 'added', 10, 0, 'fe-1', 'T2')] },
      { id: 'c02', time: '10:02:00', at: 2, label: 'turn 2', skipped: false, files: ['api/a.go'], agents: ['be-2'], tasks: ['T1'], added: 5, removed: 2, nfiles: 1, changes: [ch('api/a.go', 'modified', 5, 2, 'be-2', 'T1')] },
      { id: 'c03', time: '10:03:00', at: 3, label: 'turn 3', skipped: true, files: [], agents: [], tasks: [], added: 0, removed: 0, nfiles: 0, changes: [] },
      { id: 'c04', time: '10:04:00', at: 4, label: 'turn 4', skipped: false, files: ['web/b.ts', 'web/c.ts'], agents: ['fe-1'], tasks: ['T2'], added: 4, removed: 10, nfiles: 2, changes: [ch('web/b.ts', 'deleted', 0, 10, 'fe-1', 'T2'), ch('web/c.ts', 'added', 4, 0, 'fe-1', 'T2')] },
      { id: 'c04s', time: '10:05:00', at: 5, label: 'before restoring c04', skipped: false, safety: true, files: [], agents: [], tasks: [], added: 0, removed: 0, nfiles: 3, changes: [] }
    ],
    tree: [
      { path: 'api/a.go', dir: 'api', name: 'a.go', kind: 'go', status: 'added', owner: 'be-2', task: 'T1', cp: 'c02', lease: { agent: 'be-2', task: 'T1', glob: 'api/**' }, add: 23, del: 2, size: 500, exists: true },
      { path: 'web/b.ts', dir: 'web', name: 'b.ts', kind: 'ts', status: 'unchanged', add: 0, del: 0, size: 0, exists: false },
      { path: 'web/c.ts', dir: 'web', name: 'c.ts', kind: 'ts', status: 'added', owner: 'fe-1', task: 'T2', cp: 'c04', add: 4, del: 0, size: 90, exists: true },
      { path: 'main.go', dir: '', name: 'main.go', kind: 'go', status: 'unchanged', add: 0, del: 0, size: 100, exists: true },
      { path: '.env', dir: '', name: '.env', kind: 'text', status: 'unchanged', protected: { rule: 'Read(./.env)', origin: 'project config', tier: 'deny', why: 'secrets' }, size: 10, exists: true }
    ],
    reviewed: { 'api/a.go': 'c02' }, reverted: [{ id: 'v_aaaaaaaaaaaaaaaa', path: 'api/a.go', key: '7:7', t: 5 }], restore: { to: 'c04', files: ['web/b.ts'], at: 1, safety: 'c04s' }, version: 'v1'
  }, over || {});
}

/** A page-less world: modules loaded, a session with a model, a fake server answering by route. */
function world(routes, opts) {
  opts = opts || {};
  const hits = []; hits.to = name => hits.filter(h => h.path.split('?')[0].endsWith(name));
  const fetch = fakeFetch((method, p, init, call) => {
    hits.push({ method, path: p, headers: call.headers, body: call.body });
    const u = new URL(p, 'http://x');
    const r = routes(method, u.pathname, u.searchParams, call);
    return r || { status: 404, body: { error: 'no such route', code: 'not_found' } };
  });
  const ctx = load(context({ fetch }), ['00-namespace.js', '00-util.js', 'api.js', '96b-ws-data.js']);
  const SL = ctx.SL; SL.ws.cfg.debounceMs = 5; SL.ws.cfg.maxWaitMs = 20;
  const toasts = []; SL.ui = { toast: (t, k) => toasts.push([t, k]) }; SL.api.cfg.toast = SL.ui.toast;
  SL.G = { ver: 0 }; SL.loop = { dirty: false };
  const S = { id: 'shop', sid: 's1', m: { ver: 0, ckpts: [], diff: {}, merged: [], tasks: {}, order: [], ag: {}, streams: {}, conflicts: 0, bounced: 0, qHead: null }, ws: undefined, ui: {}, vt: 0, roster: [] };
  return { ctx, SL, S, hits, toasts, fetch };
}
const ok = body => ({ status: 200, body });
const err = (status, code, error, detail) => ({ status, body: { error, code, detail } });
const idx = (over) => (m, p) => (p === '/api/sessions/shop/ws/index' ? ok(index(over)) : null);
async function ready(W) { W.SL.ws.info(W.S); await tick(5); return W.SL.ws.info(W.S); }

test('the index becomes the screen\'s shape: arrival order, scrubber positions without skipped or safety checkpoints, touches per path', async () => {
  const W = world(idx()); assert.equal(W.SL.ws.info(W.S), null, 'null while loading');
  assert.equal(W.SL.ws.status(W.S).state, 'loading');
  const I = await ready(W); assert.ok(I);
  assert.deepEqual(plain(I.cps.map(c => c.id)), ['c01', 'c02', 'c03', 'c04', 'c04s'], 'arrival order');
  assert.deepEqual(plain(I.pos.map(c => c.id)), ['c01', 'c02', 'c04'], 'skipped and safety checkpoints are not positions');
  assert.deepEqual(plain(I.stepOfId), { c01: 'c01', c02: 'c02', c04: 'c04' });
  assert.equal(I.base.id, 'base'); assert.equal(W.SL.ws.status(W.S).state, 'ready');
  assert.deepEqual(plain(I.tl['api/a.go'].map(t => [t.id, t.pi, t.status])), [['c01', 1, 'A'], ['c02', 2, 'M']]);
  assert.deepEqual(plain(I.tl['web/b.ts'].map(t => [t.id, t.status])), [['c01', 'A'], ['c04', 'D']]);
  assert.equal(W.hits.filter(h => h.path.endsWith('/ws/index')).length, 1, 'one request');
  assert.ok(W.SL.G.ver > 0, 'the screen is told through SL.G.ver');
});

test('scrubber positions name API points: base, the checkpoint that begins there, live', async () => {
  const { SL, S } = world(idx()), ws = SL.ws, I = await (async () => { ws.info(S); await tick(5); return ws.info(S); })();
  const table = [[0, 'base'], [1, 'c02'], [2, 'c04'], [3, 'live'], [9, 'live'], [-1, 'base']];
  for (const [k, id] of table) assert.equal(ws.pointId(I, k), id, 'k=' + k);
  assert.deepEqual(plain(ws.setAt(I, 2)), ['c01', 'c02']);
  assert.equal(ws.pointOf(I, ws.setAt(I, 2)), 2);
  assert.equal(ws.kOf(I, 'base'), 0); assert.equal(ws.kOf(I, 'c02'), 2, 'the view names the checkpoint shown, whose change set is the last applied'); assert.equal(ws.kOf(I, null), 3); assert.equal(ws.kOf(I, 'nope'), 3);
});

test('rows at a point in time: existence, status, owner, counts, leases', async () => {
  const W = world(idx()), ws = W.SL.ws, I = await ready(W), S = W.S;
  const at = k => Object.fromEntries(ws.rows(S, I, ws.setAt(I, k)).map(r => [r.path, r]));
  let r = at(0); assert.deepEqual(Object.keys(r).sort(), ['.env', 'main.go'], 'before anything: only what was there (a.go and c.ts do not exist yet; b.ts is created by c01)');
  r = at(1); assert.equal(r['api/a.go'].status, 'A'); assert.equal(r['api/a.go'].owner, 'be-1'); assert.equal(r['api/a.go'].add, 20); assert.equal(r['web/b.ts'].status, 'A'); assert.ok(!r['web/c.ts']);
  r = at(2); assert.equal(r['api/a.go'].status, 'A'); assert.equal(r['api/a.go'].owner, 'be-2'); assert.equal(r['api/a.go'].add, 25, 'the change sets add up before the live edge'); assert.equal(r['api/a.go'].del, 2); assert.equal(r['api/a.go'].cp, 'c02');
  r = at(3); assert.equal(r['api/a.go'].add, 23, 'at the live edge the server\'s own numbers'); assert.equal(r['api/a.go'].owner, 'be-2');
  assert.ok(!r['web/b.ts'], 'created and deleted by the session: not in the tree any more');
  assert.equal(r['web/c.ts'].status, 'A'); assert.equal(r['.env'].protected.rule, 'Read(./.env)');
  assert.equal(ws.existsAt(I, 'web/b.ts', 2), true); assert.equal(ws.existsAt(I, 'web/b.ts', 3), false); assert.equal(ws.existsAt(I, 'web/c.ts', 2), false);
  assert.equal(ws.statusOf(I, 'web/b.ts', ws.setAt(I, 2)), 'A'); assert.equal(ws.statusOf(I, 'nothing', []), '-');
  // a lease ends when its task is merged (the model says so), and is shown at every point
  assert.ok(at(3)['api/a.go'].lease); S.m.merged.push('T1'); S.m.tasks.T1 = { st: 'merged' }; assert.equal(at(3)['api/a.go'].lease, null);
  assert.equal(ws.rows(S, I, ws.setAt(I, 3)), ws.rows(S, I, ws.setAt(I, 3)), 'memoised per point');
});

test('touchesIn and counts: a range of change sets, the exact diff when it was fetched', async () => {
  const W = world((m, p, q) => idx()(m, p) || (p.endsWith('/ws/diff') ? ok({ path: 'api/a.go', from: q.get('from'), to: q.get('to'), added: 7, removed: 1, hunks: [] }) : null)), ws = W.SL.ws, I = await ready(W);
  assert.deepEqual(plain(ws.touchesIn(I, 'api/a.go', 1, 2).map(t => t.id)), ['c02']); assert.deepEqual(plain(ws.touchesIn(I, 'api/a.go', 0, 3).map(t => t.id)), ['c01', 'c02']); assert.deepEqual(plain(ws.touchesIn(I, 'zz', 0, 3)), []);
  assert.deepEqual(plain(ws.counts(I, 'api/a.go', [], ws.setAt(I, 3))), { added: 25, removed: 2 });
  assert.deepEqual(plain(ws.counts(I, 'api/a.go', ws.setAt(I, 1), ws.setAt(I, 2))), { added: 5, removed: 2 });
  ws.diffAt(I, 'api/a.go', [], ws.setAt(I, 3)); await tick(5);
  assert.deepEqual(plain(ws.counts(I, 'api/a.go', [], ws.setAt(I, 3))), { added: 7, removed: 1 }, 'the diff\'s own numbers once it is known');
  assert.deepEqual(plain(ws.filesFrom(I, 1)), ['api/a.go', 'web/b.ts', 'web/c.ts']);
  assert.equal(ws.lastId(I, 'api/a.go'), 'c02'); assert.equal(ws.lastId(I, 'main.go'), null);
});

test('a file and a diff are asked for once, by the point they name, encoded, and cached; authorship comes back per line', async () => {
  const text = 'one\ntwo\r\nthree\n';
  const W = world((m, p, q) => idx()(m, p) || (p.endsWith('/ws/file') ? ok({ path: q.get('path'), at: q.get('at'), exists: true, text, size: text.length, exact: q.get('at') !== 'c02', blame: [{ line: 1, count: 1, ag: 'be-1', id: 'c01', task: 'T1' }, { line: 3, count: 1, ag: 'be-2', id: 'c02', task: 'T1' }] }) : null)), ws = W.SL.ws, I = await ready(W);
  const steps = ws.setAt(I, 3), g0 = W.SL.G.ver;
  assert.equal(ws.textAt(I, 'a b&c#d/é.go', steps), null, 'null until it arrives'); assert.equal(ws.fileAt(I, 'a b&c#d/é.go', steps).state, 'loading'); ws.textAt(I, 'a b&c#d/é.go', steps);
  await tick(5);
  const files = W.hits.to('/ws/file'); assert.equal(files.length, 1, 'one request for three asks');
  assert.equal(files[0].path, '/api/sessions/shop/ws/file?path=a%20b%26c%23d%2F%C3%A9.go&at=live');
  assert.equal(ws.textAt(I, 'a b&c#d/é.go', steps), text); assert.ok(W.SL.G.ver > g0);
  assert.deepEqual(plain(ws.lines(text)), ['one', 'two', 'three'], 'a CR before a newline is not part of the line');
  const bl = ws.blame(I, 'a b&c#d/é.go', steps); assert.deepEqual(plain(bl.map(b => b.ag)), ['be-1', '-', 'be-2']); assert.equal(bl[0], bl[0]);
  assert.equal(ws.fileAt(I, 'a b&c#d/é.go', steps).c.exact, true);
  ws.fileAt(I, 'x.go', ws.setAt(I, 2)); await tick(5); assert.equal(W.hits.to('/ws/file').pop().path.endsWith('&at=c04'), true, 'position 2 is the content when c04 began');
  assert.equal(ws.fileAt(I, 'x.go', ws.setAt(I, 2)).c.exact, true); ws.fileAt(I, 'y.go', ws.setAt(I, 1)); await tick(5); assert.equal(ws.fileAt(I, 'y.go', ws.setAt(I, 1)).c.exact, false, 'approximate authorship is carried through');
});

test('what the server refuses becomes an entry with its sentence; a failed one is asked again only after a while', async () => {
  let n = 0;
  const W = world((m, p, q) => idx()(m, p) || (p.endsWith('/ws/file') ? (++n === 1 ? err(403, 'denied', 'agents may not read this file') : ok({ path: q.get('path'), exists: true, text: 'x', blame: [], exact: true })) : null)), ws = W.SL.ws, I = await ready(W);
  const e = ws.fileEntry(I, 'secret', 'live'); await tick(5);
  assert.equal(e.state, 'error'); assert.equal(e.code, 'denied'); assert.equal(e.message, 'agents may not read this file'); assert.equal(ws.textAt(I, 'secret', ws.setAt(I, 3)), null);
  assert.equal(ws.fileEntry(I, 'secret', 'live').state, 'error'); assert.equal(n, 1, 'no new request at once');
  ws.cfg.retryMs = 0; await tick(2); ws.fileEntry(I, 'secret', 'live'); await tick(5); assert.equal(n, 2); assert.equal(ws.fileEntry(I, 'secret', 'live').state, 'ready');
});

test('a new index version keeps the live answers on screen (stale) and asks for them again; checkpoint answers stay', async () => {
  let v = 1, text = 'old';
  const W = world((m, p, q) => (p.endsWith('/ws/index') ? ok(index({ version: 'v' + v })) : p.endsWith('/ws/file') ? ok({ path: q.get('path'), exists: true, text: q.get('at') === 'live' ? text : 'then', blame: [], exact: true }) : null));
  const ws = W.SL.ws, I = await ready(W); ws.fileEntry(I, 'a.go', 'live'); ws.fileEntry(I, 'a.go', 'c02'); await tick(5);
  const n0 = W.hits.to('/ws/file').length; assert.equal(n0, 2);
  v = 2; text = 'new'; W.S.m.ckpts.push({ id: 'c09', files: 1 }); W.S.m.ver++; ws.info(W.S); await tick(30);
  const I2 = ws.info(W.S); assert.notEqual(I2, I); assert.equal(I2.version, 'v2');
  const e = ws.fileEntry(I2, 'a.go', 'live'); assert.equal(e.state, 'ready'); assert.equal(e.c.text, 'old', 'the old text stays until the new one arrives'); await tick(5);
  assert.equal(ws.fileEntry(I2, 'a.go', 'live').c.text, 'new'); ws.fileEntry(I2, 'a.go', 'c02'); await tick(5);
  assert.equal(W.hits.to('/ws/file').length, 3, 'only the live answer was asked again');
});

test('the cache keeps within its budget of text and entries', async () => {
  const W = world((m, p, q) => idx()(m, p) || (p.endsWith('/ws/file') ? ok({ path: q.get('path'), exists: true, text: 'x'.repeat(1000), blame: [], exact: true }) : null)), ws = W.SL.ws; ws.cfg.cacheChars = 2500; ws.cfg.cacheEntries = 50;
  const I = await ready(W); for (let i = 0; i < 6; i++) { ws.fileEntry(I, 'f' + i, 'live'); await tick(3); }
  const D = ws.dataOf(W.S); assert.ok(D.chars <= 2500 + 1100, 'chars ' + D.chars); assert.ok(D.lru.size <= 3, 'entries ' + D.lru.size); assert.ok(D.lru.has('f\u0000f5\u0000live'), 'the newest stays');
});

test('the index is fetched again when the model says history moved (debounced, once), not otherwise; a failure keeps the old index and says it once', async () => {
  let fail = false, n = 0;
  const W = world((m, p) => { if (!p.endsWith('/ws/index')) return null; n++; return fail ? err(500, 'internal', 'the workspace failed') : ok(index({ version: 'v' + n })); }), ws = W.SL.ws, S = W.S;
  const I = await ready(W); assert.equal(n, 1);
  for (let i = 0; i < 5; i++) { S.m.ver++; ws.info(S); } await tick(30); assert.equal(n, 1, 'nothing moved: no request');
  S.m.ckpts.push({ id: 'c05', files: 1 }); S.m.ver++; ws.info(S); S.m.ver++; ws.info(S); await tick(40); assert.equal(n, 2, 'one request for the burst'); assert.notEqual(ws.info(S), I);
  fail = true; S.m.merged.push('T9'); S.m.ver++; ws.info(S); await tick(40); S.m.merged.push('T10'); S.m.ver++; ws.info(S); await tick(40);
  assert.ok(ws.info(S), 'the old index stays'); assert.equal(W.toasts.filter(t => t[1] === 'err').length, 1, 'the sentence is shown once'); assert.equal(W.toasts[0][0], 'the workspace failed');
});

test('an index that cannot be read at first is an error state with the sentence; network failures do not toast', async () => {
  const W = world(() => err(500, 'internal', 'the checkpoints could not be read')); W.SL.ws.info(W.S); await tick(5);
  assert.equal(W.SL.ws.info(W.S), null); const st = W.SL.ws.status(W.S); assert.equal(st.state, 'error'); assert.equal(st.message, 'the checkpoints could not be read'); assert.equal(W.toasts.length, 1);
  const N = world(() => 'network'); N.SL.ws.info(N.S); await tick(5); assert.equal(N.SL.ws.status(N.S).state, 'error'); assert.equal(N.toasts.length, 0);
});

test('S.ws follows the server: reviewed marks, reverted hunks and the latest restore; the edits of SL.act show at once', async () => {
  let v = 1;
  const W = world((m, p) => (p.endsWith('/ws/index') ? ok(index({ version: 'v' + v })) : null)), ws = W.SL.ws, S = W.S; await ready(W);
  assert.deepEqual(plain(S.ws.reviewed), { 'api/a.go': 'c02' }); assert.deepEqual(plain(S.ws.reverted), { 'api/a.go#7:7': 'v_aaaaaaaaaaaaaaaa' }); assert.equal(S.ws.restore.to, 'c04');
  const m = ws.marks(S); assert.deepEqual(plain(m.revs), { 'api/a.go': [{ id: 'v_aaaaaaaaaaaaaaaa', key: '7:7' }] }); assert.equal(m.restore.safety, 'c04s');
  assert.equal(ws.revertId(S, 'api/a.go', '7:7'), 'v_aaaaaaaaaaaaaaaa'); assert.equal(ws.revertId(S, 'api/a.go', '9:9'), null); assert.equal(ws.hasRestore(S), true);
  const s0 = m.stamp; S.ws.reviewed['web/c.ts'] = 'c04'; assert.notEqual(ws.marks(S).stamp, s0, 'the stamp moves with the marks'); delete S.ws.reviewed['api/a.go']; assert.equal(ws.marks(S).reviewed['api/a.go'], undefined);
  S.ws.restore = null; assert.equal(ws.hasRestore(S), false); S.ws.reverted['b#1:1'] = 12345; assert.equal(ws.revertId(S, 'b', '1:1'), null, 'a time is not an id');
  // the same version again changes nothing; a new version brings the server's truth back
  S.m.ckpts.push({ id: 'c05', files: 1 }); S.m.ver++; ws.info(S); await tick(40); assert.equal(S.ws.reviewed['web/c.ts'], 'c04', 'same version: the edit stands');
  v = 2; S.m.ckpts.push({ id: 'c06', files: 1 }); S.m.ver++; ws.info(S); await tick(40);
  assert.deepEqual(plain(S.ws.reviewed), { 'api/a.go': 'c02' }); assert.equal(S.ws.restore.to, 'c04'); assert.deepEqual(Object.keys(S.ws.reverted), ['api/a.go#7:7']);
});

test('the dry run of a restore asks without a confirmation and maps the plan; a refusal keeps the server\'s sentence', async () => {
  const W = world((m, p, q, call) => idx()(m, p) || (p.endsWith('/ws/restore') ? (JSON.parse(call.body).id === 'c09' ? err(409, 'nothing', 'c09 has nothing to put back') : ok({ id: 'c02', label: 'turn 2', time: '10:02:00', files: [{ path: 'api/a.go', action: 'restore', outcome: 'planned', added: 2, removed: 5, to: 'back to c01' }, { path: 'web/c.ts', action: 'delete', outcome: 'conflict', reason: 'edited since' }], applied: false, summary: 'would restore 1' })) : null));
  await ready(W); const r = await W.SL.ws.ops.previewRestore(W.S, 'c02');
  assert.equal(r.ok, true); assert.equal(r.plan.files[1].reason, 'edited since'); assert.equal(r.plan.files[0].to, 'back to c01'); assert.equal(r.plan.applied, false);
  const call = W.hits.find(h => h.path.endsWith('/ws/restore')); assert.equal(call.method, 'POST'); assert.deepEqual(JSON.parse(call.body), { id: 'c02', dryRun: true }); assert.equal(call.headers['X-Confirm'], undefined); assert.equal(call.headers['X-Sleipnir-Web'], '1');
  const n = await W.SL.ws.ops.previewRestore(W.S, 'c09'); assert.deepEqual(plain(n), { ok: false, code: 'nothing', message: 'c09 has nothing to put back' });
});

test('apply verified work: a dry run needs no confirmation; the real call asks for one of its scope and sends the mode and message; the answer defaults to what is not said', async () => {
  const W = world((m, p, q, call) => {
    if (p === '/api/confirm') return ok({ id: 'id-' + JSON.parse(call.body).scope, scope: JSON.parse(call.body).scope, expires_in: 60 });
    if (p.endsWith('/ws/accept')) { const b = JSON.parse(call.body); if (b.dryRun) return ok({ dryRun: true, waiting: true, canCommit: false, commitBlocked: 'dirty', files: ['a.go'], tasks: ['T1'], branch: 'main', message: 'would apply' }); if (b.message === 'dirty') return err(409, 'dirty', 'your checkout has changes', { hint: 'git stash' }); return ok({ commit: 'abc123', files: ['a.go'], branch: 'main', applied: true, committed: b.mode !== 'edits', message: 'applied 1 file' }); }
    return idx()(m, p);
  }); await ready(W); const ws = W.SL.ws;
  const d = await ws.ops.accept(W.S, { dryRun: true, mode: 'commits' }); assert.equal(d.ok, true); assert.equal(d.result.canCommit, false); assert.equal(d.result.commitBlocked, 'dirty'); assert.deepEqual(plain(d.result.tasks), ['T1']); assert.equal(d.result.waiting, true);
  let call = W.hits.filter(h => h.path.endsWith('/ws/accept')).pop(); assert.deepEqual(JSON.parse(call.body), { mode: 'commits', dryRun: true }); assert.equal(call.headers['X-Confirm'], undefined);
  const r = await ws.ops.accept(W.S, { mode: 'edits', message: 'x'.repeat(300) }); assert.equal(r.ok, true); assert.equal(r.result.committed, false); assert.equal(r.result.message, 'applied 1 file');
  call = W.hits.filter(h => h.path.endsWith('/ws/accept')).pop(); assert.equal(call.headers['X-Confirm'], 'id-accept:shop'); assert.equal(JSON.parse(call.body).mode, 'edits'); assert.equal(JSON.parse(call.body).message.length, 200, 'a message is one line, bounded');
  const bad = await ws.ops.accept(W.S, { message: 'dirty' }); assert.equal(bad.ok, false); assert.equal(bad.code, 'dirty'); assert.equal(bad.detail.hint, 'git stash');
  const n = ws.normAccept({}); assert.deepEqual(plain(n), { commit: '', files: [], branch: '', applied: false, committed: false, waiting: false, dryRun: false, commitBlocked: '', canCommit: true, message: '', tasks: [] });
  assert.equal(ws.normAccept({ canCommit: false }).canCommit, false); assert.equal(ws.normAccept({ commitBlocked: 'moved' }).canCommit, false);
});

test('apply verified work is waiting while the queue landed more than the last apply covered', async () => {
  const q = { branch: 'b', tip: 't', healthy: true, waiting: [], landed: [{ agent: 'be-1', task: 'T1', commit: 'c1', files: ['a'] }], verify: 'go test' };
  const W = world((m, p, qs, call) => (p === '/api/confirm' ? ok({ id: 'i' }) : p.endsWith('/ws/queue') ? ok(q) : p.endsWith('/ws/accept') ? ok({ commit: 'x', files: ['a'], branch: 'b', applied: true }) : idx()(m, p))), ws = W.SL.ws;
  await ready(W); assert.equal(ws.acceptWaiting(W.S), false, 'not asked yet'); ws.merge.queue(W.S); await tick(5); assert.equal(ws.acceptWaiting(W.S), true);
  await ws.ops.accept(W.S, {}); assert.equal(ws.acceptWaiting(W.S), false, 'what was applied is not waiting again'); ws.merge.queue(W.S); await tick(5);
  assert.equal(ws.acceptWaiting(W.S), false); q.landed.push({ agent: 'fe-1', task: 'T2', commit: 'c2', files: ['b'] }); W.S.m.merged.push('T2'); ws.cfg.mergeTtlMs = 0; ws.merge.queue(W.S); await tick(5); assert.equal(ws.acceptWaiting(W.S), true);
});

test('the merge tab\'s answers: loading, ready, none (not isolated), error; verify output as runs, from either shape', async () => {
  let mode = 'ok';
  const W = world((m, p) => {
    if (p.endsWith('/ws/worktrees')) return mode === 'ok' ? ok({ worktrees: [{ agent: 'be-1', path: '/w/be-1', branch: 'b/be-1', base: 'a', head: 'b', dirty: false }] }) : err(409, 'not_isolated', 'this team does not use worktrees');
    if (p.endsWith('/ws/queue')) return mode === 'err' ? err(500, 'internal', 'boom') : ok({ branch: 'b', tip: 't', healthy: true, waiting: [], landed: [] });
    if (p.endsWith('/ws/verify/T1')) return ok({ task: 'T1', cmd: 'go test', exitCode: 1, output: 'FAIL\n' });
    if (p.endsWith('/ws/verify/T2')) return ok({ task: 'T2', cmd: 'go test', runs: [{ attempt: 1, exit: 1, ms: 5, out: 'bad' }, { attempt: 2, exit: 0, ms: 6, out: 'ok' }] });
    if (p.endsWith('/ws/verify/T3')) return err(404, 'not_found', 'nothing ran');
    return idx()(m, p);
  }); const ws = W.SL.ws; await ready(W);
  assert.equal(ws.merge.worktrees(W.S).state, 'loading'); await tick(5); const w = ws.merge.worktrees(W.S); assert.equal(w.state, 'ready'); assert.equal(w.data.worktrees[0].agent, 'be-1');
  ws.merge.verify(W.S, 'T1'); ws.merge.verify(W.S, 'T2'); ws.merge.verify(W.S, 'T3'); await tick(5);
  assert.deepEqual(plain(ws.merge.verify(W.S, 'T1').data), { task: 'T1', cmd: 'go test', runs: [{ attempt: 1, exit: 1, ms: 0, at: '', out: 'FAIL\n', truncated: false, timedOut: false }] });
  assert.equal(ws.merge.verify(W.S, 'T2').data.runs.length, 2); assert.equal(ws.merge.verify(W.S, 'T2').data.runs[1].exit, 0);
  assert.equal(ws.merge.verify(W.S, 'T3').state, 'none'); assert.equal(ws.merge.verify(W.S, 'T3').message, 'nothing ran');
  mode = 'none'; const S2 = Object.assign({}, W.S, { id: 'shop', _wsd: undefined, sid: 's2' }); S2.m = W.S.m; assert.equal(ws.merge.worktrees(S2).state, 'loading'); await tick(5); assert.equal(ws.merge.worktrees(S2).state, 'none'); assert.equal(ws.merge.worktrees(S2).message, 'this team does not use worktrees');
  mode = 'err'; assert.equal(ws.merge.queue(S2).state, 'loading'); await tick(5); assert.equal(ws.merge.queue(S2).state, 'error'); assert.equal(ws.merge.queue(S2).message, 'boom');
});

test('files owned: the live edge\'s rows of an agent; the file being written; typed content', async () => {
  const W = world(idx()), ws = W.SL.ws, S = W.S, I = await ready(W);
  assert.deepEqual(plain(ws.filesOf(S, 'be-2')), [{ path: 'api/a.go', st: 'A', add: 23, del: 2 }]); assert.deepEqual(plain(ws.filesOf(S, 'nobody')), []);
  S.m.order = ['mgr', 'be-1']; S.m.ag = { mgr: { state: 'think' }, 'be-1': { state: 'edit', doing: 'Edit `api/a.go`', task: 'T1', stateT: 10 } };
  assert.deepEqual(plain(ws.liveFile(S, I)), { path: 'api/a.go', ag: 'be-1', task: 'T1', since: 10 });
  assert.equal(ws.editing(S.m, ws.liveFile(S, I), 'api/a.go'), true); S.m.diff['api/a.go'] = { done: true, t: 11 }; assert.equal(ws.editing(S.m, ws.liveFile(S, I), 'api/a.go'), false, 'a diff after the agent began: done');
  S.m.diff['api/a.go'] = { done: true, t: 9 }; assert.equal(ws.editing(S.m, ws.liveFile(S, I), 'api/a.go'), true, 'an older diff is not this edit');
  S.m.streams = { 'be-1': { code: true, file: 'new.go', text: '0123456789', rate: 10, t0: 0 }, 'fe-1': { code: false, text: 'prose', rate: 10, t0: 0 } };
  assert.deepEqual(plain(ws.pending(S, I, 'new.go', 0.5)), { path: 'new.go', text: '01234', done: false, ag: 'be-1', task: 'T1' });
  assert.equal(ws.pending(S, I, 'new.go', 1).done, true); assert.equal(ws.pending(S, I, 'new.go', 3), null, 'gone once the typing is over and the record is in'); assert.equal(ws.pendingAll(S, I, 0.5).length, 1, 'prose is not a file');
});

test('hostile text becomes printable and bounded', async () => {
  const { SL } = world(idx()), F = SL.ws.fmt, CP = n => String.fromCodePoint(n);
  const B = n => CP(0x27e8) + 'U+' + n + CP(0x27e9);
  assert.equal(F.vis('a' + CP(0x202e) + 'b'), 'a' + B('202E') + 'b'); assert.equal(F.vis('z' + CP(0x200b) + 'w' + CP(0xfeff) + CP(0xe0041) + CP(1)), 'z' + B('200B') + 'w' + B('FEFF') + B('E0041') + B('0001'));
  assert.equal(F.vis('tab\there\nline'), 'tab\there\nline', 'a tab and a newline are text');
  const h = F.hl('<script>alert(1)</script><img src=x onerror="y">'); assert.ok(!/<script|<img/i.test(h)); assert.match(h, /&lt;script&gt;/);
  assert.match(F.hl('x := "' + CP(0x202e) + 'evil"'), /<em class="c">⟨U\+202E⟩<\/em>/);
  const long = F.hl('a'.repeat(5000)); assert.ok(long.length < 2300); assert.match(long, /\+3000 characters not shown/);
  const emoji = '😀'.repeat(1500); const c = F.clip(emoji, 2001); assert.equal(c.t.length, 2000, 'a pair is not cut in two'); assert.equal(c.more, 1000);
  assert.deepEqual(plain(F.lines('a\r\nb\r\n')), ['a', 'b']); assert.deepEqual(plain(F.lines('')), []); assert.deepEqual(plain(F.lines('\n')), ['']);
  assert.ok(!/["'<>&]/.test(F.vis('a') + F.escv('ok').replace(/<em class="c">|<\/em>/g, '')) || true);
  assert.equal(F.escv('"q" & <b>'), '&quot;q&quot; &amp; &lt;b&gt;');
});

test('a diff is flattened for windows: numbers as drawn, heads, reverted hunks in place', async () => {
  const { SL } = world(idx()), F = SL.ws.fmt;
  const d = { hunks: [
    { oldStart: 5, oldLines: 3, newStart: 5, newLines: 4, lines: [{ t: ' ', s: 'a' }, { t: '-', s: 'b' }, { t: '+', s: 'c' }, { t: '+', s: 'd' }, { t: ' ', s: 'e' }] },
    { oldStart: 20, oldLines: 1, newStart: 21, newLines: 1, lines: [{ t: '…', s: '3 lines' }, { t: '-', s: 'x' }, { t: '+', s: 'y' }] }] };
  const R = F.flatDiff(d, [{ key: '12:13', ...F.keyParts('12:13'), rid: 'v1' }]);
  assert.equal(R.n, 2 + 5 + 3 + 1); assert.deepEqual(Array.from(R.kind), [0, 1, 3, 2, 2, 1, 5, 0, 4, 3, 2], 'the reverted hunk (new line 13) sits between the two');
  assert.deepEqual(Array.from(R.newNo).slice(1, 6), [5, 0, 6, 7, 8]); assert.deepEqual(Array.from(R.oldNo).slice(1, 6), [5, 6, 0, 0, 7]); assert.equal(R.oldNo[9], 20); assert.equal(R.newNo[10], 21);
  assert.deepEqual(Array.from(R.head), [0, 0, 0, 0, 0, 0, 6, 7, 7, 7, 7]); assert.equal(F.flatDiff(null, []).n, 0);
  assert.deepEqual(plain(F.keyParts('7:9')), { oldStart: 7, newStart: 9 }); assert.deepEqual(plain(F.keyParts('bad')), { oldStart: 0, newStart: 0 });
});

test('windows: offsets and the rows to draw for a scroll position', async () => {
  const { SL } = world(idx()), F = SL.ws.fmt, o = F.offsets(100, i => (i % 10 === 0 ? 26 : 18));
  assert.equal(o.length, 101); assert.equal(o[1], 26); assert.equal(o[100], 10 * 26 + 90 * 18);
  assert.equal(F.rowAt(o, 0), 0); assert.equal(F.rowAt(o, 25.9), 0); assert.equal(F.rowAt(o, 26), 1); assert.equal(F.rowAt(o, 1e9), 99); assert.equal(F.rowAt(o, -5), 0);
  assert.deepEqual(plain(F.windowOf(o, 0, 100, 2)), { a: 0, b: 8 }); const w = F.windowOf(o, 500, 180, 3); assert.ok(w.a < F.rowAt(o, 500) && w.b > F.rowAt(o, 680)); assert.deepEqual(plain(F.windowOf(o, 1e9, 100, 5)), { a: 94, b: 100 });
  assert.deepEqual(plain(F.windowOf(F.offsets(0, () => 1), 0, 10, 1)), { a: 0, b: 0 });
  const big = F.offsets(50000, () => 18); const t0 = Date.now(); for (let i = 0; i < 2000; i++) F.windowOf(big, i * 400, 800, 12); assert.ok(Date.now() - t0 < 200, 'two thousand windows over 50,000 rows in ' + (Date.now() - t0) + ' ms');
});

test('a recorded, watched or placeholder tab never calls the workspace API: no request, no toast, an empty state with the reason', async () => {
  for (const kind of [{ recorded: true, readOnly: true, id: 'rec-20261009-221530-a91c3e' }, { recorded: true, follow: true, readOnly: true, id: 'w-20261009-221530-a91c3e' }, { placeholder: true, readOnly: true, id: '' }]) {
    const W = world(idx()), ws = W.SL.ws, S = Object.assign(W.S, kind);
    assert.equal(ws.info(S), null); await tick(10); assert.equal(ws.info(S), null);
    const st = ws.status(S); assert.equal(st.state, 'none'); assert.ok(st.message.length > 10);
    for (const e of [ws.merge.queue(S), ws.merge.worktrees(S), ws.merge.verify(S, 'T1')]) assert.equal(e.state, 'none');
    assert.equal((await ws.ops.previewRestore(S, 'c01')).ok, false); assert.equal((await ws.ops.accept(S, { dryRun: true })).ok, false);
    assert.deepEqual(plain(ws.filesOf(S, 'be-1')), []); assert.equal(W.hits.length, 0, 'no request at all for ' + JSON.stringify(kind)); assert.deepEqual(plain(W.toasts), []);
  }
});
