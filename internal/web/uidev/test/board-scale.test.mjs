// board-scale.test.mjs: the task board, its Dependencies graph and the Cockpit's merge queue with a large team (82-ui-board.js,
// 90-views-a.js, 83-ui-cockpit.js), on the shipped page booted on the fake DOM of pagedom.mjs. A column's count is the number of its
// tasks and every one of them is in it (a subtask whose parent is in another column too); past COMPACT_AT merged tasks the merged
// column is one row per task, newest merge first; the graph folds the merged tasks nothing open waits on into one node and never
// draws two nodes over each other; task ids are in numeric order, never string order (T1 T10 T100 T11).
import test from 'node:test';
import assert from 'node:assert/strict';
import { boot } from './pagedom.mjs';

const col = (L, k) => L.$('.kan [data-col="' + k + '"]');
const count = (L, k) => Number(col(L, k).querySelector('.cnt').textContent.split(' ')[0]);
const cards = (L, k) => col(L, k).querySelectorAll('.tcard').map(n => n.dataset.task);
const top = (L, k) => col(L, k).querySelector('.cl').children.filter(n => n.classList.contains('tcard')).map(n => n.dataset.task);

test('a subtask is drawn in the column of its own status, and each column counts the tasks it shows', async () => {
  const L = await boot();
  await L.event('task', { id: 'T5', title: 'Pool of docs', owner: 'be-1', s: 'running', poolPhase: 'executing', poolMembers: ['be-1', 'fe-1'] });
  await L.event('task', { id: 'T6', title: 'docs one', parent: 'T5', s: 'todo' });
  await L.event('task', { id: 'T7', title: 'docs two', parent: 'T5', s: 'todo' });
  await L.event('task', { id: 'T8', title: 'docs three', parent: 'T5', owner: 'fe-1', s: 'running' });
  await L.event('task', { id: 'T9', title: 'docs four', parent: 'T5', owner: 'fe-1', s: 'merged' });
  await L.step(10); L.SL.views.show('board'); await L.settle();
  assert.deepEqual(cards(L, 'todo'), ['T6', 'T7'], 'the subtasks waiting are in todo, not hidden in their running parent');
  assert.equal(count(L, 'todo'), 2);
  for (const id of ['T6', 'T7']) assert.match(L.$('.tcard[data-task="' + id + '"]').textContent, /↳ T5/, 'the card names its parent');
  assert.deepEqual(cards(L, 'running'), ['T5', 'T8'], 'a subtask in its parent\'s column is inside the parent\'s card');
  assert.ok(L.$('.tcard[data-task="T5"] .subtasks .tcard[data-task="T8"]'));
  assert.equal(count(L, 'running'), 2);
  assert.deepEqual(cards(L, 'merged'), ['T9']); assert.equal(count(L, 'merged'), 1);
  const parent = L.$('.tcard[data-task="T5"]');
  assert.match(parent.textContent, /▾ 1 subtask\b/); assert.match(parent.textContent, /\+ 2 in todo · 1 merged/, 'the parent says where its other subtasks are');
  parent.querySelector('[data-pool-toggle]').click(); await L.settle();
  assert.equal(L.$('.tcard[data-task="T8"]'), null, 'folded');
  assert.match(L.$('.tcard[data-task="T5"]').textContent, /▸ 1 subtask\b/, 'the folded toggle still counts what it holds');
  assert.equal(count(L, 'running'), 2);
  await L.event('task', { id: 'T6', title: 'docs one', parent: 'T5', owner: 'be-1', s: 'running' }); await L.step(1);
  assert.deepEqual(cards(L, 'todo'), ['T7']); assert.equal(count(L, 'todo'), 1);
  assert.match(L.$('.tcard[data-task="T5"]').textContent, /▸ 2 subtasks/, 'the subtask that started joins its parent\'s card');
  for (const k of ['todo', 'running', 'verify', 'merged']) assert.equal(L.$('.kan [data-col="' + k + '"] .bempty') !== null, count(L, k) === 0, k + ': "nothing" exactly when the count is 0');
});

test('a column\'s count matches its cards on a large team: every task is in the column of its status', async () => {
  const L = await boot();
  /* the shape of a long run: pool parents running, their subtasks waiting, and a long merged column */
  for (let i = 1; i <= 30; i++) await L.event('task', { id: 'T' + i, title: 'work ' + i, owner: 'be-1', s: 'merged' });
  await L.event('task', { id: 'T40', title: 'docs pool', owner: 'fe-1', s: 'running', poolPhase: 'executing', poolMembers: ['fe-1'] });
  for (let i = 41; i <= 46; i++) await L.event('task', { id: 'T' + i, title: 'docs ' + i, parent: 'T40', s: 'todo' });
  await L.step(10); L.SL.views.show('board'); await L.settle();
  const m = L.SL.sessions.active.m;
  for (const k of ['todo', 'running', 'verify', 'merged']) {
    const ids = m.torder.filter(id => m.tasks[id].st === k);
    assert.equal(count(L, k), ids.length, k + ': the head counts every task of the status');
    if (k !== 'merged') assert.deepEqual(cards(L, k).slice().sort(), [...ids].sort(), k + ': and every one of them is drawn');   /* [...]: an array of this realm */
  }
  assert.equal(count(L, 'todo'), 6); assert.deepEqual(top(L, 'todo'), ['T41', 'T42', 'T43', 'T44', 'T45', 'T46']);
});

test('past COMPACT_AT merged tasks the merged column is one row per task, newest merge first, with its time; its subtasks are reachable', async () => {
  const L = await boot(), n = L.SL.ui.board.COMPACT_AT + 4, order = [];
  for (let i = 1; i <= n; i++) await L.event('task', { id: 'T' + i, title: 'task ' + i, owner: i % 2 ? 'be-1' : 'fe-1', s: 'running' });
  await L.event('task', { id: 'T30', title: 'subtask of T3', parent: 'T3', owner: 'fe-1', s: 'running' });
  /* merged out of id order: T2 last, the subtask T30 right after its parent */
  const mergeOrder = [];
  for (let i = 1; i <= n; i++) if (i !== 2) mergeOrder.push('T' + i);
  mergeOrder.splice(2, 0, 'T30'); mergeOrder.push('T2');
  for (const id of mergeOrder) { await L.event('merge', { id, cmd: 'go test ./...', ms: 900 }); order.unshift(id); }
  await L.step(10); L.SL.views.show('board'); await L.settle();
  assert.equal(count(L, 'merged'), n + 1);
  const rows = col(L, 'merged').querySelector('.cl').children;
  assert.ok(rows.every(r => r.classList.contains('trow')), 'one-line rows');
  assert.deepEqual(rows.map(r => r.dataset.task), order.filter(id => id !== 'T30'), 'newest merge first');
  assert.match(rows[0].querySelector('.tat').textContent, /^\d\d:\d\d$/, 'the time of the merge');
  assert.match(rows[0].textContent, /T2.*fe-1.*task 2/, 'id, owner and title on the line');
  assert.equal(L.$('.tcard[data-task="T30"]'), null, 'a merged parent\'s subtasks are folded in it');
  const t3 = L.$('.tcard[data-task="T3"]'), toggle = t3.querySelector('[data-pool-toggle]');
  assert.equal(toggle.textContent, '▸ 1'); assert.equal(toggle.getAttribute('aria-expanded'), 'false');
  toggle.click(); await L.settle();
  assert.ok(L.$('.tcard[data-task="T3"] .subtasks .tcard[data-task="T30"].trow'), 'and one click away');
  /* a task reopened and merged again is the newest */
  await L.event('task', { id: 'T5', title: 'task 5', owner: 'be-1', s: 'running' }); await L.event('merge', { id: 'T5', cmd: 'go test ./...', ms: 900 }); await L.step(1);
  assert.equal(col(L, 'merged').querySelector('.cl').children[0].dataset.task, 'T5');
  assert.ok(L.$('.kan').classList.contains('compact-merged'));
  assert.equal(L.$('.kan').style['--bw-verify'] || L.$('.kan').style.getPropertyValue('--bw-verify'), '.5fr', 'an empty column is narrower');
});

test('a few merged tasks stay cards; a failed task in todo is counted apart, so the count still covers every card', async () => {
  const L = await boot();
  for (let i = 1; i <= 3; i++) await L.event('task', { id: 'T' + i, title: 'task ' + i, owner: 'be-1', s: 'merged' });
  await L.event('task', { id: 'T4', title: 'task 4', s: 'todo', failed: true, closure: 'attempts exhausted' });
  await L.event('task', { id: 'T5', title: 'task 5', s: 'todo' });
  await L.step(10); L.SL.views.show('board'); await L.settle();
  assert.ok(col(L, 'merged').querySelectorAll('.tcard').every(c => !c.classList.contains('trow')));
  assert.deepEqual(top(L, 'merged'), ['T3', 'T2', 'T1'], 'newest first');
  assert.equal(col(L, 'todo').querySelector('.cnt').textContent, '1 + 1 ✗');
  assert.deepEqual(cards(L, 'todo'), ['T4', 'T5']);
});

test('the merge queue names the newest merges first, in numeric order, and counts the rest', async () => {
  const L = await boot(), ids = [];
  for (let i = 1; i <= 120; i++) ids.push('T' + i);
  for (const id of ids) { await L.event('task', { id, title: 'task ' + id, owner: 'be-1', s: 'running' }); }
  for (const id of ids) await L.event('merge', { id, cmd: 'go test ./...', ms: 500 });
  await L.step(10); L.SL.views.show('cockpit'); await L.step(0.1);
  const meta = L.$('.p-queue .qmeta').textContent;
  assert.match(meta, /^merged T120 T119 T118 T117 T116 T115 T114 T113 T112 T111 T110 T109 and 108 earlier/, meta);
  assert.equal(L.$('.p-queue .qsum').textContent, '120 merged');
  assert.deepEqual(['T10', 'T2', 'T100', 'T1', 'T11', 'be-10', 'be-2'].sort(L.SL.ui.board.idCmp), ['T1', 'T2', 'T10', 'T11', 'T100', 'be-2', 'be-10']);
});

/** A model of a run with `merged` merged tasks, `open` unfinished ones, and the dependencies `deps` (id -> [ids]). */
function model(merged, open, deps) {
  const m = { tasks: {}, torder: [], merged: [], goal: { state: 'active' } };
  const add = (id, st) => { m.tasks[id] = { id, title: 'a task with a rather long title ' + id, owner: 'be-1', deps: (deps && deps[id]) || [], st, t: m.torder.length }; m.torder.push(id); if (st === 'merged') m.merged.push(id); };
  for (let i = 1; i <= merged; i++) add('T' + i, 'merged');
  for (let i = merged + 1; i <= merged + open; i++) add('T' + i, i % 3 ? 'todo' : 'running');
  return m;
}
/** Every pair of node boxes of a layout, checked apart. */
function assertApart(lay, DG) {
  const ps = Array.from(lay.pos.entries());
  for (let i = 0; i < ps.length; i++) for (let j = i + 1; j < ps.length; j++) {
    const [a, p] = ps[i], [b, q] = ps[j];
    const apart = p.x + DG.nw <= q.x || q.x + DG.nw <= p.x || p.y + DG.nh <= q.y || q.y + DG.nh <= p.y;
    assert.ok(apart, a + ' and ' + b + ' overlap: ' + JSON.stringify([p, q]));
  }
  for (const [id, p] of ps) assert.ok(p.x >= 0 && p.y >= 0 && p.x + DG.nw <= lay.W && p.y + DG.nh <= lay.H, id + ' is outside the drawing');
}

test('the Dependencies graph of a small team draws every task, in the box, apart', async () => {
  const L = await boot(), lay = L.SL.ui.depLayout, DG = lay.DG;
  const m = model(5, 3, { T3: ['T1'], T4: ['T1', 'T2'], T6: ['T3', 'T4'], T7: ['T6'], T8: ['T5'] });
  const g = lay(m, 1300, 300);
  assert.equal(g.folded.length, 0, 'nothing folded');
  assert.equal(g.pos.size, 9, 'eight tasks and the goal');
  assert.equal(g.W, 1300); assert.equal(g.H, 300, 'it fits the box');
  assert.ok(g.pos.get('T7').x > g.pos.get('T6').x && g.pos.get('T6').x > g.pos.get('T3').x && g.pos.get('T3').x > g.pos.get('T1').x, 'a task is right of what it waits for');
  assertApart(g, DG);
});

test('the Dependencies graph of a long run folds the settled merged tasks into one node and keeps what open work waits on', async () => {
  const L = await boot(), lay = L.SL.ui.depLayout, DG = lay.DG;
  /* 125 merged; 15 open, three of which wait on a merged task and one on another open task */
  const m = model(125, 15, { T126: ['T7'], T130: ['T100', 'T126'], T131: ['T120'] });
  const g = lay(m, 1340, 360);
  assert.equal(g.folded.length, 125 - 3, 'the merged tasks nothing open waits on');
  for (const id of ['T7', 'T100', 'T120']) assert.ok(g.pos.has(id), id + ', which open work waits on, is drawn');
  for (let i = 126; i <= 140; i++) assert.ok(g.pos.has('T' + i), 'T' + i + ' (open) is drawn');
  assert.ok(g.pos.has(lay.FOLD) && g.pos.has(lay.GOAL));
  assert.equal(g.W, 1340); assert.equal(g.H, 360, 'it fits the box');
  assertApart(g, DG);
  assert.ok(g.edges.some(([a, b]) => a === 'T126' && b === 'T130'));
  /* drawn in the Board view: the fold says what it holds, the head says it folded */
  const S = L.SL.sessions.active; S.m.tasks = m.tasks; S.m.torder = m.torder; S.m.merged = m.merged;
  await L.step(10); L.SL.views.show('board'); await L.settle();
  assert.match(L.$('.dg .nd.fold').textContent, /✓ 122 merged/);
  assert.match(L.$('.dgsum').textContent, /122 merged tasks folded into one/);
  assert.equal(L.doc.querySelectorAll('.dg .nd').length, 3 + 15 + 2);
});

test('the Dependencies graph never overlaps: too much open work makes the drawing larger than the box', async () => {
  const L = await boot(), lay = L.SL.ui.depLayout, DG = lay.DG;
  const deps = {}; for (let i = 61; i <= 70; i++) deps['T' + i] = ['T' + (i - 1)];   /* a chain of ten, deeper than the box is wide */
  const m = model(20, 50, deps), g = lay(m, 900, 260);
  assert.ok(g.W > 900 || g.H > 260, 'larger than the box: its host scrolls');
  assertApart(g, DG);
  const cyc = model(0, 4, { T1: ['T2'], T2: ['T1'] });   /* a loop of dependencies is drawn, not followed for ever */
  assertApart(lay(cyc, 900, 260), DG);
});

test('a task resolved as no longer needed is not counted or listed as merged, and its card says resolved', async () => {
  const L = await boot();
  await L.event('task', { id: 'T1', title: 'wired', owner: 'be-1', s: 'merged', closure: 'verified' });
  await L.event('task', { id: 'T2', title: 'superseded wiring', s: 'merged', closure: 'unneeded' });
  await L.step(10); L.SL.views.show('board'); await L.settle();
  assert.match(L.$('.bbsum').textContent, /^1 merged · .* · 1 resolved\b/, 'the summary counts merged work only');
  const card = L.$('.tcard[data-task="T2"]').textContent;
  assert.match(card, /resolved/); assert.doesNotMatch(card, /✓/, 'nothing of it was merged');
  assert.match(L.$('.tcard[data-task="T1"]').textContent, /✓/);
  L.SL.views.show('cockpit'); await L.settle();
  const q = L.$('.p-queue .qmeta').textContent;
  assert.match(q, /merged T1/); assert.doesNotMatch(q, /T2/, 'the merge queue lists only what was merged');
});

test('past COMPACT_AT, a resolved task\'s row says resolved, and the graph\'s fold counts it apart from the merged ones', async () => {
  const L = await boot(), n = L.SL.ui.board.COMPACT_AT + 2;
  for (let i = 1; i <= n; i++) await L.event('task', { id: 'T' + i, title: 'task ' + i, owner: 'be-1', s: 'merged', closure: 'verified' });
  await L.event('task', { id: 'T99', title: 'superseded', s: 'merged', closure: 'unneeded' });
  await L.step(10); L.SL.views.show('board'); await L.settle();
  const row = L.$('.tcard[data-task="T99"]');
  assert.ok(row.classList.contains('trow'), 'a compact row');
  assert.match(row.textContent, /resolved/); assert.match(row.getAttribute('title'), /resolved: no longer needed, nothing merged/);
  assert.doesNotMatch(row.getAttribute('title'), / merged \d/, 'no merge time');
  assert.match(L.$('.dg .nd.fold').textContent, new RegExp('✓ ' + n + ' merged · 1 resolved'));
  assert.match(L.$('.dgsum').textContent, new RegExp(n + ' merged · 1 resolved tasks folded into one'));
});

test('a fold of resolved tasks only is not drawn as merged work', async () => {
  const L = await boot(), m = model(125, 15, {});
  Object.values(m.tasks).forEach(t => { if (t.st === 'merged') t.closure = 'unneeded'; });
  const S = L.SL.sessions.active; S.m.tasks = m.tasks; S.m.torder = m.torder; S.m.merged = [];
  await L.step(10); L.SL.views.show('board'); await L.settle();
  const fold = L.$('.dg .nd.fold');
  assert.equal(fold.dataset.st, 'resolved'); assert.match(fold.textContent, /– 125 resolved/); assert.doesNotMatch(fold.textContent, /✓/);
});
