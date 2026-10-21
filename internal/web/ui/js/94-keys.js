/* 94-keys.js: SL.keys, the page's keyboard handling (shell scope).
 * The page has no keyboard shortcuts: a key pressed where it lands does what the browser does with it (a button, a list, a field), and
 * the message box's own keys (Enter sends, Up recalls, shift+tab steps the mode, / and @ complete) are the message box's. A shortcut that
 * acts on a key typed without the focus in a field was a trap: text typed before clicking the message box paused the view, changed the
 * view or answered a question. Here
 * Esc closes what is open (a dialog, the inbox, a menu, the drawer), and in a field it leaves the field. */
(function (SL) {
  'use strict';
  const U = SL.u, { $ } = U, ui = SL.ui = SL.ui || {};
  const typing = t => t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.tagName === 'SELECT' || t.isContentEditable);

  function mount(sc) {
    sc.listen(document, 'keydown', e => {
      if (U.composing(e)) return;
      const t = e.target, inField = typing(t), mod = e.key === 'Shift' || e.key === 'Control' || e.key === 'Alt' || e.key === 'Meta';
      if (!mod) SL.time.noteKey();
      if (e.key === 'Escape') {
        /* a field with a list open (a folder field) takes the key first: it closes the list, and the next one closes the dialog */
        if (t && t.hasAttribute && t.hasAttribute('data-popup-open')) return;
        if (ui.hasModal()) { e.preventDefault(); ui.closeModal(); return; }
        if (ui.inbox && ui.inbox.isOpen()) { ui.inbox.close(); e.preventDefault(); return; }
        if ($('.sessmenu') || $('.budpop') || $('.modemenu')) return;
        if (ui.drawerOpen && ui.drawerOpen()) { e.preventDefault(); ui.closeDrawer(); return; }
        if (inField && t.id === 'input' && !$('#slash').hidden) return;
        if (inField) t.blur();
        return;
      }
    }, true);
  }
  SL.keys = { mount };
})(SL);
