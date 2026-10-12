/* 83b-ui-dash.js: the Cockpit as a dashboard of cards (SL.dash, the layout, and ui.dash, the grid that shows it).
 *
 * The Cockpit is a grid of COLS columns and rows of one unit each (ROWS_FIT units fill the height of the view, never less than ROW_MIN
 * pixels each). A card is {id, x, y, w, h} in those units. The cards are the Cockpit's own panels (the horse, the manager's card and
 * the stalls, the gantt, the task board, the merge queue, the latest mail, the governor) and every view of the views rail, drawn by the
 * view's own code inside the card. The default layout is the Cockpit's arrangement of its own panels.
 *
 * Locked (the default), the cards are the Cockpit. Unlocked (the lock button of the Cockpit's bar), a card moves when it is dragged,
 * resizes from its right edge, bottom edge and corner, and leaves with its ×; `Add card` lists the cards not on the grid and `Reset`
 * puts back the default layout. With the keyboard, a focused card moves with the arrows, resizes with shift and the arrows and leaves
 * with Delete. A card that lands on others pushes them down; nothing else moves. Every change is saved in this browser
 * (localStorage, KEY): a stored layout of another version, or one that does not read, gives the default layout.
 *
 * The layout functions are pure: they take a layout and return a new one, and the tests call them without a page. */
(function (SL) {
  'use strict';
  const U = SL.u, { esc } = U, ui = SL.ui = SL.ui || {};
  const COLS = 12, ROWS_FIT = 18, ROW_MIN = 30, MAX_H = 48, VERSION = 1, KEY = 'sleipnir.web.cockpit';

  /** The cards a Cockpit can hold: the Cockpit's own panels (kind part) and the views of the rail (kind view, drawn by the view).
   *  w and h are the size a card gets when it is added; minW and minH the smallest it can be made. */
  const PARTS = [
    { id: 'hero', title: 'The horse', desc: 'the team as a horse: each of the eight legs is one role of the team', minW: 2, minH: 4, w: 4, h: 8 },
    { id: 'team', title: 'Manager and stalls', desc: 'the manager\'s card and one stall per worker; a free leg names its free slot', minW: 3, minH: 4, w: 8, h: 8 },
    { id: 'gantt', title: 'Swarm gantt', desc: 'what each agent did in the last 60 seconds, with the replay controls', minW: 3, minH: 4, w: 6, h: 7 },
    { id: 'tasks', title: 'Task board', desc: 'the tasks by status: todo, running, verify, merged', minW: 3, minH: 4, w: 6, h: 7 },
    { id: 'queue', title: 'Merge queue', desc: 'what is being verified and merged, conflicts and bounces', minW: 2, minH: 2, w: 4, h: 3 },
    { id: 'mail', title: 'Mail, latest', desc: 'the last four mails between agents', minW: 2, minH: 2, w: 5, h: 3 },
    { id: 'gov', title: 'Governor', desc: 'requests per minute, 429s and retries', minW: 2, minH: 2, w: 3, h: 3 },
  ];
  /** The views of the rail a card can hold: [view, title, description]. The Workspace card has its four tabs (Files, Changes,
   *  Checkpoints, Merge), as the view has. */
  const VIEWS = [
    ['workspace', 'Workspace', 'files, changes, checkpoints and merge, with the diff'],
    ['cache', 'Cache', 'the prompt by layer, the requests, cache breaks'],
    ['mail', 'Mail', 'every mail between agents, with its text'],
    ['board', 'Board', 'the task board with its dependencies'],
    ['replay', 'Replay', 'the session as a recording: scrub and speed'],
    ['sessions', 'Sessions', 'live and recorded sessions'],
    ['tools', 'Tools', 'every sleipnir command, with a form'],
    ['doctor', 'Doctor', 'probe an endpoint'],
    ['schedule', 'Schedule', 'cron jobs and the daemon'],
    ['settings', 'Settings', 'models, roles, budget, permissions and the rest'],
  ];
  const CATALOG = PARTS.map(p => Object.assign({ kind: 'part' }, p)).concat(VIEWS.map(([v, t, d]) => ({ id: 'v:' + v, kind: 'view', view: v, title: t, desc: d, minW: 3, minH: 4, w: 6, h: 9 })));
  const BY_ID = {}; CATALOG.forEach(c => { BY_ID[c.id] = c; });
  /** The Cockpit's arrangement of its own panels: the horse and the stalls on top, the gantt and the board, then the queue, mail and
   *  the governor in a short band. */
  const DEFAULT = [
    { id: 'hero', x: 0, y: 0, w: 4, h: 8 }, { id: 'team', x: 4, y: 0, w: 8, h: 8 },
    { id: 'gantt', x: 0, y: 8, w: 6, h: 7 }, { id: 'tasks', x: 6, y: 8, w: 6, h: 7 },
    { id: 'queue', x: 0, y: 15, w: 4, h: 3 }, { id: 'mail', x: 4, y: 15, w: 5, h: 3 }, { id: 'gov', x: 9, y: 15, w: 3, h: 3 },
  ];
  const copy = L => L.map(c => ({ id: c.id, x: c.x, y: c.y, w: c.w, h: c.h }));
  const int = (v, d) => (typeof v === 'number' && isFinite(v) ? Math.round(v) : d);
  /** A card kept inside the grid and inside its own limits. */
  function clampCard(c) {
    const d = BY_ID[c.id]; const w = Math.max(d.minW, Math.min(COLS, int(c.w, d.w))), h = Math.max(d.minH, Math.min(MAX_H, int(c.h, d.h)));
    return { id: c.id, w, h, x: Math.max(0, Math.min(COLS - w, int(c.x, 0))), y: Math.max(0, int(c.y, 0)) };
  }
  const overlaps = (a, b) => a.x < b.x + b.w && b.x < a.x + a.w && a.y < b.y + b.h && b.y < a.y + a.h;
  /** The layout with no two cards on the same cell: the card `fixed` keeps its place, every other card that lands on a placed one goes
   *  down below it (in the order of their rows, then columns), and nothing moves up. */
  function resolve(L, fixed) {
    const placed = [], out = copy(L), first = out.find(c => c.id === fixed);
    if (first) placed.push(first);
    out.filter(c => c !== first).sort((a, b) => a.y - b.y || a.x - b.x).forEach(c => {
      for (let hit = placed.find(p => overlaps(p, c)); hit; hit = placed.find(p => overlaps(p, c))) c.y = hit.y + hit.h;
      placed.push(c);
    });
    return out;
  }
  /** A layout read from storage, or null when it is not one: an object of this VERSION with a list of cards; unknown and repeated
   *  cards are left out, sizes and places are clamped and overlaps resolved. */
  function normalize(raw) {
    if (!raw || typeof raw !== 'object' || raw.v !== VERSION || !Array.isArray(raw.cards)) return null;
    const seen = new Set(), cards = [];
    raw.cards.forEach(c => { if (c && typeof c === 'object' && BY_ID[c.id] && !seen.has(c.id)) { seen.add(c.id); cards.push(clampCard(c)); } });
    return resolve(cards);
  }
  /** The stored layout (try/catch: storage may be missing or blocked), else the default. */
  function load(store) {
    try { const s = store && store.getItem(KEY); if (s) { const L = normalize(JSON.parse(s)); if (L) return L; } } catch (e) { /* unreadable: the default */ }
    return copy(DEFAULT);
  }
  /** Keep the layout in storage; false when storage refused it. */
  function save(store, L) { try { store.setItem(KEY, JSON.stringify({ v: VERSION, cards: copy(L) })); return true; } catch (e) { return false; } }
  /** Forget the stored layout. */
  function forget(store) { try { store.removeItem(KEY); } catch (e) { /* nothing stored */ } }
  /** The layout with card id moved to (x, y). */
  function move(L, id, x, y) { const c = L.find(k => k.id === id); if (!c) return copy(L); return resolve(L.map(k => (k === c ? clampCard({ id, x, y, w: c.w, h: c.h }) : k)), id); }
  /** The layout with card id resized to w by h, from its top-left corner. */
  function resize(L, id, w, h) { const c = L.find(k => k.id === id); if (!c) return copy(L); return resolve(L.map(k => (k === c ? clampCard({ id, x: c.x, y: c.y, w, h }) : k)), id); }
  /** The first place, row by row, where a card of w by h touches no other. */
  function firstFit(L, w, h) {
    for (let y = 0; ; y++) for (let x = 0; x + w <= COLS; x++) { const c = { x, y, w, h }; if (!L.some(k => overlaps(k, c))) return { x, y }; }
  }
  /** The layout with card id added at its own size in the first place it fits (unchanged when it is there already or unknown). */
  function add(L, id) {
    const d = BY_ID[id]; if (!d || L.some(k => k.id === id)) return copy(L);
    const w = Math.min(COLS, d.w), p = firstFit(L, w, d.h); return copy(L).concat([clampCard({ id, x: p.x, y: p.y, w, h: d.h })]);
  }
  /** The layout without card id. */
  const remove = (L, id) => copy(L.filter(k => k.id !== id));
  /** The number of rows the layout takes. */
  const rows = L => L.reduce((m, c) => Math.max(m, c.y + c.h), 0);
  /** What a key does to a focused card when the Cockpit is unlocked: the arrows move it one cell, shift and the arrows resize it by
   *  one cell. Returns the new layout, or null for a key that does nothing. */
  function keyOp(L, id, key, shift) {
    const c = L.find(k => k.id === id), d = { ArrowLeft: [-1, 0], ArrowRight: [1, 0], ArrowUp: [0, -1], ArrowDown: [0, 1] }[key]; if (!c || !d) return null;
    return shift ? resize(L, id, c.w + d[0], c.h + d[1]) : move(L, id, c.x + d[0], c.y + d[1]);
  }
  /** Whether two layouts put the same cards in the same places (the order of the list does not count). */
  const same = (A, B) => A.length === B.length && A.every(a => { const b = B.find(k => k.id === a.id); return b && b.x === a.x && b.y === a.y && b.w === a.w && b.h === a.h; });
  /** The height in pixels of one row for a view of height H (ROWS_FIT rows fill it, never under ROW_MIN). */
  const rowHeight = (H, gap) => Math.max(ROW_MIN, Math.floor((H - gap * (ROWS_FIT - 1)) / ROWS_FIT));
  SL.dash = { COLS, ROWS_FIT, ROW_MIN, MAX_H, VERSION, KEY, CATALOG, BY_ID, DEFAULT, clampCard, overlaps, resolve, normalize, load, save, forget, move, resize, add, remove, rows, keyOp, firstFit, same, rowHeight };

  /* ---------------------------------------------------------------------------------------------------- the grid on the page */
  const LOCK = '<svg viewBox="0 0 16 16" width="15" height="15" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="7" width="10" height="7" rx="1.3"/><path d="M5.2 7V5a2.8 2.8 0 0 1 5.6 0v2"/></svg>';
  const UNLOCK = '<svg viewBox="0 0 16 16" width="15" height="15" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="7" width="10" height="7" rx="1.3"/><path d="M5.2 7V5a2.8 2.8 0 0 1 5.5-.8"/></svg>';
  /** The lock of the page: locked until the person unlocks it (it is not kept: every page load starts locked). */
  const D = { locked: true };
  /** The mounted dashboard ({root, layout, setLocked, add, remove, reset, picker}), or null. */
  let M = null;
  /** Say a line to a screen reader (the page's polite live region). */
  const say = t => { const a = document.getElementById('announcer'); if (a) a.textContent = t; };
  const store = () => { try { return window.localStorage; } catch (e) { return null; } };

  /**
   * Mount the dashboard into root (the Cockpit view's root): sc is the view's scope, S the session, parts maps a part id to
   * fn(cardScope, body, S) that draws it. Each card has a scope of its own, disposed when the card leaves or the view unmounts.
   */
  function mount(sc, root, S, parts) {
    let L = load(store());
    root.innerHTML = '<div class="dash" data-edit="0"><div class="dash-bar" role="toolbar" aria-label="The Cockpit\'s cards"><span class="dash-hint" hidden>Drag a card to move it, its edges to resize it. On a focused card: arrows move, shift+arrows resize, Delete removes. Saved in this browser.</span><span class="sp"></span>' +
      '<button class="btn sm dash-add" type="button" hidden>+ Add card</button><button class="btn sm dash-reset" type="button" hidden>Reset layout</button>' +
      '<button class="dash-lock" type="button" aria-pressed="false"></button></div><div class="dash-grid" aria-label="Cockpit cards"></div></div>';
    const dash = root.querySelector('.dash'), grid = root.querySelector('.dash-grid'), lockB = root.querySelector('.dash-lock'), addB = root.querySelector('.dash-add'), resetB = root.querySelector('.dash-reset'), hint = root.querySelector('.dash-hint');
    const cards = {};   // id -> {el, body, scope}
    const gap = () => { const g = parseFloat(window.getComputedStyle ? getComputedStyle(grid).rowGap || getComputedStyle(grid).gap : '') ; return isFinite(g) ? g : 8; };
    function fit() { const bar = root.querySelector('.dash-bar'), H = root.clientHeight - (bar ? bar.offsetHeight : 0) - 2 * gap(); grid.style.setProperty('--rowh', rowHeight(H > 0 ? H : 0, gap()) + 'px'); }
    function place(c) { const k = cards[c.id]; if (!k) return; k.el.style.gridColumn = (c.x + 1) + ' / span ' + c.w; k.el.style.gridRow = (c.y + 1) + ' / span ' + c.h; k.el.style.setProperty('--h', c.h); k.el.style.order = String(c.y * COLS + c.x); const sz = k.el.querySelector('.dc-size'); if (sz) sz.textContent = c.w + ' × ' + c.h; }
    function paintAll() { L.forEach(place); grid.style.setProperty('--rows', Math.max(rows(L), 1)); }
    function commit(next) { L = next; save(store(), L); paintAll(); }
    function cardHtml(d) {
      const t = esc(d.title);
      return '<div class="dc-edit"><span class="dc-grip" aria-hidden="true">⠿</span><b>' + t + '</b><span class="dc-size num"></span><button class="dc-x" type="button" aria-label="Remove the ' + t + ' card" title="Remove this card">×</button></div>' +
        (d.kind === 'view' ? '<div class="dc-h"><h2>' + t + '</h2><button class="dc-open" type="button" title="Open the ' + t + ' view" aria-label="Open the ' + t + ' view">open ↗</button></div>' : '') +
        '<div class="dc-body"></div><i class="dc-rs" data-rs="e" aria-hidden="true"></i><i class="dc-rs" data-rs="s" aria-hidden="true"></i><i class="dc-rs" data-rs="se" aria-hidden="true"></i>';
    }
    function mountCard(c) {
      const d = BY_ID[c.id], el = U.mk('section', { class: 'dcard dc-' + (d.kind === 'view' ? 'view' : c.id), 'data-card': c.id, 'aria-label': d.title }, cardHtml(d));
      grid.appendChild(el); const body = el.querySelector('.dc-body'), cs = SL.makeScope(el, 'card:' + c.id), card = { el, body, scope: cs, cancelDrag: null }; cards[c.id] = card;
      cs.onUnmount(() => { if (card.cancelDrag) card.cancelDrag(); });
      try {
        if (d.kind === 'part') { if (parts[c.id]) parts[c.id](cs, body, S); }
        else {
          const def = SL.views.V.reg[d.view], inner = U.mk('section', { class: 'cview v-' + d.view, 'data-view': d.view, 'aria-label': d.title }); body.appendChild(inner);
          if (def) def.mount(cs, inner, {}, S); else inner.innerHTML = '<p class="stubnote">this view is not available here</p>';
          if (SL.data && SL.data.PAGES && SL.data.PAGES[d.view]) SL.data.forPage(d.view);
        }
      } catch (e) { console.error(e); }
      paintEdit(el); place(c);
    }
    function unmountCard(id) { const k = cards[id]; if (!k) return; delete cards[id]; k.scope.dispose(); k.el.remove(); }
    function paintEdit(el) { const on = !D.locked; if (on) { el.setAttribute('tabindex', '0'); el.setAttribute('aria-roledescription', 'movable card'); } else { el.removeAttribute('tabindex'); el.removeAttribute('aria-roledescription'); } }
    function paintLock() {
      const on = !D.locked; dash.dataset.edit = on ? '1' : '0'; lockB.innerHTML = (on ? UNLOCK : LOCK) + '<span class="dl-t">' + (on ? 'Done' : 'Customise') + '</span>';
      lockB.setAttribute('aria-pressed', String(on)); lockB.setAttribute('aria-label', on ? 'Lock the Cockpit: stop arranging its cards' : 'Unlock the Cockpit to move, resize, add and remove its cards');
      lockB.title = on ? 'Lock the cards in place' : 'Unlock: move, resize, add and remove cards';
      addB.hidden = resetB.hidden = hint.hidden = !on; if (on) grid.setAttribute('data-owns-keys', ''); else grid.removeAttribute('data-owns-keys');
      Object.keys(cards).forEach(id => paintEdit(cards[id].el));
    }
    function setLocked(v) { D.locked = !!v; paintLock(); SL.bus.emit('dash-lock', D.locked); }
    L.forEach(mountCard); paintAll(); paintLock(); fit();
    sc.observe(root, () => fit()); sc.listen(window, 'resize', fit);
    const me = { root };
    sc.onUnmount(() => { Object.keys(cards).forEach(id => { const k = cards[id]; delete cards[id]; k.scope.dispose(); }); if (M === me) M = null; });

    /* ---- editing: the lock, add, reset, remove ---- */
    sc.listen(lockB, 'click', () => setLocked(!D.locked));
    sc.listen(addB, 'click', () => openPicker());
    sc.listen(resetB, 'click', () => ui.confirm({ title: 'Reset the Cockpit', kicker: 'layout', text: 'Put back the default layout? The cards you added leave and the Cockpit\'s own panels return to their places.', ok: 'Reset', run: () => reset() }));
    function reset() { const next = DEFAULT.map(c => Object.assign({}, c)); Object.keys(cards).forEach(id => { if (!next.some(c => c.id === id)) unmountCard(id); }); forget(store()); L = next; L.forEach(c => { if (!cards[c.id]) mountCard(c); }); paintAll(); ui.toast('the Cockpit has its default layout', 'ok'); }
    function addCard(id) { const next = add(L, id); if (next.length === L.length) return; const c = next.find(k => k.id === id); commit(next); mountCard(c); const k = cards[id]; if (k) { k.el.focus({ preventScroll: true }); if (k.el.scrollIntoView) k.el.scrollIntoView({ block: 'nearest' }); } ui.toast('added ' + BY_ID[id].title, 'ok'); }
    function removeCard(id) { const i = L.findIndex(c => c.id === id); if (i < 0) return; unmountCard(id); commit(remove(L, id)); const nx = L[Math.min(i, L.length - 1)]; if (nx && cards[nx.id]) cards[nx.id].el.focus({ preventScroll: true }); else lockB.focus(); ui.toast('removed ' + BY_ID[id].title + ': Add card brings it back'); }
    function openPicker() {
      const free = CATALOG.filter(c => !L.some(k => k.id === c.id)), row = c => '<li><button class="btn more dpick" type="button" data-add="' + esc(c.id) + '"><b>' + esc(c.title) + '</b><span>' + esc(c.desc) + '</span></button></li>';
      const group = (t, list) => list.length ? '<h3 class="lab">' + t + '</h3><ul class="dpicks">' + list.map(row).join('') + '</ul>' : '';
      ui.modal({ title: 'Add a card', kicker: 'Cockpit', desc: 'every panel of the Cockpit and every view of the rail', color: 'var(--fe)', cls: 'dashpick', focus: '.dpick',
        body: (free.length ? group('Cockpit panels', free.filter(c => c.kind === 'part')) + group('Views', free.filter(c => c.kind === 'view')) : '<p class="stubnote">Every card is on the Cockpit.</p>') +
          '<div class="row2" style="margin-top:12px"><button class="btn sm" type="button" data-reset>Reset to the default layout</button></div>',
        onMount(b, scm, close) { scm.listen(b, 'click', e => { const a = e.target.closest('[data-add]'); if (a) { close(); addCard(a.dataset.add); return; } if (e.target.closest('[data-reset]')) { close(); reset(); } }); } });
    }
    sc.listen(grid, 'click', e => {
      const x = e.target.closest('.dc-x'); if (x) { removeCard(x.closest('.dcard').dataset.card); return; }
      const o = e.target.closest('.dc-open'); if (o) { const v = BY_ID[o.closest('.dcard').dataset.card].view; if (v === 'workspace' && ui.nav) ui.nav.go((S.ui.ws && S.ui.ws.tab) || 'files'); else SL.views.show(v); }
    });
    sc.listen(grid, 'keydown', e => {
      if (D.locked) return; const el = e.target; if (!el.classList || !el.classList.contains('dcard')) return; const id = el.dataset.card;
      if (e.key === 'Delete') { e.preventDefault(); removeCard(id); return; }
      const next = keyOp(L, id, e.key, e.shiftKey); if (!next) return; e.preventDefault(); e.stopPropagation();
      if (!same(next, L)) { commit(next); const c = L.find(k => k.id === id); say(BY_ID[id].title + ': column ' + (c.x + 1) + ', row ' + (c.y + 1) + ', ' + c.w + ' by ' + c.h); }
    });

    /* ---- pointer: drag to move, the edges to resize; the grid follows while the pointer moves ---- */
    sc.listen(grid, 'pointerdown', e => {
      if (D.locked || e.button !== 0 || e.target.closest('.dc-x')) return; const el = e.target.closest('.dcard'); if (!el) return;
      const id = el.dataset.card, card = cards[id]; if (!card) return; if (card.cancelDrag) card.cancelDrag();
      const c0 = L.find(k => k.id === id); if (!c0) return; const rs = e.target.closest('[data-rs]'), mode = rs ? rs.dataset.rs : 'move';
      if (dash.dataset.stack === '1' && mode === 'move') return;   // a single column: the keyboard moves
      e.preventDefault(); const r = grid.getBoundingClientRect(), g = gap(), colP = (r.width + g) / COLS, rowP = (parseFloat(grid.style.getPropertyValue('--rowh')) || ROW_MIN) + g;
      const L0 = L, x0 = e.clientX, y0 = e.clientY; el.classList.add('dragging'); document.body.classList.add('resizing'); try { el.setPointerCapture(e.pointerId); } catch (er) { /* synthetic event */ }
      let cur = L0;
      const mv = ev => {
        const dx = Math.round((ev.clientX - x0) / colP), dy = Math.round((ev.clientY - y0) / rowP);
        const next = mode === 'move' ? move(L0, id, c0.x + dx, c0.y + dy) : resize(L0, id, c0.w + (mode !== 's' ? dx : 0), c0.h + (mode !== 'e' ? dy : 0));
        if (!same(next, cur)) { cur = next; L = next; paintAll(); }
      };
      const clean = () => { el.removeEventListener('pointermove', mv); el.removeEventListener('pointerup', up); el.removeEventListener('pointercancel', cancel); el.classList.remove('dragging'); document.body.classList.remove('resizing'); card.cancelDrag = null; try { el.releasePointerCapture(e.pointerId); } catch (er) { /* already released */ } };
      const up = () => { clean(); if (!same(cur, L0)) commit(cur); };
      const cancel = () => { clean(); L = L0; paintAll(); }; card.cancelDrag = cancel;
      el.addEventListener('pointermove', mv); el.addEventListener('pointerup', up); el.addEventListener('pointercancel', cancel);
    });
    /* a single column (a narrow view): the grid stacks the cards in their order; the edges do not resize there */
    const stackQ = () => { const w = root.clientWidth; dash.dataset.stack = w && w <= 620 ? '1' : '0'; };
    sc.observe(root, stackQ); stackQ();
    Object.assign(me, { layout: () => copy(L), setLocked, add: addCard, remove: removeCard, reset, picker: openPicker }); M = me;
  }
  /** The mounted dashboard's controls (for Settings and the tour): the layout, the lock, add, remove, reset and the picker. Without a
   *  mounted Cockpit they act on what is stored and on the lock of the page. */
  ui.dash = {
    mount, get root() { return M ? M.root : null; }, locked: () => D.locked,
    layout: () => (M ? M.layout() : load(store())),
    setLocked(v) { if (M) M.setLocked(v); else D.locked = !!v; },
    add(id) { if (M) M.add(id); }, remove(id) { if (M) M.remove(id); },
    reset() { if (M) M.reset(); else forget(store()); }, picker() { if (M) M.picker(); },
  };
})(SL);
