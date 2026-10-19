// dash-pages.test.mjs: every page of the web UI is a page of cards (83b-ui-dash.js). Each registered view, each tab of the Workspace
// and each page of Settings is walked on the shipped page booted on the fake DOM: every panel it draws is in a card, and every card
// hides into the Hidden tray, comes back and moves. The first version's Cockpit layout is carried over; a card of one page is added to
// another and drawn there by its own view; the layout goes out as a file and comes back; the header and the rail hide and collapse
// their parts; a narrow screen only reorders; the server's copy of the layout wins at boot.
import test from 'node:test';
import assert from 'node:assert/strict';
import { page } from './pages-harness.mjs';
import { boot } from './pagedom.mjs';

const plain = x => JSON.parse(JSON.stringify(x));
const dash = () => page({ modules: ['83b-ui-dash.js'] }).SL.dash;
const memStore = init => { const m = new Map(Object.entries(init || {})); return { getItem: k => (m.has(k) ? m.get(k) : null), setItem: (k, v) => m.set(k, String(v)), removeItem: k => m.delete(k), m }; };
const sel = id => '.dcard[data-card="' + id + '"]';
const shown = L => L.doc.querySelectorAll('#views .dcard').map(c => c.dataset.card);
/** Open page key as a person would (its view, its Workspace tab or its Settings page) and wait for it. */
async function open(L, key) { L.SL.ui.dash.openPage(key); await L.settle(); await L.step(0.05); }

test('every page of every registered view is cards: each panel is in a card, and each card hides, comes back and moves', async () => {
  const L = await boot(), { SL } = L, D = SL.dash;
  const pages = Object.keys(D.PAGES).filter(k => D.PAGES[k].view);
  const views = new Set(pages.map(k => D.PAGES[k].view));
  /* every view of the page is a page of cards, the Plugins view too */
  for (const v of Object.keys(SL.views.V.reg)) assert.ok(views.has(v), 'the ' + v + ' view has a page of cards');
  for (const key of pages) {
    await open(L, key);
    assert.equal(SL.ui.dash.key, key, key + ' is the page on screen');
    const panels = L.doc.querySelectorAll('#views .panel');
    assert.ok(panels.length > 0 || key === 'tools', key + ' draws panels');
    const grid = L.$('#views .dash-grid'); assert.ok(grid, key + ' has a grid');
    for (const ch of grid.children) assert.ok(ch.classList.contains('dcard'), key + ': the grid holds cards only');
    for (const b of L.doc.querySelectorAll('#views .dcard > .dc-body')) assert.ok(b.children.length > 0, key + ': the card ' + b.closest('.dcard').dataset.card + ' is drawn');
    for (const p of panels) assert.ok(p.closest('.dcard'), key + ': a panel (' + (p._attrs.get('class') || '') + ') is outside every card');
    assert.deepEqual(plain(shown(L)).sort(), plain(D.PAGES[key].cards).sort(), key + ': its own cards, each once');
    SL.ui.dash.setLocked(false); await L.settle();
    for (const id of D.PAGES[key].cards) {
      const el = L.focus(sel(id)); assert.equal(el.getAttribute('tabindex'), '0', key + ' ' + id + ': a focus stop while unlocked');
      L.press('delete'); await L.settle();
      assert.ok(!shown(L).includes(id), key + ' ' + id + ': hidden');
      const c = SL.ui.dash.all().find(k => k.id === id); assert.equal(c && c.hidden, true, key + ' ' + id + ': kept, hidden');
      assert.match(L.$('.dash-tray').textContent, /Hidden 1/, key + ' ' + id + ': in the Hidden tray');
      SL.ui.dash.add(id); await L.settle();
      assert.ok(shown(L).includes(id), key + ' ' + id + ': restored');
      assert.equal(L.$('.dash-tray').hidden, true, key + ' ' + id + ': the tray is empty again');
      const before = SL.ui.dash.all().find(k => k.id === id);
      L.focus(sel(id)); const e = L.press('down'); await L.settle();
      assert.ok(e.defaultPrevented, key + ' ' + id + ': the arrow is the card\'s');
      assert.equal(SL.ui.dash.all().find(k => k.id === id).y, before.y + 1, key + ' ' + id + ': moved one row down');
    }
    SL.ui.dash.reset(); await L.settle();
    assert.deepEqual(plain(SL.ui.dash.all().map(c => [c.id, c.x, c.y, c.w])), plain(D.defaults(key).map(c => [c.id, c.x, c.y, c.w])), key + ': Reset layout puts back its default');
    SL.ui.dash.setLocked(true); await L.settle();
  }
  assert.deepEqual(L.errors, [], 'no page failed');
});

test('the first version\'s Cockpit layout is carried over: its cards where they were, the panels it had taken off hidden', async () => {
  const D = dash(), old = { v: 1, cards: [{ id: 'hero', x: 0, y: 0, w: 6, h: 8 }, { id: 'gov', x: 6, y: 0, w: 6, h: 4 }, { id: 'v:cache', x: 0, y: 8, w: 12, h: 9 }] };
  const s = memStore({ [D.OLD_KEY]: JSON.stringify(old) }), d = D.readDoc(s);
  assert.equal(d.version, D.VERSION);
  assert.deepEqual(plain(D.visible(d.pages.cockpit)), old.cards);
  assert.deepEqual(plain(d.pages.cockpit.filter(c => c.hidden).map(c => c.id)), ['team', 'gantt', 'tasks', 'queue', 'mail'], 'what the old layout left out waits in the tray');
  assert.equal(D.readDoc(memStore({ [D.OLD_KEY]: '{"v":7}' })).pages.cockpit, undefined, 'an unknown old layout is nothing');
  const L = await boot({ storage: { [D.OLD_KEY]: JSON.stringify(old) } });
  assert.deepEqual(shown(L), ['hero', 'gov', 'v:cache']);
  L.SL.ui.dash.setLocked(false); await L.settle();
  assert.match(L.$('.dash-tray').textContent, /Hidden 5/);
  L.SL.ui.dash.add('team'); await L.settle(); assert.ok(L.$(sel('team') + ' .stall'), 'a hidden panel comes back drawn');
  assert.equal(JSON.parse(L.store.get(D.KEY)).version, D.VERSION, 'kept in this version\'s document from now on');
});

test('a card of one page added to another is drawn there by its own view, kept, and removed (not hidden) from that page', async () => {
  const L = await boot(), { SL } = L;
  await open(L, 'board'); SL.ui.dash.setLocked(false); await L.settle();
  const offered = SL.dash.addable('board', SL.ui.dash.all()).map(o => o.id);
  for (const id of ['gov', 'cache.hit', 'settings.budget.cost', 'mail.list', 'v:sessions']) assert.ok(offered.includes(id), id + ' is offered on the Board');
  SL.ui.dash.add('gov'); SL.ui.dash.add('cache.hit'); SL.ui.dash.add('settings.budget.cost'); await L.settle(); L.advance(50); await L.settle();
  assert.ok(L.$(sel('gov') + '.dc-foreign .gov'), 'the governor, drawn by the Cockpit');
  assert.ok(L.$(sel('cache.hit') + ' .cview .c-hit'), 'the hit ratio, drawn by the Cache view');
  assert.equal(L.doc.querySelectorAll(sel('cache.hit') + ' .panel').filter(p => !p.closest('.dash-store')).length, 1, 'only that card of the Cache view shows (the view keeps its other cards aside)');
  assert.ok(L.$(sel('settings.budget.cost') + ' #budTbl tbody tr'), 'the cost of every agent, drawn by Settings');
  assert.equal(L.$(sel('settings.budget.cost') + ' .setnav2'), null, 'without the list of Settings pages');
  /* the next page load has them */
  const L2 = await boot({ storage: { [SL.dash.KEY]: L.store.get(SL.dash.KEY) } });
  await open(L2, 'board');
  for (const id of ['gov', 'cache.hit', 'settings.budget.cost']) assert.ok(shown(L2).includes(id), id + ' is still on the Board');
  /* a card of another page leaves: it is not hidden on this page, the picker offers it again */
  L2.SL.ui.dash.setLocked(false); await L2.settle(); L2.focus(sel('gov')); L2.press('delete'); await L2.settle();
  assert.ok(!shown(L2).includes('gov')); assert.equal(L2.SL.ui.dash.all().find(c => c.id === 'gov'), undefined);
  assert.ok(L2.SL.dash.addable('board', L2.SL.ui.dash.all()).some(o => o.id === 'gov'));
  assert.deepEqual(L2.errors, []);
});

test('the layout goes out as a file and comes back: pure, and through Settings > Appearance', async () => {
  const D = dash(), doc = { version: D.VERSION, pages: { cockpit: D.hide(D.DEFAULT, 'gov'), board: D.add(D.defaults('board'), 'gov'), 'settings.look': D.setOpt(D.defaults('settings.look'), 'settings.look.keys', 'density', 'compact') } };
  const text = D.exportDoc(doc), back = D.importDoc(text);
  assert.deepEqual(plain(back), plain(doc), 'the round trip is the same document');
  assert.match(D.importDoc('not json').message, /not JSON/);
  assert.match(D.importDoc('{"version":1,"pages":{}}').message, /version 1/);
  assert.match(D.importDoc('{"a":1}').message, /not a layout/);
  assert.deepEqual(plain(D.importDoc(JSON.stringify({ version: D.VERSION, pages: { board: [{ id: 'board.deps', x: 0, y: 0, w: 99, h: 1 }, { id: 'nope', x: 0, y: 0, w: 1, h: 1 }] } })).pages.board), [{ id: 'board.deps', x: 0, y: 0, w: 12, h: 4 }], 'what is imported is checked as what is stored');

  const A = await boot();
  await open(A, 'board'); A.SL.ui.dash.setLocked(false); await A.settle(); A.focus(sel('board.deps')); A.press('delete'); await A.settle();
  await open(A, 'settings.look');
  for (const k of ['layout-export', 'layout-import', 'layout-reset-all', 'chrome-menu']) assert.ok(A.$('[data-do="' + k + '"]'), 'Appearance has ' + k);
  const file = A.SL.ui.dash.exportText();
  const B = await boot(); const n = B.posts.length;
  assert.equal(B.SL.ui.dash.importText('{"version":2}'), 'the file is not a layout of Sleipnir', 'a file that is not one is refused');
  assert.equal(B.SL.ui.dash.importText(file), true); await B.settle();
  const put = B.posts.slice(n).filter(p => p.path === '/api/layout');
  assert.equal(put.length, 1, 'an import is sent to the server at once'); assert.deepEqual(plain(put[0].body), JSON.parse(file));
  await open(B, 'board'); assert.deepEqual(shown(B), ['board.tasks'], 'the imported layout is the page\'s');
  B.SL.ui.dash.resetAll(); await B.settle();
  assert.ok(B.posts.some(p => p.method === 'DELETE' && p.path === '/api/layout'), 'Reset all pages forgets the server\'s copy');
  assert.deepEqual(shown(B).sort(), ['board.deps', 'board.tasks']);
});

test('the header\'s widgets and the rail\'s parts hide, collapse and come back, and the state is kept', async () => {
  const L = await boot(), { SL, $ } = L;
  SL.ui.chrome.set('hud.hit', 'hidden'); SL.ui.chrome.set('rail.feed', 'collapsed'); await L.settle();
  assert.equal($('#ringHit').getAttribute('data-cust'), 'hidden');
  assert.equal($('#feedWrap').getAttribute('data-cust'), 'collapsed');
  const stub = $('.railstub[data-part="rail.feed"]'); assert.ok(stub, 'a collapsed part is a line that opens it');
  assert.ok($('#rail .railcust'), 'the rail has its own customise button');
  const doc = JSON.parse(L.store.get(SL.dash.KEY));
  assert.deepEqual(plain(doc.pages.hud), [{ id: 'hud.hit', x: 0, y: 0, w: 1, h: 1, hidden: true }]);
  const L2 = await boot({ storage: { [SL.dash.KEY]: L.store.get(SL.dash.KEY) } });
  assert.equal(L2.$('#ringHit').getAttribute('data-cust'), 'hidden', 'kept across a page load');
  L2.$('.railstub[data-part="rail.feed"]').click(); await L2.settle();
  assert.equal(L2.$('#feedWrap').getAttribute('data-cust'), null); assert.equal(L2.$('.railstub'), null);
  L2.SL.ui.chrome.set('hud.hit', 'shown'); await L2.settle(); assert.equal(L2.$('#ringHit').getAttribute('data-cust'), null);
});

test('list cards have a density; a card sized by hand keeps its height; Reset size gives it back', () => {
  const D = dash();
  let L = D.defaults('cache');
  L = D.setOpt(L, 'cache.table', 'density', 'compact');
  assert.deepEqual(plain(L.find(c => c.id === 'cache.table').opts), { density: 'compact' });
  assert.equal(D.BY_ID['cache.table'].list, true); assert.equal(D.BY_ID['cache.head'].list, false);
  L = D.setOpt(L, 'cache.table', 'size', 'fixed'); L = D.resize(L, 'cache.table', 12, 20);
  assert.equal(L.find(c => c.id === 'cache.table').h, 20);
  L = D.resetSize(L, 'cache.table'); const t = L.find(c => c.id === 'cache.table');
  assert.equal(t.h, 7); assert.deepEqual(plain(t.opts), { density: 'compact' }, 'the size goes back to following its content; the density stays');
  /* a card whose height follows its content: taller pushes down, shorter lets what was under it come up */
  const F = D.fitHeight(D.defaults('cache'), 'cache.hit', 10), hit = F.find(c => c.id === 'cache.hit'), fold = F.find(c => c.id === 'cache.fold');
  assert.equal(hit.h, 10); assert.equal(fold.y, hit.y + hit.h);
  const G = D.fitHeight(F, 'cache.hit', 3); assert.equal(G.find(c => c.id === 'cache.fold').y, 8);
});

test('a narrow screen stacks the cards in one column: customising there hides, restores and reorders only', async () => {
  const L = await boot({ globals: { matchMedia: q => ({ matches: /max-width: 900px/.test(q), addEventListener() {}, addListener() {} }) } }), { SL } = L;
  await open(L, 'cache');
  assert.equal(L.$('.dash').dataset.stack, '1');
  SL.ui.dash.setLocked(false); await L.settle();
  const order = () => plain(SL.dash.order(SL.ui.dash.all()).map(c => c.id));
  assert.deepEqual(order().slice(0, 2), ['cache.head', 'cache.hit']);
  const before = JSON.stringify(SL.ui.dash.all()), agent = SL.ui.cacheAgent;
  L.focus(sel('cache.head')); L.press('right'); await L.settle(); assert.equal(JSON.stringify(SL.ui.dash.all()), before, 'no columns to move to');
  L.press('shift+down'); await L.settle(); assert.equal(JSON.stringify(SL.ui.dash.all()), before, 'no resizing');
  assert.equal(SL.ui.cacheAgent, agent, 'an arrow on a card stays the card\'s: the view\'s own arrows do not run');
  L.focus(sel('cache.head')); L.press('down'); await L.settle();
  assert.deepEqual(order().slice(0, 2), ['cache.hit', 'cache.head'], 'down: one place later');
  L.focus(sel('cache.head')); L.press('up'); await L.settle();
  assert.deepEqual(order().slice(0, 2), ['cache.head', 'cache.hit'], 'up: back');
  L.focus(sel('cache.fold')); L.press('delete'); await L.settle(); assert.ok(!shown(L).includes('cache.fold'));
  SL.ui.dash.add('cache.fold'); await L.settle(); assert.ok(shown(L).includes('cache.fold'));
  const D = SL.dash, two = D.defaults('cache');
  assert.equal(D.keyOp(two, 'cache.head', 'ArrowLeft', false, true), null); assert.equal(D.keyOp(two, 'cache.head', 'ArrowDown', true, true), null);
});

test('at boot the server\'s copy of the layout wins over this browser\'s; with none, this browser\'s is sent to it', async () => {
  const srv = { version: 2, pages: { board: [{ id: 'board.tasks', x: 0, y: 0, w: 12, h: 10 }, { id: 'board.deps', x: 0, y: 10, w: 12, h: 8, hidden: true }] } };
  const local = JSON.stringify({ version: 2, pages: { board: [{ id: 'board.tasks', x: 0, y: 0, w: 12, h: 10, hidden: true }, { id: 'board.deps', x: 0, y: 10, w: 12, h: 8 }] } });
  const L = await boot({ storage: { 'sleipnir.web.layout': local }, route: (m, p) => (m === 'GET' && p === '/api/layout' ? srv : undefined) });
  await open(L, 'board');
  assert.deepEqual(shown(L), ['board.tasks'], 'the server\'s layout');
  assert.deepEqual(JSON.parse(L.store.get('sleipnir.web.layout')).pages.board, srv.pages.board, 'and this browser keeps it');
  const N = await boot({ storage: { 'sleipnir.web.layout': local }, route: (m, p) => (m === 'GET' && p === '/api/layout' ? { version: 0, pages: {} } : undefined) });
  await N.settle(); N.advance(10); await N.settle();
  const put = N.posts.filter(p => p.path === '/api/layout');
  assert.equal(put.length, 1); assert.deepEqual(put[0].body, JSON.parse(local), 'the server had none: it gets this browser\'s');
});
