/* 95-ui-tour.js: the guided tour (SL.tour, the steps and their order; ui.tour, the overlay).
 *
 * The tour runs by itself the first time the page opens in a browser, once a session is on screen and no dialog is open; it never runs
 * by itself in a browser driven by automation (navigator.webdriver). Each step dims and blurs the page around one region and explains
 * it in a card with Back, Next and Skip tour; Escape skips, the arrows step. Finishing the tour, or skipping it with "Don't show this
 * again" ticked, keeps it from running by itself again (localStorage, KEY); a skip without the tick leaves it to run the next time the
 * page opens. It runs again on demand from the Help sheet (?), the command palette (Take the tour) and Settings › Appearance.
 *
 * A step about a card the person took off the Cockpit is passed over; a step whose region is not on screen (hidden on a narrow
 * screen) is shown without a spotlight. The spotlight follows the region when the window is resized or the page moves. */
(function (SL) {
  'use strict';
  const U = SL.u, { $, esc, mk } = U, ui = SL.ui = SL.ui || {};
  const KEY = 'sleipnir.web.tour', VERSION = 1;
  /** The steps: title, body (page markup), at (selectors tried in order; the first on screen is the region; none = the whole page),
   *  need (what is shown first: the Cockpit, or the Radio rail) and card (a card of the Cockpit: the step is passed over when the
   *  person took that card off the Cockpit). */
  const STEPS = [
    { id: 'welcome', title: 'Welcome to Sleipnir', body: '<p>A short tour of the page: where the sessions are, how to talk to the manager, how to answer its questions and how to read what the team does.</p><p class="dim">Next or → steps, Back or ← goes back, Esc skips. The tour can be taken again from <b>?</b> (Help), <kbd>ctrl+K</kbd> and Settings › Appearance.</p>' },
    { id: 'sessions', title: 'Sessions', at: ['#sstrip'], body: '<p>Each tab is a session: a manager and its team, running in its own folder. A tab keeps running, and can ask questions, while another is in front.</p><p><b>+ New</b> starts a session: the folder, the model, the permission mode and the team (how many workers, which roles, the verify command). Every option has a <b>?</b> beside it that says what it does. <b>Resume</b> continues a recorded session.</p>' },
    { id: 'hud', title: 'Goal, budget and cache', at: ['#hud'], body: '<p>The top bar follows the session in front: its goal and how long it has run, the money spent against the budget (<kbd>/budget</kbd>), the share of the prompt read from the cache, how long the shared prefix stays warm, and how many agents are active.</p>' },
    { id: 'nav', title: 'Views', at: ['#nav', '#mnav'], body: '<p>The rail switches views: the Cockpit, the Workspace (Files, Changes, Checkpoints, Merge), Cache, Mail, Board, Replay, Sessions, Tools and Settings. <kbd>g</kbd> then a letter goes straight to one (<kbd>g</kbd> <kbd>c</kbd> is the Cockpit); <b>Widen</b> shows the letters.</p>' },
    { id: 'hero', title: 'The horse', need: 'cockpit', card: 'hero', at: ['[data-card="hero"]'], body: '<p>The team as a horse: each of the eight legs is one role of the team (architect, scout, backend, frontend, devops, tester, docs, reviewer), coloured by the role, and moves with the busiest of that role\'s workers. A leg is labelled with its worker, or with the role and its count when the role has several (<b>be ×4</b>); point at it to list them. A role with no worker started yet stands as a <b>free slot</b>.</p>' },
    { id: 'team', title: 'Manager and stalls', need: 'cockpit', card: 'team', at: ['[data-card="team"]'], body: '<p>The manager\'s card, then one stall per worker: its state, what it is doing, its task, its leg and its scope, its tokens and cost. A dashed stall is a free slot: it names the role the manager can still start when the work needs one.</p><p>A click on a stall opens that agent\'s details, read-only: you talk to the manager only.</p>' },
    { id: 'gantt', title: 'Swarm gantt', need: 'cockpit', card: 'gantt', at: ['[data-card="gantt"]'], body: '<p>What each agent did in the last minute: thinking, tools, edits, waits and questions. Its controls are the replay: pause, 1x 2x 4x and the scrub bar move through the session; <b>● live</b> comes back.</p>' },
    { id: 'board', title: 'Task board and merge queue', need: 'cockpit', card: 'tasks', at: ['[data-card="tasks"]', '[data-card="queue"]'], body: '<p>The board holds the tasks by status: todo, running, verify, merged; a ✗ marks a failed task. The merge queue rebases each submitted task and runs the verify command before it merges, and counts conflicts and bounces.</p>' },
    { id: 'radio', title: 'The manager', need: 'rail', at: ['#rail .talkwrap', '#rail'], body: '<p>The Radio rail is the conversation with the manager, the only agent you talk to. Team activity under it is read-only: what the workers do, the mail between them, merges and cache breaks.</p>' },
    { id: 'composer', title: 'Talking to it', need: 'rail', at: ['#composer'], body: '<p><kbd>Enter</kbd> sends. While a turn runs, Enter queues the line for after it: each queued line has <b>Send now</b>, <b>Edit</b> and ×. <kbd>ctrl+Enter</kbd> steers the running turn instead, and <kbd>Esc</kbd> interrupts it (the goal pauses).</p><p><kbd>/</kbd> lists the commands, <kbd>@</kbd> completes a path; the button on the left sets the permission mode.</p>' },
    { id: 'approvals', title: 'Questions', need: 'rail', at: ['#qSlot .qstrip', '#qSlot > *', '#rail'], body: '<p>When an agent wants to do something the permission mode does not allow, its question appears here: the command or the diff, where and why. Answer with the buttons or <kbd>1</kbd> yes, <kbd>2</kbd> yes and do not ask again, <kbd>3</kbd> no (or <kbd>Esc</kbd>).</p><p>The keys count only after the keyboard has been quiet for a moment, so text typed ahead never answers. A paused or scrubbed view cannot answer: go <b>live</b> first.</p>' },
    { id: 'cache', title: 'The cache', at: ['[data-nav="cache"]', '#nav', '#mnav'], body: '<p>The Cache view shows the prompt by layer, every request and the share read from the cache, and each <b>cache break</b>: the layer that changed and why. A stall marks a break with ⚠ for a few seconds.</p>' },
    { id: 'customise', title: 'Make the Cockpit yours', need: 'cockpit', at: ['.dash-lock'], body: '<p><b>Customise</b> unlocks the Cockpit (every page has the same button): drag a card to move it, its edges to resize it, × to hide it in the <b>Hidden</b> tray, ⋯ for its options, and <b>Add card</b> to put any card of any page, or any view of the rail, on it. With the keyboard, a focused card moves with the arrows and resizes with shift and the arrows.</p><p>The layout follows you from one browser to another; <b>Reset layout</b> brings the default back. <b>Done</b> locks it again.</p>' },
    { id: 'settings', title: 'Settings', at: ['[data-nav="settings"]', '#nav', '#mnav'], body: '<p>Models and roles, the budget, permissions and trust, the team\'s run settings, tool servers, skills, providers, the configuration layers, and the look of this page.</p>' },
    { id: 'again', title: 'That is the tour', body: '<p>Take it again whenever you like: the <b>Take the tour</b> button of the Help sheet (<kbd>?</kbd>), <b>Take the tour</b> in the command palette (<kbd>ctrl+K</kbd>), or Settings › Appearance.</p>' },
  ];
  /** What is stored: 'done' (finished), 'never' (skipped with "Don't show this again"), or null (nothing yet, or unreadable). */
  function stored(store) { try { const o = JSON.parse((store && store.getItem(KEY)) || 'null'); return o && o.v === VERSION && (o.state === 'done' || o.state === 'never') ? o.state : null; } catch (e) { return null; } }
  /** Keep how the tour ended: 'done' or 'never'. */
  function record(store, state) { try { store.setItem(KEY, JSON.stringify({ v: VERSION, state })); return true; } catch (e) { return false; } }
  /** Whether the tour starts by itself: nothing stored and no automation driving the browser. */
  const shouldAuto = (store, nav) => !(nav && nav.webdriver) && stored(store) === null;
  /** What ending the tour stores: finishing it stores 'done'; a skip stores 'never' with the tick, nothing without it. */
  const ending = (how, never) => (how === 'finish' ? 'done' : never ? 'never' : null);
  /** The step after i in direction dir (+1 or -1), passing over the steps that cannot be shown (has(step) is false: their card is
   *  not on the Cockpit); -1 before the first, steps.length after the last. */
  function stepFrom(steps, i, dir, has) { let j = i + dir; while (j >= 0 && j < steps.length && !has(steps[j])) j += dir; return Math.max(-1, Math.min(steps.length, j)); }
  /** Whether a step can be shown with the Cockpit's layout L: a step about a card needs that card on it. */
  const hasIn = L => step => !step.card || L.some(c => c.id === step.card);
  SL.tour = { KEY, VERSION, STEPS, stored, record, shouldAuto, ending, stepFrom, hasIn };

  /* ---------------------------------------------------------------------------------------------------- the overlay */
  const store = () => { try { return window.localStorage; } catch (e) { return null; } };
  const T = { el: null, i: 0, steps: STEPS, timer: 0, prev: null, off: null, auto: false };
  const PAD = 6;
  /** The element a step points at: the first of its selectors with a box on screen (several selectors of a step are joined into one
   *  region only when the first is on screen and the others are its neighbours: the board and the queue). */
  function regionOf(step) {
    if (!step.at) return null; const vis = el => { if (!el) return null; const r = el.getBoundingClientRect(); return r.width > 2 && r.height > 2 ? r : null; };
    const first = step.at.map(s => vis($(s))).find(Boolean); if (!first) return null;
    if (step.id === 'board') { const q = vis($(step.at[1])); if (q && vis($(step.at[0]))) return { left: Math.min(first.left, q.left), top: Math.min(first.top, q.top), right: Math.max(first.right, q.right), bottom: Math.max(first.bottom, q.bottom) }; }
    return { left: first.left, top: first.top, right: first.right, bottom: first.bottom };
  }
  /** Show what a step needs before it is measured: the Cockpit, or the Radio rail (on a phone, the pane of one or the other). */
  function prepare(step) {
    const app = $('#app'), phone = window.innerWidth <= 900;
    if (step.need === 'cockpit') { if (!SL.views.current || SL.views.current.name !== 'cockpit') SL.views.show('cockpit'); if (phone && app) app.dataset.pv = 'cockpit'; }
    else if (step.need === 'rail') { if (ui.rail) ui.rail.expand(); if (phone && app) app.dataset.pv = 'radio'; }
    else if (phone && app && app.dataset.pv === 'radio') app.dataset.pv = 'main';
  }
  /** Whether a step can be shown now (its card is on the Cockpit). */
  const has = step => hasIn(ui.dash ? ui.dash.layout() : [])(step);
  /** Scroll the region of a step into view when part of it is off screen (a card further down the Cockpit, a phone's long pane). */
  function reveal(step) {
    const el = (step.at || []).map(x => $(x)).find(e => e && e.getBoundingClientRect().width > 2); if (!el || !el.scrollIntoView) return;
    const v = el.closest && el.closest('.view'), box = v ? v.getBoundingClientRect() : { top: 0, bottom: window.innerHeight }, r = el.getBoundingClientRect();
    if (r.top < box.top || r.bottom > box.bottom) el.scrollIntoView({ block: r.height > (box.bottom - box.top) * 0.6 ? 'start' : 'center' });
  }
  function layout() {
    if (!T.el) return; const step = T.steps[T.i], r = regionOf(step), W = window.innerWidth, H = window.innerHeight, sh = T.el.querySelectorAll('.tour-sh'), ring = T.el.querySelector('.tour-ring'), card = T.el.querySelector('.tour-card');
    const box = (el, x, y, w, h) => { el.style.left = x + 'px'; el.style.top = y + 'px'; el.style.width = Math.max(0, w) + 'px'; el.style.height = Math.max(0, h) + 'px'; };
    if (!r) { box(sh[0], 0, 0, W, H); [1, 2, 3].forEach(k => box(sh[k], 0, 0, 0, 0)); ring.hidden = true; }
    else {
      const x0 = Math.max(0, r.left - PAD), y0 = Math.max(0, r.top - PAD), x1 = Math.min(W, r.right + PAD), y1 = Math.min(H, r.bottom + PAD);
      box(sh[0], 0, 0, W, y0); box(sh[1], 0, y1, W, H - y1); box(sh[2], 0, y0, x0, y1 - y0); box(sh[3], x1, y0, W - x1, y1 - y0);
      ring.hidden = false; box(ring, x0, y0, x1 - x0, y1 - y0); T.hole = { x0, y0, x1, y1 };
    }
    const cw = card.offsetWidth, ch = card.offsetHeight, M = 12, cl = (v, a, b) => Math.max(a, Math.min(b, v)); let x, y;
    if (!r) { x = (W - cw) / 2; y = (H - ch) / 2; }
    else {
      const { x0, y0, x1, y1 } = T.hole;
      if (y1 + M + ch <= H - 8) { y = y1 + M; x = cl(x0, 8, W - cw - 8); }
      else if (y0 - M - ch >= 8) { y = y0 - M - ch; x = cl(x0, 8, W - cw - 8); }
      else if (x1 + M + cw <= W - 8) { x = x1 + M; y = cl(y0, 8, H - ch - 8); }
      else if (x0 - M - cw >= 8) { x = x0 - M - cw; y = cl(y0, 8, H - ch - 8); }
      else { x = cl(x1 - cw - 16, 8, W - cw - 8); y = cl(y1 - ch - 16, 8, H - ch - 8); }   // a region as large as the screen: the card sits inside it
    }
    card.style.left = Math.round(x) + 'px'; card.style.top = Math.round(y) + 'px';
  }
  function paint() {
    const step = T.steps[T.i], n = T.steps.length, card = T.el.querySelector('.tour-card'), last = stepFrom(T.steps, T.i, 1, has) >= n;
    prepare(step); reveal(step);
    card.querySelector('.tour-n').textContent = (T.i + 1) + ' / ' + n;
    card.querySelector('.tour-bar i').style.width = Math.round((T.i + 1) / n * 100) + '%';
    card.querySelector('#tourT').textContent = step.title; card.querySelector('#tourB').innerHTML = step.body;
    const back = card.querySelector('[data-t="back"]'), next = card.querySelector('[data-t="next"]');
    back.disabled = stepFrom(T.steps, T.i, -1, has) < 0; next.textContent = last ? 'Finish' : 'Next';
    T.el.dataset.step = step.id; layout();
    /* the page may move under the spotlight (a view mounting, the rail opening): measure again once it settled */
    setTimeout(layout, 60); setTimeout(layout, 260);
  }
  function go(dir) { const j = stepFrom(T.steps, T.i, dir, has); if (j < 0) return; if (j >= T.steps.length) { end('finish'); return; } T.i = j; paint(); const nx = T.el.querySelector('[data-t="next"]'); if (nx) nx.focus(); }
  /** End the tour: how is 'finish' or 'skip'; the tick of "Don't show this again" decides what a skip stores. quiet: no toast. */
  function end(how, quiet) {
    if (!T.el) return; const never = !!(T.el.querySelector('#tourNever') || {}).checked, st = ending(how, never);
    if (st) record(store(), st);
    clearInterval(T.timer); if (T.off) T.off(); T.el.remove(); T.el = null; document.body.classList.remove('touring');
    if (T.view && (!SL.views.current || SL.views.current.name !== T.view)) SL.views.show(T.view);
    const app = $('#app'); if (app) { if (T.pane == null) delete app.dataset.pv; else app.dataset.pv = T.pane; }
    if (T.prev && T.prev.focus && document.contains(T.prev)) T.prev.focus();
    if (!quiet) ui.toast(how === 'finish' ? 'tour finished: ? (Help) has it again' : st === 'never' ? 'the tour will not start by itself again: ? (Help) has it' : 'tour skipped: it comes back the next time the page opens, or from ? (Help)', 'quiet');
    SL.bus.emit('tour', false);
  }
  /** Start the tour at its first step (or at the step with id `from`). Closes an open dialog first. */
  function start(from) {
    if (T.el) end('skip', true);
    if (ui.closeModal) ui.closeModal(true);
    if (ui.drawerOpen && ui.drawerOpen() && ui.closeDrawer) ui.closeDrawer();
    T.view = SL.views.current && SL.views.current.name; T.pane = $('#app') && $('#app').dataset.pv;
    T.prev = document.activeElement; T.i = Math.max(0, STEPS.findIndex(s => s.id === from)); T.steps = STEPS;
    const el = T.el = mk('div', { class: 'tour', id: 'tour' });
    el.innerHTML = '<div class="tour-sh"></div><div class="tour-sh"></div><div class="tour-sh"></div><div class="tour-sh"></div><div class="tour-ring" aria-hidden="true"></div>' +
      '<section class="tour-card" role="dialog" aria-modal="true" aria-labelledby="tourT" aria-describedby="tourB" tabindex="-1"><div class="tour-k"><span class="tour-n num"></span><span class="tour-bar" aria-hidden="true"><i></i></span><span class="tour-lab">tour</span></div>' +
      '<h2 id="tourT"></h2><div id="tourB" class="tour-b"></div>' +
      '<label class="chk tour-never"><input type="checkbox" id="tourNever"> Don\'t show this again</label>' +
      '<div class="tour-btns"><button class="btn sm" type="button" data-t="skip">Skip tour</button><span class="sp"></span><button class="btn sm" type="button" data-t="back">Back</button><button class="btn sm pri" type="button" data-t="next">Next</button></div></section>';
    document.body.appendChild(el); document.body.classList.add('touring');
    el.addEventListener('click', e => { const b = e.target.closest('[data-t]'); if (!b) return; const t = b.dataset.t; if (t === 'skip') end('skip'); else if (t === 'back') go(-1); else go(1); });
    /* the tour holds the keyboard: Escape skips, the arrows step, Tab stays in its card, and no key reaches the page behind it */
    const key = e => {
      if (!T.el) return; const card = T.el.querySelector('.tour-card'), inCard = card.contains(e.target);
      if (e.key === 'Escape') { e.preventDefault(); e.stopImmediatePropagation(); end('skip'); return; }
      if ((e.key === 'ArrowRight' || e.key === 'ArrowLeft') && !e.ctrlKey && !e.metaKey && !e.altKey) { e.preventDefault(); e.stopImmediatePropagation(); go(e.key === 'ArrowRight' ? 1 : -1); return; }
      if (e.key === 'Tab') { const f = Array.prototype.slice.call(card.querySelectorAll('button:not([disabled]),input')); const a = f[0], z = f[f.length - 1]; if (!inCard || (e.shiftKey && document.activeElement === a) || (!e.shiftKey && document.activeElement === z)) { e.preventDefault(); (e.shiftKey ? z : a).focus(); } e.stopImmediatePropagation(); return; }
      e.stopImmediatePropagation(); if (!inCard) e.preventDefault();
    };
    const rs = () => layout();
    window.addEventListener('keydown', key, true); window.addEventListener('resize', rs);
    T.off = () => { window.removeEventListener('keydown', key, true); window.removeEventListener('resize', rs); };
    T.timer = setInterval(layout, 400);
    if (!has(STEPS[T.i])) T.i = Math.min(STEPS.length - 1, stepFrom(STEPS, T.i, 1, has));
    paint(); el.querySelector('[data-t="next"]').focus(); SL.bus.emit('tour', true);
  }
  /** Start the tour by itself once, when it should (shouldAuto) and the page is ready for it: a session on screen, no dialog open.
   *  sc is the shell's scope: the waiting stops with it. */
  function auto(sc) {
    if (!shouldAuto(store(), typeof navigator !== 'undefined' ? navigator : null)) return;
    const id = sc.interval(() => {
      const S = SL.sessions.active; if (T.el) { sc.clear(id); return; }
      if (!S || S.placeholder || !SL.sessions.list.length || (ui.hasModal && ui.hasModal()) || !SL.views.current) return;
      sc.clear(id); T.auto = true; start();
    }, 1200);
  }
  ui.tour = { start, end, auto, next: () => go(1), back: () => go(-1), get open() { return !!T.el; }, get step() { return T.el ? T.steps[T.i].id : null; } };
})(SL);
