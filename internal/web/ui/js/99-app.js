/* 99-app.js: boot. Order: settings; the live data (hello, stream, snapshots: live.js); the shell scope (HUD, strip, rail, approvals,
 * keys, link); the first view; the loop. Until the sessions exist the page keeps the empty shell of index.html (no sample values).
 * The server may host no tab at all: the shell then mounts on the placeholder session (an empty shell) and opens the New session
 * dialog. */
(function (SL) {
  'use strict';
  const U = SL.u, { $, $$ } = U, ui = SL.ui;
  /** The tab title: `(? N) Sleipnir` while questions wait in any tab, `✓ <tab> · Sleipnir` after a long turn ended unseen. */
  const TITLE = { base: document.title || 'Sleipnir Web', done: null };
  function badgeOn() { const s = SL.settings.title; if (s === 'on') return true; if (s === 'off') return false; const h = SL.live.hello; return !(h && h.ui && h.ui.bell === false); }
  function paintTitle() {
    const n = SL.sessions.needs().length, on = badgeOn();
    const t = !on ? TITLE.base : n ? '(? ' + n + ') ' + TITLE.base : TITLE.done ? '✓ ' + TITLE.done + ' · ' + TITLE.base : TITLE.base;
    if (document.title !== t) document.title = t;
  }
  function boot() {
    SL.settings.load(); SL.time.setMode(SL.settings.hover); ui.applyMotion(); document.body.classList.toggle('compact', SL.settings.density === 'compact'); document.body.classList.toggle('cq', SL.settings.cache === 'quiet');
    SL.live.start().then(h => { serverMotion(h); mountShell(); });
  }
  /** Motion "auto" also stands still when the server runs with SLEIPNIR_ANIM=0 or REDUCE_MOTION; a choice the person
   *  made in Appearance wins. */
  function serverMotion(h) {
    if (!(h && h.ui && h.ui.reduceMotion)) return;
    const base = ui.still; ui.still = () => base() || SL.settings.motion === 'auto'; ui.applyMotion();
  }
  /** The views whose layout is built from the roster when they mount. */
  const ROSTER_VIEWS = new Set(['cockpit']);
  /**
   * What a person has in a view that is about to be mounted again: every field's value (by id, else by its place), the focused field
   * with its selection, and the scroll of every scrolled box. Returns restore(), which puts them back into the new view.
   */
  ui.keepForm = function (root) {
    /* what a person types: text fields and text areas (a slider, a checkbox or a select is the view's own state, drawn again from it) */
    if (!root) return null; const fields = $$('input,textarea', root).filter(f => f.tagName === 'TEXTAREA' || /^(text|search|number|email|url|tel|password)$/.test(f.type)), path = el => { const all = $$(el.tagName.toLowerCase(), root); return el.tagName + '#' + all.indexOf(el); };
    const vals = fields.map(f => ({ id: f.id, p: path(f), v: f.value }));
    const a = document.activeElement, focus = a && root.contains(a) && /^(INPUT|TEXTAREA|SELECT)$/.test(a.tagName) ? { id: a.id, p: path(a), s: a.selectionStart, e: a.selectionEnd } : null;
    const scrolls = [root].concat($$('*', root)).filter(el => el.scrollTop || el.scrollLeft).map(el => ({ id: el.id, cls: el.className, i: $$('*', root).indexOf(el), t: el.scrollTop, l: el.scrollLeft }));
    return function restore() {
      const find = x => (x.id && root.querySelector('#' + CSS.escape(x.id))) || (() => { const m = /^(\w+)#(\d+)$/.exec(x.p); return m ? $$(m[1].toLowerCase(), root)[+m[2]] : null; })();
      vals.forEach(x => { const f = find(x); if (f && f.value !== x.v) { f.value = x.v; f.dispatchEvent(new Event('input', { bubbles: true })); } });
      if (focus) { const f = find(focus); if (f) { f.focus(); try { if (focus.s != null) f.setSelectionRange(focus.s, focus.e); } catch (e) { /* no selection on this field */ } } }
      scrolls.forEach(x => { const el = x.i < 0 ? root : (x.id && root.querySelector('#' + CSS.escape(x.id))) || $$('*', root)[x.i]; if (el) { el.scrollTop = x.t; el.scrollLeft = x.l; } });
    };
  };
  function mountShell() {
    SL.sessions.init();
    SL.views.V.host = $('#views');
    const sh = SL.shell = SL.makeScope($('#app'), 'shell');
    /* first of all the page's key handlers: a key of an input method's composition reaches none of them (no Enter sends half a word) */
    sh.listen(document, 'keydown', e => { if (U.composing(e)) e.stopImmediatePropagation(); }, true);
    ui.shell.mount(sh); ui.navMount(sh); SL.chat.mount(sh); ui.approvals.mount(sh); SL.link.bind(sh); SL.keys.mount(sh);
    sh.on('activated', S => { SL.views.show(S.ui.view || 'cockpit'); });
    sh.on('ask-arrived', ({ S, q }) => { const act = SL.sessions.active; if (S !== act || SL.time.holdWanted() || S.replay) { ui.toast((S === act ? (S.replay ? 'a question is waiting (the view is paused): ' : 'a question is waiting behind the hold: ') : S.name + ': ') + q.agent + ' wants to run ' + SL.u.clip1(q.cmd, 80), 'warm'); } paintTitle(); });
    sh.on('goal-met', S => { if (S !== SL.sessions.active) ui.toast(S.name + ': goal met', 'ok'); });
    sh.on('session-done', S => {
      const m = S.wm, f = m.final, start = (m.turnStart || 0); if (document.hidden && f && f.t - start >= 30) { TITLE.done = S.name; paintTitle(); }
    });
    sh.on('needs-changed', paintTitle); sh.on('title-badge', paintTitle); sh.on('sessions-changed', paintTitle);
    sh.listen(document, 'visibilitychange', () => { if (!document.hidden && TITLE.done) { TITLE.done = null; paintTitle(); } });
    /* a roster frame or a fresh snapshot: only a view drawn from the roster at mount (the cockpit's stalls) is mounted again, and only
       when the roster itself changed, keeping what is typed, selected and scrolled; any other view repaints in place */
    const rosterSig = S => (S.roster || []).map(r => r.id + ':' + r.role + ':' + r.code).join(',');
    let mountedSig = '';
    sh.on('view-mounted', () => { const S = SL.sessions.active; mountedSig = S ? S.id + '|' + rosterSig(S) : ''; });
    sh.on('roster-changed', S => {
      if (S !== SL.sessions.active) return; const v = SL.views.current, sig = S.id + '|' + rosterSig(S);
      if (!v || sig === mountedSig || !ROSTER_VIEWS.has(v.name)) { mountedSig = sig; SL.loop.dirty = true; if (S.touch) S.touch(); return; }
      const keep = ui.keepForm ? ui.keepForm($('#views')) : null; SL.views.show(v.name); if (keep) keep();
    });
    sh.on('sessions-changed', () => { SL.loop.dirty = true; if (!SL.sessions.list.length && !SL.sessions.active.placeholder) SL.sessions.activate(null); });
    sh.on('meta', () => { SL.loop.dirty = true; });
    sh.on('view-mounted', name => { if (SL.data.PAGES[name]) SL.data.forPage(name); });
    sh.interval(() => { const v = SL.views.current; if (!document.hidden && v && (v.name === 'settings' || v.name === 'tools' || v.name === 'schedule' || v.name === 'doctor')) SL.data.forPage(v.name, { force: true }); }, 30000);
    const params = new URLSearchParams(location.search), first = params.get('view') || SL.sessions.active.ui.view || 'cockpit';
    const want = params.get('session') || (SL.live.hello && SL.live.hello.active);
    if (want && SL.sessions.get(want) && SL.sessions.active !== SL.sessions.get(want)) SL.act.switchSession(want);
    SL.views.show(SL.views.V.reg[first] ? first : 'cockpit');
    SL.data.onSwitch();
    SL.loop.start();
    paintTitle();
    if (!SL.sessions.list.length) ui.dialogs.newSession();
  }
  SL.boot = boot;
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', boot); else boot();
})(SL);
