/* 86b-ui-ctxmenu.js: SL.ui.ctx, the page's own right-click menu (shell scope).
 *
 * A right-click (a long press on a touch screen, the Menu key or Shift+F10 on the focused element) opens a menu of what can be done
 * with the thing under the pointer, instead of the browser's menu. The thing is read from the markup the views already draw:
 *
 *   a session tab (.stab[data-sid])   switch, rename, stop the run, start the team again, open in a new browser tab, copy its id, close
 *   an agent ([data-ag])              its details, ask the manager about it, copy its name, stop the worker (the manager: interrupt)
 *   a task ([data-task])              its verify output, the Board, ask the manager about it, copy its id
 *   a file ([data-file])              open its diff, the whole file or its preview in the Workspace, mention it (@path), copy the path
 *   a folder ([data-dir])             mention it, copy the path
 *   a message (.msg in the Radio)     copy its text, quote it in a reply, edit and send again (your own lines)
 *   a view of the rail ([data-nav])   open it, open it in a new browser tab
 *   a card of a page (.dcard)         its options, hide it, add a card, arrange the page
 *
 * One element can be several things (a stall is an agent and its task; a tool row is a message, a file and an agent): each is a
 * section of the one menu, the closest first. Where nothing matches, the menu holds the page's own entries (the command palette,
 * the manager, a new session, help). Every entry calls what a button of the page already does (SL.act, ui.dialogs, ui.ws, ui.dash):
 * the menu adds a way to reach an action, never an action.
 *
 * The browser's own menu stays where it is the useful one: in a text field, on a link, a picture, a video or a frame, over selected
 * text, and on any right-click with Shift held. ui.ctx.add(fn) lets another module add a section: fn(target, S) returns
 * {title, items} or null, and its section comes after the built-in ones that match. */
(function (SL) {
  'use strict';
  const U = SL.u, { $, esc, mk } = U, ui = SL.ui = SL.ui || {};

  /** The open menu ({p, prev, offs}) or null. */
  const CUR = { m: null };
  /** Sections added by other modules: fn(target, S) -> {title, items} | null. */
  const EXTRA = [];

  /** Close the open menu; back gives the focus back to what had it. */
  function close(back) {
    const c = CUR.m; if (!c) return false; CUR.m = null; c.offs.forEach(f => f()); c.p.remove();
    if (back && c.prev && c.prev.focus && document.contains(c.prev)) c.prev.focus({ preventScroll: true });
    return true;
  }

  /** Copy text and say whether it worked. */
  const copy = (txt, what) => U.copy(String(txt)).then(ok => ui.toast(ok ? what + ' copied' : 'copy is not available here', ok ? 'ok' : 'warm'));
  /** Put text in the message box (append, or replace what is there) and give it the focus. */
  function compose(text, replace) {
    const i = $('#input'); if (!i || i.disabled) { ui.toast('the message box is not available here', 'warm'); return; }
    if (ui.rail && ui.rail.open) ui.rail.open();
    const cur = replace ? '' : i.value; i.value = cur + (cur && !/\s$/.test(cur) ? ' ' : '') + text;
    i.dispatchEvent(new Event('input', { bubbles: true }));
    setTimeout(() => { if (i.disabled) return; i.focus(); const n = i.value.length; try { i.setSelectionRange(n, n); } catch (e) { /* not a text field */ } }, 40);
  }
  /** The page's address with ?view and ?session, for a new browser tab (the session cookie goes with it). */
  function pageUrl(view, sid) {
    const q = new URLSearchParams(); if (view) q.set('view', view); if (sid) q.set('session', sid);
    return location.pathname + '?' + q.toString();
  }
  const newTab = url => { window.open(url, '_blank', 'noopener'); };
  /** The result of an SL.act call: a refusal is toasted. */
  const did = r => { if (r && r.ok === false && r.why) ui.toast(r.why, 'warm'); return r; };
  /** The text a person reads in a message row: without its header, its time and its buttons. */
  function rowText(row) {
    const full = row.querySelector('[data-full]'); if (full && full.dataset.full) return full.dataset.full;
    const walk = n => {
      if (n.nodeType === 3) return n.textContent; if (n.nodeType !== 1 || n.matches('.who,time,button,.sg,.tg,.lg')) return '';
      let t = ''; n.childNodes.forEach(c => { t += walk(c); });
      return /^(div|p|li|pre|tr|h[1-6]|blockquote)$/.test(n.localName) ? t + '\n' : n.localName === 'br' ? '\n' : t;
    };
    return walk(row).replace(/[ \t]+\n/g, '\n').replace(/\n{3,}/g, '\n\n').trim();
  }
  /** An agent id from a data-ag value ("be-1", or "be-1 fe-1" on a mail row: the first). */
  const agOf = el => String(el.dataset.ag || '').split(' ')[0];

  /* ------------------------------------------------------------------------------------------------ the sections, closest first */
  function sessionSection(el) {
    const S = SL.sessions.get(el.dataset.sid); if (!S) return null; const act = SL.sessions.active === S, live = !S.recorded && !S.placeholder;
    const viaActive = fn => () => { if (!act) SL.act.switchSession(S.id); fn(); };
    return { title: 'Session · ' + S.name, items: [
      { label: 'Switch to it', off: act, act: () => SL.act.switchSession(S.id) },
      { label: 'Rename…', hint: 'F2', off: !live, act: () => ui.dialogs.rename(S) },
      { label: 'Open in a new browser tab', act: () => newTab(pageUrl(S.ui && S.ui.view, S.id)) },
      { label: 'Copy the session id', act: () => copy(S.id, 'session id') },
      { sep: '' },
      { label: 'Stop the run…', off: !live, act: () => ui.confirm({ title: 'Stop the run', text: 'Stop <b>' + esc(S.name) + '</b>? ' + SL.stopAsk(S) + ' The session stays open and its log is kept.', ok: 'Stop the run', danger: true, run: () => did(SL.act.stopAll(S.id)) }) },
      { label: 'Start the team again…', off: !live, act: viaActive(() => ui.settingsPage('run')) },
      { label: 'Close this session…', hint: 'Del', danger: true, off: S.placeholder, act: () => ui.closeSessionAsk(S) },
    ] };
  }
  function agentSection(el, S) {
    const id = agOf(el); if (!id || id === 'you' || !S || !S.m || !S.m.ag || !S.m.ag[id]) return null;
    if (id === 'mgr') return { title: 'Manager', items: [
      { label: 'Open its details', act: () => ui.openDrawer(id) },
      { label: 'Message the manager', act: () => ui.rail.focus() },
      { label: 'Interrupt its turn', danger: true, off: !(SL.calc.turnRunning(S.wm) || S.meta.running), act: () => did(SL.act.interrupt('turn')) },
    ] };
    const q = S.wm && S.wm.qs ? S.wm.qs.find(x => !x.answered && x.agent === id) : null;
    return { title: 'Agent · ' + id, items: [
      q ? { label: 'Answer its question', sub: U.clip1(q.cmd || '', 60), act: () => ui.approvals.goto(S.id, q.id) } : null,
      { label: 'Open its details', act: () => ui.openDrawer(id) },
      { label: 'Ask the manager about ' + id, act: () => compose('About ' + id + ': ') },
      { label: 'Copy its name', act: () => copy(id, 'name') },
      { label: 'Stop this worker…', sub: 'its task goes back to todo and the manager is told', danger: true, off: !!S.replay || !!S.recorded,
        act: () => ui.confirm({ title: 'Stop ' + id, text: 'Stop <b>' + esc(id) + '</b>? Its run ends, its task goes back to todo and the manager is told.', ok: 'Stop the worker', danger: true, run: () => did(SL.act.stopAgent(id)) }) },
    ].filter(Boolean) };
  }
  function taskSection(el) {
    const id = el.dataset.task || el.dataset.taskf || el.dataset.vtask; if (!id) return null;
    return { title: 'Task · ' + id, items: [
      { label: 'Show its verify output', act: () => ui.ws.verify(id) },
      { label: 'Show the task board', act: () => SL.views.show('board') },
      { label: 'Ask the manager about ' + id, act: () => compose('About task ' + id + ': ') },
      { label: 'Copy the task id', act: () => copy(id, 'task id') },
    ] };
  }
  function fileSection(el) {
    const path = el.dataset.file; if (!path) return null; const pk = SL.media && SL.media.kind ? SL.media.kind(path) : '';
    return { title: 'File · ' + path.split('/').pop(), items: [
      { label: 'Open its changes', act: () => ui.ws.open(path, null, 'diff') },
      { label: 'Open the whole file', act: () => ui.ws.open(path, null, 'file') },
      pk ? { label: 'Preview it', act: () => ui.ws.open(path, null, 'preview') } : null,
      { label: 'Mention it in a message', sub: '@' + path, act: () => compose('@' + path + ' ') },
      { label: 'Copy the path', act: () => copy(path, 'path') },
    ].filter(Boolean) };
  }
  function dirSection(el) {
    const path = el.dataset.dir; if (!path) return null;
    return { title: 'Folder · ' + (path.split('/').pop() || path), items: [
      { label: 'Mention it in a message', sub: '@' + path + '/', act: () => compose('@' + path + '/ ') },
      { label: 'Copy the path', act: () => copy(path, 'path') },
    ] };
  }
  function messageSection(row, S) {
    const text = rowText(row), mine = row.classList.contains('you'); if (!text) return null;
    const ro = !S || S.replay || S.recorded || S.placeholder;
    return { title: mine ? 'Your message' : 'Message', items: [
      { label: 'Copy the text', act: () => copy(text, 'text') },
      { label: 'Quote it in a reply', off: ro, act: () => compose(text.split('\n').map(l => '> ' + l).join('\n') + '\n\n') },
      mine ? { label: 'Edit and send again', off: ro, act: () => compose(text, true) } : null,
    ].filter(Boolean) };
  }
  function navSection(el) {
    const id = el.dataset.nav, view = SL.views.V.reg[id] ? id : null, S = SL.sessions.active;
    return { title: 'View · ' + ((el.getAttribute('aria-label') || id).split(',')[0]), items: [
      { label: 'Open it', act: () => { const b = document.querySelector('[data-nav="' + CSS.escape(id) + '"]'); if (b) b.click(); } },
      { label: 'Open it in a new browser tab', off: !view, act: () => newTab(pageUrl(view, S && !S.placeholder ? S.id : '')) },
    ] };
  }
  /** A card's section; short (its options, hide it) when a closer thing already has a section. */
  function cardSection(el, short) {
    const id = el.dataset.card; if (!id || !ui.dash) return null; const locked = ui.dash.locked();
    return { title: 'Card', items: [
      { label: 'Options of this card…', act: () => ui.dash.menu(id) },
      { label: 'Hide this card', sub: short ? '' : 'the Hidden tray brings it back', act: () => ui.dash.hide(id) },
      short ? null : { label: 'Add a card…', act: () => ui.dash.picker() },
      short ? null : { label: locked ? 'Arrange the cards' : 'Lock the layout', act: () => ui.dash.setLocked(!locked) },
    ].filter(Boolean) };
  }
  function pageSection(S) {
    const live = S && !S.placeholder;
    return { title: 'Sleipnir', items: [
      { label: 'Command palette…', act: () => SL.palette.open() },
      { label: 'Message the manager', off: !live, act: () => ui.rail.focus() },
      { label: 'New session…', act: () => ui.dialogs.newSession() },
      { label: 'Resume a recorded session…', act: () => ui.dialogs.resume() },
      { label: 'Help', act: () => ui.sheets.help() },
    ] };
  }

  /** The sections of the menu for target t, closest first; [] when the browser's menu is the useful one. */
  function sectionsFor(t) {
    const S = SL.sessions.active, out = [], seen = new Set();
    const push = (key, sec) => { if (sec && !seen.has(key) && sec.items.length) { seen.add(key); out.push(sec); } };
    for (let el = t; el && el !== document.body; el = el.parentElement) {
      if (!el.dataset) continue;
      if (el.dataset.sid && el.classList.contains('stab')) push('sid', sessionSection(el));
      if (el.classList.contains('msg') && el.closest('#talk,#feed,.chatdock')) push('msg', messageSection(el, S));
      if (el.dataset.file) push('file', fileSection(el));
      if (el.dataset.dir) push('dir', dirSection(el));
      if (el.dataset.ag) push('ag', agentSection(el, S));
      if (el.dataset.task || el.dataset.taskf || el.dataset.vtask) push('task', taskSection(el));
      if (el.dataset.nav) push('nav', navSection(el));
      if (el.dataset.card && el.classList.contains('dcard')) push('card', cardSection(el, out.length > 0));
    }
    EXTRA.forEach(fn => { try { const sec = fn(t, S); if (sec && sec.items && sec.items.length) out.push(sec); } catch (e) { console.error(e); } });
    if (!out.length || out.every(s => s.title === 'Card')) out.push(pageSection(S));
    return out;
  }

  /** Is the browser's own menu the useful one here (a field, a link, media, selected text)? */
  function native(e) {
    const t = e.target; if (e.shiftKey || !t || !t.closest) return true;
    if (t.closest('input,textarea,select,[contenteditable=""],[contenteditable="true"],a[href],img,video,audio,iframe,canvas,.scrim')) return true;
    const sel = window.getSelection && window.getSelection(); if (sel && !sel.isCollapsed && String(sel).trim()) { for (let i = 0; i < sel.rangeCount; i++) if (sel.getRangeAt(i).intersectsNode(t)) return true; }
    return false;
  }

  /**
   * Open a menu at (x, y) in the window, or under the element anchor when it is given. items are {label, sub?, hint?, act(), off?,
   * danger?} or {sep: 'heading'} ('' is a line). The arrows, Home and End move between the items, Enter or a click does one, Escape,
   * Tab, a press elsewhere, a resize or a scroll that moves target (the element it was opened on) closes it.
   */
  function open(x, y, items, label, anchor, target) {
    close(false); if (ui.dash && ui.dash.closeMenu) ui.dash.closeMenu(false); if (ui.closeSessionMenu) ui.closeSessionMenu(false);
    const prev = document.activeElement, p = mk('div', { class: 'popover dmenu ctxmenu', role: 'menu', 'aria-label': label || 'Menu' });
    items.forEach((it, i) => {
      if (it.sep != null) { p.appendChild(it.sep ? mk('div', { class: 'dmh', role: 'presentation' }, esc(it.sep)) : mk('div', { class: 'ctxsep', role: 'separator' })); return; }
      const b = mk('button', { type: 'button', role: 'menuitem', class: 'dmi' + (it.danger ? ' bad' : ''), 'data-i': String(i) }, '<span class="dml">' + esc(it.label) + (it.sub ? '<small>' + esc(it.sub) + '</small>' : '') + '</span>' + (it.hint ? '<kbd>' + esc(it.hint) + '</kbd>' : ''));
      if (it.off) b.disabled = true; p.appendChild(b);
    });
    p.appendChild(mk('div', { class: 'ctxfoot', role: 'presentation' }, 'Shift + right-click: the browser’s menu'));
    document.body.appendChild(p);
    /* inside the window: flipped left or up where it would cross the edge */
    const W = window.innerWidth, H = window.innerHeight, r = p.getBoundingClientRect();
    if (anchor) { const a = anchor.getBoundingClientRect(); x = a.left + Math.min(24, a.width / 2); y = a.bottom - Math.min(8, a.height / 2); }
    p.style.left = Math.max(6, Math.min(x, W - r.width - 6)) + 'px';
    p.style.top = Math.max(6, y + r.height > H - 6 ? Math.max(6, y - r.height) : y) + 'px';
    const btns = () => Array.from(p.querySelectorAll('.dmi')).filter(b => !b.disabled);
    const first = btns()[0]; if (first) first.focus({ preventScroll: true });
    const offs = [], on = (t, ty, fn, o) => { t.addEventListener(ty, fn, o); offs.push(() => t.removeEventListener(ty, fn, o)); };
    on(window, 'keydown', e => {
      if (e.key === 'Escape') { e.preventDefault(); e.stopImmediatePropagation(); close(true); return; }
      if (e.key === 'Tab') { e.preventDefault(); close(true); return; }
      if (['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(e.key)) {
        const bs = btns(), i = bs.indexOf(document.activeElement); if (!bs.length) return; e.preventDefault(); e.stopImmediatePropagation();
        const n = e.key === 'Home' ? 0 : e.key === 'End' ? bs.length - 1 : i < 0 ? 0 : (i + (e.key === 'ArrowDown' ? 1 : bs.length - 1)) % bs.length; bs[n].focus();
      }
    }, true);
    on(document, 'pointerdown', e => { if (!p.contains(e.target)) close(false); }, true);
    on(window, 'resize', () => close(false)); on(window, 'blur', () => close(false));
    /* a scroll that moves what the menu was opened on closes it; a box the views draw again (a new element) does not */
    const held = []; for (let el = anchor || target || null; el; el = el.parentElement) if (el.scrollHeight > el.clientHeight || el.scrollWidth > el.clientWidth) held.push([el, el.scrollTop, el.scrollLeft]);
    const page = [window.scrollX, window.scrollY];
    on(document, 'scroll', e => { if (p.contains(e.target)) return; if (window.scrollX !== page[0] || window.scrollY !== page[1] || held.some(([el, t, l]) => el.isConnected && (el.scrollTop !== t || el.scrollLeft !== l))) close(false); }, true);
    p.addEventListener('click', e => { const b = e.target.closest('.dmi'); if (!b || b.disabled) return; const it = items[+b.dataset.i]; close(true); if (it && it.act) { try { it.act(); } catch (er) { console.error(er); } } });
    p.addEventListener('contextmenu', e => e.preventDefault());
    CUR.m = { p, prev, offs };
    return p;
  }

  /** The menu of the thing under e (a contextmenu event): its sections, one after another. */
  function onContext(e) {
    if (native(e)) { close(false); return; }
    const secs = sectionsFor(e.target); if (!secs.length) return;
    e.preventDefault();
    const items = []; secs.forEach(s => { items.push({ sep: s.title }); s.items.forEach(it => items.push(it)); });
    /* the Menu key and Shift+F10 fire with no pointer: the menu opens under the focused element */
    const kb = e.button !== 2 && !(e.pointerType === 'touch' || e.pointerType === 'pen');
    const anchor = kb && e.clientX === 0 && e.clientY === 0 ? (e.target.closest('button,[tabindex],.msg,.dcard') || e.target) : null;
    open(e.clientX, e.clientY, items, secs[0].title, anchor, e.target);
  }

  ui.ctx = {
    /** Listen for right-clicks under the shell scope sc. */
    mount(sc) { sc.listen(document, 'contextmenu', onContext); sc.on('view-mounted', () => close(false)); sc.on('activated', () => close(false)); },
    open, close, sectionsFor,
    /** Add a section: fn(target, S) returns {title, items} or null. Returns a function that removes it. */
    add(fn) { EXTRA.push(fn); return () => { const i = EXTRA.indexOf(fn); if (i >= 0) EXTRA.splice(i, 1); }; },
    isOpen: () => !!CUR.m,
  };
})(SL);
