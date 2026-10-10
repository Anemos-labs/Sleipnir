/* 99-app.js: boot. Order: settings, sessions, the shell scope (HUD, strip, rail, approvals, keys, link), the first view, the loop.
 * A different shell layout keeps this file and swaps the pieces it mounts. */
(function (SL) {
  'use strict';
  const U = SL.u, { $ } = U, ui = SL.ui;
  function boot() {
    SL.settings.load(); SL.time.setMode(SL.settings.hover); ui.applyMotion(); document.body.classList.toggle('compact', SL.settings.density === 'compact'); document.body.classList.toggle('cq', SL.settings.cache === 'quiet');
    SL.sessions.init();
    SL.views.V.host = $('#views');
    const sh = SL.shell = SL.makeScope($('#app'), 'shell');
    ui.shell.mount(sh); ui.navMount(sh); SL.chat.mount(sh); ui.approvals.mount(sh); SL.link.bind(sh); SL.keys.mount(sh);
    sh.on('activated', S => { SL.views.show(S.ui.view || 'cockpit'); });
    sh.on('ask-arrived', ({ S, q }) => { const act = SL.sessions.active; if (S !== act || SL.time.holdWanted()) { ui.toast((S === act ? 'a question is waiting behind the hold: ' : S.name + ': ') + q.agent + ' wants to run ' + q.cmd, 'warm'); const an = $('#announcer'); if (an) an.textContent = S.name + ': a question is waiting for you'; } });
    sh.on('session-done', S => { if (S !== SL.sessions.active) ui.toast(S.name + ': goal met', 'ok'); });
    sh.on('roster-changed', S => { if (S === SL.sessions.active) SL.views.show(SL.views.current ? SL.views.current.name : 'cockpit'); });
    sh.on('sessions-changed', () => { SL.loop.dirty = true; });
    sh.on('meta', () => { SL.loop.dirty = true; });
    const params = new URLSearchParams(location.search), first = params.has('kit') ? 'kit' : (params.get('view') || SL.sessions.active.ui.view || 'cockpit');
    SL.views.show(SL.views.V.reg[first] ? first : 'cockpit');
    if (params.get('session') && SL.sessions.get(params.get('session'))) SL.act.switchSession(params.get('session'));
    SL.loop.start();
  }
  SL.boot = boot;
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', boot); else boot();
})(SL);
