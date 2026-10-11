/* 86-ui-overlays.js: modal sheets and dialogs (SL.ui.modal), toasts, the in-page confirm, icons and the `still` (reduced motion) switch.
 * A modal owns its own scope: closing it cancels what it started. Modals are NOT views: they cover the app and take focus until closed;
 * view keys are off while one is open. Transient layers of VIEWS (arcs, drawer, tooltips) are never created here. */
(function (SL) {
  'use strict';
  const U = SL.u, { $, $$, esc, mk } = U, ui = SL.ui = SL.ui || {};

  ui.IC = {
    pause: '<svg class="ic" viewBox="0 0 12 12" width="12" height="12" aria-hidden="true"><rect x="2" y="1.5" width="3" height="9"/><rect x="7" y="1.5" width="3" height="9"/></svg>',
    play: '<svg class="ic" viewBox="0 0 12 12" width="12" height="12" aria-hidden="true"><path d="M3 1.5 L10.5 6 L3 10.5Z"/></svg>',
    start: '<svg class="ic" viewBox="0 0 12 12" width="12" height="12" aria-hidden="true"><rect x="1.5" y="1.5" width="2" height="9"/><path d="M10.5 1.5 L4.5 6 L10.5 10.5Z"/></svg>',
    end: '<svg class="ic" viewBox="0 0 12 12" width="12" height="12" aria-hidden="true"><rect x="8.5" y="1.5" width="2" height="9"/><path d="M1.5 1.5 L7.5 6 L1.5 10.5Z"/></svg>',
  };
  /** Motion off: the horse stands, arcs and flashes are skipped; the clocks still advance. */
  const mq = window.matchMedia ? window.matchMedia('(prefers-reduced-motion: reduce)') : null;
  ui.still = () => { const m = SL.settings.motion; return m === 'reduce' || (m === 'auto' && !!(mq && mq.matches)); };
  ui.applyMotion = () => document.body.classList.toggle('still', ui.still());
  if (mq) (mq.addEventListener ? mq.addEventListener('change', ui.applyMotion) : mq.addListener(ui.applyMotion));

  /* ---------- toasts ---------- */
  /**
   * A toast: a line at the bottom left, spoken once by the toasts' live region. An error stays until it is dismissed (a click, or its ×)
   * or for 12 s, and the pointer over it pauses that; any other toast goes after 3.6 s. The last TOAST_KEEP stay on screen.
   */
  const TOAST_KEEP = 5, TOAST_MS = 3600, TOAST_ERR_MS = 12000;
  ui.toast = function (msg, kind) {
    const host = $('#toasts'); if (!host) return; const err = kind === 'err', t = mk('div', { class: 'toast ' + (kind || '') + (err ? ' sticky' : '') });
    t.textContent = msg; host.appendChild(t);
    const drop = () => { clearTimeout(t._id); t.remove(); }, arm = ms => { clearTimeout(t._id); t._id = setTimeout(drop, ms); };
    arm(err ? TOAST_ERR_MS : TOAST_MS);
    if (err) { const x = mk('button', { type: 'button', class: 'tx', 'aria-label': 'Dismiss' }); x.textContent = '×'; t.appendChild(x); t.addEventListener('click', drop); t.addEventListener('pointerenter', () => clearTimeout(t._id)); t.addEventListener('pointerleave', () => arm(TOAST_ERR_MS)); }
    while (host.children.length > TOAST_KEEP) { clearTimeout(host.firstChild._id); host.firstChild.remove(); }
  };

  /* ---------- modal ---------- */
  const MODAL = ui.modalState = { cur: null };
  /** Open a modal. o = {title, kicker, desc, body (html), wide, color, cls, focus (css), onMount(bodyEl, scope, close)}. Returns {close, el, scope}. */
  ui.modal = function (o) {
    ui.closeModal(true);
    const host = $('#overlayHost'), prev = document.activeElement, root = mk('div', { class: 'scrim' });
    root.innerHTML = '<div class="sheet ' + (o.wide ? 'wide ' : '') + (o.cls || '') + '" role="dialog" aria-modal="true" aria-label="' + esc(o.title) + '" style="--c:' + (o.color || 'var(--mgr)') + '" tabindex="-1"><div class="sh-h"><h2>' + esc(o.title) + '</h2>' + (o.kicker ? '<span class="k">' + esc(o.kicker) + '</span>' : '') + '<span class="d">' + esc(o.desc || '') + '</span><button class="x" type="button" data-close>esc · close</button></div><div class="sh-b">' + (o.body || '') + '</div></div>';
    host.appendChild(root); const sc = SL.makeScope(root, 'modal:' + o.title), sh = $('.sheet', root), body = $('.sh-b', root);
    const rec = { el: root, sheet: sh, body, scope: sc, close: () => ui.closeModal(), prev, onClose: o.onClose };
    MODAL.cur = rec;
    sc.listen(root, 'mousedown', e => { if (e.target === root) rec.close(); }); sc.listen($('[data-close]', root), 'click', () => rec.close());
    sc.listen(sh, 'keydown', e => { if (e.key !== 'Tab') return; const f = $$('button:not([disabled]),input:not([disabled]),textarea,select,[tabindex="0"]', sh).filter(x => x.offsetParent !== null); if (!f.length) return; const a = f[0], z = f[f.length - 1]; if (e.shiftKey && document.activeElement === a) { e.preventDefault(); z.focus(); } else if (!e.shiftKey && document.activeElement === z) { e.preventDefault(); a.focus(); } });
    if (o.onMount) o.onMount(body, sc, rec.close, rec);
    const first = o.focus ? $(o.focus, sh) : null; (first || sh).focus(); SL.bus.emit('modal', true);
    return rec;
  };
  ui.closeModal = function (quiet) {
    const r = MODAL.cur; if (!r) return false; MODAL.cur = null; r.scope.dispose(); r.el.remove(); if (r.onClose) r.onClose();
    if (!quiet && r.prev && r.prev.focus && document.contains(r.prev)) r.prev.focus(); SL.bus.emit('modal', false); return true;
  };
  ui.hasModal = () => !!MODAL.cur;

  /** In-page confirm (never window.confirm). ok(): runs on the confirm button. */
  ui.confirm = function (o) {
    return ui.modal({ title: o.title, kicker: o.kicker || 'confirm', desc: '', color: o.danger ? 'var(--err)' : 'var(--warm)', cls: 'confirm', body: '<p class="cf-t">' + o.text + '</p>' + (o.detail ? '<div class="cf-d">' + o.detail + '</div>' : '') + '<div class="row2"><button class="btn ' + (o.danger ? 'danger' : 'pri') + '" type="button" data-ok>' + esc(o.ok || 'Confirm') + '</button><button class="btn" type="button" data-no>' + esc(o.cancel || 'Cancel') + '</button></div>', focus: '[data-no]',
      onMount(b, sc, close) { sc.listen($('[data-ok]', b), 'click', () => { close(); o.run(); }); sc.listen($('[data-no]', b), 'click', close); } });
  };

  /* ---------- help dots: a ? beside what needs explaining ---------- */
  /** The attribute that gives an element an explanation (any element of a dialog that called ui.helpMount): the text, which may hold
   *  <b>, <kbd> and <span class="mono">, is the page's own copy. */
  ui.helpAttr = text => ' data-help="' + esc(text) + '"';
  /** A ? dot that shows the explanation `text` of `about` on hover, on keyboard focus and on a tap. */
  ui.help = (text, about) => '<button type="button" class="qh" aria-label="' + esc('What is ' + about + '?') + '"' + ui.helpAttr(text) + '>?</button>';
  /**
   * Make the explanations of a dialog show: an element with data-help shows its text in one popover when the pointer is over it or the
   * focus is on it (a tap shows it and another hides it), placed by the element and kept on screen, and Escape hides it before it
   * closes the dialog. body: the dialog's body, sc: its scope; the popover goes with the dialog.
   */
  ui.helpMount = function (body, sc) {
    const tip = mk('div', { class: 'helptip', role: 'tooltip', id: 'helpTip' }); tip.hidden = true; sc.root.appendChild(tip);
    let cur = null;
    const of = e => (e.target && e.target.closest ? e.target.closest('[data-help]') : null);
    const hide = () => { if (cur) { cur.removeAttribute('aria-describedby'); cur.removeAttribute('data-popup-open'); } cur = null; tip.hidden = true; };
    const place = d => {
      if (!d.getBoundingClientRect) return;
      const r = d.getBoundingClientRect(), vw = window.innerWidth || 800, vh = window.innerHeight || 600;
      tip.style.maxWidth = Math.min(340, vw - 16) + 'px'; tip.style.left = '0px'; tip.style.top = '0px';
      const w = tip.offsetWidth || 0, h = tip.offsetHeight || 0; let top = r.bottom + 6;
      if (top + h > vh - 8) top = Math.max(8, r.top - h - 6);
      tip.style.left = Math.min(Math.max(8, r.left + r.width / 2 - w / 2), Math.max(8, vw - w - 8)) + 'px'; tip.style.top = top + 'px';
    };
    const show = d => { if (cur === d) return; hide(); cur = d; tip.innerHTML = d.getAttribute('data-help'); tip.hidden = false; d.setAttribute('aria-describedby', 'helpTip'); d.setAttribute('data-popup-open', ''); place(d); };
    sc.listen(body, 'mouseover', e => { const d = of(e); if (d) show(d); });
    sc.listen(body, 'mouseout', e => { const d = of(e); if (d && !(e.relatedTarget && d.contains && d.contains(e.relatedTarget))) hide(); });
    sc.listen(body, 'focusin', e => { const d = of(e); if (d) show(d); else hide(); });
    sc.listen(body, 'focusout', hide);
    sc.listen(body, 'click', e => { const d = e.target && e.target.closest ? e.target.closest('.qh') : null; if (d) { e.preventDefault(); if (cur === d) hide(); else show(d); } });
    sc.listen(body, 'keydown', e => { if (e.key === 'Escape' && cur && !tip.hidden) { e.stopPropagation(); e.preventDefault(); hide(); } });
    sc.listen(body, 'scroll', hide, true);
  };

  /* ---------- the session menu (rename / stop / close) ---------- */
  /** The one open session menu, or null: every way it closes (an item, Escape, a press elsewhere, its button again) goes through
   *  close(), which removes the two document listeners it added. */
  const MENU = { cur: null };
  function closeMenu(focusBtn) { const c = MENU.cur; if (!c) return false; MENU.cur = null; c.p.remove(); document.removeEventListener('pointerdown', c.away, true); document.removeEventListener('keydown', c.esck, true); if (focusBtn && c.btn && document.contains(c.btn)) c.btn.focus(); return true; }
  ui.closeSessionMenu = closeMenu;
  /** Toggle the session menu under btn: open it, or close it when it is open. */
  ui.sessionMenu = function (btn) {
    if (MENU.cur) { closeMenu(false); return; }
    const S = SL.sessions.active, app = $('#app');
    const p = mk('div', { class: 'popover sessmenu', role: 'menu' }, '<button role="menuitem" type="button" data-a="rename">Rename…</button><button role="menuitem" type="button" data-a="restart">Start the team again…</button><button role="menuitem" type="button" data-a="stop">Stop the run…</button><button role="menuitem" type="button" data-a="close" class="bad">Close this session…</button>');
    app.appendChild(p); const r = btn.getBoundingClientRect(), ar = app.getBoundingClientRect(); p.style.top = (r.bottom - ar.top + 4) + 'px'; p.style.left = Math.max(8, r.left - ar.left) + 'px'; $('button', p).focus();
    /* a press on the menu's own button is left to its click, which toggles: it does not close here and reopen there */
    const away = e => { if (!p.contains(e.target) && !(btn.contains && btn.contains(e.target))) closeMenu(false); }, esck = e => { if (e.key === 'Escape') { e.stopPropagation(); closeMenu(true); } };
    MENU.cur = { p, btn, away, esck };
    document.addEventListener('pointerdown', away, true); document.addEventListener('keydown', esck, true);
    p.addEventListener('click', e => { const a = e.target.closest('[data-a]'); if (!a) return; closeMenu(false); const k = a.dataset.a;
      if (k === 'rename') ui.dialogs.rename(S);
      else if (k === 'restart') ui.settingsPage('run');
      else if (k === 'stop') ui.confirm({ title: 'Stop the run', text: 'Stop <b>' + esc(S.name) + '</b>? Every agent is interrupted and the goal is paused. The session stays open and its log is kept.', ok: 'Stop the run', danger: true, run: () => { const r2 = SL.act.interrupt('turn'); ui.toast(r2.ok ? 'stopped: the goal is paused' : (r2.why || 'nothing was running'), 'warm'); } });
      else ui.closeSessionAsk(S);
    });
  };
})(SL);
