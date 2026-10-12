// dash-tour.test.mjs: the Cockpit's dashboard (83b-ui-dash.js) and the guided tour (95-ui-tour.js). The layout functions and the
// tour's order and storage are pure and are tested on their own; the grid, the lock, the keys of an unlocked card, the cards of views,
// the tour's keys and its ways back (the Help sheet, the palette, Settings) are tested on the shipped page booted on the fake DOM.
import test from 'node:test';
import assert from 'node:assert/strict';
import { page } from './pages-harness.mjs';
import { boot, FakeEvent } from './pagedom.mjs';

test('disposing a dragged card cancels its listeners and does not persist its transient layout', async () => {
  for (const remove of [false, true]) {
    const L = await boot(), D = L.SL.ui.dash;
    D.setLocked(false);
    L.$('.dash').dataset.stack = '0';
    const el = L.$('.dcard[data-card="gov"]');
    el.dispatchEvent(new FakeEvent('pointerdown', { bubbles: true, button: 0, pointerId: 1, clientX: 0, clientY: 0 }));
    assert.equal(L.doc.body.classList.contains('resizing'), true);
    el.dispatchEvent(new FakeEvent('pointermove', { clientX: 0, clientY: 100 }));
    if (remove) D.remove('gov'); else L.SL.views.show('cache');
    await L.settle();
    assert.equal(L.doc.body.classList.contains('resizing'), false);
    assert.equal(el.classList.contains('dragging'), false);
    const stored = L.store.get(L.SL.dash.KEY);
    el.dispatchEvent(new FakeEvent('pointerup'));
    assert.equal(L.store.get(L.SL.dash.KEY), stored, 'detached pointerup cannot commit');
    if (!remove) assert.equal(stored, undefined);
  }
});

test('pointer cancellation restores the original layout, and a restarted drag reads that restored position', async () => {
  const L = await boot(), D = L.SL.ui.dash;
  D.setLocked(false); L.$('.dash').dataset.stack = '0';
  const el = L.$('.dcard[data-card="gov"]'), original = plain(D.layout()), stored = L.store.get(L.SL.dash.KEY);
  const down = () => el.dispatchEvent(new FakeEvent('pointerdown', { bubbles: true, button: 0, pointerId: 1, clientX: 0, clientY: 0 }));
  const move = () => el.dispatchEvent(new FakeEvent('pointermove', { clientX: 0, clientY: 100 }));
  down(); move(); const preview = plain(D.layout());
  assert.notDeepEqual(preview, original, 'the drag actually moved');
  el.dispatchEvent(new FakeEvent('pointercancel'));
  assert.deepEqual(plain(D.layout()), original);
  assert.equal(L.store.get(L.SL.dash.KEY), stored, 'cancel does not save');
  assert.equal(el.classList.contains('dragging'), false);
  assert.equal(L.doc.body.classList.contains('resizing'), false);
  down(); move(); down();
  assert.deepEqual(plain(D.layout()), original, 'a new pointerdown cancels the preview');
  move();
  assert.deepEqual(plain(D.layout()), preview, 'the restarted drag uses the original position');
  el.dispatchEvent(new FakeEvent('pointerup'));
  assert.notEqual(L.store.get(L.SL.dash.KEY), stored, 'pointerup still saves');
});

const plain = x => JSON.parse(JSON.stringify(x));
const dash = () => page({ modules: ['83b-ui-dash.js'] }).SL.dash;
const tour = () => page({ modules: ['95-ui-tour.js'] }).SL.tour;
const memStore = init => { const m = new Map(Object.entries(init || {})); return { getItem: k => (m.has(k) ? m.get(k) : null), setItem: (k, v) => m.set(k, String(v)), removeItem: k => m.delete(k), m }; };
const noOverlap = (D, L) => L.every((a, i) => L.every((b, j) => i === j || !D.overlaps(a, b)));
const at = (L, id) => { const c = L.find(k => k.id === id); return c ? [c.x, c.y, c.w, c.h] : null; };

test('the default layout is the Cockpit: its seven panels, no overlap, inside 12 columns, filling the rows of one screen', () => {
  const D = dash();
  assert.deepEqual(plain(D.DEFAULT.map(c => c.id)), ['hero', 'team', 'gantt', 'tasks', 'queue', 'mail', 'gov']);
  assert.ok(noOverlap(D, D.DEFAULT));
  assert.ok(D.DEFAULT.every(c => c.x >= 0 && c.x + c.w <= D.COLS));
  assert.equal(D.rows(D.DEFAULT), D.ROWS_FIT);
  assert.ok(D.CATALOG.filter(c => c.kind === 'view').length >= 10, 'every view of the rail is a card');
});

test('a stored layout: read back as saved; another version, broken JSON, no storage or a throwing storage give the default', () => {
  const D = dash(), s = memStore();
  assert.deepEqual(plain(D.load(s)), plain(D.DEFAULT), 'nothing stored');
  const mine = [{ id: 'gantt', x: 0, y: 0, w: 12, h: 5 }, { id: 'v:cache', x: 0, y: 5, w: 6, h: 9 }];
  assert.equal(D.save(s, mine), true);
  assert.deepEqual(plain(D.load(s)), mine);
  s.setItem(D.KEY, JSON.stringify({ v: 0, cards: mine })); assert.deepEqual(plain(D.load(s)), plain(D.DEFAULT), 'an older version');
  s.setItem(D.KEY, '{not json'); assert.deepEqual(plain(D.load(s)), plain(D.DEFAULT), 'unreadable');
  assert.deepEqual(plain(D.load(null)), plain(D.DEFAULT), 'no storage');
  const bad = { getItem() { throw new Error('blocked'); }, setItem() { throw new Error('blocked'); } };
  assert.deepEqual(plain(D.load(bad)), plain(D.DEFAULT)); assert.equal(D.save(bad, mine), false);
  D.forget(s); assert.equal(s.getItem(D.KEY), null);
});

test('normalize: unknown and repeated cards leave, sizes and places are clamped, overlaps are pushed down', () => {
  const D = dash();
  assert.equal(D.normalize(null), null); assert.equal(D.normalize({ v: 1 }), null); assert.equal(D.normalize({ v: 2, cards: [] }), null);
  assert.deepEqual(plain(D.normalize({ v: 1, cards: [] })), [], 'an empty Cockpit is a layout');
  const L = D.normalize({ v: 1, cards: [{ id: 'nope', x: 0, y: 0, w: 2, h: 2 }, { id: 'gov', x: 11, y: -3, w: 1, h: 99 }, { id: 'gov', x: 0, y: 0, w: 3, h: 3 }, { id: 'queue', x: 'a', y: 0, w: 4, h: 3 }, null] });
  assert.deepEqual(plain(L.map(c => c.id)), ['gov', 'queue']);
  assert.deepEqual(at(L, 'gov'), [10, 0, 2, D.MAX_H], 'w raised to its minimum and kept inside, h to the maximum, y to 0');
  assert.deepEqual(at(L, 'queue'), [0, 0, 4, 3]);
  const O = D.normalize({ v: 1, cards: [{ id: 'gantt', x: 0, y: 0, w: 6, h: 7 }, { id: 'tasks', x: 2, y: 3, w: 6, h: 7 }] });
  assert.ok(noOverlap(D, O)); assert.deepEqual(at(O, 'tasks'), [2, 7, 6, 7]);
});

test('move, resize, add, remove and the keys of a focused card', () => {
  const D = dash(), L0 = D.DEFAULT;
  let L = D.move(L0, 'gov', 0, 0);   // onto the horse: the horse and what is under it go down
  assert.deepEqual(at(L, 'gov'), [0, 0, 3, 3]); assert.ok(noOverlap(D, L)); assert.equal(at(L, 'hero')[1], 3);
  assert.deepEqual(at(D.move(L0, 'gov', 40, -5), 'gov'), [9, 0, 3, 3], 'clamped into the grid');
  L = D.resize(L0, 'gantt', 3, 18); assert.deepEqual(at(L, 'gantt'), [0, 8, 3, 18]); assert.ok(noOverlap(D, L));
  assert.deepEqual(at(D.resize(L0, 'gantt', 1, 1), 'gantt'), [0, 8, 3, 4], 'not under its minimum');
  assert.deepEqual(at(D.resize(L0, 'tasks', 30, 7), 'tasks'), [6, 8, 6, 7].map((v, i) => (i === 0 ? 0 : i === 2 ? 12 : v)), 'as wide as the grid, moved left to fit');
  L = D.add(L0, 'v:cache'); assert.deepEqual(at(L, 'v:cache'), [0, 18, 6, 9], 'the first place it fits'); assert.ok(noOverlap(D, L));
  assert.equal(D.add(L, 'v:cache').length, L.length, 'a card is on the grid once'); assert.equal(D.add(L, 'nope').length, L.length);
  const R = D.remove(L0, 'gov'); assert.equal(R.length, 6); assert.deepEqual(at(D.add(R, 'gov'), 'gov'), [9, 15, 3, 3], 'it comes back where there is room');
  assert.deepEqual(at(D.keyOp(L0, 'queue', 'ArrowRight', false), 'queue'), [1, 15, 4, 3]);
  assert.deepEqual(at(D.keyOp(L0, 'queue', 'ArrowDown', true), 'queue'), [0, 15, 4, 4], 'shift: resize');
  assert.deepEqual(at(D.keyOp(L0, 'queue', 'ArrowLeft', true), 'queue'), [0, 15, 3, 3]);
  assert.equal(D.keyOp(L0, 'queue', 'Enter', false), null);
  assert.ok(D.same(L0, L0.slice().reverse())); assert.ok(!D.same(L0, D.move(L0, 'gov', 0, 0)));
  assert.equal(D.rowHeight(900, 8), Math.floor((900 - 8 * 17) / 18)); assert.equal(D.rowHeight(100, 8), D.ROW_MIN);
});

test('the tour: when it starts by itself, what ending it stores, and the steps it passes over', () => {
  const T = tour(), s = memStore();
  assert.equal(T.shouldAuto(s, {}), true, 'a first visit');
  assert.equal(T.shouldAuto(s, { webdriver: true }), false, 'a browser driven by automation');
  assert.equal(T.ending('finish', false), 'done'); assert.equal(T.ending('skip', true), 'never'); assert.equal(T.ending('skip', false), null);
  T.record(s, 'done'); assert.equal(T.stored(s), 'done'); assert.equal(T.shouldAuto(s, {}), false);
  s.setItem(T.KEY, '{"v":99,"state":"done"}'); assert.equal(T.stored(s), null, 'another version is nothing stored');
  s.setItem(T.KEY, 'oops'); assert.equal(T.shouldAuto(s, {}), true);
  const ids = T.STEPS.map(x => x.id);
  for (const id of ['sessions', 'team', 'composer', 'approvals', 'board', 'cache', 'hud', 'nav', 'customise', 'settings', 'again']) assert.ok(ids.includes(id), id);
  const all = T.hasIn([{ id: 'hero' }, { id: 'team' }, { id: 'gantt' }, { id: 'tasks' }]), noTeam = T.hasIn([{ id: 'hero' }]);
  const hero = ids.indexOf('hero');
  assert.equal(T.stepFrom(T.STEPS, hero, 1, all), hero + 1);
  assert.equal(ids[T.stepFrom(T.STEPS, hero, 1, noTeam)], 'radio', 'the steps about cards taken off the Cockpit are passed over');
  assert.equal(ids[T.stepFrom(T.STEPS, ids.indexOf('radio'), -1, noTeam)], 'hero');
  assert.equal(T.stepFrom(T.STEPS, 0, -1, all), -1); assert.equal(T.stepFrom(T.STEPS, T.STEPS.length - 1, 1, all), T.STEPS.length);
});

test('the page: the Cockpit is a grid of its panels; unlocked, a focused card moves and resizes with the keys and leaves with Delete; the layout is kept', async () => {
  const L = await boot(), { SL, $, doc } = L;
  const cards = () => doc.querySelectorAll('.dcard').map(c => c.dataset.card);
  assert.deepEqual(cards(), ['hero', 'team', 'gantt', 'tasks', 'queue', 'mail', 'gov']);
  assert.ok($('.dcard[data-card="team"] .stall'), 'the stalls are in the team card');
  assert.equal($('.dash').dataset.edit, '0'); assert.equal($('.dash-lock').getAttribute('aria-pressed'), 'false');
  assert.equal($('.dcard[data-card="gov"]').getAttribute('tabindex'), null, 'locked: the cards are not focus stops');
  $('.dash-lock').click(); await L.settle();
  assert.equal($('.dash').dataset.edit, '1'); assert.equal($('.dash-lock').getAttribute('aria-pressed'), 'true'); assert.equal(SL.ui.dash.locked(), false);
  assert.equal($('.dash-add').hidden, false);
  const gov = L.focus('.dcard[data-card="gov"]');
  const e = L.press('left'); await L.settle(); assert.ok(e.defaultPrevented);
  assert.deepEqual(plain(SL.ui.dash.layout().find(c => c.id === 'gov')), { id: 'gov', x: 8, y: 15, w: 3, h: 3 });
  assert.match(gov.style.gridColumn, /^9 \/ span 3$/);
  L.press('shift+down'); await L.settle();
  assert.equal(SL.ui.dash.layout().find(c => c.id === 'gov').h, 4);
  const stored = JSON.parse(L.store.get(SL.dash.KEY)); assert.equal(stored.v, 1); assert.deepEqual(plain(stored.cards.find(c => c.id === 'gov')), { id: 'gov', x: 8, y: 15, w: 3, h: 4 });
  L.focus('.dcard[data-card="gov"]'); L.press('delete'); await L.settle();
  assert.ok(!cards().includes('gov')); assert.ok(!JSON.parse(L.store.get(SL.dash.KEY)).cards.some(c => c.id === 'gov'));
  assert.equal(L.posts.length, 0, 'arranging cards sends nothing to the server');
  $('.dash-lock').click(); await L.settle(); assert.equal($('.dash').dataset.edit, '0');
  /* the next page load reads the layout back */
  const L2 = await boot({ storage: { [SL.dash.KEY]: L.store.get(SL.dash.KEY) } });
  assert.deepEqual(L2.doc.querySelectorAll('.dcard').map(c => c.dataset.card), ['hero', 'team', 'gantt', 'tasks', 'queue', 'mail']);
  assert.deepEqual(plain(L2.SL.ui.dash.layout().find(c => c.id === 'queue')), { id: 'queue', x: 0, y: 15, w: 4, h: 3 });
});

test('the page: a card of a view draws the view with its own code, leaves with its scope, and a broken stored layout gives the default', async () => {
  const L = await boot({ storage: { 'sleipnir.web.cockpit': '{"v":1,"cards":[{"id":"v:cache","x":0,"y":0,"w":6,"h":9},{"id":"v:mail","x":6,"y":0,"w":6,"h":9}]}' } }), { SL, $ } = L;
  assert.ok($('.dcard[data-card="v:cache"] .cview .cache'), 'the Cache view in its card');
  assert.ok($('.dcard[data-card="v:mail"] .cview .mailv'), 'the Mail view in its card');
  const before = SL.views.live.size;
  SL.ui.dash.setLocked(false); await L.settle(); L.focus('.dcard[data-card="v:mail"]'); L.press('delete'); await L.settle();
  assert.equal(SL.views.live.size, before - 1, 'the card\'s scope is disposed with it');
  SL.ui.dash.add('gov'); await L.settle(); assert.ok($('.dcard[data-card="gov"] .gov'));
  SL.ui.dash.setLocked(true);
  const B = await boot({ storage: { 'sleipnir.web.cockpit': '{"v":7,"cards":"x"}' } });
  assert.equal(B.doc.querySelectorAll('.dcard').length, 7);
  /* leaving the Cockpit disposes every card's scope */
  const n = B.SL.views.live.size; B.SL.views.show('cache'); await B.settle(); assert.ok(B.SL.views.live.size <= n - 7);
});

test('the page: the tour opens over the page, Escape skips it without interrupting the turn, and it comes back from Help, the palette and Settings', async () => {
  const L = await boot(), { SL, $ } = L;
  await L.step(3); assert.equal(SL.ui.tour.open, false, 'a page driven by a test: no tour by itself');
  SL.ui.tour.start(); await L.settle();
  assert.ok($('#tour .tour-card')); assert.equal(SL.ui.tour.step, 'welcome'); assert.equal($('#tourT').textContent, 'Welcome to Sleipnir');
  assert.equal($('[data-t="back"]').disabled, true);
  L.press('right'); await L.settle(); assert.equal(SL.ui.tour.step, 'sessions');
  $('[data-t="next"]').click(); await L.settle(); assert.equal(SL.ui.tour.step, 'hud');
  $('[data-t="back"]').click(); await L.settle(); assert.equal(SL.ui.tour.step, 'sessions');
  const n = L.posts.length, e = L.press('esc'); await L.settle();
  assert.ok(e.defaultPrevented); assert.equal(SL.ui.tour.open, false); assert.equal($('#tour'), null);
  assert.equal(L.posts.length, n, 'Escape ended the tour and reached nothing else (no interrupt)');
  assert.equal(L.store.get(SL.tour.KEY), undefined, 'a skip without the tick stores nothing');
  /* the keys of the page do nothing while it is open */
  SL.ui.tour.start(); await L.settle(); const v = SL.views.current.name; L.press('g'); L.press('a'); await L.settle(); assert.equal(SL.views.current.name, v);
  $('#tourNever').checked = true; $('[data-t="skip"]').click(); await L.settle();
  assert.equal(JSON.parse(L.store.get(SL.tour.KEY)).state, 'never');
  /* the Help sheet's button, the palette's entry and Settings › Appearance */
  SL.ui.sheets.help(); await L.settle(); $('[data-tour]').click(); await L.settle(); assert.equal(SL.ui.tour.open, true); assert.equal(SL.ui.hasModal(), false, 'the sheet closed');
  SL.ui.tour.end('skip'); await L.settle();
  const item = SL.palette.filter('take the tour', false)[0]; assert.equal(item.name, 'Take the tour'); SL.palette.run(item, ''); await L.settle(); assert.equal(SL.ui.tour.open, true);
  SL.ui.tour.end('skip'); await L.settle();
  SL.ui.settingsPage('look'); await L.settle(); $('[data-do="tour"]').click(); await L.settle(); assert.equal(SL.ui.tour.open, true);
  /* finishing: walk to the end */
  for (let i = 0; i < 30 && SL.ui.tour.open; i++) { $('[data-t="next"]').click(); await L.settle(); }
  assert.equal(SL.ui.tour.open, false); assert.equal(JSON.parse(L.store.get(SL.tour.KEY)).state, 'done');
});

test('the page: the tour starts by itself once in a browser no test drives, and not after it was finished', async () => {
  const L = await boot({ automation: false });
  await L.step(2); assert.equal(L.SL.ui.tour.open, true, 'started by itself');
  L.SL.ui.tour.end('finish'); await L.settle();
  const again = await boot({ automation: false, storage: { [L.SL.tour.KEY]: L.store.get(L.SL.tour.KEY) } });
  await again.step(3); assert.equal(again.SL.ui.tour.open, false);
});
