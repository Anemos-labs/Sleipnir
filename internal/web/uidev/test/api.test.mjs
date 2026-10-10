// api.test.mjs: SL.api against a fake fetch and a fake EventSource: headers and bodies, the error shape, 401, 428, the confirm
// flow, 429 retries, timeouts, aborts, the network state, d16 and the stream's reopen.
import test from 'node:test';
import assert from 'node:assert/strict';
import { webcrypto } from 'node:crypto';
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

/** A server that refuses /api/sessions/a/mode with 428 until it gets X-Confirm good for the scope; 'bad' ids are refused 403. */
function confirmServer(o) {
  o = o || {}; const issued = [];
  const routes = (m, p, init) => {
    if (p === '/api/confirm') { const sc = JSON.parse(init.body).scope; const id = 'id' + (issued.length + 1); issued.push([id, sc]); return { status: 200, body: { id, scope: sc, expires_in: 60 } }; }
    const id = init.headers['X-Confirm'];
    if (!id) return { status: 428, headers: { 'X-Confirm-Scope': 'restart:a:0123456789abcdef' }, body: { error: 'this action needs a confirmation: obtain an id for the scope and send it as X-Confirm', code: 'confirm_required', detail: { scope: 'restart:a:0123456789abcdef', reasons: ['permission mode yolo', 'allow Bash(go test:*)'] } } };
    const got = issued.find(x => x[0] === id);
    if (!got || got[1] !== 'restart:a:0123456789abcdef' || (o.expireFirst && id === 'id1')) return { status: 403, body: { error: 'the confirmation is unknown, already used, expired or for something else; obtain a new one', code: 'confirm_invalid' } };
    return { status: 202, body: { gen: 2 } };
  };
  return routes;
}

test('428 on any request: the shell is asked with the server\'s scope and reasons; yes confirms that scope and repeats the same request once', async () => {
  const { SL, fetch, toasts } = setup(confirmServer());
  const asks = []; SL.api.cfg.askConfirm = async a => { asks.push(a); return true; };
  const r = await SL.api.post('/api/sessions/a/restart', { kind: 'swarm', swarm: 3, fresh: false });
  assert.equal(r.ok, true); assert.equal(r.status, 202);
  assert.deepEqual(plain(asks), [{ scope: 'restart:a:0123456789abcdef', reasons: ['permission mode yolo', 'allow Bash(go test:*)'], message: 'this action needs a confirmation: obtain an id for the scope and send it as X-Confirm', method: 'POST', path: '/api/sessions/a/restart' }]);
  const tries = fetch.calls.filter(c => c.path === '/api/sessions/a/restart');
  assert.equal(tries.length, 2); assert.equal(tries[0].body, tries[1].body, 'the same body is sent again');
  assert.equal(tries[0].headers['X-Confirm'], undefined); assert.equal(tries[1].headers['X-Confirm'], 'id1');
  assert.deepEqual(fetch.calls.filter(c => c.path === '/api/confirm').map(c => JSON.parse(c.body)), [{ scope: 'restart:a:0123456789abcdef' }]);
  assert.deepEqual(toasts, []);
});

test('428: no from the person sends nothing more and toasts nothing; without the hook the 428 comes back as it is', async () => {
  const s1 = setup(confirmServer());
  s1.SL.api.cfg.askConfirm = async () => false;
  const r = await s1.SL.api.post('/api/sessions/a/restart', { kind: 'new', fresh: true });
  assert.equal(r.status, 428); assert.equal(r.code, 'confirm_required'); assert.equal(r.declined, true); assert.equal(r.scope, 'restart:a:0123456789abcdef');
  assert.equal(s1.fetch.calls.length, 1); assert.deepEqual(s1.toasts, []);
  const s2 = setup(confirmServer());
  const r2 = await s2.SL.api.post('/api/sessions/a/restart', { kind: 'new', fresh: true });
  assert.equal(r2.status, 428); assert.equal(r2.declined, undefined); assert.equal(s2.fetch.calls.length, 1); assert.deepEqual(s2.toasts, []);
  const s3 = setup(confirmServer());
  let n = 0; s3.SL.api.cfg.askConfirm = async () => { n++; return true; };
  const r3 = await s3.SL.api.post('/api/sessions/a/restart', {}, { noAsk: true });
  assert.equal(r3.status, 428); assert.equal(n, 0);
});

test('428: an id that is refused on the repeat is renewed once for the same scope; the person is asked only once', async () => {
  const { SL, fetch } = setup(confirmServer({ expireFirst: true }));
  let n = 0; SL.api.cfg.askConfirm = async () => { n++; return true; };
  const r = await SL.api.post('/api/sessions/a/restart', { kind: 'swarm', swarm: 2 });
  assert.equal(r.ok, true); assert.equal(n, 1);
  assert.deepEqual(fetch.calls.filter(c => c.path === '/api/sessions/a/restart').map(c => c.headers['X-Confirm'] || ''), ['', 'id1', 'id2']);
});

test('a caller\'s own id for another scope: refused 403, the server then names its scope and the generic question is asked once', async () => {
  const { SL, fetch } = setup(confirmServer());
  let n = 0; SL.api.cfg.askConfirm = async () => { n++; return true; };
  const r = await SL.api.post('/api/sessions/a/restart', { kind: 'restart' }, { confirm: 'restart:a:wrongwrongwrong0' });
  assert.equal(r.ok, true); assert.equal(n, 1);
  assert.deepEqual(fetch.calls.filter(c => c.path === '/api/sessions/a/restart').map(c => c.headers['X-Confirm'] || ''), ['id1', 'id2', '', 'id3']);
  const held = setup(confirmServer()); let m = 0; held.SL.api.cfg.askConfirm = async () => { m++; return true; };
  const r2 = await held.SL.api.post('/api/sessions/a/restart', {}, { confirmId: 'stale' });
  assert.equal(r2.ok, true); assert.equal(m, 1);
  assert.deepEqual(held.fetch.calls.filter(c => c.path === '/api/sessions/a/restart').map(c => c.headers['X-Confirm'] || ''), ['stale', '', 'id1']);
});

test('a caller that already asked (the scope the server wants) is not asked again', async () => {
  const { SL } = setup(confirmServer());
  let n = 0; SL.api.cfg.askConfirm = async () => { n++; return true; };
  const r = await SL.api.post('/api/sessions/a/restart', {}, { confirm: 'restart:a:0123456789abcdef' });
  assert.equal(r.ok, true); assert.equal(n, 0);
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

test('cid: a new random id for every action, never the same in two page loads (the server answers a repeated id with its first answer)', () => {
  const a = setup(() => ({ status: 200 })).SL, b = setup(() => ({ status: 200 })).SL, seen = new Set();
  for (const SL of [a, b]) for (let i = 0; i < 500; i++) { const id = SL.api.cid(); assert.match(id, /^c[0-9a-f-]{32,36}$/); assert.ok(!seen.has(id), 'repeated ' + id); seen.add(id); assert.ok(id.length <= 64, 'the server takes at most 64 characters'); }
  /* without crypto.randomUUID (a page that is not a secure context): 128 bits from getRandomValues */
  const ctx = context({ fetch: fakeFetch(() => ({ status: 200 })), crypto: { getRandomValues: x => webcrypto.getRandomValues(x), subtle: webcrypto.subtle } });
  load(ctx, ['00-namespace.js', '00-util.js', 'api.js']);
  const x = ctx.SL.api.cid(), y = ctx.SL.api.cid(); assert.match(x, /^c[0-9a-f]{32}$/); assert.notEqual(x, y);
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
