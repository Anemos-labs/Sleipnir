// c3-sessions.test.mjs: the Sessions view (91-views-b.js): the recorded table with its selection column, which rows can be deleted, the
// confirmations of delete and prune (the ids are sorted, the scope carries their digest), the preview and result texts, the live cards, and
// hostile text in every field of a session.
import test from 'node:test';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { page, EVIL, hasMarkup, scriptAttrs } from './c3-harness.mjs';

const plain = x => JSON.parse(JSON.stringify(x));
const rec = (id, o) => Object.assign({ id, first: 'a prompt', model: 'a/m', cost: 0.5, mb: 1.25, ageS: 90000, agents: 3, resumable: true, lastWritten: 1 }, o || {});
const d16 = v => createHash('sha256').update(JSON.stringify(v)).digest('hex').slice(0, 16);

test('which recorded sessions can be deleted from the page, and why not', () => {
  const { SL } = page({ modules: ['91-views-b.js'] }), S = SL.c3.sessions;
  const hosted = new Set(['20261001-000000-aaaaaa']);
  assert.equal(S.whyNotDeletable(rec('20261001-000000-aaaaaa'), hosted), 'open in a tab: close it first');
  assert.equal(S.whyNotDeletable(rec('x', { locked: true }), hosted), 'another process is writing it');
  assert.equal(S.whyNotDeletable(rec('y'), hosted), '');
  assert.equal(S.isLive(rec('l', { locked: true })), true); assert.equal(S.isLive(rec('v', { live: true })), true, 'the server says it is being written');
  assert.equal(S.isLive(rec('w', { lastWritten: 100000 }), 105000), true, 'written ten seconds ago or less');
  assert.equal(S.isLive(rec('o', { lastWritten: 100000 }), 111000), false);
});

test('the recorded table: a box per row, disabled with its reason; Watch for a live one; the copy button of an unapplied result', () => {
  const { SL } = page({ modules: ['91-views-b.js'] }), S = SL.c3.sessions;
  const list = [rec('a1'), rec('b2', { locked: true, lastWritten: 1 }), rec('c3', { interrupted: true, integration: { applied: false, message: 'your checkout has changes', hint: 'git merge sleipnir/c3' } }), rec('d4', { name: 'my name', resumable: false })];
  const html = S.recHtml(list, { sel: new Set(['a1']), hosted: new Set(['d4']), now: 1e12 });
  const row = id => html.split('<tr data-rid="').find(p => p.startsWith(id + '"'));
  assert.match(row('a1'), /data-pick="a1" checked>/);
  assert.match(row('b2'), /data-pick="b2" disabled/); assert.match(row('b2'), /another process is writing it/);
  assert.match(row('d4'), /data-pick="d4" disabled/); assert.match(row('d4'), /open in a tab: close it first/);
  assert.match(row('b2'), /data-watch="b2"/, 'a row another process holds is Watch, not Replay');
  const live = S.recHtml([rec('e5', { lastWritten: 1e12 - 2000 })], { sel: new Set(), hosted: new Set(), now: 1e12 });
  assert.match(live, /data-watch="e5"/); assert.doesNotMatch(live, /data-rep=/);
  assert.match(row('c3'), /⚠ not applied/); assert.match(row('c3'), /data-hint="c3"/); assert.match(row('c3'), /⚠<\/span>/);
  assert.doesNotMatch(row('a1'), /data-hint/); assert.match(row('d4'), /disabled>↺ Resume/);
  assert.match(html, /<th><label[^>]*><input type="checkbox" data-pickall>/, 'the header box is there and not checked while some are not');
  const none = S.recHtml([rec('z', { locked: true })], { sel: new Set(), hosted: new Set(), now: 0 });
  assert.match(none, /data-pickall disabled/);
  const all = S.recHtml([rec('p1'), rec('p2')], { sel: new Set(['p1', 'p2']), hosted: new Set(), now: 0 });
  assert.match(all, /data-pickall checked/);
});

test('every field of a recorded session is shown as text', () => {
  const { SL } = page({ modules: ['91-views-b.js'] }), S = SL.c3.sessions;
  const r = rec('20261001-000000-' + EVIL, { name: EVIL, first: EVIL, model: EVIL, cwd: EVIL, agents: EVIL, cost: EVIL, mb: EVIL, ageS: EVIL, interrupted: true, integration: { applied: false, message: EVIL, hint: EVIL } });
  const html = S.recHtml([r], { sel: new Set([r.id]), hosted: new Set(), now: 0 });
  assert.ok(!hasMarkup(html)); assert.deepEqual(plain(scriptAttrs(html)), []); assert.ok(html.includes('&lt;img'));
  const n = S.doneNote('deleted', { list: [r], mb: 1, branchesKept: [EVIL, 'a/b: reason', 'kept Git branch c/d: why'], error: EVIL });
  assert.ok(!hasMarkup(S.noteHtml(n))); assert.deepEqual(plain(n.kept.slice(1)), ['kept Git branch a/b: reason', 'kept Git branch c/d: why', EVIL], 'the server\'s own words are not prefixed twice; a part that failed is told');
  const sp = S.deleteSpec([r], 'delete'); assert.ok(!hasMarkup(sp.detail) && !hasMarkup(sp.text) && !hasMarkup(sp.title));
  const sp2 = S.deleteSpec([r], 'prune'); assert.ok(!hasMarkup(sp2.detail) && !hasMarkup(sp2.text));
});

test('the prune preview and the confirm texts', () => {
  const { SL } = page({ modules: ['91-views-b.js'] }), S = SL.c3.sessions;
  assert.equal(S.pruneText({ list: [rec('a', { ageS: 40 * 86400, mb: 2 })], mb: 2 }), 'would delete 1 session (2.0 MB):\n  a  40d  2.0 MB\n\nnothing is deleted without --yes');
  assert.equal(S.pruneText({ list: [], mb: 0 }), 'would delete 0 sessions (0.0 MB):\n  none\n\nnothing is deleted without --yes');
  assert.equal(S.pruneText({ error: 'bad --older-than: 3x' }), 'bad --older-than: 3x');
  const sp = S.deleteSpec([rec('a'), rec('b')], 'delete');
  assert.equal(sp.title, 'Delete 2 recorded sessions'); assert.equal(sp.ok, 'Delete them'); assert.match(sp.text, /Edits left in worker trees are kept as Git branches/);
  assert.equal(S.deleteSpec([rec('a')], 'prune').title, 'Prune with --yes');
  assert.equal(S.doneNote('deleted', { list: [rec('a')], mb: 1.25 }).head, 'deleted 1 session, freed 1.3 MB');
});

/** the server of the confirmation tests: it issues ids and refuses a request whose id is not for its scope */
function server(answer) {
  const issued = new Map();
  return call => {
    if (call.path === '/api/confirm') { const id = 'cf_' + (issued.size + 1); issued.set(id, call.body.scope); return { status: 200, body: { id, scope: call.body.scope, expires_in: 60 } }; }
    return answer(call, issued);
  };
}

test('deleting sessions confirms the digest of their sorted ids, once, and sends them sorted', async () => {
  const ids = ['20261008-101500-4be2f1', '20261001-090000-0a7d55', '20261007-153000-91ac03'];
  const t = page({ modules: ['91-views-b.js'], routes: server((call, issued) => {
    if (call.path === '/api/recorded/delete') { const ok = issued.get(call.headers['X-Confirm']) === 'delete:' + d16(ids.slice().sort()); return ok ? { status: 200, body: { list: [], mb: 1, applied: true } } : { status: 403, body: { error: 'wrong scope', code: 'confirm_invalid' } }; }
    return { status: 404, body: { error: 'x', code: 'not_found' } };
  }) });
  const r = await t.SL.c3.sessions.deleteIds(ids);
  assert.equal(r.ok, true);
  assert.deepEqual(plain(t.fetch.calls.map(c => c.path)), ['/api/confirm', '/api/recorded/delete']);
  assert.deepEqual(plain(t.fetch.calls[1].body), { ids: ids.slice().sort() });
  assert.equal(t.fetch.calls[0].body.scope, 'delete:' + d16(ids.slice().sort()));
});

test('pruning confirms the ids of the plan it applies, with the same arguments as the preview', async () => {
  const ids = ['b', 'a'];
  const t = page({ modules: ['91-views-b.js'], routes: server((call, issued) => {
    if (call.path === '/api/recorded/prune') return issued.get(call.headers['X-Confirm']) === 'prune:' + d16(['a', 'b']) ? { status: 200, body: { list: [], mb: 0, applied: true } } : { status: 403, body: { error: 'wrong scope', code: 'confirm_invalid' } };
    return { status: 404, body: { error: 'x', code: 'not_found' } };
  }) });
  const r = await t.SL.c3.sessions.pruneApply('30d', 20, ids);
  assert.equal(r.ok, true); assert.deepEqual(plain(t.fetch.calls[1].body), { olderThan: '30d', keep: 20, apply: true });
});

test('a refusal of the server reaches the person in its own words', async () => {
  const t = page({ modules: ['91-views-b.js'], routes: server(() => ({ status: 409, body: { error: '20261001-090000-0a7d55 is open in a tab: close it first', code: 'hosted' } })) });
  const r = await t.SL.c3.sessions.deleteIds(['20261001-090000-0a7d55']);
  assert.equal(r.ok, false); t.SL.c3.net.fail(r);
  assert.deepEqual(plain(t.toasts), [['20261001-090000-0a7d55 is open in a tab: close it first', 'warm']]);
  t.toasts.length = 0; t.SL.c3.net.fail({ ok: false, status: 500, message: 'boom' }); assert.deepEqual(plain(t.toasts), [['boom', 'err']]);
  t.toasts.length = 0; t.SL.c3.net.fail({ ok: false, status: 0, code: 'aborted', message: 'canceled' }); assert.deepEqual(plain(t.toasts), [], 'a canceled request is not an error');
});

test('a live session card shows hostile text as text', () => {
  const S = { id: EVIL, name: EVIL, sid: EVIL, recorded: false, state: () => 'run', roster: [{ id: 'mgr' }, { id: 'be-1' }], wm: { merged: [1], torder: [1, 2] }, meta: { cwd: EVIL, model: 'p/' + EVIL, mode: EVIL, launch: EVIL, started: EVIL, budget: 5, headless: true, askTimeout: EVIL } };
  const { SL } = page({ modules: ['91-views-b.js'], sessions: [S] });
  const html = SL.c3.sessions.cardHtml(S);
  assert.ok(!hasMarkup(html)); assert.deepEqual(plain(scriptAttrs(html)), []); assert.ok(html.includes('&lt;img'));
  assert.match(html, /data-stop/); S.recorded = true; S.follow = true;
  const ro = SL.c3.sessions.cardHtml(S); assert.doesNotMatch(ro, /data-stop/); assert.match(ro, /watching/);
});
