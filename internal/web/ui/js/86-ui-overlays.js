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
  ui.toast = function (msg, kind) {
    const host = $('#toasts'); if (!host) return; const t = mk('div', { class: 'toast ' + (kind || '') }); t.textContent = msg; host.appendChild(t);
    const id = setTimeout(() => t.remove(), 3600); t._id = id; while (host.children.length > 3) { clearTimeout(host.firstChild._id); host.firstChild.remove(); }
    const an = $('#announcer'); if (an && kind !== 'quiet') an.textContent = msg;
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

  /* ---------- the session menu (rename / stop / close) ---------- */
  ui.sessionMenu = function (btn) {
    const S = SL.sessions.active, app = $('#app'); const old = $('.sessmenu'); if (old) { old.remove(); return; }
    const p = mk('div', { class: 'popover sessmenu', role: 'menu' }, '<button role="menuitem" type="button" data-a="rename">Rename…</button><button role="menuitem" type="button" data-a="restart">Start the team again…</button><button role="menuitem" type="button" data-a="stop">Stop the run…</button><button role="menuitem" type="button" data-a="close" class="bad">Close this session…</button>');
    app.appendChild(p); const r = btn.getBoundingClientRect(), ar = app.getBoundingClientRect(); p.style.top = (r.bottom - ar.top + 4) + 'px'; p.style.left = Math.max(8, r.left - ar.left) + 'px'; $('button', p).focus();
    const off = () => { p.remove(); document.removeEventListener('pointerdown', away, true); document.removeEventListener('keydown', esck, true); };
    const away = e => { if (!p.contains(e.target)) off(); }, esck = e => { if (e.key === 'Escape') { e.stopPropagation(); off(); btn.focus(); } };
    document.addEventListener('pointerdown', away, true); document.addEventListener('keydown', esck, true);
    p.addEventListener('click', e => { const a = e.target.closest('[data-a]'); if (!a) return; off(); const k = a.dataset.a;
      if (k === 'rename') ui.dialogs.rename(S);
      else if (k === 'restart') ui.settingsPage('run');
      else if (k === 'stop') ui.confirm({ title: 'Stop the run', text: 'Stop <b>' + esc(S.name) + '</b>? Every agent is interrupted and the goal is paused. The session stays open and its log is kept.', ok: 'Stop the run', danger: true, run: () => { const r2 = SL.act.interrupt('turn'); ui.toast(r2.ok ? 'stopped: the goal is paused' : (r2.why || 'nothing was running'), 'warm'); } });
      else ui.closeSessionAsk(S);
    });
  };
})(SL);
