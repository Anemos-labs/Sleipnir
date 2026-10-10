/* 96-ui-nav.js: the Dock's workspace shell: the left navigation rail (SL.ui.nav), the persistent Radio rail's collapse and resize (SL.ui.rail),
 * the `g` then letter chords, and the phone's bottom bar. Mounted once under the shell scope; it renders from the active session.
 *
 * The rail items are views of the same page: Cockpit, Files / Changes / Checkpoints (three tabs of ONE Workspace view), Cache, Mail, Board,
 * Replay, Sessions, Tools, Settings; `Radio` is not a view: it opens the Radio rail on the right and focuses its composer. */
(function (SL) {
  'use strict';
  const U = SL.u, { $, $$, esc, mk } = U, calc = SL.calc, ui = SL.ui = SL.ui || {};
  const svg = d => '<svg class="nvg" viewBox="0 0 20 20" width="20" height="20" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round">' + d + '</svg>';
  const ICONS = {
    cockpit: svg('<path d="M3.2 14.5a7.2 7.2 0 1 1 13.6 0"/><path d="M10 14l3.2-4.6"/><circle cx="10" cy="14" r="1.1" fill="currentColor" stroke="none"/><path d="M5.4 5.6l.9.9M10 3.6v1.2M14.6 5.6l-.9.9" stroke-width="1.3"/>'),
    radio: svg('<circle cx="10" cy="10" r="1.6" fill="currentColor" stroke="none"/><path d="M6.9 6.9a4.4 4.4 0 0 0 0 6.2M13.1 6.9a4.4 4.4 0 0 1 0 6.2M4.4 4.4a7.9 7.9 0 0 0 0 11.2M15.6 4.4a7.9 7.9 0 0 1 0 11.2"/>'),
    files: svg('<path d="M2.6 5h5l1.6 2H17.4v9H2.6z"/><path d="M2.6 8.6h14.8" stroke-width="1.2"/>'),
    changes: svg('<rect x="3" y="3" width="14" height="14" rx="1.6"/><path d="M6.8 7.6h6.4M10 4.9v5.4M6.8 13.6h6.4"/>'),
    checkpoints: svg('<path d="M4.6 17V3.4"/><path d="M4.6 4h10.2l-2.4 3.2 2.4 3.2H4.6"/>'),
    cache: svg('<path d="M10 2.8l7.2 3.6L10 10 2.8 6.4z"/><path d="M2.8 10L10 13.6 17.2 10M2.8 13.6L10 17.2l7.2-3.6"/>'),
    mail: svg('<rect x="2.6" y="4.4" width="14.8" height="11.2" rx="1.6"/><path d="M3.2 5.4L10 11l6.8-5.6"/>'),
    board: svg('<rect x="3" y="3.4" width="3.8" height="13.2" rx=".9"/><rect x="8.1" y="3.4" width="3.8" height="9" rx=".9"/><rect x="13.2" y="3.4" width="3.8" height="11" rx=".9"/>'),
    replay: svg('<circle cx="10" cy="10" r="7.2"/><path d="M8.2 6.9l5 3.1-5 3.1z" fill="currentColor"/>'),
    sessions: svg('<rect x="2.6" y="7" width="11" height="9.4" rx="1.3"/><path d="M5.6 7V5.3c0-.7.5-1.3 1.3-1.3h8c.7 0 1.3.6 1.3 1.3v6.4c0 .7-.6 1.3-1.3 1.3H13.6"/>'),
    tools: svg('<rect x="2.6" y="3.6" width="14.8" height="12.8" rx="1.6"/><path d="M6 8l3 2.2L6 12.4M10.8 13h3.6"/>'),
    settings: svg('<path d="M3 6h14M3 10h14M3 14h14"/><circle cx="7" cy="6" r="1.9" fill="var(--panel)"/><circle cx="13" cy="10" r="1.9" fill="var(--panel)"/><circle cx="8.6" cy="14" r="1.9" fill="var(--panel)"/>'),
    more: svg('<circle cx="4.5" cy="10" r="1.3" fill="currentColor" stroke="none"/><circle cx="10" cy="10" r="1.3" fill="currentColor" stroke="none"/><circle cx="15.5" cy="10" r="1.3" fill="currentColor" stroke="none"/>'),
  };
  /** id, view, tab (Workspace), label, chord letter, group */
  const ITEMS = [
    { id: 'cockpit', view: 'cockpit', label: 'Cockpit', key: 'c', grp: 'team' },
    { id: 'radio', label: 'Radio', key: 'r', radio: true },
    { id: 'files', view: 'workspace', tab: 'files', label: 'Files', key: 'f', grp: 'workspace' },
    { id: 'changes', view: 'workspace', tab: 'changes', label: 'Changes', key: 'h' },
    { id: 'checkpoints', view: 'workspace', tab: 'checkpoints', label: 'Checkpoints', key: 'k', short: 'Points' },
    { id: 'cache', view: 'cache', label: 'Cache', key: 'a', grp: 'observe' },
    { id: 'mail', view: 'mail', label: 'Mail', key: 'm' },
    { id: 'board', view: 'board', label: 'Board', key: 'b' },
    { id: 'replay', view: 'replay', label: 'Replay', key: 'p' },
    { id: 'sessions', view: 'sessions', label: 'Sessions', key: 's', grp: 'manage' },
    { id: 'tools', view: 'tools', label: 'Tools', key: 't' },
    { id: 'settings', view: 'settings', label: 'Settings', key: 'e' },
  ];
  const GRP = { team: 'team', workspace: 'workspace', observe: 'observe', manage: 'manage' };
  const OWN = { runner: 'tools', doctor: 'tools', schedule: 'tools', kit: '' };    // views that belong to a rail item
  const PREF_KEY = 'sleipnir.web.dock';
  const pref = { nav: null, rail: 'open', railW: 344 };
  try { const o = JSON.parse(localStorage.getItem(PREF_KEY) || '{}'); if (o.nav === 'rail' || o.nav === 'wide') pref.nav = o.nav; if (o.rail === 'min' || o.rail === 'open') pref.rail = o.rail; if (o.railW >= 280 && o.railW <= 640) pref.railW = o.railW; } catch (e) { /* no storage: defaults */ }
  const save = () => { try { localStorage.setItem(PREF_KEY, JSON.stringify(pref)); } catch (e) { /* ignore */ } };
  const isPhone = () => window.innerWidth <= 900;

  /** where the view currently shown sits in the rail */
  function currentId(S) {
    const cur = SL.views.current ? SL.views.current.name : ''; if (!cur) return '';
    if (cur === 'workspace') return (S && S.ui.ws && S.ui.ws.tab) || 'files';
    return OWN[cur] != null ? OWN[cur] : cur;
  }
  function go(id) {
    const it = ITEMS.find(x => x.id === id); if (!it) return false;
    if (it.radio) { ui.rail.focus(); return true; }
    const S = SL.sessions.active;
    if (it.view === 'workspace') { if (SL.views.current && SL.views.current.name === 'workspace') { ui.ws.setTab(it.tab); return true; } if (S) { S.ui.ws = S.ui.ws || {}; S.ui.ws.tab = it.tab; } }
    if (isPhone()) $('#app').dataset.pv = it.view;
    SL.views.show(it.view); return true;
  }

  /* ---------- the rail's own state: items with badges ---------- */
  function badges(S) {
    const m = S && S.m, b = {}; if (!m) return b;
    const q = calc.waiting(m); if (q) b.radio = ['? ' + q, 'warm', q + ' question' + (q > 1 ? 's' : '') + ' waiting'];
    if (ui.ws && ui.ws.counts) { const c = ui.ws.counts(S); if (c.changed) b.changes = [String(c.changed), '', c.changed + ' changed file' + (c.changed > 1 ? 's' : '')]; if (c.ckpts) b.checkpoints = [String(c.ckpts), '', c.ckpts + ' checkpoint' + (c.ckpts > 1 ? 's' : '')]; if (c.reviewed < c.changed && false) b.changes = null; }
    if (m.anomalies.length) b.cache = SL.settings.cache === 'full' ? ['⚠ ' + m.anomalies.length, 'err', m.anomalies.length + ' cache anomaly'] : ['⚠', '', m.anomalies.length + ' cache anomaly'];
    if (m.mail.length) b.mail = [String(m.mail.length), '', m.mail.length + ' mails routed'];
    b.sessions = [String(SL.sessions.list.length), '', SL.sessions.list.length + ' sessions live'];
    return b;
  }

  function mount(sc) {
    const app = $('#app'), nav = $('#nav'), N = { sig: '' };
    const wide = () => (pref.nav || (window.innerWidth >= 1700 ? 'wide' : 'rail')) === 'wide';
    function paintNav(S) {
      const cur = currentId(S), bd = badges(S), w = wide(), railOpen = app.dataset.rail === 'open';
      const sig = [cur, w, railOpen, SL.settings.cache, Object.keys(bd).map(k => k + (bd[k] && bd[k][0])).join(','), S && S.id].join('|'); if (N.sig === sig) return; N.sig = sig;
      app.dataset.navmode = w ? 'wide' : 'rail'; let g = null;
      const items = ITEMS.map(it => {
        const b = bd[it.id], isCur = !it.radio && it.id === cur, head = it.grp && it.grp !== g ? (g = it.grp, (g !== 'team' ? '<span class="nvh" aria-hidden="true">' + GRP[g] + '</span>' : '')) : '';
        return head + '<button class="nvi" type="button" data-nav="' + it.id + '"' + (isCur ? ' aria-current="page"' : '') + (it.radio ? ' aria-pressed="' + railOpen + '" aria-controls="rail"' : '') + (b ? ' aria-label="' + esc(it.label + ', ' + b[2]) + '"' : '') + ' title="' + esc(it.label + (it.radio ? ' · opens the Radio rail' : '') + ' (g ' + it.key + ')') + '">' + ICONS[it.id] + '<span class="nl">' + esc(w || !it.short ? it.label : it.short) + '</span><kbd class="nk">g ' + it.key + '</kbd>' + (b ? '<span class="nbd ' + (b[1] || '') + '" aria-hidden="true">' + esc(b[0]) + '</span>' : '') + '</button>';
      }).join('');
      nav.innerHTML = '<div class="nv-list">' + items + '</div><button class="nvt" type="button" id="nvTog" aria-expanded="' + w + '" title="' + (w ? 'Narrow the rail' : 'Widen the rail: labels and chord keys') + '"><svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true"><path d="' + (w ? 'M10 3L5 8l5 5' : 'M6 3l5 5-5 5') + '" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"/></svg><span class="nl">' + (w ? 'Narrow' : 'Widen') + '</span></button><div class="chordhint" id="chordHint" hidden></div>';
      N.hint = $('#chordHint', nav);
      paintPhone(S, cur);
    }
    function render(S) { paintNav(S); }
    sc.on('cache-detail', () => { N.sig = ''; paintNav(SL.sessions.active); });
    sc.listen(nav, 'click', e => {
      const b = e.target.closest('[data-nav]'); if (b) { go(b.dataset.nav); return; }
      if (e.target.closest('#nvTog')) { pref.nav = wide() ? 'rail' : 'wide'; save(); N.sig = ''; paintNav(SL.sessions.active); }
    });
    sc.listen(nav, 'keydown', e => {
      if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp' && e.key !== 'Home' && e.key !== 'End') return; const bs = $$('.nvi', nav), i = bs.indexOf(document.activeElement); if (i < 0) return;
      e.preventDefault(); const n = e.key === 'Home' ? 0 : e.key === 'End' ? bs.length - 1 : (i + (e.key === 'ArrowDown' ? 1 : bs.length - 1)) % bs.length; bs[n].focus();
    });
    sc.on('view-mounted', () => { N.sig = ''; paintNav(SL.sessions.active); });
    sc.on('activated', () => { N.sig = ''; });
    sc.on('needs-changed', () => { N.sig = ''; });
    sc.update(S => { if (S) paintNav(S); });
    sc.listen(window, 'resize', () => { N.sig = ''; });

    /* ---------- chords: g then a letter ---------- */
    const CH = { until: 0 };
    function chordShow(on) { if (!N.hint) return; N.hint.hidden = !on; if (on) N.hint.innerHTML = '<b>g</b> then ' + ITEMS.map(it => '<span><kbd>' + it.key + '</kbd> ' + esc(it.label.toLowerCase()) + '</span>').join(''); }
    /** the keyboard handler calls this for a plain key press outside fields; returns true when it consumed the key */
    function chord(key) {
      const now = SL.time.T.wall;
      if (CH.until > now) { CH.until = 0; chordShow(false); const it = ITEMS.find(x => x.key === key.toLowerCase()); if (it) { go(it.id); return true; } return key === 'g'; }
      if (key === 'g') { CH.until = now + 1500; chordShow(true); sc.timeout(() => { if (CH.until && SL.time.T.wall >= CH.until - 1) { CH.until = 0; chordShow(false); } }, 1600); return true; }
      return false;
    }

    /* ---------- phone: a bottom bar of five ---------- */
    const mnav = $('#mnav');
    function paintPhone(S, cur) {
      const pv = app.dataset.pv, nd = SL.sessions.needs().length, items = [['cockpit', 'Cockpit'], ['radio', 'Radio'], ['changes', 'Changes'], ['sessions', 'Sessions'], ['more', 'More']];
      mnav.innerHTML = items.map(([k, t]) => { const on = k === 'radio' ? pv === 'radio' : (pv !== 'radio' && k === cur); return '<button type="button" data-n="' + k + '"' + (on ? ' aria-current="page"' : '') + '><span class="g">' + ICONS[k === 'more' ? 'more' : k] + '</span>' + t + (k === 'radio' && nd ? '<span class="ping"></span>' : '') + '</button>'; }).join('');
    }
    sc.listen(mnav, 'click', e => {
      const b = e.target.closest('[data-n]'); if (!b) return; const k = b.dataset.n;
      if (k === 'radio') { app.dataset.pv = 'radio'; N.sig = ''; paintNav(SL.sessions.active); }
      else if (k === 'more') moreSheet(); else { app.dataset.pv = 'main'; go(k); }
    });
    function moreSheet() {
      ui.modal({ title: 'Views', kicker: 'g then a letter', desc: 'every screen', color: 'var(--fe)', body: '<div class="moregrid">' + ITEMS.filter(it => !it.radio).map(it => '<button class="btn more" type="button" data-nav="' + it.id + '">' + ICONS[it.id] + '<span>' + esc(it.label) + '</span></button>').join('') + '<button class="btn more" type="button" data-pal>' + ICONS.tools + '<span>Command palette</span></button></div>',
        onMount(b, scm, close) { scm.listen(b, 'click', e => { const x = e.target.closest('[data-nav]'); if (x) { close(); app.dataset.pv = 'main'; go(x.dataset.nav); } else if (e.target.closest('[data-pal]')) { close(); SL.palette.open(''); } }); } });
    }

    ui.nav = { render, go, chord, items: ITEMS, currentId, moreSheet, get wide() { return wide(); }, setWide(on) { pref.nav = on ? 'wide' : 'rail'; save(); N.sig = ''; paintNav(SL.sessions.active); } };
    Object.defineProperty(ui.nav, 'sig', { get() { return N.sig; }, set(v) { N.sig = v; } });

    /* ---------- the Radio rail: collapse and resize ---------- */
    const rail = $('#rail'), split = $('#railSplit'), tog = $('#railTog'), min = $('#railMin'), R = { open: pref.rail !== 'min', w: pref.railW, custom: pref.railW !== 344 };
    const maxW = () => Math.max(300, Math.min(640, Math.round(window.innerWidth * 0.5)));
    function paintRail(persist) {
      R.w = Math.max(280, Math.min(maxW(), R.w));
      app.dataset.rail = R.open ? 'open' : 'min'; if (R.custom) { app.dataset.railc = '1'; app.style.setProperty('--railw', R.w + 'px'); } else { delete app.dataset.railc; app.style.removeProperty('--railw'); }
      split.setAttribute('aria-valuenow', String(R.w)); split.setAttribute('aria-valuemax', String(maxW()));
      tog.setAttribute('aria-expanded', String(R.open)); min.setAttribute('aria-expanded', String(R.open));
      if (persist) { pref.rail = R.open ? 'open' : 'min'; pref.railW = R.w; save(); }
      N.sig = ''; paintNav(SL.sessions.active); SL.loop.dirty = true;
    }
    sc.listen(tog, 'click', () => { R.open = false; paintRail(true); });
    sc.listen(min, 'click', () => { R.open = true; paintRail(true); sc.timeout(() => { const i = $('#input'); if (i && !i.disabled) i.focus({ preventScroll: true }); }, 30); });
    sc.listen(split, 'pointerdown', e => {
      if (e.button !== 0 || isPhone()) return; e.preventDefault(); const x0 = e.clientX, w0 = R.w; split.classList.add('drag'); document.body.classList.add('resizing');
      const mv = ev => { R.w = w0 + (x0 - ev.clientX); R.custom = true; app.dataset.railc = '1'; app.style.setProperty('--railw', Math.max(280, Math.min(maxW(), R.w)) + 'px'); split.setAttribute('aria-valuenow', String(Math.max(280, Math.min(maxW(), R.w)))); };
      const up = () => { window.removeEventListener('pointermove', mv); window.removeEventListener('pointerup', up); window.removeEventListener('pointercancel', up); split.classList.remove('drag'); document.body.classList.remove('resizing'); R.w = Math.max(280, Math.min(maxW(), R.w)); paintRail(true); };
      window.addEventListener('pointermove', mv); window.addEventListener('pointerup', up); window.addEventListener('pointercancel', up);
    });
    sc.listen(split, 'keydown', e => {
      const d = { ArrowLeft: 24, ArrowRight: -24 }[e.key]; if (d) { e.preventDefault(); R.w += d; R.custom = true; paintRail(true); } else if (e.key === 'Home') { e.preventDefault(); R.w = 280; R.custom = true; paintRail(true); } else if (e.key === 'End') { e.preventDefault(); R.w = maxW(); R.custom = true; paintRail(true); } else if (e.key === 'Enter') { e.preventDefault(); R.open = false; paintRail(true); }
    });
    sc.listen(split, 'dblclick', () => { R.w = 344; R.custom = false; paintRail(true); });
    sc.listen(window, 'resize', () => { if (R.w > maxW()) { R.w = maxW(); paintRail(false); } });
    ui.rail = {
      get isOpen() { return R.open; }, get width() { return R.w; },
      /** open the rail (does not move focus) */
      expand() { if (!R.open) { R.open = true; paintRail(true); } },
      open() { ui.rail.expand(); if (isPhone()) { app.dataset.pv = 'radio'; N.sig = ''; paintNav(SL.sessions.active); } },
      collapse() { if (R.open) { R.open = false; paintRail(true); } },
      /** Radio in the left rail: open the rail and focus the composer (phone: switch to the Radio pane) */
      focus() { ui.rail.open(); sc.timeout(() => { const i = $('#input'); if (i && !i.disabled) i.focus(); }, 30); },
      setWidth(w) { R.w = w; R.custom = true; paintRail(true); },
    };
    paintRail(false);
    /* the rail's mini state: a badge on the collapsed strip when a question waits or new rows arrived */
    const mb = $('#railMinB');
    sc.update((S, m) => { if (!S || !m) return; const q = calc.waiting(m); mb.hidden = !q; mb.textContent = q ? '? ' + q : ''; $('#qBanner').classList.toggle('minq', !R.open); });
  }
  /** an agent was picked (a stall, a leg, a gantt row, a task card, Enter on the selection): its read-only Details drawer opens. The person talks to the manager only. */
  ui.focusAgent = function (id) {
    if (!id) return; const S = SL.sessions.active; if (!S || !S.m || !S.m.ag[id]) return; ui.openDrawer(id);
  };
  ui.navMount = mount;
})(SL);
