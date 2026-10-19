// true-counts.test.mjs: a long run of a large team on the shipped page. The page keeps bounded lists (the newest mails, cache anomalies
// and compactions, the manager's decisions), and every number it shows is a count of all of them: the rail's badges, the Cache view, the
// Mail view and the cockpit's mail line, with a keyframe's tally added for the ones the server's snapshot no longer carries. The Mail
// view's drawing stays legible from a team of eight to one far larger than 32 workers, and a replay counts the mailman as no worker.
import test from 'node:test';
import assert from 'node:assert/strict';
import { boot } from './pagedom.mjs';

const ROLES = ['architect', 'scout', 'backend', 'frontend', 'devops', 'tester', 'docs', 'reviewer'], CODES = ['ar', 'sc', 'be', 'fe', 'dv', 'ts', 'dc', 'rv'];
/** A roster: the manager and `per[k]` workers of the k-th role (the 15-hour run's team by default: 32 workers). */
function roster(per) {
  const out = [{ id: 'mgr', role: 'manager', code: 'mgr', nth: 0, k: 0, leg: -1, scope: '-', ro: false, model: 'm', spawn: 0 }];
  ROLES.forEach((r, k) => { for (let i = 1; i <= per[k]; i++) out.push({ id: CODES[k] + '-' + i, role: r, code: CODES[k], nth: i, k: out.length, leg: k, scope: '**', ro: false, model: 'm', spawn: 1 }); });
  return out;
}
const RUN = [2, 1, 8, 8, 3, 4, 2, 4];
const snapshot = (ros, keyframe, events, meta) => ({ tab: { id: 'a', sid: 's-a', name: 'crew', cwd: '/p/a', gen: 1, order: 0 }, gen: 1, seq: events.length, now: 100, meta: Object.assign({ model: 'm', mode: 'default', startedAt: 1760000000000, swarm: ros.length - 1, mailman: true }, meta || {}), roster: ros, keyframe, events, hist: [], questions: [] });
const brk = (t, quiet) => ({ t, k: 'break', id: 'be-1', kind: 'low_hit', read: 0, expected: 1000, why: 'the prompt prefix did not change: the endpoint did not serve it', quiet: !!quiet });
const cmp = t => ({ t, k: 'compact', id: 'fe-1', from: 20000, to: 5000, pct: -75, moment: 'warm', mode: 'fork' });
const mail = (t, from, to) => ({ t, k: 'mail', from, to, text: 'a note ' + t });

test('the reducer counts what its bounded lists let go, and a tally adds what a keyframe stands for', async () => {
  const L = await boot(), SL = L.SL, m = SL.model.newModel({ id: 'x', meta: {}, roster: roster(RUN) });
  for (let i = 0; i < 640; i++) { SL.model.reduce(m, brk(i, i >= 3)); SL.model.reduce(m, mail(i, 'be-1', 'mgr')); if (i < 560) SL.model.reduce(m, cmp(i)); }
  let n = SL.calc.counts(m);
  assert.deepEqual([m.anomalies.length, m.compactions.length, m.mail.length], [500, 500, 400], 'the lists keep the newest');
  assert.deepEqual([n.anomalies, n.loud, n.compactions, n.mail], [640, 3, 560, 640], 'the counts are of all of them');
  SL.model.reduce(m, { t: 700, k: 'tally', breaks: 100, loud: 1, compacts: 7, mails: 9 });
  n = SL.calc.counts(m);
  assert.deepEqual([n.anomalies, n.loud, n.compactions, n.mail], [740, 4, 567, 649], 'a tally adds its numbers');
  SL.model.reduce(m, { t: 701, k: 'tally', breaks: -5, loud: 'x', compacts: Infinity, mails: 2.5 });
  n = SL.calc.counts(m);
  assert.deepEqual([n.anomalies, n.loud, n.compactions, n.mail], [740, 4, 567, 651], 'a number that is not a count adds nothing');
  SL.model.reduce(m, { t: 702, k: 'mailstat', sent: 2730, delivered: 2665, dropped: 0, routed: 774 });
  assert.equal(SL.calc.counts(m).mail, 2730, 'mail is the terminal\'s figure once the server sends the counts: the larger of sent and routed');
});

test('a 15-hour run: the rail, the Cache view, the Mail view and the cockpit show totals, and say when a list shows only the newest', async () => {
  const ros = roster(RUN);
  /* the keyframe carries the newest few and a tally of the rest; the retained events add more */
  const keyframe = [brk(1, true), brk(2, true), cmp(3), mail(4, 'be-1', 'mgr'), mail(5, 'fe-1', 'be-1'),
    { t: 6, k: 'mailstat', sent: 2730, delivered: 2665, dropped: 0, routed: 774, digests: 32, batches: 34, mailman: 'up' },
    { t: 6, k: 'tally', breaks: 6440, loud: 3, compacts: 1012, mails: 1000 }];
  const events = [brk(7, true), brk(8, false), cmp(9), cmp(10), mail(11, 'mgr', 'fe-2')].map((e, i) => Object.assign({ seq: i + 1 }, e));
  const L = await boot({ route: (m, p) => p === '/api/sessions/a/snapshot' ? snapshot(ros, keyframe, events) : undefined });
  const S = L.SL.sessions.active; await L.step(1);
  const b = L.SL.ui.nav.badges(S);
  assert.equal(b.mail[0], '2730', 'the rail counts the mails the run sent, not the 400 the page keeps');
  assert.match(b.cache[2], /^4 cache anomalies$/, 'the rail counts the anomalies a person is told of, the evicted ones too');
  assert.match(L.SL.ui.mailNote(S.m, S), /^routed 2730 · mailman on$/, 'the cockpit says what the terminal says');

  L.SL.views.show('cache'); await L.step(0.1);
  const cache = L.$('.cache').textContent;
  assert.match(cache, /1,015 so far/, 'compactions: all of them');
  assert.match(cache, /Unexpected cache misses6,444the newest 4 shown/, 'anomalies: all of them, and the list says it shows the newest');
  assert.match(cache, /1,015 compactions/); assert.match(cache, /6,444 unexpected misses/);

  L.SL.views.show('mail'); await L.step(0.1);
  assert.match(L.$('.mcount').textContent, /^2730 sent · 2665 delivered/);
  assert.equal(L.$('.menv').textContent, 'newest first · the newest 3 shown');
});

test('a small run says no more than it has: the newest are all of them', async () => {
  const L = await boot();
  const S = L.SL.sessions.active;
  await L.event('mail', { from: 'be-1', to: 'fe-1', text: 'hi' }); await L.event('compact', { id: 'fe-1', from: 9000, to: 3000, pct: -66 });
  await L.step(3);
  L.SL.views.show('mail'); await L.step(0.1);
  assert.equal(L.$('.menv').textContent, 'newest first');
  assert.match(L.$('.mcount').textContent, /^1 routed · mailman off$/);
  assert.equal(L.SL.ui.nav.badges(S).mail[0], '1');
  L.SL.views.show('cache'); await L.step(0.1);
  assert.match(L.$('.cache').textContent, /Folding the conversation1 so far/);
  assert.doesNotMatch(L.$('.cache').textContent, /newest/);
});

/** The room each id label takes along the line, as the layout estimates it (JetBrains Mono: 0.6 em). */
const idWidth = (id, fs) => id.length * 0.6 * fs;

test('who writes to whom: ids never run together and the role row never overprints, from 8 workers to 256', async () => {
  const L = await boot(), SL = L.SL;
  for (const per of [[1, 1, 1, 1, 1, 1, 1, 1], RUN, [2, 2, 2, 2, 2, 2, 2, 2], [4, 4, 4, 4, 4, 4, 4, 4], [8, 8, 8, 8, 8, 8, 8, 8], [16, 4, 16, 16, 4, 4, 2, 2], [32, 32, 32, 32, 32, 32, 32, 32]]) {
    const ros = roster(per), m = SL.model.newModel({ id: 'x', meta: {}, roster: ros }), F = SL.ui.flowLayout(m), n = ros.length, what = n + ' agents';
    assert.equal(F.ids[0], 'mgr', what + ': the manager first');
    assert.deepEqual(Array.from(F.ids.slice(1), id => id.split('-')[0]), CODES.flatMap((c, k) => Array(per[k]).fill(c)), what + ': grouped by role, in the team\'s order');
    if (F.rot) {
      assert.ok(F.fs >= 7 && F.dx >= F.fs, what + ': upright ids, each in its own column (' + F.fs + ' in ' + F.dx + ')');
    } else {
      assert.ok(F.fs >= 9.5, what + ': ids at ' + F.fs);
      const step = F.rows === 2 ? 2 : 1;
      for (let i = step; i < n; i++) {
        const a = F.ids[i - step], c = F.ids[i], gap = (F.xs[i] - idWidth(c, F.fs) / 2) - (F.xs[i - step] + idWidth(a, F.fs) / 2);
        assert.ok(gap >= 2, what + ': ' + a + ' and ' + c + ' run together (gap ' + gap.toFixed(1) + ')');
      }
    }
    if (F.each) {
      assert.equal(F.groups.length, 0);
      for (const id of F.ids) assert.ok(String(id === 'mgr' ? 'manager' : m.ag[id].role).length * 6.9 + 6 <= F.dx || n === 1, what + ': a role under every node only when each fits');
    } else {
      const shown = F.groups.filter(g => g.word);
      assert.ok(F.groups.length >= 8, what + ': one group a role');
      for (const g of shown) assert.ok(g.word.length * 6.9 <= (g.i2 - g.i1 + 1) * F.dx - 6, what + ': ' + g.word + ' fits its group');
      for (let i = 1; i < shown.length; i++) assert.ok(shown[i].cx - shown[i].word.length * 3.45 > shown[i - 1].cx + shown[i - 1].word.length * 3.45, what + ': role labels apart');
    }
  }
  /* a team of eight looks as it did: 12-unit ids and a role under every node */
  const eight = SL.ui.flowLayout(SL.model.newModel({ id: 'x', meta: {}, roster: roster([1, 1, 1, 1, 1, 1, 1, 1]) }));
  assert.deepEqual([eight.fs, eight.rows, eight.rot, eight.each, eight.W, eight.H].slice(), [12, 1, false, true, 1040, 200]);
  const huge = SL.ui.flowLayout(SL.model.newModel({ id: 'x', meta: {}, roster: roster([32, 32, 32, 32, 32, 32, 32, 32]) }));
  assert.ok(huge.rot && huge.W > 1040, 'past what the width holds at 9 units a node, the drawing grows wider and scrolls');
});

test('the Mail view of the 15-hour run: one role label a group, ids in role order, arcs inside the drawing', async () => {
  const ros = roster(RUN), events = [mail(2, 'be-1', 'rv-4'), mail(3, 'rv-4', 'mgr'), mail(4, 'ar-1', 'dc-2')].map((e, i) => Object.assign({ seq: i + 1 }, e));
  const L = await boot({ route: (m, p) => p === '/api/sessions/a/snapshot' ? snapshot(ros, [], events) : undefined });
  L.SL.views.show('mail'); await L.step(0.1);
  const svg = L.$('.flowsvg'), vb = svg.getAttribute('viewBox').split(' ').map(Number);
  const nodeIds = svg.querySelectorAll('[data-open] text').map(t => t.textContent);
  assert.equal(nodeIds.length, 33, 'one id per agent, no role text under each');
  assert.deepEqual(Array.from(nodeIds.slice(0, 6)), ['mgr', 'ar-1', 'ar-2', 'sc-1', 'be-1', 'be-2']);
  assert.deepEqual(Array.from(svg.querySelectorAll('.fgrp text'), t => t.textContent), ['AR', 'SC', 'BACKEND', 'FRONTEND', 'DEVOPS', 'TESTER', 'DOCS', 'REVIEWER']);
  for (const p of svg.querySelectorAll('.arcs-static path')) {
    const cy = +/Q\S+ (-?[\d.]+)/.exec(p.getAttribute('d'))[1], apex = (150 + cy) / 2;
    assert.ok(apex >= 0 && apex <= vb[3], 'an arc stays inside the drawing (apex ' + apex + ')');
  }
});

test('a replay counts the mailman as no worker, and says whether the run had it on', async () => {
  const sid = '20261015-003715-2b8061', evs = [{ t: 1, k: 'state', id: 'be-1', s: 'edit', doing: 'x' }, { t: 2, k: 'break', id: 'mm-1', kind: 'low_hit', read: 0, expected: 900, why: 'w', quiet: true }, { t: 3, k: 'state', id: 'fe-1', s: 'think', doing: 'y' }, { t: 4, k: 'mailstat', sent: 3, delivered: 3, dropped: 0, mailman: 'up' }];
  const L = await boot({ route: (m, p) => p.startsWith('/api/recorded/' + sid + '/events') ? { events: evs, next: '' } : undefined });
  assert.deepEqual(Array.from(L.SL.live.rosterOf(evs), r => r.id), ['mgr', 'be-1', 'fe-1'], 'the mailman\'s own cache break does not make it a worker');
  L.SL.sessions.reg.recorded = [{ id: sid, first: 'x', model: 'm', cost: 0, list: 4.3, plan: true, mb: 1, ageS: 60, agents: 3, resumable: true, dur: 60, lastWritten: 1760000060000, mailman: true }];
  await L.SL.live.openRecorded(sid, {}); await L.step(0.5);
  const S = L.SL.sessions.active;
  assert.equal(S.recorded, true);
  assert.deepEqual(Array.from(S.roster, r => r.id), ['mgr', 'be-1', 'fe-1']);
  assert.equal(S.meta.mailman, true, 'the recorded run had the mailman on (its session.start says so)');
  assert.match(L.$('#teamLab').textContent, /^manager \+ 2 workers$/);
  assert.match(L.SL.ui.mailNote(S.m, S), /mailman on$/);
});

test('the manager\'s decisions: the line counts all of them when its list keeps the newest', async () => {
  const L = await boot(), A = L.SL.ui.approvals, list = Array.from({ length: 300 }, (_, i) => ({ agent: 'be-1', cmd: 'go test ./x' + i, ok: i !== 5 }));
  assert.equal(A.mgrLabel(list), 'the manager answered 300 questions: 299 approved, 1 denied');
  assert.equal(A.mgrLabel(list, { n: 350, d: 2 }), 'the manager answered 350 questions: 348 approved, 2 denied');
  assert.match(A.mgrHtml(list, true, 'qmgr', { n: 350, d: 2 }), /290 earlier ones are in the log/);
  const SL = L.SL, m = SL.model.newModel({ id: 'x', meta: {}, roster: roster([0, 0, 1, 0, 0, 0, 0, 0]) });
  for (let i = 0; i < 320; i++) {
    SL.model.reduce(m, { t: i, k: 'ask', q: { id: 'q' + i, agent: 'be-1', cmd: 'go test', kind: 'command' } });
    SL.model.reduce(m, { t: i + 0.5, k: 'answer', qid: 'q' + i, choice: 1, by: 'manager' });
  }
  const row = m.chan.mgr.filter(e => e.k === 'mgrdec').slice(-1)[0];
  assert.deepEqual([row.list.length, row.n, row.nd], [300, 320, 0], 'one row of all 320, keeping the newest 300');
  assert.deepEqual(plainCount(A.mgrRecentCount(m)), { n: 320, d: 0 });
});
const plainCount = c => ({ n: c.n, d: c.d });
