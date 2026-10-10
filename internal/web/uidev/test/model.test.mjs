// model.test.mjs: the page's reducer (30-model.js) on the translator's golden UI streams (internal/web/translate/testdata/*.ui.jsonl)
// and on hand-made events for the fields real data carries beyond what a screen draws (their types are in internal/web/wire/events.go).
import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { context, load, plain } from './harness.mjs';

const GOLDEN = path.resolve(path.dirname(new URL(import.meta.url).pathname), '../../translate/testdata');
const goldens = fs.existsSync(GOLDEN) ? fs.readdirSync(GOLDEN).filter(f => f.endsWith('.ui.jsonl')) : [];
const read = f => fs.readFileSync(path.join(GOLDEN, f), 'utf8').split('\n').filter(Boolean).map(l => JSON.parse(l));

function setup() {
  const ctx = load(context({}), ['00-namespace.js', '00-util.js', 'api.js', '11-data-live.js', '20-clock.js', '30-model.js']);
  return ctx.SL;
}
/** The agents a stream talks about, as a roster (the manager first). */
function rosterOf(events) {
  const ids = ['mgr']; events.forEach(e => { const id = e.k === 'ask' ? e.q.agent : e.id; if (id && /^[a-z]+-\d+$/.test(id) && !ids.includes(id)) ids.push(id); });
  return ids.map((id, i) => ({ id, role: id === 'mgr' ? 'manager' : 'worker', code: id.split('-')[0], nth: 1, k: i, leg: i - 1, scope: '', ro: false, model: 'm', spawn: 0 }));
}
const session = events => ({ id: 't', meta: { goalText: '' }, roster: rosterOf(events) });
/** Every kind the reducer handles (the thirty of the reference page, the five additive kinds and the two the translator adds). */
const KNOWN = new Set('say sys local tool note state task plan verdict req use warm gov mail ckpt ask answer queue merge break compact stream diff goal final steer reply interrupt refuse digest more turn stall handover layers alert mailstat'.split(' '));

test('the golden streams exist', () => { assert.ok(goldens.length >= 2, 'no golden UI stream under ' + GOLDEN); });

for (const f of goldens) {
  test('golden ' + f + ': every kind is known, the chat model and the world model agree, and a held (digested) run ends equal', () => {
    const SL = setup(), evs = read(f), S = session(evs);
    evs.forEach(e => assert.ok(KNOWN.has(e.k), f + ': unknown kind ' + e.k));
    const world = SL.model.newModel(S, { noChat: true }), view = SL.model.newModel(S), held = SL.model.newModel(S);
    evs.forEach(e => { SL.model.reduce(world, e); SL.model.reduce(view, e); });
    const half = Math.floor(evs.length / 2), dg = { tools: 0, merged: [], submitted: [], breaks: 0, mails: 0, asks: 0, compacts: 0, rows: [] };
    evs.forEach((e, i) => SL.model.reduce(held, e, i < half ? { digest: dg } : {}));
    const sig = m => plain({ tasks: m.tasks, merged: m.merged, plan: m.plan, planText: m.planText, goal: m.goal, verdict: m.verdict, ckpts: m.ckpts, qs: m.qs.map(q => [q.id, q.answered, q.by]), ag: Object.fromEntries(m.order.map(id => [id, [m.ag[id].state, m.ag[id].rd, m.ag[id].un, m.ag[id].out, m.ag[id].wr, m.ag[id].cost, m.ag[id].layers, m.ag[id].ratios.length]])), ttl: m.ttl, r429: m.r429, retries: m.retries, conflicts: m.conflicts, bounced: m.bounced, mailstat: m.mailstat, alerts: m.alerts, stalls: m.stalls, turn: m.turn });
    assert.deepEqual(sig(view), sig(world));
    assert.deepEqual(sig(held), sig(world));
    // the numbers on screen come from the server's cost when it is sent
    const T = SL.calc.totals(world), sum = world.order.reduce((s, id) => s + (typeof world.ag[id].cost === 'number' ? world.ag[id].cost : 0), 0);
    assert.ok(Math.abs(T.cost - sum) < 1e-9, 'cost is the sum of the reported costs');
    // a message the server streamed in pieces ends whole and done
    const mids = Object.keys(view.mids); mids.forEach(mid => { const ends = evs.some(e => e.k === 'more' && e.mid === mid && e.end); if (ends) view.mids[mid].forEach(en => assert.equal(en.done, true, mid + ' ends')); });
  });
}

test('more: every copy of a streamed message grows and ends; an unknown mid is ignored', () => {
  const SL = setup(), S = session([{ k: 'state', id: 'be-1' }]), m = SL.model.newModel(S);
  SL.model.reduce(m, { t: 1, k: 'say', who: 'mgr', text: 'I will', stream: true, rate: 240, mid: 'm1' });
  SL.model.reduce(m, { t: 1.2, k: 'stream', id: 'be-1', text: 'reading', rate: 240, mid: 'm2', file: 'a.go' });
  SL.model.reduce(m, { t: 1.3, k: 'more', mid: 'm1', text: ' split it' });
  SL.model.reduce(m, { t: 1.3, k: 'more', mid: 'm2', text: ' the code' });
  SL.model.reduce(m, { t: 1.4, k: 'more', mid: 'nope', text: 'x' });
  SL.model.reduce(m, { t: 1.5, k: 'more', mid: 'm1', text: '', end: true });
  const say = m.chan.mgr.find(e => e.k === 'say');
  assert.equal(say.text, 'I will split it'); assert.equal(say.done, true);
  assert.equal(m.streams['be-1'].text, 'reading the code'); assert.equal(m.streams['be-1'].file, 'a.go');
  assert.deepEqual(plain(m.chan['be-1'].filter(e => e.k === 'stream').map(e => e.text)), ['reading the code']);
  assert.deepEqual(plain(m.chan.mgr.filter(e => e.k === 'stream').map(e => e.text)), ['reading the code']);   // the feed's copy too
});

test('plan, verdict, goal, ckpt, task, answer, turn: the additive fields', () => {
  const SL = setup(), S = session([{ k: 'state', id: 'fe-1' }]), m = SL.model.newModel(S);
  SL.model.reduce(m, { t: 0, k: 'plan', steps: ['a', 'b', 'c'], st: ['done', 'doing', 'pending'] });
  assert.deepEqual(plain(m.planText), ['a', 'b', 'c']); assert.deepEqual(plain(m.plan), ['done', 'act', 'pending']);
  SL.model.reduce(m, { t: 0, k: 'verdict', text: 'not yet: x', kind: 'continue', left: ['T1 merged'] });
  assert.equal(m.verdictKind, 'continue'); assert.deepEqual(plain(m.left), ['T1 merged']);
  SL.model.reduce(m, { t: 0, k: 'goal', s: 'paused', objective: 'O', turns: 3, max: 20, paused: 'you paused it' });
  assert.equal(m.goal.state, 'paused'); assert.equal(m.goal.turns, 3); assert.equal(m.goal.paused, 'you paused it');
  SL.model.reduce(m, { t: 1, k: 'goal', s: 'active', objective: 'O', turns: 4, max: 20 });
  assert.equal(m.goal.paused, '');
  SL.model.reduce(m, { t: 1, k: 'ckpt', cid: 'c07', ts: '10:00:00', files: 0, note: 'turn', skipped: true });
  SL.model.reduce(m, { t: 2, k: 'ckpt', cid: 'c07', ts: '10:00:00', files: 2, note: 'turn', skipped: false, agents: ['fe-1'], add: 3, del: 1, at: 5 });
  assert.equal(m.ckpts.length, 1); assert.equal(m.ckpts[0].files, 2); assert.equal(m.ckpts[0].skipped, false); assert.deepEqual(plain(m.ckpts[0].agents), ['fe-1']);
  SL.model.reduce(m, { t: 2, k: 'task', id: 'T1', title: 'one', owner: 'fe-1', s: 'running' });
  SL.model.reduce(m, { t: 3, k: 'task', id: 'T1', owner: 'be-2', s: 'todo', failed: true, closure: 'exhausted', attempts: 2 });
  assert.equal(m.tasks.T1.owner, 'be-2'); assert.equal(m.tasks.T1.failed, true); assert.equal(m.tasks.T1.closure, 'exhausted'); assert.equal(m.tasks.T1.attempts, 2); assert.equal(m.tasks.T1.title, 'one');
  SL.model.reduce(m, { t: 3, k: 'ask', q: { id: 'q1', agent: 'fe-1', cmd: 'npm i', why: 'w', what: 'this command' } });
  assert.equal(SL.calc.openQuestion(m).id, 'q1');
  SL.model.reduce(m, { t: 4, k: 'answer', qid: 'q1', choice: 3, by: 'timeout' });
  assert.equal(SL.calc.openQuestion(m), null); assert.equal(m.qs[0].by, 'timeout');
  SL.model.reduce(m, { t: 5, k: 'final' }); assert.ok(m.final); assert.equal(SL.calc.turnRunning(m), false);
  SL.model.reduce(m, { t: 6, k: 'turn', s: 'start' }); assert.equal(m.final, null); assert.equal(SL.calc.turnRunning(m), true);
  SL.model.reduce(m, { t: 7, k: 'turn', s: 'end' }); assert.equal(SL.calc.turnRunning(m), false);
});

test('use, warm, gov, queue, layers, stall, alert, handover, mailstat: counters and costs', () => {
  const SL = setup(), S = session([{ k: 'state', id: 'be-1' }]), m = SL.model.newModel(S);
  SL.model.reduce(m, { t: 1, k: 'use', id: 'be-1', rd: 900, un: 100, out: 50, wr: 100, cost: 0.25, saved: 0.1 });
  const c = SL.calc.agent(m.ag['be-1']); assert.equal(c.cost, 0.25); assert.equal(c.saved, 0.1); assert.equal(c.wr, 100); assert.equal(c.pct, 90);
  SL.model.reduce(m, { t: 2, k: 'warm', ttl: 300 }); assert.equal(m.ttl, 300); assert.equal(SL.calc.warmLeft(m, 12), 290);
  SL.model.reduce(m, { t: 2, k: 'gov', rpm: 40, r429: 2, retries: 3 }); assert.equal(m.r429, 2); assert.equal(m.retries, 3);
  SL.model.reduce(m, { t: 2, k: 'queue', head: null, conflicts: 1, bounced: 4 }); assert.equal(m.conflicts, 1); assert.equal(m.bounced, 4);
  SL.model.reduce(m, { t: 2, k: 'layers', id: 'be-1', toks: [1, 2, 3, 4, 5, 6] }); assert.deepEqual(plain(m.ag['be-1'].layers), [1, 2, 3, 4, 5, 6]);
  SL.model.reduce(m, { t: 3, k: 'stall', id: 'be-1', task: 'T1', kind: 'claimed_no_progress', s: 'raise', text: 'quiet' }); assert.equal(Object.keys(m.stalls).length, 1);
  SL.model.reduce(m, { t: 4, k: 'stall', id: 'be-1', task: 'T1', kind: 'claimed_no_progress', s: 'clear' }); assert.equal(Object.keys(m.stalls).length, 0);
  SL.model.reduce(m, { t: 4, k: 'alert', s: 'raise', kind: 'stalled', key: 'be-1', text: 'quiet' }); assert.equal(m.alerts['be-1'].text, 'quiet');
  SL.model.reduce(m, { t: 5, k: 'alert', s: 'clear', kind: 'stalled', key: 'be-1' }); assert.equal(Object.keys(m.alerts).length, 0);
  SL.model.reduce(m, { t: 5, k: 'handover', task: 'T1', from: 'be-1', to: 'be-2', s: 'done' }); assert.equal(m.handovers.length, 1);
  SL.model.reduce(m, { t: 5, k: 'mailstat', sent: 3, delivered: 2, dropped: 1 }); assert.equal(m.mailstat.sent, 3);
  // the hidden kinds do not count as visible (the hold chip's "new")
  ['more', 'turn', 'stall', 'handover', 'layers'].forEach(k => assert.equal(SL.model.isVisible({ k }), false));
});

test('addAgent adds a worker to a running model once', () => {
  const SL = setup(), S = session([]), m = SL.model.newModel(S);
  SL.model.addAgent(m, { id: 'be-3', role: 'backend', code: 'be', nth: 3, k: 1, leg: 0, scope: 'api/**', ro: false, model: 'x', spawn: 4 });
  SL.model.addAgent(m, { id: 'be-3', role: 'backend', code: 'be', nth: 3, k: 1, leg: 0, scope: 'api/**', ro: false, model: 'x', spawn: 4 });
  assert.deepEqual(plain(m.order), ['mgr', 'be-3']); assert.ok(Array.isArray(m.chan['be-3']));
  SL.model.reduce(m, { t: 5, k: 'state', id: 'be-3', s: 'edit', doing: 'Edit a.go', task: 'T1' }); assert.equal(m.ag['be-3'].state, 'edit');
});
