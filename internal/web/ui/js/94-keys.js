/* 94-keys.js: SL.keys, the global keyboard handling (shell scope).
 * Esc precedence, in this order: (1) release a hold, (2) close an overlay (modal, inbox, session menu, drawer, slash menu), (3) answer a
 * question with choice 3 when the keyboard has been quiet, (4) leave a field, (5) interrupt the running turn (pauses the goal).
 * Answer keys 1/2/3 count only after the quiet period (see 85-ui-approvals.js); every other key press resets that period. */
(function (SL) {
  'use strict';
  const U = SL.u, { $ } = U, ui = SL.ui = SL.ui || {}, calc = SL.calc;
  const VIEW_KEYS = { o: 'cockpit', c: 'cache', m: 'mail', b: 'board', r: 'replay', s: 'sessions', ',': 'settings', '.': 'tools' };
  const typing = t => t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.tagName === 'SELECT' || t.isContentEditable);

  function mount(sc) {
    sc.listen(document, 'keydown', e => {
      const t = e.target, inField = typing(t), mod = e.key === 'Shift' || e.key === 'Control' || e.key === 'Alt' || e.key === 'Meta';
      if (ui.modeMenu && ui.modeMenu.isOpen() && !e.ctrlKey && !e.metaKey && !e.altKey && /^(Arrow(Up|Down|Left|Right)|Home|End|Enter| |Tab)$/.test(e.key)) return;   /* the open mode menu owns its keys */
      /* an approval takes a key only when the keyboard has been quiet; text typed ahead goes to the prompt */
      if (!mod && !e.ctrlKey && !e.metaKey && !e.altKey && (e.key === '1' || e.key === '2' || e.key === '3') && !ui.hasModal() && (!inField || (t.id === 'input' && t.value === ''))) { if (ui.approvals.tryKey(e.key)) { e.preventDefault(); return; } }
      if (!mod) SL.time.noteKey();
      if (e.key === 'Escape') {
        if (SL.time.holdWanted()) { const S = SL.sessions.active; S.hold.pinned = false; S.hold.vtSaved = null; SL.time.pin(false); SL.time.release(); e.preventDefault(); return; }
        if (ui.hasModal()) { e.preventDefault(); ui.closeModal(); return; }
        if (ui.inbox && ui.inbox.isOpen()) { ui.inbox.close(); e.preventDefault(); return; }
        if ($('.sessmenu') || $('.budpop') || $('.modemenu')) return;
        if (ui.drawerOpen && ui.drawerOpen()) { e.preventDefault(); ui.closeDrawer(); return; }
        if (inField && t.id === 'input' && !$('#slash').hidden) return;
        if (!inField && ui.approvals.tryKey('Escape')) { e.preventDefault(); return; }
        if (inField) { t.blur(); return; }
        const S = SL.sessions.active; if (S && S.replay) { S.goLive(); S.touch(); return; }
        const r = SL.act.interrupt('turn'); ui.toast(r.ok ? 'interrupted: the turn is stopped and the goal is paused (/goal resume)' : 'nothing is running to interrupt', r.ok ? 'warm' : 'quiet'); return;
      }
      if (e.altKey && !e.ctrlKey && !e.metaKey && !e.shiftKey && /^Digit[1-9]$/.test(e.code)) { const S2 = SL.sessions.list[+e.code.slice(5) - 1]; e.preventDefault(); if (S2) SL.act.switchSession(S2.id); return; }   // alt+1..9: the nth session tab
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'k') { e.preventDefault(); SL.palette.open(''); return; }
      if ((e.ctrlKey || e.altKey) && !e.shiftKey && !e.metaKey && e.key.toLowerCase() === 't') { e.preventDefault(); SL.views.show('cache'); return; }
      if ((e.ctrlKey || e.altKey) && !e.shiftKey && !e.metaKey && e.key.toLowerCase() === 'g') { e.preventDefault(); SL.views.show('cockpit'); return; }
      if (e.ctrlKey && !e.shiftKey && !e.altKey && e.key.toLowerCase() === 'o') { e.preventDefault(); const tk = $('#talk'); tk.classList.toggle('xo'); ui.toast(tk.classList.contains('xo') ? 'tool output expanded (ctrl+o)' : 'tool output collapsed'); return; }
      if (inField || ui.hasModal() || e.ctrlKey || e.metaKey || e.altKey) return;
      const k = e.key, S = SL.sessions.active;
      if (ui.nav && ui.nav.chord(k)) { e.preventDefault(); return; }
      if (k === '/') { e.preventDefault(); if (window.innerWidth <= 900 || getComputedStyle($('#rail')).display === 'none') SL.palette.open(''); else { ui.rail.expand(); const i = $('#input'); i.focus(); i.value = '/'; i.dispatchEvent(new Event('input')); } return; }
      if (k === '?') { e.preventDefault(); ui.sheets.help(); return; }
      if (VIEW_KEYS[k]) { e.preventDefault(); if (window.innerWidth <= 900) $('#app').dataset.pv = 'main'; SL.views.show(VIEW_KEYS[k]); return; }
      if (k === ' ' && !(t && (t.tagName === 'BUTTON' || t.tagName === 'A' || t.getAttribute('role') === 'button'))) {
        e.preventDefault(); if (SL.time.T.hover === 'chat' || SL.time.holdWanted()) { ui.togglePin(); return; }
        if (!S.replay) S.seek(S.vt, false); else S.replay.playing = !S.replay.playing; S.touch(); return;
      }
      if (k === 'ArrowLeft' || k === 'ArrowRight') { if (t && t.matches && t.matches('input[type=range]')) return; e.preventDefault(); const d = (e.shiftKey ? 60 : 10) * (k === 'ArrowLeft' ? -1 : 1); S.seek(S.vt + d, S.replay ? S.replay.playing : false); S.touch(); return; }
      if (k === 'Home') { S.seek(0, false); S.touch(); return; } if (k === 'End') { S.goLive(); S.touch(); return; }
      if (k === '+' || k === '=') { if (!S.replay) S.seek(S.vt, true); S.replay.speed = Math.min(4, S.replay.speed * 2); S.touch(); return; } if (k === '-') { if (S.replay) { S.replay.speed = Math.max(1, S.replay.speed / 2); S.touch(); } return; }
      if ((k === 'ArrowDown' || k === 'ArrowUp') && !(t && t.closest && t.closest('.ctable,.cmds,.sheet,.stalls'))) {
        e.preventDefault(); const ids = S.roster.map(r => r.id), i = ids.indexOf(SL.link.sel), nx = ids[i < 0 ? (k === 'ArrowDown' ? 0 : ids.length - 1) : (i + (k === 'ArrowDown' ? 1 : ids.length - 1)) % ids.length]; SL.link.select(nx); return;
      }
      if (k === 'Enter' && SL.link.sel && document.activeElement === document.body) { ui.focusAgent(SL.link.sel, e); return; }
    }, true);
    sc.listen(document, 'keyup', () => { /* reserved */ });
  }
  SL.keys = { mount };
})(SL);
