// parity-facts.test.mjs: the page's half of the facts dimension of the parity guard (internal/parity/facts_test.go has the terminal's).
//
// A fix to how an event is interpreted cannot be seen in a list of what either interface shows: the number changes. So the same
// recordings are run through both interfaces and the same facts must come out. internal/parity/facts_test.go folds each recording with
// the State of internal/tui/state, which every terminal view reads, and writes the facts to internal/parity/testdata/facts-<recording>.json.
// This test loads the page stream the translator makes of the same recording (internal/web/translate/testdata/*.ui.jsonl, and
// internal/parity/testdata/team.ui.jsonl for the hand-built team log), reduces it with the page's reducer exactly as model.test.mjs does,
// computes the same facts (their definitions are in the header of facts_test.go) and compares. A fact on which the interfaces differ is
// listed in internal/parity/contract/facts.json as "<recording>/<fact>" with its reason: a listed fact that no longer differs, and a
// difference that is not listed, both fail.
import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { context, load } from './harness.mjs';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const TRANSLATE = path.resolve(HERE, '../../translate/testdata');
const PARITY = path.resolve(HERE, '../../../parity');
const MODS = ['00-namespace.js', '00-util.js', 'api.js', '11-data-live.js', '20-clock.js', '30-model.js'];

/** The recordings and the page streams made of them. hosted: the stream is a hosted tab's, whose tool rows and refusals come from the
 *  agent's sink, which the golden test has no sink for; the other streams are a followed or replayed log's, which read every row from the log. */
const RECORDINGS = [
  { name: 'handbook', streams: [{ file: path.join(TRANSLATE, 'demo-handbook.ui.jsonl'), hosted: true }, { file: path.join(TRANSLATE, 'demo-handbook-watch.ui.jsonl'), hosted: false }] },
  { name: 'shop', streams: [{ file: path.join(TRANSLATE, 'demo-shop.ui.jsonl'), hosted: true }, { file: path.join(TRANSLATE, 'demo-shop-watch.ui.jsonl'), hosted: false }] },
  { name: 'team', streams: [{ file: path.join(PARITY, 'testdata', 'team.ui.jsonl'), hosted: false }] },
];
/** The facts that a hosted tab's stream does not carry because they come from the sink. */
const SINK_FED = new Set(['tool_calls', 'tool_errors', 'permission_denials']);
const TOLERANCE = 1.5e-6;

const readJSON = f => JSON.parse(fs.readFileSync(f, 'utf8'));
const readStream = f => fs.readFileSync(f, 'utf8').split('\n').filter(Boolean).map(l => JSON.parse(l));
const num = id => { const m = /^T(\d+)$/.exec(id); return m ? +m[1] : Infinity; };
const taskLess = (a, b) => num(a) - num(b) || (a < b ? -1 : a > b ? 1 : 0);

/** The state words of the facts: the page's states, with think, tool and edit one. */
const CLASS = { think: 'working', tool: 'working', edit: 'working', wait: 'waiting', ask: 'asking', idle: 'idle', done: 'done', stuck: 'stuck' };

/** The agents a stream talks about, as a roster, the manager first (the stream carries no roster frame; the server sends one). */
function rosterOf(events) {
  const ids = ['mgr'];
  events.forEach(e => { const id = e.k === 'ask' ? e.q.agent : e.id; if (id && /^[a-z]+-\d+$/.test(id) && !ids.includes(id)) ids.push(id); });
  return ids.map((id, i) => ({ id, role: id === 'mgr' ? 'manager' : 'worker', code: id.split('-')[0], nth: 1, k: i, leg: i - 1, scope: '', ro: false, model: 'm', spawn: 0 }));
}

/** Reduce a stream into a model with the page's reducer (the view model: with the chat rows). */
function reduceStream(events) {
  const SL = load(context({}), MODS).SL;
  const m = SL.model.newModel({ id: 't', meta: { goalText: '' }, roster: rosterOf(events) }, {});
  events.forEach(e => SL.model.reduce(m, e));
  return { SL, m };
}

/** The facts the page computes from a stream. Their definitions are the header of internal/parity/facts_test.go. */
export function webFacts(events) {
  const { SL, m } = reduceStream(events);
  const T = SL.calc.totals(m);
  const workers = m.order.filter(id => id !== 'mgr');
  const byState = {};
  workers.forEach(id => { const c = CLASS[m.ag[id].state] || 'working'; byState[c] = (byState[c] || 0) + 1; });
  const status = { todo: 0, running: 0, verify: 0, merged: 0, failed: 0 };
  m.torder.forEach(id => { const t = m.tasks[id]; status[t.failed ? 'failed' : t.st] = (status[t.failed ? 'failed' : t.st] || 0) + 1; });
  const rows = Object.values(m.chan).flat().filter(e => e.k === 'tool');
  const ms = m.mailstat || {};
  const g = m.goal || {};
  return {
    agents: m.order.length,
    manager_state: m.ag.mgr ? CLASS[m.ag.mgr.state] || 'working' : 'none',
    workers_by_state: byState,
    tasks: m.torder.length,
    tasks_by_status: status,
    merged_tasks: m.merged.slice().sort(taskLess),
    merge_conflicts: m.conflicts,
    merge_bounced: m.bounced,
    tokens_prompt: T.prompt,
    tokens_cache_read: T.read,
    tokens_cache_write: T.wr,
    tokens_output: T.out,
    cost_usd: T.cost,
    savings_usd: T.saved,
    cache_hit_ratio: T.hit,
    main_responses: m.order.reduce((n, id) => n + m.ag[id].nreq, 0),
    tool_calls: T.calls,
    tool_errors: rows.filter(e => e.ok === false).length,
    permission_denials: m.refused,
    questions_asked: m.qs.length,
    checkpoints: m.ckpts.length,
    checkpoint_files: m.ckpts.reduce((n, c) => n + (c.files || 0), 0),
    mail_sent: ms.sent || 0,
    mail_delivered: ms.delivered || 0,
    mail_dropped: ms.dropped || 0,
    goal_state: g.state,
    goal_turns: g.turns || 0,
    goal_verdict: m.verdictKind || '',
    stalls_open: Object.keys(m.stalls).length,
    handovers: m.handovers.length,
    alerts_open: Object.keys(m.alerts).length,
    compactions: m.compactions.length,
    cache_breaks: m.anomalies.length,
    retries: m.retries,
    rate_limited: m.r429,
  };
}

/** Whether two values of one fact are the same: numbers within the tolerance, everything else exactly. */
export function sameFact(a, b) {
  if (typeof a === 'number' && typeof b === 'number') return Math.abs(a - b) <= TOLERANCE;
  if (Array.isArray(a) || Array.isArray(b)) return Array.isArray(a) && Array.isArray(b) && a.length === b.length && a.every((v, i) => sameFact(v, b[i]));
  if (a && b && typeof a === 'object' && typeof b === 'object') {
    const ka = Object.keys(a), kb = Object.keys(b);
    return ka.length === kb.length && ka.every(k => k in b && sameFact(a[k], b[k]));
  }
  return a === b;
}

/** Every fact of a recording on which the page differs from the file in one stream: [{item, stream, want, got}]. A fact the page does not
 *  compute, or computes and the file lacks, differs too (its page value is undefined, or its file value). */
export function differences(recording, want, got, stream, hosted) {
  const out = [];
  const names = [...new Set([...Object.keys(want), ...Object.keys(got)])].sort();
  for (const k of names) {
    if (hosted && SINK_FED.has(k)) continue;
    if (!(k in want) || !(k in got) || !sameFact(want[k], got[k])) out.push({ item: recording + '/' + k, stream, want: want[k], got: got[k] });
  }
  return out;
}

const spell = v => v === undefined ? 'nothing' : JSON.stringify(v);

/** What the contract and the differences say, as problems: a difference nobody listed, a listed fact that no longer differs, an entry
 *  without a reason, a fact listed twice or under web_only (which means nothing for a fact: the file holds the terminal's number). */
export function judge(contract, diffs, allItems) {
  const problems = [];
  const listed = new Map();
  const differing = new Map();
  diffs.forEach(d => { if (!differing.has(d.item)) differing.set(d.item, d); });
  if ((contract.web_only || []).length) problems.push('facts: web_only means nothing for a fact: the file holds the terminal\'s number, and a page number that differs from it is a terminal_only or a gap');
  for (const list of ['terminal_only', 'gaps']) {
    for (const e of contract[list] || []) {
      if (!e.item) { problems.push('facts: an entry under ' + list + ' has no item'); continue; }
      if (String(e.reason || '').trim().length < 15) problems.push('facts: "' + e.item + '" under ' + list + ' needs a reason (at least 15 characters)');
      if (listed.has(e.item)) problems.push('facts: "' + e.item + '" is listed under ' + listed.get(e.item) + ' and under ' + list);
      listed.set(e.item, list);
      if (!allItems.has(e.item)) problems.push('facts: "' + e.item + '" under ' + list + ' names no fact of a recording');
      else if (!differing.has(e.item)) problems.push('facts: "' + e.item + '" under ' + list + ' is stale: the page computes the terminal\'s number now, so the difference is closed; remove it');
    }
  }
  for (const [item, d] of [...differing].sort((a, b) => a[0] < b[0] ? -1 : 1)) {
    if (listed.has(item)) continue;
    const [rec, fact] = [item.slice(0, item.indexOf('/')), item.slice(item.indexOf('/') + 1)];
    problems.push('recording "' + rec + '", fact "' + fact + '", stream ' + path.basename(d.stream) + ': the terminal\'s State computes ' + spell(d.want) + ' (internal/parity/testdata/facts-' + rec + '.json), the page\'s reducer computes ' + spell(d.got) +
      '. A fix to how an event is read has to be made in both interfaces; or list "' + item + '" in internal/parity/contract/facts.json with the reason it differs (a difference that is to be closed goes under gaps).');
  }
  return problems;
}

// ---- the recordings, computed once -------------------------------------------------------------------------------------------------

const contract = readJSON(path.join(PARITY, 'contract', 'facts.json'));
const files = {}, results = [];
for (const r of RECORDINGS) {
  files[r.name] = readJSON(path.join(PARITY, 'testdata', 'facts-' + r.name + '.json'));
  for (const s of r.streams) {
    const events = readStream(s.file);
    results.push({ recording: r.name, stream: s.file, hosted: s.hosted, events, facts: webFacts(events), want: files[r.name] });
  }
}
const allItems = new Set(Object.entries(files).flatMap(([rec, f]) => Object.keys(f).map(k => rec + '/' + k)));
const allDiffs = results.flatMap(r => differences(r.recording, r.want, r.facts, r.stream, r.hosted));

test('the recordings, their streams and their facts exist', () => {
  for (const r of RECORDINGS) {
    for (const s of r.streams) assert.ok(readStream(s.file).length > 50, s.file + ' holds a page stream');
    assert.ok(Object.keys(files[r.name]).length >= 12, r.name + ': at least twelve facts');
  }
  const names = Object.keys(files.shop);
  for (const r of RECORDINGS) assert.deepEqual(Object.keys(files[r.name]).sort(), names.sort(), r.name + ' has the facts the other recordings have');
  assert.deepEqual(Object.keys(webFacts(results[0].events)).sort(), names.sort(), 'the page computes exactly the facts of the files');
});

test('every fact the terminal computes, the page computes from the same recording', () => {
  const problems = judge({ terminal_only: contract.terminal_only, gaps: contract.gaps, web_only: contract.web_only }, allDiffs, allItems);
  assert.equal(problems.length, 0, '\n' + problems.join('\n'));
});

test('the gaps are reported on every run', t => {
  for (const g of contract.gaps || []) t.diagnostic('facts: gap "' + g.item + '": ' + g.reason);
});

// ---- the guard guards ---------------------------------------------------------------------------------------------------------------

test('a stream that the page reads differently is found: without its use events or its merged task events the facts move', () => {
  const r = results.find(x => x.recording === 'shop' && !x.hosted);
  const moved = events => differences('shop', r.want, webFacts(events), r.stream, false).map(x => x.item.split('/')[1]);
  assert.ok(!allDiffs.some(x => x.item === 'shop/tokens_output' || x.item === 'shop/tasks_by_status'), 'the real stream agrees on tokens and task columns');
  const noUse = moved(r.events.filter(e => e.k !== 'use'));
  for (const f of ['tokens_prompt', 'tokens_output', 'tokens_cache_read', 'cost_usd', 'savings_usd', 'cache_hit_ratio']) assert.ok(noUse.includes(f), 'without use events ' + f + ' moves: ' + noUse.join(', '));
  const noMerged = moved(r.events.filter(e => !(e.k === 'task' && e.s === 'merged') && e.k !== 'merge'));
  for (const f of ['tasks_by_status', 'merged_tasks']) assert.ok(noMerged.includes(f), 'without the merged tasks ' + f + ' moves: ' + noMerged.join(', '));
});

test('a changed number of the terminal is found, and named with the fact, the recording and both values', () => {
  const r = results.find(x => x.recording === 'shop' && !x.hosted);
  const changed = { ...r.want, tokens_output: r.want.tokens_output + 1, tasks_by_status: { ...r.want.tasks_by_status, merged: 7 } };
  const diffs = differences('shop', changed, r.facts, r.stream, false);
  const problems = judge({}, diffs, allItems);
  const text = problems.join('\n');
  assert.match(text, /recording "shop", fact "tokens_output", stream demo-shop-watch\.ui\.jsonl: the terminal's State computes 3941 .*the page's reducer computes 3940/);
  assert.match(text, /fact "tasks_by_status".*"merged":7.*"merged":8/);
});

test('a listed difference passes, and one that has closed, one without a reason and a web_only entry fail', () => {
  const r = results.find(x => x.recording === 'shop' && !x.hosted);
  const changed = { ...r.want, tool_calls: 99 };
  const diffs = differences('shop', changed, r.facts, r.stream, false);
  assert.deepEqual(judge({ gaps: [{ item: 'shop/tool_calls', reason: 'the page lacks five rows of the shop recording' }] }, diffs, allItems), []);
  assert.deepEqual(judge({ terminal_only: [{ item: 'shop/tool_calls', reason: 'the page has no such row, on purpose' }] }, diffs, allItems), []);
  const stale = judge({ gaps: [{ item: 'shop/tasks', reason: 'this difference was closed some time ago' }] }, [], allItems).join('\n');
  assert.match(stale, /"shop\/tasks" under gaps is stale/);
  assert.match(judge({ gaps: [{ item: 'shop/tool_calls', reason: 'short' }] }, diffs, allItems).join('\n'), /needs a reason/);
  assert.match(judge({ web_only: [{ item: 'shop/tool_calls', reason: 'meaningless for a fact in every way' }] }, diffs, allItems).join('\n'), /web_only means nothing/);
  assert.match(judge({ gaps: [{ item: 'shop/nope', reason: 'this fact does not exist at all' }] }, [], allItems).join('\n'), /names no fact of a recording/);
  assert.match(judge({ gaps: [{ item: 'shop/tool_calls', reason: 'listed under two headings as well' }], terminal_only: [{ item: 'shop/tool_calls', reason: 'listed under two headings as well' }] }, diffs, allItems).join('\n'), /is listed under/);
});

test('a hosted stream is not asked for the facts that come from the sink; a followed one is', () => {
  const hosted = results.find(x => x.recording === 'shop' && x.hosted);
  const watched = results.find(x => x.recording === 'shop' && !x.hosted);
  assert.equal(hosted.facts.tool_calls, 0, 'a hosted tab\'s golden stream has no sink: no tool rows');
  assert.ok(!differences('shop', hosted.want, hosted.facts, hosted.stream, true).some(d => SINK_FED.has(d.item.split('/')[1])));
  assert.ok(differences('shop', hosted.want, hosted.facts, hosted.stream, false).some(d => d.item === 'shop/tool_calls'));
  assert.ok(watched.facts.tool_calls > 0);
});

test('sameFact: numbers within a millionth, lists and maps whole', () => {
  assert.ok(sameFact(0.0914905, 0.0914906));
  assert.ok(!sameFact(0.09, 0.0914));
  assert.ok(sameFact({ a: [1, 2], b: 'x' }, { b: 'x', a: [1, 2] }));
  assert.ok(!sameFact({ a: [1, 2] }, { a: [1, 2, 3] }));
  assert.ok(!sameFact({ a: 1 }, { a: 1, b: 2 }));
  assert.ok(!sameFact('1', 1));
});
