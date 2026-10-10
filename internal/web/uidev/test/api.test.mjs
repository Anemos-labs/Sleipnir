// api.test.mjs: SL.api against a fake fetch and a fake EventSource: headers and bodies, the error shape, 401, 428, the confirm
// flow, 429 retries, timeouts, aborts, the network state, d16 and the stream's reopen.
import test from 'node:test';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { context, load, fakeFetch, FakeES, tick, plain } from './harness.mjs';

function setup(routes) {
  const fetch = fakeFetch(routes);
  const ctx = load(context({ fetch }), ['00-namespace.js', '00-util.js', 'api.js']);
  const toasts = [];
  ctx.SL.api.cfg.toast = (t, k) => toasts.push([t, k]);
  return { ctx, SL: ctx.SL, fetch, toasts };
}

test('GET sends neither the custom header nor a content type; mutations send the header, JSON only with a body', async () => {
  const { SL, fetch } = setup(() => ({ status: 200, body: { ok: true } }));
  assert.deepEqual(plain(await SL.api.get('/api/hello')), { ok: true, status: 200, data: { ok: true } });
  await SL.api.post('/api/sessions/a/stop');
  await SL.api.post('/api/sessions/a/mode', { mode: 'plan' });
  const [g, nb, wb] = fetch.calls;
  assert.equal(g.method, 'GET');
  assert.deepEqual(g.headers, {});
  assert.equal(nb.headers['X-Sleipnir-Web'], '1');
  assert.equal(nb.headers['Content-Type'], undefined);
  assert.equal(nb.body, undefined);
  assert.equal(wb.headers['Content-Type'], 'application/json');
  assert.equal(wb.body, '{"mode":"plan"}');
});

test('only /api/ paths are requested', async () => {
  const { SL, fetch } = setup(() => ({ status: 200, body: {} }));
  for (const p of ['/x', 'http://evil.test/api/', '//evil.test/api/x', '/api//evil']) {
    const r = await SL.api.get(p);
    assert.equal(r.ok, false);
    assert.equal(r.code, 'bad_path');
  }
  assert.equal(fetch.calls.length, 0);
});

test('an error body becomes {ok:false, status, code, message, detail}', async () => {
  const { SL } = setup(() => ({ status: 409, body: { error: 'nothing was running', code: 'idle', detail: { a: 1 } } }));
  assert.deepEqual(plain(await SL.api.post('/api/sessions/a/stop')), { ok: false, status: 409, code: 'idle', message: 'nothing was running', detail: { a: 1 } });
});

test('a body that is not JSON still gives a sentence', async () => {
  const { SL } = setup(() => ({ status: 502, body: '<html>' }));
  const r = await SL.api.get('/api/update');
  assert.equal(r.ok, false);
  assert.equal(r.code, 'http_502');
  assert.match(r.message, /502/);
});

test('401 reloads the page (the server answers with its sign-in page)', async () => {
  const { ctx, SL } = setup(() => ({ status: 401, body: { error: 'sign in', code: 'unauthenticated' } }));
  const r = await SL.api.get('/api/hello');
  assert.equal(r.status, 401);
  assert.equal(ctx.reloads.n, 1);
});

test('428 is a page bug: toasted, not retried', async () => {
  const { SL, fetch, toasts } = setup(() => ({ status: 428, body: { error: 'needs a confirmation', code: 'confirm_required' }, headers: { 'X-Confirm-Scope': 'mode:yolo:a' } }));
  const r = await SL.api.post('/api/sessions/a/mode', { mode: 'yolo' });
  assert.equal(r.scope, 'mode:yolo:a');
  assert.equal(fetch.calls.length, 1);
  assert.deepEqual(toasts, [['needs a confirmation', 'err']]);
});

test('opts.confirm obtains an id for the scope and sends it as X-Confirm; an expired id is replaced once', async () => {
  let n = 0, firstMode = true;
  const { SL, fetch } = setup((m, p, init, call) => {
    if (p === '/api/confirm') { n++; return { status: 200, body: { id: 'id' + n, scope: JSON.parse(init.body).scope, expires_in: 60 } }; }
    if (firstMode) { firstMode = false; return { status: 403, body: { error: 'expired', code: 'confirm_invalid' } }; }
    return { status: 200, body: { ok: true } };
  });
  const r = await SL.api.post('/api/sessions/a/mode', { mode: 'yolo' }, { confirm: 'mode:yolo:a' });
  assert.equal(r.ok, true);
  const modes = fetch.calls.filter(c => c.path.endsWith('/mode'));
  assert.deepEqual(modes.map(c => c.headers['X-Confirm']), ['id1', 'id2']);
  assert.deepEqual(fetch.calls.filter(c => c.path === '/api/confirm').map(c => JSON.parse(c.body)), [{ scope: 'mode:yolo:a' }, { scope: 'mode:yolo:a' }]);
});

test('a second confirm_invalid stands; confirm() returns null when no id is issued', async () => {
  const { SL } = setup((m, p) => p === '/api/confirm' ? { status: 200, body: { id: 'x' } } : { status: 403, body: { error: 'bad', code: 'confirm_invalid' } });
  const r = await SL.api.post('/api/recorded/prune', { apply: true }, { confirm: 'prune:0' });
  assert.equal(r.code, 'confirm_invalid');
  const s2 = setup(() => ({ status: 429, body: { error: 'too many', code: 'rate_limited' } }));
  assert.equal(await s2.SL.api.confirm('x'), null);
});

test('confirmId is sent as given', async () => {
  const { SL, fetch } = setup(() => ({ status: 201, body: { tab: {} } }));
  await SL.api.post('/api/sessions', { cwd: '/p' }, { confirmId: 'chal' });
  assert.equal(fetch.calls[0].headers['X-Confirm'], 'chal');
  assert.equal(fetch.calls.length, 1);
});

test('429: a GET is retried once after Retry-After; a mutation is not', async () => {
  let n = 0;
  const { SL, fetch } = setup((m) => (m === 'GET' && ++n === 1) || m !== 'GET' ? { status: 429, body: { error: 'slow down', code: 'rate_limited' }, headers: { 'Retry-After': '0' } } : { status: 200, body: { x: 1 } });
  assert.equal((await SL.api.get('/api/models')).ok, true);
  const r = await SL.api.post('/api/models/fav', { ref: 'a', on: true });
  assert.equal(r.code, 'rate_limited');
  assert.equal(fetch.calls.length, 3);
});

test('a network failure resolves code network and tells live.js; the next answer clears it', async () => {
  let down = true;
  const { SL } = setup(() => down ? 'network' : { status: 200, body: {} });
  const seen = [];
  SL.bus.on('api-offline', () => seen.push('off'));
  SL.bus.on('api-online', () => seen.push('on'));
  const r = await SL.api.get('/api/hello');
  assert.deepEqual(plain(r), { ok: false, status: 0, code: 'network', message: 'the server is not reachable' });
  assert.equal(SL.api.reachable(), false);
  down = false;
  await SL.api.get('/api/hello');
  assert.deepEqual(seen, ['off', 'on']);
});

test('timeouts and the caller\'s abort', async () => {
  const { SL } = setup(() => 'hang');
  const r = await SL.api.get('/api/hello', { timeout: 20 });
  assert.equal(r.code, 'timeout');
  const ctl = new AbortController();
  const p = SL.api.get('/api/hello', { signal: ctl.signal, timeout: 5000 });
  ctl.abort();
  assert.equal((await p).code, 'aborted');
});

test('d16 is the first 16 hex of SHA-256 over canonical JSON (sorted keys, Go escaping)', async () => {
  const { SL } = setup(() => ({ status: 200 }));
  const v = { to: 'live', path: 'a<b>&c.go', key: '3:4', from: 'c07' };
  const canon = '{"from":"c07","key":"3:4","path":"a\\u003cb\\u003e\\u0026c.go","to":"live"}';
  assert.equal(SL.api.canonical(v), canon);
  assert.equal(await SL.api.d16(v), createHash('sha256').update(canon).digest('hex').slice(0, 16));
  assert.equal(SL.api.canonical(['b', 'a']), '["b","a"]');
});

test('cid counts up', () => {
  const { SL } = setup(() => ({ status: 200 }));
  assert.equal(SL.api.cid(), 'c1');
  assert.equal(SL.api.cid(), 'c2');
});

test('stream: named frames, last id, states, and a reopen with ?after= after the browser gives up', async () => {
  FakeES.all.length = 0;
  const { SL } = setup(() => ({ status: 200 }));
  SL.api.cfg.reopenMs = 5;
  const frames = [], states = [];
  let asked = 0;
  const s = SL.api.stream(41, { frame: (t, d, id) => frames.push([t, d, id]), state: x => states.push(x), beforeReopen: async () => { asked++; return true; } });
  const es = FakeES.all[0];
  assert.equal(es.url, '/api/stream?after=41');
  es.open();
  es.emit('ev', { tab: 'a', ev: { k: 'say' } }, 42);
  es.emit('gap', { reason: 'aged', dropped: 3, last: 40 }, 43);
  assert.deepEqual(frames.map(f => [f[0], f[2]]), [['ev', 42], ['gap', 43]]);
  assert.equal(s.lastId(), 43);
  es.fail(false); // the browser reconnects by itself
  assert.equal(FakeES.all.length, 1);
  es.fail(true); // the browser gave up
  await tick(20);
  assert.equal(asked, 1);
  assert.equal(FakeES.all.length, 2);
  assert.equal(FakeES.all[1].url, '/api/stream?after=43');
  FakeES.all[1].open();
  assert.deepEqual(states, ['open', 'reconnecting', 'open']);
  s.close();
  assert.equal(FakeES.all[1].closed, true);
  assert.equal(states.at(-1), 'closed');
});

test('stream: beforeReopen false keeps it closed; a malformed frame is ignored', async () => {
  FakeES.all.length = 0;
  const { SL } = setup(() => ({ status: 200 }));
  SL.api.cfg.reopenMs = 5;
  const frames = [];
  SL.api.stream(0, { frame: (t) => frames.push(t), beforeReopen: async () => false });
  const es = FakeES.all[0];
  (es.listeners.ev || []).forEach(f => f({ data: '{bad', lastEventId: '1' }));
  assert.deepEqual(frames, []);
  es.fail(true);
  await tick(20);
  assert.equal(FakeES.all.length, 1);
});
