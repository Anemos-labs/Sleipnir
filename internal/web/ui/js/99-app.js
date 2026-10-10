/* 99-app.js: boot. Order: settings; the live data (hello, stream, snapshots: live.js); the shell scope (HUD, strip, rail, approvals,
 * keys, link); the first view; the loop. Until the sessions exist the page keeps the empty shell of index.html (no sample values).
 * The server may host no tab at all: the shell then mounts on the placeholder session (an empty shell) and opens the New session
 * dialog. */
(function (SL) {
  'use strict';
  const U = SL.u, { $ } = U, ui = SL.ui;
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
  function mountShell() {
    SL.sessions.init();
    SL.views.V.host = $('#views');
    const sh = SL.shell = SL.makeScope($('#app'), 'shell');
    ui.shell.mount(sh); ui.navMount(sh); SL.chat.mount(sh); ui.approvals.mount(sh); SL.link.bind(sh); SL.keys.mount(sh);
    sh.on('activated', S => { SL.views.show(S.ui.view || 'cockpit'); });
    sh.on('ask-arrived', ({ S, q }) => { const act = SL.sessions.active; if (S !== act || SL.time.holdWanted()) { ui.toast((S === act ? 'a question is waiting behind the hold: ' : S.name + ': ') + q.agent + ' wants to run ' + SL.u.clip1(q.cmd, 80), 'warm'); const an = $('#announcer'); if (an) an.textContent = S.name + ': a question is waiting for you'; } paintTitle(); });
    sh.on('goal-met', S => { if (S !== SL.sessions.active) ui.toast(S.name + ': goal met', 'ok'); });
    sh.on('session-done', S => {
      const m = S.wm, f = m.final, start = (m.turnStart || 0); if (document.hidden && f && f.t - start >= 30) { TITLE.done = S.name; paintTitle(); }
    });
    sh.on('needs-changed', paintTitle); sh.on('title-badge', paintTitle); sh.on('sessions-changed', paintTitle);
    sh.listen(document, 'visibilitychange', () => { if (!document.hidden && TITLE.done) { TITLE.done = null; paintTitle(); } });
    sh.on('roster-changed', S => { if (S === SL.sessions.active) SL.views.show(SL.views.current ? SL.views.current.name : 'cockpit'); });
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
